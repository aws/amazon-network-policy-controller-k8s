package policyendpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/exp/maps"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	policyequality "github.com/aws/amazon-network-policy-controller-k8s/pkg/equality"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/metrics"
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

	ingressGroups, sharedIngressRules, egressRules, err := m.endpointsResolver.ResolveGroups(ctx, policy)
	if err != nil {
		return err
	}
	metrics.SetPolicyTargetGroups(k8s.NamespacedName(policy), len(ingressGroups))

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

	createList, updateList, deleteList, err := m.computePolicyEndpoints(policy, existingPolicyEndpoints, ingressGroups, sharedIngressRules, egressRules)
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
	metrics.ForgetPolicyTargetGroups(k8s.NamespacedName(policy))
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
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressGroups []resolvers.IngressGroup,
	sharedIngressEndpoints, egressEndpoints []policyinfo.EndpointInfo) ([]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint, error) {

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

	var createPolicyEndpoints, updatePolicyEndpoints, deletePolicyEndpoints []policyinfo.PolicyEndpoint

	if len(ingressGroups) > 1 {
		// The selected pods resolved different port sets, so each group gets its own
		// PolicyEndpoints and the packing runs over that group's existing ones only. Otherwise
		// one group's rules would be packed into another group's object.
		byGroup := map[string][]policyinfo.PolicyEndpoint{}
		for _, pe := range existingPolicyEndpoints {
			byGroup[pe.Spec.TargetGroup] = append(byGroup[pe.Spec.TargetGroup], pe)
		}
		seen := sets.New[string]()
		for _, g := range ingressGroups {
			seen.Insert(g.Key)
			c, u, d := m.packPolicyEndpointGroup(npMetadata, g.Key, byGroup[g.Key], g.Ingress, nil, g.PodEndpoints)
			createPolicyEndpoints = append(createPolicyEndpoints, c...)
			updatePolicyEndpoints = append(updatePolicyEndpoints, u...)
			deletePolicyEndpoints = append(deletePolicyEndpoints, d...)
		}
		// Egress and the ingress rules that named no port do not vary by group, so they stay
		// ungrouped and apply to every pod: one copy serves every group rather than each group
		// carrying its own. This pass also drains any pre-grouping PolicyEndpoint of the pods it
		// used to carry.
		c, u, d := m.packPolicyEndpointGroup(npMetadata, "", byGroup[""], sharedIngressEndpoints, egressEndpoints, nil)
		createPolicyEndpoints = append(createPolicyEndpoints, c...)
		updatePolicyEndpoints = append(updatePolicyEndpoints, u...)
		deletePolicyEndpoints = append(deletePolicyEndpoints, d...)
		// Objects of a group that no longer exists have nothing left to carry.
		for group, pes := range byGroup {
			if group == "" || seen.Has(group) {
				continue
			}
			deletePolicyEndpoints = append(deletePolicyEndpoints, pes...)
		}
	} else {
		var ingress []policyinfo.EndpointInfo
		var podSelectorEndpoints []policyinfo.PodEndpoint
		if len(ingressGroups) == 1 {
			ingress, podSelectorEndpoints = ingressGroups[0].Ingress, ingressGroups[0].PodEndpoints
		}
		createPolicyEndpoints, updatePolicyEndpoints, deletePolicyEndpoints = m.packPolicyEndpointGroup(
			npMetadata, "", existingPolicyEndpoints, ingress, egressEndpoints, podSelectorEndpoints)
	}

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

// packPolicyEndpointGroup runs the purge-then-binpack flow over one group's existing objects and
// stamps the group on everything it creates or keeps, so an object never carries another group's
// rules. targetGroup is empty for an ungrouped policy, which is the common case.
func (m *policyEndpointsManager) packPolicyEndpointGroup(npMetadata PolicyMetadata, targetGroup string,
	existing []policyinfo.PolicyEndpoint, ingressEndpoints, egressEndpoints []policyinfo.EndpointInfo,
	podSelectorEndpoints []policyinfo.PodEndpoint) ([]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint) {

	ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, targets := m.processExistingPolicyEndpoints(
		npMetadata.PolicyTypes, existing, ingressEndpoints, egressEndpoints, podSelectorEndpoints,
	)

	// An object that moves to this group is written for the restamp, so it is a free target too.
	for i := range targets.modified {
		if targets.modified[i].Spec.TargetGroup != targetGroup {
			targets.markWritten(&targets.modified[i])
		}
	}

	m.packingIngressRules(npMetadata, ingressEndpointsMap, targets)
	m.packingEgressRules(npMetadata, egressEndpointsMap, targets)
	m.packingPodSelectorEndpoints(npMetadata, podSelectorEndpointSet.UnsortedList(), targets)

	createPolicyEndpoints, updatePolicyEndpoints, deletePolicyEndpoints := targets.result()
	for i := range createPolicyEndpoints {
		createPolicyEndpoints[i].Spec.TargetGroup = targetGroup
	}
	for i := range updatePolicyEndpoints {
		updatePolicyEndpoints[i].Spec.TargetGroup = targetGroup
	}
	return createPolicyEndpoints, updatePolicyEndpoints, deletePolicyEndpoints
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
	var key strings.Builder
	if ep.DomainName != "" {
		key.WriteString("fqdn|" + string(ep.DomainName) + "|")
	} else {
		key.WriteString("cidr|" + string(ep.CIDR) + "|")
	}

	sortedExcepts := make([]policyinfo.NetworkAddress, len(ep.Except))
	copy(sortedExcepts, ep.Except)
	sort.Slice(sortedExcepts, func(i, j int) bool {
		return string(sortedExcepts[i]) < string(sortedExcepts[j])
	})
	// Append through the Builder: "key += ..." copies the whole key on every
	// iteration, which is quadratic in the number of excepts.
	for _, except := range sortedExcepts {
		key.WriteString(string(except) + "|")
	}

	return key.String()
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

// getEndpointInfoKey is the identity a reconcile matches stored rules against desired ones by. A
// stored rule whose key is not desired is purged and its replacement packed anew, possibly into
// another object, so the key must not change for the same rule: ports are hashed in canonical
// order. Excepts are hashed as given, since they come from the policy spec in a stable order.
func (m *policyEndpointsManager) getEndpointInfoKey(info policyinfo.EndpointInfo) string {
	hasher := sha256.New()
	// Each "field=value|" record is assembled in a small scratch buffer and
	// written straight to the hasher. This runs once per except on every
	// reconcile, so it avoids fmt and never builds the whole byte stream.
	var scratch [96]byte
	write := func(field, value string) {
		hasher.Write(append(append(append(scratch[:0], field...), value...), '|'))
	}

	// Every field is written delimited and self-describing so that no two
	// semantically different endpoints can produce the same byte stream. A bare
	// concatenation (e.g. port "1" followed by endPort "23") is ambiguous with a
	// single port "123"; the "field=value|" framing removes that ambiguity.
	// Handle FQDN case for ApplicationNetworkPolicy
	if info.DomainName != "" {
		write("domainName=", string(info.DomainName))
	} else {
		// Handle CIDR case for NetworkPolicy
		write("cidr=", string(info.CIDR))
		for _, except := range info.Except {
			write("except=", string(except))
		}
	}

	var portScratch [8]policyinfo.Port
	for _, port := range canonicalPortOrder(info.Ports, portScratch[:]) {
		write("proto=", protocolKeyPart(port.Protocol))
		write("port=", int32PtrKeyPart(port.Port))
		write("endPort=", int32PtrKeyPart(port.EndPort))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// canonicalPortOrder returns ports sorted by policyequality.ComparePorts without reordering the
// caller's slice, which a resolver may share across many rules. A sorted list is returned as is.
// An unsorted one is sorted in scratch, so a caller that passes a stack array does not allocate
// for lists that fit in it.
func canonicalPortOrder(ports, scratch []policyinfo.Port) []policyinfo.Port {
	if slices.IsSortedFunc(ports, policyequality.ComparePorts) {
		return ports
	}
	sorted := append(scratch[:0], ports...)
	slices.SortFunc(sorted, policyequality.ComparePorts)
	return sorted
}

// protocolKeyPart renders a protocol pointer for hashing, using "nil" for an
// absent value so a missing protocol is distinguishable from any real one.
func protocolKeyPart(p *corev1.Protocol) string {
	if p == nil {
		return "nil"
	}
	return string(*p)
}

// int32PtrKeyPart renders an int32 pointer for hashing, using "nil" for an
// absent value so a missing port is distinguishable from port 0.
func int32PtrKeyPart(p *int32) string {
	if p == nil {
		return "nil"
	}
	return strconv.Itoa(int(*p))
}

// processExistingPolicyEndpoints processes the existing policies with the incoming policy changes
// it returns the rules and pod selector endpoints still to place, and the existing policy endpoints to place them into.
func (m *policyEndpointsManager) processExistingPolicyEndpoints(
	policyTypes []networking.PolicyType,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint,
) (
	map[string]policyinfo.EndpointInfo,
	map[string]policyinfo.EndpointInfo,
	sets.Set[policyinfo.PodEndpoint],
	*packTargets[policyinfo.PolicyEndpoint],
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
	targets := newPolicyEndpointTargets()

	// We loop through existing PolicyEndpoint resources for the current Policy and purge any stale endpoints across Ingress,
	// Egress and PodSelector endpoints. Once a PolicyEndpoint resource is updated/processed we place it in the modified targets,
	// and if a particular PolicyEndpoint resource is purged of all the endpoints, we mark it as a potential delete candidate.
	// We then start bin-packing any new Ingress, Egress, PodSelector endpoints across the existing PolicyEndpoint resources,
	// cheapest first (see packTargets.fillOrder). We only create new PolicyEndpoint resources if we exhaust all the existing resources.
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

		empty := len(ingEndpointList) == 0 && len(egEndpointList) == 0 && len(podSelectorEndpointList) == 0
		changed := len(existingPolicyEndpoints[i].Spec.Ingress) != len(ingEndpointList) || len(existingPolicyEndpoints[i].Spec.Egress) != len(egEndpointList) ||
			len(existingPolicyEndpoints[i].Spec.PodSelectorEndpoints) != len(podSelectorEndpointList) || policyEndpointChanged
		if empty || changed {
			existingPolicyEndpoints[i].Spec.Ingress = ingEndpointList
			existingPolicyEndpoints[i].Spec.Egress = egEndpointList
			existingPolicyEndpoints[i].Spec.PodSelectorEndpoints = podSelectorEndpointList
		}
		targets.addExisting(existingPolicyEndpoints[i], empty, changed)
	}
	targets.sortPotentialDeletes()
	return ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, targets
}

// packingIngressRules iterates over ingress rules across available policy endpoints and required ingress rule changes.
// it fills the targets cheapest first and creates new policy endpoints for the ingress rules that do not fit.
func (m *policyEndpointsManager) packingIngressRules(metadata PolicyMetadata,
	rulesMap map[string]policyinfo.EndpointInfo, targets *packTargets[policyinfo.PolicyEndpoint]) {
	if len(rulesMap) == 0 {
		return
	}
	chunkStartIdx := 0
	chunkEndIdx := 0
	ingressList := maps.Keys(rulesMap)

	// fill the targets cheapest first, then create new policy endpoints if needed
	for _, sliceToCheck := range targets.fillOrder(func(pe *policyinfo.PolicyEndpoint) int { return len(pe.Spec.Ingress) }) {
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
				targets.markWritten(&sliceToCheck[i])
			}
		}
	}

	// if the incoming ingress rules haven't been all processed yet, we need new PE(s).
	if chunkEndIdx < len(ingressList) {
		ingressRuleChunks := lo.Chunk(ingressList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range ingressRuleChunks {
			newEP := m.newPolicyEndpoint(metadata, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil, nil)
			targets.created = append(targets.created, newEP)
		}
	}
}

// packingEgressRules iterates over egress rules across available policy endpoints and required egress rule changes.
// it fills the targets cheapest first and creates new policy endpoints for the egress rules that do not fit.
func (m *policyEndpointsManager) packingEgressRules(metadata PolicyMetadata,
	rulesMap map[string]policyinfo.EndpointInfo, targets *packTargets[policyinfo.PolicyEndpoint]) {
	if len(rulesMap) == 0 {
		return
	}
	chunkStartIdx := 0
	chunkEndIdx := 0
	egressList := maps.Keys(rulesMap)

	// fill the targets cheapest first, then create new policy endpoints if needed
	for _, sliceToCheck := range targets.fillOrder(func(pe *policyinfo.PolicyEndpoint) int { return len(pe.Spec.Egress) }) {
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
				targets.markWritten(&sliceToCheck[i])
			}
		}
	}

	// if the incoming egress rules haven't been all processed yet, we need new PE(s).
	if chunkEndIdx < len(egressList) {
		egressRuleChunks := lo.Chunk(egressList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range egressRuleChunks {
			newEP := m.newPolicyEndpoint(metadata, nil, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil)
			targets.created = append(targets.created, newEP)
		}
	}
}

// packingPodSelectorEndpoints iterates over pod selectors across available policy endpoints and required pod selector changes.
// it fills the targets cheapest first and creates new policy endpoints for the pod selectors that do not fit.
func (m *policyEndpointsManager) packingPodSelectorEndpoints(metadata PolicyMetadata,
	psList []policyinfo.PodEndpoint, targets *packTargets[policyinfo.PolicyEndpoint]) {
	if len(psList) == 0 {
		return
	}

	chunkStartIdx := 0
	chunkEndIdx := 0

	// fill the targets cheapest first, then create new policy endpoints if needed
	for _, sliceToCheck := range targets.fillOrder(func(pe *policyinfo.PolicyEndpoint) int { return len(pe.Spec.PodSelectorEndpoints) }) {
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
				targets.markWritten(&sliceToCheck[i])
			}
		}
	}

	// if the incoming podselectors haven't been all processed yet, we need new PE(s).
	if chunkEndIdx < len(psList) {
		psChunks := lo.Chunk(psList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range psChunks {
			newEP := m.newPolicyEndpoint(metadata, nil, nil, chunk)
			targets.created = append(targets.created, newEP)
		}
	}
}

// computeApplicationNetworkPolicyEndpoints computes the policy endpoints for the given ANP
// The return values are list of policy endpoints to create, update and delete
func (m *policyEndpointsManager) computeApplicationNetworkPolicyEndpoints(anp *policyinfo.ApplicationNetworkPolicy,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint) ([]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint, error) {

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

	// ANP rules pack exactly like an ungrouped NetworkPolicy's.
	createPolicyEndpoints, updatePolicyEndpoints, deletePolicyEndpoints := m.packPolicyEndpointGroup(anpMetadata, "",
		existingPolicyEndpoints, ingressEndpoints, egressEndpoints, podSelectorEndpoints)
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

	ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, targets := m.processExistingClusterPolicyEndpoints(
		existingCPEs, ingressRules, egressRules, podSelectorEndpoints, cnpMetadata,
	)

	m.packingClusterIngressRules(cnpMetadata, ingressEndpointsMap, targets)
	m.packingClusterEgressRules(cnpMetadata, egressEndpointsMap, targets)
	m.packingClusterPodSelectorEndpoints(cnpMetadata, podSelectorEndpointSet.UnsortedList(), targets)

	createCPEs, updateCPEs, deleteCPEs := targets.result()

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
