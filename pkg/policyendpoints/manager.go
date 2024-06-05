package policyendpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

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
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
	adminnetworking "sigs.k8s.io/network-policy-api/apis/v1alpha1"
)

type PolicyEndpointsManager interface {
	Reconcile(ctx context.Context, policy *networking.NetworkPolicy, isAdmin bool, namespaces []corev1.Namespace) error
	ReconcileAdmin(ctx context.Context, adminpolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool, namespaces []corev1.Namespace) error
	Cleanup(ctx context.Context, policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool, namespaces []corev1.Namespace) error
}

// NewPolicyEndpointsManager constructs a new policyEndpointsManager
func NewPolicyEndpointsManager(k8sClient client.Client, endpointChunkSize int, logger logr.Logger) *policyEndpointsManager {
	endpointsResolver := resolvers.NewEndpointsResolver(k8sClient, logger.WithName("endpoints-resolver"))
	return &policyEndpointsManager{
		k8sClient:         k8sClient,
		endpointsResolver: endpointsResolver,
		endpointChunkSize: endpointChunkSize,
		logger:            logger,
	}
}

var _ PolicyEndpointsManager = (*policyEndpointsManager)(nil)

type policyEndpointsManager struct {
	k8sClient         client.Client
	endpointsResolver resolvers.EndpointsResolver
	endpointChunkSize int
	logger            logr.Logger
}

func (m *policyEndpointsManager) Reconcile(ctx context.Context, policy *networking.NetworkPolicy, isAdmin bool, namespaces []corev1.Namespace) error {
	err := m.reconcileHelper(ctx, policy, nil, false, nil)
	return err
}

func (m *policyEndpointsManager) ReconcileAdmin(ctx context.Context, adminpolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool, namespaces []corev1.Namespace) error {
	var err error
	if namespaces != nil {
		err = m.reconcileHelper(ctx, nil, adminpolicy, true, namespaces)
		return err
	}
	// Cluster wide PE in kube-system
	err = m.reconcileHelper(ctx, nil, adminpolicy, true, nil)
	return err
}

func (m *policyEndpointsManager) cleanupStalePEs(ctx context.Context, pes map[string]*policyinfo.PolicyEndpoint) error {
	for _, pe := range pes {
		if err := m.k8sClient.Delete(ctx, pe); err != nil {
			return errors.Wrap(err, "unable to delete policyendpoint")
		}
		m.logger.Info("Deleted policy endpoint", "id", k8s.NamespacedName(pe))
	}
	return nil
}

