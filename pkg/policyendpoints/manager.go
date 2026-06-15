package policyendpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"golang.org/x/exp/maps"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	policyequality "github.com/aws/amazon-network-policy-controller-k8s/pkg/equality"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

const (
	// LastChangeTriggerTimeAnnotation is set on PolicyEndpoint/ClusterPolicyEndpoint
	// to track when NPC last created or updated the resource. NPA reads this to
	// compute E2E policy programming latency (same pattern as kube-proxy's
	// endpoints.kubernetes.io/last-change-trigger-time on EndpointSlice).
	LastChangeTriggerTimeAnnotation = "networking.k8s.aws/last-change-trigger-time"
)

// setLastChangeTriggerTime sets the E2E latency annotation on a PolicyEndpoint.
func setLastChangeTriggerTime(pe *policyinfo.PolicyEndpoint) {
	if pe.Annotations == nil {
		pe.Annotations = make(map[string]string)
	}
	pe.Annotations[LastChangeTriggerTimeAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
}

// setLastChangeTriggerTimeOnCPE sets the E2E latency annotation on a ClusterPolicyEndpoint.
func setLastChangeTriggerTimeOnCPE(cpe *policyinfo.ClusterPolicyEndpoint) {
	if cpe.Annotations == nil {
		cpe.Annotations = make(map[string]string)
	}
	cpe.Annotations[LastChangeTriggerTimeAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
}

// PolicyMetadata contains the minimal metadata needed for creating PolicyEndpoints
type PolicyMetadata struct {
	Name        string
	Namespace   string
	UID         types.UID
	PodSelector metav1.LabelSelector
	PolicyTypes []networking.PolicyType
	APIVersion  string
	Kind        string
}

type PolicyEndpointsManager interface {
	Reconcile(ctx context.Context, policy *networking.NetworkPolicy) error
	Cleanup(ctx context.Context, policy *networking.NetworkPolicy) error
	ReconcileANP(ctx context.Context, anp *policyinfo.ApplicationNetworkPolicy) error
	CleanupANP(ctx context.Context, anp *policyinfo.ApplicationNetworkPolicy) error
	ReconcileCNP(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) error
	CleanupCNP(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) error
}

// NewPolicyEndpointsManager constructs a new policyEndpointsManager
func NewPolicyEndpointsManager(k8sClient client.Client, endpointChunkSize int, logger logr.Logger) *policyEndpointsManager {
	endpointsResolver := resolvers.NewEndpointsResolver(k8sClient, logger.WithName("endpoints-resolver"))
	anpEndpointsResolver := resolvers.NewApplicationNetworkPolicyEndpointsResolver(k8sClient, logger.WithName("anp-endpoints-resolver"))
	cnpEndpointsResolver := resolvers.NewClusterNetworkPolicyEndpointsResolver(k8sClient, logger.WithName("cnp-endpoints-resolver"))
	return &policyEndpointsManager{
		k8sClient:            k8sClient,
		endpointsResolver:    endpointsResolver,
		anpEndpointsResolver: anpEndpointsResolver,
		cnpEndpointsResolver: cnpEndpointsResolver,
		endpointChunkSize:    endpointChunkSize,
		logger:               logger,
	}
}

var _ PolicyEndpointsManager = (*policyEndpointsManager)(nil)

type policyEndpointsManager struct {
	k8sClient            client.Client
	endpointsResolver    resolvers.EndpointsResolver
	anpEndpointsResolver resolvers.ApplicationNetworkPolicyEndpointsResolver
	cnpEndpointsResolver resolvers.ClusterNetworkPolicyEndpointsResolver
	endpointChunkSize    int
	logger               logr.Logger
}

func (m *policyEndpointsManager) Reconcile(ctx context.Context, policy *networking.NetworkPolicy) error {
	computeStart := time.Now()

	ingressRules, egressRules, podSelectorEndpoints, err := m.endpointsResolver.Resolve(ctx, policy)
	if err != nil {
		return err
	}

	policyEndpointList := &policyinfo.PolicyEndpointList{}
	if err := m.k8sClient.List(ctx, policyEndpointList,
		client.InNamespace(policy.Namespace),
		client.MatchingFields{IndexKeyPolicyReferenceName: policy.Name}); err != nil {
		return err
	}
	existingPolicyEndpoints := make([]policyinfo.PolicyEndpoint, 0, len(policyEndpointList.Items))
	for _, policyEndpoint := range policyEndpointList.Items {
		existingPolicyEndpoints = append(existingPolicyEndpoints, policyEndpoint)
	}

	createList, updateList, deleteList, err := m.computePolicyEndpoints(policy, existingPolicyEndpoints, ingressRules, egressRules, podSelectorEndpoints)
	if err != nil {
		return err
	}

	computeDuration := time.Since(computeStart)
	crudStart := time.Now()

	for i := range createList {
		setLastChangeTriggerTime(&createList[i])
		if err := m.k8sClient.Create(ctx, &createList[i]); err != nil {
			return err
		}
		m.logger.Info("Created policy endpoint", "id", k8s.NamespacedName(&createList[i]))
	}

	for i := range updateList {
		oldRes := &policyinfo.PolicyEndpoint{}
		if err := m.k8sClient.Get(ctx, k8s.NamespacedName(&updateList[i]), oldRes); err != nil {
			return err
		}
		if policyequality.EqualPolicyEndpointSpec(oldRes.Spec, updateList[i].Spec) {
			m.logger.V(1).Info("Policy endpoint already up to date", "id", k8s.NamespacedName(&updateList[i]))
			continue
		}
		setLastChangeTriggerTime(&updateList[i])
		if err := m.k8sClient.Patch(ctx, &updateList[i], client.MergeFrom(oldRes)); err != nil {
			return err
		}
		m.logger.Info("Updated policy endpoint", "id", k8s.NamespacedName(&updateList[i]))
	}

	for _, policyEndpoint := range deleteList {
		if err := m.k8sClient.Delete(ctx, &policyEndpoint); err != nil {
			return err
		}
		m.logger.Info("Deleted policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}

	m.logger.Info("NP reconcile phase durations", "policy", policy.Name, "namespace", policy.Namespace,
		"computeDuration", computeDuration.Seconds(), "crudDuration", time.Since(crudStart).Seconds(),
		"create", len(createList), "update", len(updateList), "delete", len(deleteList))

	return nil
}

func (m *policyEndpointsManager) Cleanup(ctx context.Context, policy *networking.NetworkPolicy) error {
	policyEndpointList := &policyinfo.PolicyEndpointList{}
	if err := m.k8sClient.List(ctx, policyEndpointList,
		client.InNamespace(policy.Namespace),
		client.MatchingLabels{IndexKeyPolicyReferenceName: policy.Name}); err != nil {
		return errors.Wrap(err, "unable to list policyendpoints")
	}
	for _, policyEndpoint := range policyEndpointList.Items {
		if err := m.k8sClient.Delete(ctx, &policyEndpoint); err != nil {
			return errors.Wrap(err, "unable to delete policyendpoint")
		}
		m.logger.Info("Deleted policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}
	return nil
}

func (m *policyEndpointsManager) ReconcileANP(ctx context.Context, anp *policyinfo.ApplicationNetworkPolicy) error {
	computeStart := time.Now()

	ingressRules, egressRules, podSelectorEndpoints, err := m.anpEndpointsResolver.ResolveApplicationNetworkPolicy(ctx, anp)
	if err != nil {
		return err
	}

	policyEndpointList := &policyinfo.PolicyEndpointList{}
	if err := m.k8sClient.List(ctx, policyEndpointList,
		client.InNamespace(anp.Namespace),
		client.MatchingFields{IndexKeyPolicyReferenceName: anp.Name}); err != nil {
		return err
	}
	existingPolicyEndpoints := make([]policyinfo.PolicyEndpoint, 0, len(policyEndpointList.Items))
	for _, policyEndpoint := range policyEndpointList.Items {
		existingPolicyEndpoints = append(existingPolicyEndpoints, policyEndpoint)
	}

	createList, updateList, deleteList, err := m.computeApplicationNetworkPolicyEndpoints(anp, existingPolicyEndpoints, ingressRules, egressRules, podSelectorEndpoints)
	if err != nil {
		return err
	}

	computeDuration := time.Since(computeStart)
	crudStart := time.Now()

	for i := range createList {
		setLastChangeTriggerTime(&createList[i])
		if err := m.k8sClient.Create(ctx, &createList[i]); err != nil {
			return err
		}
		m.logger.Info("Created ANP policy endpoint", "id", k8s.NamespacedName(&createList[i]))
	}

	for i := range updateList {
		oldRes := &policyinfo.PolicyEndpoint{}
		if err := m.k8sClient.Get(ctx, k8s.NamespacedName(&updateList[i]), oldRes); err != nil {
			return err
		}
		if policyequality.EqualPolicyEndpointSpec(oldRes.Spec, updateList[i].Spec) {
			m.logger.V(1).Info("ANP policy endpoint already up to date", "id", k8s.NamespacedName(&updateList[i]))
			continue
		}
		setLastChangeTriggerTime(&updateList[i])
		if err := m.k8sClient.Patch(ctx, &updateList[i], client.MergeFrom(oldRes)); err != nil {
			return err
		}
		m.logger.Info("Updated ANP policy endpoint", "id", k8s.NamespacedName(&updateList[i]))
	}

	for _, policyEndpoint := range deleteList {
		if err := m.k8sClient.Delete(ctx, &policyEndpoint); err != nil {
			return err
		}
		m.logger.Info("Deleted ANP policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}

	m.logger.Info("ANP reconcile phase durations", "policy", anp.Name, "namespace", anp.Namespace,
		"computeDuration", computeDuration.Seconds(), "crudDuration", time.Since(crudStart).Seconds(),
		"create", len(createList), "update", len(updateList), "delete", len(deleteList))

	return nil
}

func (m *policyEndpointsManager) CleanupANP(ctx context.Context, anp *policyinfo.ApplicationNetworkPolicy) error {
	policyEndpointList := &policyinfo.PolicyEndpointList{}
	if err := m.k8sClient.List(ctx, policyEndpointList,
		client.InNamespace(anp.Namespace),
		client.MatchingLabels{IndexKeyPolicyReferenceName: anp.Name}); err != nil {
		return errors.Wrap(err, "unable to list ANP policyendpoints")
	}
	for _, policyEndpoint := range policyEndpointList.Items {
		if err := m.k8sClient.Delete(ctx, &policyEndpoint); err != nil {
			return errors.Wrap(err, "unable to delete ANP policyendpoint")
		}
		m.logger.Info("Deleted ANP policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}
	return nil
}

// computePolicyEndpoints computes the policy endpoints for the given policy
// The return values are list of policy endpoints to create, update and delete
func (m *policyEndpointsManager) computePolicyEndpoints(policy *networking.NetworkPolicy,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint) ([]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint, error) {

	// Loop through ingressEndpoints, egressEndpoints and podSelectorEndpoints and put in map
	// also populate them into policy endpoints
	ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, modifiedEndpoints, potentialDeletes := m.processExistingPolicyEndpoints(
		policy.Spec.PolicyTypes, existingPolicyEndpoints, ingressEndpoints, egressEndpoints, podSelectorEndpoints,
	)

	doNotDelete := sets.Set[types.NamespacedName]{}

	var createPolicyEndpoints []policyinfo.PolicyEndpoint
	var updatePolicyEndpoints []policyinfo.PolicyEndpoint
	var deletePolicyEndpoints []policyinfo.PolicyEndpoint

	// Create metadata for NetworkPolicy
	npMetadata := PolicyMetadata{
		Name:        policy.Name,
		Namespace:   policy.Namespace,
		UID:         policy.UID,
		PodSelector: policy.Spec.PodSelector,
		PolicyTypes: policy.Spec.PolicyTypes,
		APIVersion:  "networking.k8s.io/v1",
		Kind:        "NetworkPolicy",
	}

	// packing new ingress rules
	createPolicyEndpoints, doNotDeleteIngress := m.packingIngressRules(npMetadata, ingressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes)
	// packing new egress rules
	createPolicyEndpoints, doNotDeleteEgress := m.packingEgressRules(npMetadata, egressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes)
	// packing new pod selector
	createPolicyEndpoints, doNotDeletePs := m.packingPodSelectorEndpoints(npMetadata, podSelectorEndpointSet.UnsortedList(), createPolicyEndpoints, modifiedEndpoints, potentialDeletes)

	doNotDelete.Insert(doNotDeleteIngress.UnsortedList()...)
	doNotDelete.Insert(doNotDeleteEgress.UnsortedList()...)
	doNotDelete.Insert(doNotDeletePs.UnsortedList()...)

	for _, ep := range potentialDeletes {
		if doNotDelete.Has(k8s.NamespacedName(&ep)) {
			updatePolicyEndpoints = append(updatePolicyEndpoints, ep)
		} else {
			deletePolicyEndpoints = append(deletePolicyEndpoints, ep)
		}
	}
	updatePolicyEndpoints = append(updatePolicyEndpoints, modifiedEndpoints...)
	if len(createPolicyEndpoints) == 0 && len(updatePolicyEndpoints) == 0 {
		if len(deletePolicyEndpoints) == 0 {
			newEP := m.newPolicyEndpoint(npMetadata, nil, nil, nil)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		} else {
			ep := deletePolicyEndpoints[0]
			updatePolicyEndpoints = append(updatePolicyEndpoints, ep)
			deletePolicyEndpoints = deletePolicyEndpoints[1:]
		}
	}

	return m.processPolicyEndpoints(createPolicyEndpoints), m.processPolicyEndpoints(updatePolicyEndpoints), deletePolicyEndpoints, nil
}

func (m *policyEndpointsManager) processPolicyEndpoints(pes []policyinfo.PolicyEndpoint) []policyinfo.PolicyEndpoint {
	var newPEs []policyinfo.PolicyEndpoint
	for _, pe := range pes {
		pe.Spec.Ingress = combineRulesEndpoints(pe.Spec.Ingress)
		pe.Spec.Egress = combineRulesEndpoints(pe.Spec.Egress)
		newPEs = append(newPEs, pe)
	}
	return newPEs
}

// getCombineKey creates a combining key from CIDR or DomainName and sorted exceptions,
// excluding ports so that entries with the same network identity get merged.
func getCombineKey(ep policyinfo.EndpointInfo) string {
	var key string
	if ep.DomainName != "" {
		key = "fqdn|" + string(ep.DomainName) + "|"
	} else {
		key = "cidr|" + string(ep.CIDR) + "|"
	}

	sortedExcepts := make([]policyinfo.NetworkAddress, len(ep.Except))
	copy(sortedExcepts, ep.Except)
	sort.Slice(sortedExcepts, func(i, j int) bool {
		return string(sortedExcepts[i]) < string(sortedExcepts[j])
	})
	for _, except := range sortedExcepts {
		key += string(except) + "|"
	}

	return key
}

func combineRulesEndpoints(ingressEndpoints []policyinfo.EndpointInfo) []policyinfo.EndpointInfo {
	combinedMap := make(map[string]policyinfo.EndpointInfo)
	for _, iep := range ingressEndpoints {
		key := getCombineKey(iep)

		if existing, ok := combinedMap[key]; ok {
			// If either has all ports, result is all ports
			if len(iep.Ports) == 0 || len(existing.Ports) == 0 {
				existing.Ports = []policyinfo.Port{}
			} else {
				// Both have specific ports, append and deduplicate
				existing.Ports = deduplicatePorts(append(existing.Ports, iep.Ports...))
			}
			combinedMap[key] = existing
		} else {
			combinedMap[key] = iep
		}
	}
	if len(combinedMap) > 0 {
		return maps.Values(combinedMap)
	}
	return nil
}

// ANP-specific helper functions
func (m *policyEndpointsManager) processANPPolicyEndpoints(pes []policyinfo.PolicyEndpoint) []policyinfo.PolicyEndpoint {
	var newPEs []policyinfo.PolicyEndpoint
	for _, pe := range pes {
		pe.Spec.Ingress = combineRulesEndpoints(pe.Spec.Ingress)
		pe.Spec.Egress = combineRulesEndpoints(pe.Spec.Egress)
		newPEs = append(newPEs, pe)
	}
	return newPEs
}

func (m *policyEndpointsManager) newPolicyEndpoint(metadata PolicyMetadata,
	ingressRules []policyinfo.EndpointInfo, egressRules []policyinfo.EndpointInfo,
	podSelectorEndpoints []policyinfo.PodEndpoint) policyinfo.PolicyEndpoint {
	blockOwnerDeletion := true
	isController := true
	policyEndpoint := policyinfo.PolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    metadata.Namespace,
			GenerateName: metadata.Name + "-",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         metadata.APIVersion,
					Kind:               metadata.Kind,
					Name:               metadata.Name,
					UID:                metadata.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
					Controller:         &isController,
				},
			},
		},
		Spec: policyinfo.PolicyEndpointSpec{
			PodSelector:          &metadata.PodSelector,
			PodSelectorEndpoints: podSelectorEndpoints,
			PolicyRef: policyinfo.PolicyReference{
				Namespace: metadata.Namespace,
				Name:      metadata.Name,
			},
			PodIsolation: metadata.PolicyTypes,
			Ingress:      ingressRules,
			Egress:       egressRules,
		},
	}
	return policyEndpoint
}

func (m *policyEndpointsManager) getListOfEndpointInfoFromHash(hashes []string, epInfo map[string]policyinfo.EndpointInfo) []policyinfo.EndpointInfo {
	var ruleList []policyinfo.EndpointInfo
	for _, key := range hashes {
		ruleList = append(ruleList, epInfo[key])
	}
	return ruleList
}

// TODO: this can return different hash for semantically same endpointinfo as port slice order can generate separate hash.
// Check how its tied in bin packing algo and fix if needed
func (m *policyEndpointsManager) getEndpointInfoKey(info policyinfo.EndpointInfo) string {
	hasher := sha256.New()

	// Handle FQDN case for ApplicationNetworkPolicy
	if info.DomainName != "" {
		hasher.Write([]byte(info.DomainName))
	} else {
		// Handle CIDR case for NetworkPolicy
		hasher.Write([]byte(info.CIDR))
		for _, except := range info.Except {
			hasher.Write([]byte(except))
		}
	}

	for _, port := range info.Ports {
		if port.Protocol != nil {
			hasher.Write([]byte(*port.Protocol))
		}
		if port.Port != nil {
			hasher.Write([]byte(strconv.Itoa(int(*port.Port))))
		}
		if port.EndPort != nil {
			hasher.Write([]byte(strconv.Itoa(int(*port.EndPort))))
		}
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// processExistingPolicyEndpoints processes the existing policies with the incoming policy changes
// it returns required rules and pod selector changes, and potential modifications and deletions on policy endpoints.
func (m *policyEndpointsManager) processExistingPolicyEndpoints(
	policyTypes []networking.PolicyType,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint,
) (
	map[string]policyinfo.EndpointInfo,
	map[string]policyinfo.EndpointInfo,
	sets.Set[policyinfo.PodEndpoint],
	[]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint,
) {

	// Loop through ingressEndpoints, egressEndpoints and podSelectorEndpoints and put in map
	ingressEndpointsMap := map[string]policyinfo.EndpointInfo{}
	for _, ingressEndpoint := range ingressEndpoints {
		ingressEndpointsMap[m.getEndpointInfoKey(ingressEndpoint)] = ingressEndpoint
	}
	egressEndpointsMap := map[string]policyinfo.EndpointInfo{}
	for _, egressEndpoint := range egressEndpoints {
		egressEndpointsMap[m.getEndpointInfoKey(egressEndpoint)] = egressEndpoint
	}
	podSelectorEndpointSet := sets.New[policyinfo.PodEndpoint](podSelectorEndpoints...)
	// Go over the existing endpoints, and remove entries that are no longer needed
	var modifiedEndpoints []policyinfo.PolicyEndpoint
	var potentialDeletes []policyinfo.PolicyEndpoint

	// We loop through existing PolicyEndpoint resources for the current Policy and purge any stale endpoints across Ingress,
	// Egress and PodSelector endpoints. Once a PolicyEndpoint resource is updated/processed we place it in modifiedEndpoints list
	// and if a particular PolicyEndpoint resource is purged of all the endpoints, we mark it as a potential delete candidate.
	// We then start bin-packing any new Ingress, Egress, PodSelector endpoints across the existing PolicyEndpoint resources placed
	// in modified and potential delete candidate lists. We only create new PolicyEndpoint resources if we exhaust all the existing resources.
	// Any PolicyEndpoint resources placed in potentialDelete bucket that aren't utilized at the end of the binpacking flow will be permanently deleted.
	for i := range existingPolicyEndpoints {
		ingEndpointList := make([]policyinfo.EndpointInfo, 0, len(existingPolicyEndpoints[i].Spec.Ingress))
		for _, ingRule := range existingPolicyEndpoints[i].Spec.Ingress {
			ruleKey := m.getEndpointInfoKey(ingRule)
			if _, exists := ingressEndpointsMap[ruleKey]; exists {
				ingEndpointList = append(ingEndpointList, ingRule)
				delete(ingressEndpointsMap, ruleKey)
			}
		}
		egEndpointList := make([]policyinfo.EndpointInfo, 0, len(existingPolicyEndpoints[i].Spec.Egress))
		for _, egRule := range existingPolicyEndpoints[i].Spec.Egress {
			ruleKey := m.getEndpointInfoKey(egRule)
			if _, exists := egressEndpointsMap[ruleKey]; exists {
				egEndpointList = append(egEndpointList, egRule)
				delete(egressEndpointsMap, ruleKey)
			}
		}
		podSelectorEndpointList := make([]policyinfo.PodEndpoint, 0, len(existingPolicyEndpoints[i].Spec.PodSelectorEndpoints))
		for _, ps := range existingPolicyEndpoints[i].Spec.PodSelectorEndpoints {
			if podSelectorEndpointSet.Has(ps) {
				podSelectorEndpointList = append(podSelectorEndpointList, ps)
				podSelectorEndpointSet.Delete(ps)
			}
		}
		policyEndpointChanged := false
		if !equality.Semantic.DeepEqual(policyTypes, existingPolicyEndpoints[i].Spec.PodIsolation) {
			existingPolicyEndpoints[i].Spec.PodIsolation = policyTypes
			policyEndpointChanged = true
		}

		if len(ingEndpointList) == 0 && len(egEndpointList) == 0 && len(podSelectorEndpointList) == 0 {
			existingPolicyEndpoints[i].Spec.Ingress = ingEndpointList
			existingPolicyEndpoints[i].Spec.Egress = egEndpointList
			existingPolicyEndpoints[i].Spec.PodSelectorEndpoints = podSelectorEndpointList
			potentialDeletes = append(potentialDeletes, existingPolicyEndpoints[i])
		} else if len(existingPolicyEndpoints[i].Spec.Ingress) != len(ingEndpointList) || len(existingPolicyEndpoints[i].Spec.Egress) != len(egEndpointList) ||
			len(existingPolicyEndpoints[i].Spec.PodSelectorEndpoints) != len(podSelectorEndpointList) || policyEndpointChanged {
			existingPolicyEndpoints[i].Spec.Ingress = ingEndpointList
			existingPolicyEndpoints[i].Spec.Egress = egEndpointList
			existingPolicyEndpoints[i].Spec.PodSelectorEndpoints = podSelectorEndpointList
			modifiedEndpoints = append(modifiedEndpoints, existingPolicyEndpoints[i])
		} else {
			modifiedEndpoints = append(modifiedEndpoints, existingPolicyEndpoints[i])
		}
	}
	return ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, modifiedEndpoints, potentialDeletes
}

// packingIngressRules iterates over ingress rules across available policy endpoints and required ingress rule changes.
// it returns the ingress rules packed in policy endpoints and a set of policy endpoints that need to be kept.
func (m *policyEndpointsManager) packingIngressRules(metadata PolicyMetadata,
	rulesMap map[string]policyinfo.EndpointInfo,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName]) {
	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0
	ingressList := maps.Keys(rulesMap)

	// try to fill existing polciy endpoints first and then new ones if needed
	for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{modifiedEndpoints, potentialDeletes, createPolicyEndpoints} {
		for i := range sliceToCheck {
			// reset start pointer if end pointer is updated
			chunkStartIdx = chunkEndIdx
			// Instead of adding the entire chunk we should try to add to full the slice
			if len(sliceToCheck[i].Spec.Ingress) < m.endpointChunkSize && chunkEndIdx < len(ingressList) {
				for len(sliceToCheck[i].Spec.Ingress)+(chunkEndIdx-chunkStartIdx+1) < m.endpointChunkSize && chunkEndIdx < len(ingressList)-1 {
					chunkEndIdx++
				}

				sliceToCheck[i].Spec.Ingress = append(sliceToCheck[i].Spec.Ingress, m.getListOfEndpointInfoFromHash(lo.Slice(ingressList, chunkStartIdx, chunkEndIdx+1), rulesMap)...)
				// move the end to next available index to prepare next appending
				chunkEndIdx++
			}
			// as long as the second pointer moves, we need to include the PE
			if chunkStartIdx != chunkEndIdx {
				doNotDelete.Insert(k8s.NamespacedName(&sliceToCheck[i]))
			}
		}
	}

	// if the incoming ingress rules haven't been all processed yet, we need new PE(s).
	if chunkEndIdx < len(ingressList) {
		ingressRuleChunks := lo.Chunk(ingressList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range ingressRuleChunks {
			newEP := m.newPolicyEndpoint(metadata, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil, nil)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete
}

// packingEgressRules iterates over egress rules across available policy endpoints and required egress rule changes.
// it returns the egress rules packed in policy endpoints and a set of policy endpoints that need to be kept.
func (m *policyEndpointsManager) packingEgressRules(metadata PolicyMetadata,
	rulesMap map[string]policyinfo.EndpointInfo,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName]) {
	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0
	egressList := maps.Keys(rulesMap)

	// try to fill existing polciy endpoints first and then new ones if needed
	for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{modifiedEndpoints, potentialDeletes, createPolicyEndpoints} {
		for i := range sliceToCheck {
			// reset start pointer if end pointer is updated
			chunkStartIdx = chunkEndIdx
			// Instead of adding the entire chunk we should try to add to full the slice
			if len(sliceToCheck[i].Spec.Egress) < m.endpointChunkSize && chunkEndIdx < len(egressList) {
				for len(sliceToCheck[i].Spec.Egress)+(chunkEndIdx-chunkStartIdx+1) < m.endpointChunkSize && chunkEndIdx < len(egressList)-1 {
					chunkEndIdx++
				}

				sliceToCheck[i].Spec.Egress = append(sliceToCheck[i].Spec.Egress, m.getListOfEndpointInfoFromHash(lo.Slice(egressList, chunkStartIdx, chunkEndIdx+1), rulesMap)...)
				// move the end to next available index to prepare next appending
				chunkEndIdx++
			}
			// as long as the second pointer moves, we need to include the PE
			if chunkStartIdx != chunkEndIdx {
				doNotDelete.Insert(k8s.NamespacedName(&sliceToCheck[i]))
			}
		}
	}

	// if the incoming egress rules haven't been all processed yet, we need new PE(s).
	if chunkEndIdx < len(egressList) {
		egressRuleChunks := lo.Chunk(egressList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range egressRuleChunks {
			newEP := m.newPolicyEndpoint(metadata, nil, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete
}

// packingPodSelectorEndpoints iterates over pod selectors across available policy endpoints and required pod selector changes.
// it returns the pod selectors packed in policy endpoints and a set of policy endpoints that need to be kept.
func (m *policyEndpointsManager) packingPodSelectorEndpoints(metadata PolicyMetadata,
	psList []policyinfo.PodEndpoint,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName]) {

	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0

	// try to fill existing polciy endpoints first and then new ones if needed
	for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{modifiedEndpoints, potentialDeletes, createPolicyEndpoints} {
		for i := range sliceToCheck {
			// reset start pointer if end pointer is updated
			chunkStartIdx = chunkEndIdx
			// Instead of adding the entire chunk we should try to add to full the slice
			if len(sliceToCheck[i].Spec.PodSelectorEndpoints) < m.endpointChunkSize && chunkEndIdx < len(psList) {
				for len(sliceToCheck[i].Spec.PodSelectorEndpoints)+(chunkEndIdx-chunkStartIdx+1) < m.endpointChunkSize && chunkEndIdx < len(psList)-1 {
					chunkEndIdx++
				}

				sliceToCheck[i].Spec.PodSelectorEndpoints = append(sliceToCheck[i].Spec.PodSelectorEndpoints, lo.Slice(psList, chunkStartIdx, chunkEndIdx+1)...)
				// move the end to next available index to prepare next appending
				chunkEndIdx++
			}
			// as long as the second pointer moves, we need to include the PE
			if chunkStartIdx != chunkEndIdx {
				doNotDelete.Insert(k8s.NamespacedName(&sliceToCheck[i]))
			}
		}
	}

	// if the incoming podselectors haven't been all processed yet, we need new PE(s).
	if chunkEndIdx < len(psList) {
		psChunks := lo.Chunk(psList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range psChunks {
			newEP := m.newPolicyEndpoint(metadata, nil, nil, chunk)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete
}

// computeApplicationNetworkPolicyEndpoints computes the policy endpoints for the given ANP
// The return values are list of policy endpoints to create, update and delete
func (m *policyEndpointsManager) computeApplicationNetworkPolicyEndpoints(anp *policyinfo.ApplicationNetworkPolicy,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint) ([]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint, error) {

	// Process existing endpoints for ANP
	ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, modifiedEndpoints, potentialDeletes := m.processExistingPolicyEndpoints(
		anp.Spec.PolicyTypes, existingPolicyEndpoints, ingressEndpoints, egressEndpoints, podSelectorEndpoints,
	)

	doNotDelete := sets.Set[types.NamespacedName]{}

	var createPolicyEndpoints []policyinfo.PolicyEndpoint
	var updatePolicyEndpoints []policyinfo.PolicyEndpoint
	var deletePolicyEndpoints []policyinfo.PolicyEndpoint

	// Create metadata for ApplicationNetworkPolicy
	anpMetadata := PolicyMetadata{
		Name:        anp.Name,
		Namespace:   anp.Namespace,
		UID:         anp.UID,
		PodSelector: anp.Spec.PodSelector,
		PolicyTypes: anp.Spec.PolicyTypes,
		APIVersion:  "networking.k8s.aws/v1alpha1",
		Kind:        "ApplicationNetworkPolicy",
	}

	// Reuse existing packing functions - ANP ingress rules are identical to NetworkPolicy
	createPolicyEndpoints, doNotDeleteIngress := m.packingIngressRules(anpMetadata, ingressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes)
	createPolicyEndpoints, doNotDeleteEgress := m.packingEgressRules(anpMetadata, egressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes)
	createPolicyEndpoints, doNotDeletePs := m.packingPodSelectorEndpoints(anpMetadata, podSelectorEndpointSet.UnsortedList(), createPolicyEndpoints, modifiedEndpoints, potentialDeletes)

	doNotDelete.Insert(doNotDeleteIngress.UnsortedList()...)
	doNotDelete.Insert(doNotDeleteEgress.UnsortedList()...)
	doNotDelete.Insert(doNotDeletePs.UnsortedList()...)

	for _, ep := range potentialDeletes {
		if doNotDelete.Has(k8s.NamespacedName(&ep)) {
			updatePolicyEndpoints = append(updatePolicyEndpoints, ep)
		} else {
			deletePolicyEndpoints = append(deletePolicyEndpoints, ep)
		}
	}
	updatePolicyEndpoints = append(updatePolicyEndpoints, modifiedEndpoints...)
	if len(createPolicyEndpoints) == 0 && len(updatePolicyEndpoints) == 0 {
		if len(deletePolicyEndpoints) == 0 {
			newEP := m.newPolicyEndpoint(anpMetadata, nil, nil, nil)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		} else {
			ep := deletePolicyEndpoints[0]
			updatePolicyEndpoints = append(updatePolicyEndpoints, ep)
			deletePolicyEndpoints = deletePolicyEndpoints[1:]
		}
	}

	return m.processANPPolicyEndpoints(createPolicyEndpoints), m.processANPPolicyEndpoints(updatePolicyEndpoints), deletePolicyEndpoints, nil
}

func (m *policyEndpointsManager) ReconcileCNP(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) error {
	computeStart := time.Now()

	ingressRules, egressRules, podSelectorEndpoints, err := m.cnpEndpointsResolver.ResolveClusterNetworkPolicy(ctx, cnp)
	if err != nil {
		return err
	}

	// List existing ClusterPolicyEndpoints for this CNP
	clusterPolicyEndpointList := &policyinfo.ClusterPolicyEndpointList{}
	if err := m.k8sClient.List(ctx, clusterPolicyEndpointList,
		client.MatchingFields{IndexKeyClusterPolicyReferenceName: cnp.Name}); err != nil {
		return err
	}

	existingClusterPolicyEndpoints := clusterPolicyEndpointList.Items

	createList, updateList, deleteList, err := m.computeClusterPolicyEndpoints(cnp, existingClusterPolicyEndpoints, ingressRules, egressRules, podSelectorEndpoints)
	if err != nil {
		return err
	}

	computeDuration := time.Since(computeStart)
	crudStart := time.Now()

	// Create, update, delete ClusterPolicyEndpoints
	for i := range createList {
		setLastChangeTriggerTimeOnCPE(&createList[i])
		if err := m.k8sClient.Create(ctx, &createList[i]); err != nil {
			return err
		}
		m.logger.Info("Created cluster policy endpoint", "name", createList[i].Name)
	}

	for i := range updateList {
		oldRes := &policyinfo.ClusterPolicyEndpoint{}
		if err := m.k8sClient.Get(ctx, k8s.NamespacedName(&updateList[i]), oldRes); err != nil {
			return err
		}
		if policyequality.EqualClusterPolicyEndpointSpec(oldRes.Spec, updateList[i].Spec) {
			m.logger.V(1).Info("Cluster policy endpoint already up to date", "id", k8s.NamespacedName(&updateList[i]))
			continue
		}
		setLastChangeTriggerTimeOnCPE(&updateList[i])
		if err := m.k8sClient.Update(ctx, &updateList[i]); err != nil {
			return err
		}
		m.logger.Info("Updated cluster policy endpoint", "name", updateList[i].Name)
	}

	for _, cpe := range deleteList {
		if err := m.k8sClient.Delete(ctx, &cpe); err != nil {
			return err
		}
		m.logger.Info("Deleted cluster policy endpoint", "name", cpe.Name)
	}

	m.logger.Info("CNP reconcile phase durations", "policy", cnp.Name,
		"computeDuration", computeDuration.Seconds(), "crudDuration", time.Since(crudStart).Seconds(),
		"create", len(createList), "update", len(updateList), "delete", len(deleteList))

	return nil
}

func (m *policyEndpointsManager) CleanupCNP(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) error {
	clusterPolicyEndpointList := &policyinfo.ClusterPolicyEndpointList{}
	if err := m.k8sClient.List(ctx, clusterPolicyEndpointList,
		client.MatchingFields{IndexKeyClusterPolicyReferenceName: cnp.Name}); err != nil {
		return err
	}

	for _, cpe := range clusterPolicyEndpointList.Items {
		if err := m.k8sClient.Delete(ctx, &cpe); err != nil {
			return err
		}
		m.logger.Info("Cleaned up cluster policy endpoint", "name", cpe.Name)
	}
	return nil
}

func (m *policyEndpointsManager) computeClusterPolicyEndpoints(cnp *policyinfo.ClusterNetworkPolicy, existingCPEs []policyinfo.ClusterPolicyEndpoint, ingressRules []policyinfo.ClusterEndpointInfo, egressRules []policyinfo.ClusterEndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint) ([]policyinfo.ClusterPolicyEndpoint, []policyinfo.ClusterPolicyEndpoint, []policyinfo.ClusterPolicyEndpoint, error) {
	// Create metadata for ClusterNetworkPolicy
	cnpMetadata := ClusterPolicyMetadata{
		Name:     cnp.Name,
		UID:      cnp.UID,
		Tier:     cnp.Spec.Tier,
		Priority: cnp.Spec.Priority,
		Subject:  cnp.Spec.Subject,
	}

	// Process existing CPEs and convert rules to maps for chunking
	ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, modifiedCPEs, potentialDeletes := m.processExistingClusterPolicyEndpoints(
		existingCPEs, ingressRules, egressRules, podSelectorEndpoints, cnpMetadata,
	)

	doNotDelete := sets.Set[types.NamespacedName]{}
	var createCPEs []policyinfo.ClusterPolicyEndpoint
	var updateCPEs []policyinfo.ClusterPolicyEndpoint
	var deleteCPEs []policyinfo.ClusterPolicyEndpoint

	// Use chunking logic (same pattern as NP/ANP)
	createCPEs, doNotDeleteIngress := m.packingClusterIngressRules(cnpMetadata, ingressEndpointsMap, createCPEs, modifiedCPEs, potentialDeletes)
	createCPEs, doNotDeleteEgress := m.packingClusterEgressRules(cnpMetadata, egressEndpointsMap, createCPEs, modifiedCPEs, potentialDeletes)
	createCPEs, doNotDeletePs := m.packingClusterPodSelectorEndpoints(cnpMetadata, podSelectorEndpointSet.UnsortedList(), createCPEs, modifiedCPEs, potentialDeletes)

	doNotDelete.Insert(doNotDeleteIngress.UnsortedList()...)
	doNotDelete.Insert(doNotDeleteEgress.UnsortedList()...)
	doNotDelete.Insert(doNotDeletePs.UnsortedList()...)

	for _, cpe := range potentialDeletes {
		if doNotDelete.Has(types.NamespacedName{Name: cpe.Name}) {
			updateCPEs = append(updateCPEs, cpe)
		} else {
			deleteCPEs = append(deleteCPEs, cpe)
		}
	}
	updateCPEs = append(updateCPEs, modifiedCPEs...)

	// Ensure at least one CPE exists
	if len(createCPEs) == 0 && len(updateCPEs) == 0 {
		if len(deleteCPEs) == 0 {
			newCPE := m.newClusterPolicyEndpoint(cnpMetadata, nil, nil, nil)
			createCPEs = append(createCPEs, newCPE)
		} else {
			cpe := deleteCPEs[0]
			updateCPEs = append(updateCPEs, cpe)
			deleteCPEs = deleteCPEs[1:]
		}
	}

	return m.processClusterPolicyEndpoints(createCPEs), m.processClusterPolicyEndpoints(updateCPEs), deleteCPEs, nil
}

func (m *policyEndpointsManager) processClusterPolicyEndpoints(cpes []policyinfo.ClusterPolicyEndpoint) []policyinfo.ClusterPolicyEndpoint {
	var newCPEs []policyinfo.ClusterPolicyEndpoint
	for _, cpe := range cpes {
		cpe.Spec.Ingress = combineClusterRulesEndpoints(cpe.Spec.Ingress)
		cpe.Spec.Egress = combineClusterRulesEndpoints(cpe.Spec.Egress)
		newCPEs = append(newCPEs, cpe)
	}
	return newCPEs
}

// combineClusterRulesEndpoints consolidates ClusterEndpointInfo entries with the same CIDR/DomainName and Action
func combineClusterRulesEndpoints(endpoints []policyinfo.ClusterEndpointInfo) []policyinfo.ClusterEndpointInfo {
	combinedMap := make(map[string]policyinfo.ClusterEndpointInfo)
	for _, ep := range endpoints {
		var key string
		if ep.CIDR != "" {
			key = string(ep.CIDR) + ":" + string(ep.Action)
		} else if ep.DomainName != "" {
			key = string(ep.DomainName) + ":" + string(ep.Action)
		} else {
			continue // Skip invalid entries
		}

		if existing, ok := combinedMap[key]; ok {
			existing.Ports = deduplicatePorts(append(existing.Ports, ep.Ports...))
			combinedMap[key] = existing
		} else {
			combinedMap[key] = ep
		}
	}

	if len(combinedMap) > 0 {
		return maps.Values(combinedMap)
	}
	return nil
}

// deduplicatePorts removes duplicate port entries
func deduplicatePorts(ports []policyinfo.Port) []policyinfo.Port {
	seen := make(map[string]bool)
	var result []policyinfo.Port

	for _, port := range ports {
		protocol := "TCP"
		if port.Protocol != nil {
			protocol = string(*port.Protocol)
		}
		portNum := int32(0)
		if port.Port != nil {
			portNum = *port.Port
		}
		endPort := int32(0)
		if port.EndPort != nil {
			endPort = *port.EndPort
		}

		key := fmt.Sprintf("%s:%d:%d", protocol, portNum, endPort)
		if !seen[key] {
			seen[key] = true
			result = append(result, port)
		}
	}

	return result
}