func (m *policyEndpointsManager) reconcileHelper(ctx context.Context, policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool, namespace []corev1.Namespace) error {
	ingressRules, egressRules, podSelectorEndpoints, err := m.endpointsResolver.Resolve(ctx, policy, adminpolicy, isAdmin, namespace)
	if err != nil {
		return err
	}
	policyEndpointList := &policyinfo.PolicyEndpointList{}
	if isAdmin {
		if err := m.k8sClient.List(ctx, policyEndpointList,
			client.InNamespace("kube-system"),
			client.MatchingFields{IndexKeyPolicyReferenceName: adminpolicy.Name}); err != nil {
			return err
		}
	} else {
		if err := m.k8sClient.List(ctx, policyEndpointList,
			client.InNamespace(policy.Namespace),
			client.MatchingFields{IndexKeyPolicyReferenceName: policy.Name}); err != nil {
			return err
		}
	}
	existingPolicyEndpoints := make([]policyinfo.PolicyEndpoint, 0, len(policyEndpointList.Items))
	existingPolicyEndpoints = append(existingPolicyEndpoints, policyEndpointList.Items...)

	createList, updateList, deleteList, err := m.computePolicyEndpoints(policy, adminpolicy, existingPolicyEndpoints, ingressRules, egressRules, podSelectorEndpoints, isAdmin, namespace)
	if err != nil {
		return err
	}

	m.logger.Info("Got policy endpoints lists", "create", len(createList), "update", len(updateList), "delete", len(deleteList))
	for _, policyEndpoint := range createList {
		if err := m.k8sClient.Create(ctx, &policyEndpoint); err != nil {
			return err
		}
		m.logger.Info("Created policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}

	for _, policyEndpoint := range updateList {
		oldRes := &policyinfo.PolicyEndpoint{}
		if err := m.k8sClient.Get(ctx, k8s.NamespacedName(&policyEndpoint), oldRes); err != nil {
			return err
		}
		if equality.Semantic.DeepEqual(oldRes.Spec, policyEndpoint.Spec) {
			m.logger.V(1).Info("Policy endpoint already up to date", "id", k8s.NamespacedName(&policyEndpoint))
			continue
		}
		if err := m.k8sClient.Patch(ctx, &policyEndpoint, client.MergeFrom(oldRes)); err != nil {
			return err
		}
		m.logger.Info("Updated policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}

	for _, policyEndpoint := range deleteList {
		if err := m.k8sClient.Delete(ctx, &policyEndpoint); err != nil {
			return err
		}
		m.logger.Info("Deleted policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}
	return nil
}

func (m *policyEndpointsManager) Cleanup(ctx context.Context, policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool, namespaces []corev1.Namespace) error {
	policyEndpointList := &policyinfo.PolicyEndpointList{}
	var policyEndpoints []policyinfo.PolicyEndpoint
	if isAdmin {
		if namespaces != nil {
			for _, ns := range namespaces {
				if err := m.k8sClient.List(ctx, policyEndpointList,
					client.InNamespace(ns.Name),
					client.MatchingLabels{IndexKeyPolicyReferenceName: adminpolicy.Name}); err != nil {
					return errors.Wrap(err, "unable to list policyendpoints")
				}
				policyEndpoints = append(policyEndpoints, policyEndpointList.Items...)
			}
		} else {
			if err := m.k8sClient.List(ctx, policyEndpointList,
				client.InNamespace("kube-system"),
				client.MatchingLabels{IndexKeyPolicyReferenceName: adminpolicy.Name}); err != nil {
				return errors.Wrap(err, "unable to list policyendpoints")
			}
			policyEndpoints = append(policyEndpoints, policyEndpointList.Items...)
		}
	} else {
		if err := m.k8sClient.List(ctx, policyEndpointList,
			client.InNamespace(policy.Namespace),
			client.MatchingLabels{IndexKeyPolicyReferenceName: policy.Name}); err != nil {
			return errors.Wrap(err, "unable to list policyendpoints")
		}
		policyEndpoints = append(policyEndpoints, policyEndpointList.Items...)
	}
	for _, policyEndpoint := range policyEndpoints {
		if err := m.k8sClient.Delete(ctx, &policyEndpoint); err != nil {
			return errors.Wrap(err, "unable to delete policyendpoint")
		}
		m.logger.Info("Deleted policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}
	return nil
}

// computePolicyEndpoints computes the policy endpoints for the given policy
// The return values are list of policy endpoints to create, update and delete
func (m *policyEndpointsManager) computePolicyEndpoints(policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint, isAdmin bool, namespace []corev1.Namespace) ([]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint, error) {

	// Loop through ingressEndpoints, egressEndpoints and podSelectorEndpoints and put in map
	// also populate them into policy endpoints
	ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, modifiedEndpoints, potentialDeletes := m.processExistingPolicyEndpoints(
		policy, adminpolicy, existingPolicyEndpoints, ingressEndpoints, egressEndpoints, podSelectorEndpoints, isAdmin,
	)

	doNotDelete := sets.Set[types.NamespacedName]{}

	var createPolicyEndpoints []policyinfo.PolicyEndpoint
	var updatePolicyEndpoints []policyinfo.PolicyEndpoint
	var deletePolicyEndpoints []policyinfo.PolicyEndpoint

	// packing new ingress rules
	createPolicyEndpoints, doNotDeleteIngress := m.packingIngressRules(policy, adminpolicy, ingressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes, isAdmin, namespace)
	// packing new egress rules
	createPolicyEndpoints, doNotDeleteEgress := m.packingEgressRules(policy, adminpolicy, egressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes, isAdmin, namespace)
	// packing new pod selector
	createPolicyEndpoints, doNotDeletePs := m.packingPodSelectorEndpoints(policy, adminpolicy, podSelectorEndpointSet.UnsortedList(), createPolicyEndpoints, modifiedEndpoints, potentialDeletes, isAdmin, namespace)

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
			newEP := m.newPolicyEndpoint(policy, adminpolicy, nil, nil, nil, isAdmin, namespace)
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
	m.logger.Info("manager processed policy endpoints to consolidate rules", "preLen", len(pes), "postLen", len(newPEs), "newPEs", newPEs)
	return newPEs
}

// the controller should consolidate the ingress and egress endpoints and put entries to one CIDR if they belong to a same CIDR
func combineRulesEndpoints(ingressEndpoints []policyinfo.EndpointInfo) []policyinfo.EndpointInfo {
	combinedMap := make(map[string]policyinfo.EndpointInfo)
	for _, iep := range ingressEndpoints {
		key := string(iep.CIDR) + iep.Action
		if _, ok := combinedMap[key]; ok {
			tempIEP := combinedMap[key]
			tempIEP.Ports = append(combinedMap[key].Ports, iep.Ports...)
			tempIEP.Except = append(combinedMap[key].Except, iep.Except...)
			combinedMap[key] = tempIEP
		} else {
			combinedMap[key] = iep
		}
	}
	if len(combinedMap) > 0 {
		return maps.Values(combinedMap)
	}
	return nil
}

func (m *policyEndpointsManager) newPolicyEndpoint(policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy,
	ingressRules []policyinfo.EndpointInfo, egressRules []policyinfo.EndpointInfo,
	podSelectorEndpoints []policyinfo.PodEndpoint, isAdmin bool, namespace []corev1.Namespace) policyinfo.PolicyEndpoint {
	var policyName, policyNamespace, kind, apiVersion string
	var podEndpointNamespaces []string
	var policyUID types.UID
	var priority int
	var isGlobal bool
	var podSelector *metav1.LabelSelector
	var podIsolation []networking.PolicyType
	if isAdmin {
		policyName = adminpolicy.Name
		policyNamespace = "kube-system"
		for _, ns := range namespace {
			podEndpointNamespaces = append(podEndpointNamespaces, ns.Name)
		}
		kind = "AdminNetworkPolicy"
		policyUID = adminpolicy.UID
		apiVersion = "policy.networking.k8s.io/v1alpha1"
		priority = int(adminpolicy.Spec.Priority)
		isGlobal = true
		if adminpolicy.Spec.Subject.Namespaces != nil {
			podSelector = nil
		} else {
			podSelector = &adminpolicy.Spec.Subject.Pods.PodSelector
		}
		if len(adminpolicy.Spec.Ingress) > 0 && len(adminpolicy.Spec.Egress) > 0 {
			podIsolation = []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress}
		} else if len(adminpolicy.Spec.Ingress) > 0 {
			podIsolation = []networking.PolicyType{networking.PolicyTypeIngress}
		} else {
			podIsolation = []networking.PolicyType{networking.PolicyTypeEgress}
		}
	} else {
		policyName = policy.Name
		policyNamespace = policy.Namespace
		apiVersion = "networking.k8s.io/v1"
		kind = "NetworkPolicy"
		policyUID = policy.UID
		priority = 1001
		isGlobal = false
		podSelector = &policy.Spec.PodSelector
		podIsolation = policy.Spec.PolicyTypes
	}
	blockOwnerDeletion := true
	isController := true
	policyEndpoint := policyinfo.PolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    policyNamespace,
			GenerateName: policyName + "-",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         apiVersion,
					Kind:               kind,
					Name:               policyName,
					UID:                policyUID,
					BlockOwnerDeletion: &blockOwnerDeletion,
					Controller:         &isController,
				},
			},
		},
		Spec: policyinfo.PolicyEndpointSpec{
			Namespaces:           podEndpointNamespaces,
			Priority:             priority,
			IsGlobal:             isGlobal,
			PodSelector:          podSelector,
			PodSelectorEndpoints: podSelectorEndpoints,
			PolicyRef: policyinfo.PolicyReference{
				Namespace: policyNamespace,
				Name:      policyName,
			},
			PodIsolation: podIsolation,
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

func (m *policyEndpointsManager) getEndpointInfoKey(info policyinfo.EndpointInfo) string {
	hasher := sha256.New()
	hasher.Write([]byte(info.CIDR))
	hasher.Write([]byte(info.Action))
	for _, except := range info.Except {
		hasher.Write([]byte(except))
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

// processExistingPolicyEndpoints processes the existing policies with the incoming network policy changes
// it returns required rules and pod selector changes, and potential modifications and deletions on policy endpoints.
func (m *policyEndpointsManager) processExistingPolicyEndpoints(
	policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint, isAdmin bool,
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

	// We loop through existing PolicyEndpoint resources for the current Network Policy and purge any stale endpoints across Ingress,
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
		if isAdmin {
			var podIsolation []networking.PolicyType
			if len(adminpolicy.Spec.Ingress) > 0 && len(adminpolicy.Spec.Egress) > 0 {
				podIsolation = []networking.PolicyType{"Ingress", "Egress"}
			} else if len(adminpolicy.Spec.Ingress) > 0 {
				podIsolation = []networking.PolicyType{"Ingress"}
			} else {
				podIsolation = []networking.PolicyType{"Egress"}
			}
			if !equality.Semantic.DeepEqual(podIsolation, existingPolicyEndpoints[i].Spec.PodIsolation) {
				existingPolicyEndpoints[i].Spec.PodIsolation = podIsolation
				policyEndpointChanged = true
			}
		} else {
			if !equality.Semantic.DeepEqual(policy.Spec.PolicyTypes, existingPolicyEndpoints[i].Spec.PodIsolation) {
				existingPolicyEndpoints[i].Spec.PodIsolation = policy.Spec.PolicyTypes
				policyEndpointChanged = true
			}
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
func (m *policyEndpointsManager) packingIngressRules(policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy,
	rulesMap map[string]policyinfo.EndpointInfo,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint, isAdmin bool, namespace []corev1.Namespace) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName]) {
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
			newEP := m.newPolicyEndpoint(policy, adminpolicy, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil, nil, isAdmin, namespace)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete
}

// packingEgressRules iterates over egress rules across available policy endpoints and required egress rule changes.
// it returns the egress rules packed in policy endpoints and a set of policy endpoints that need to be kept.
func (m *policyEndpointsManager) packingEgressRules(policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy,
	rulesMap map[string]policyinfo.EndpointInfo,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint, isAdmin bool, namespace []corev1.Namespace) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName]) {
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
			newEP := m.newPolicyEndpoint(policy, adminpolicy, nil, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil, isAdmin, namespace)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete
}

// packingPodSelectorEndpoints iterates over pod selectors across available policy endpoints and required pod selector changes.
// it returns the pod selectors packed in policy endpoints and a set of policy endpoints that need to be kept.
func (m *policyEndpointsManager) packingPodSelectorEndpoints(policy *networking.NetworkPolicy, adminpolicy *adminnetworking.AdminNetworkPolicy,
	psList []policyinfo.PodEndpoint,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint, isAdmin bool, namespace []corev1.Namespace) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName]) {

	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0

	var namespaces []string
	for _, ns := range namespace {
		namespaces = append(namespaces, ns.Name)
	}

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
			if isAdmin {
				sliceToCheck[i].Spec.Namespaces = namespaces
				sliceToCheck[i].Spec.Priority = int(adminpolicy.Spec.Priority)
			}
		}
	}

	// if the incoming podselectors haven't been all processed yet, we need new PE(s).
	if chunkEndIdx < len(psList) {
		psChunks := lo.Chunk(psList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range psChunks {
			newEP := m.newPolicyEndpoint(policy, adminpolicy, nil, nil, chunk, isAdmin, namespace)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete
}
