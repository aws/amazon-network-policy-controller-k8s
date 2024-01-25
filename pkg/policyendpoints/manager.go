package policyendpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/utils/conditions"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/utils/conversions"
)

type PolicyEndpointsManager interface {
	Reconcile(ctx context.Context, policy *networking.NetworkPolicy) error
	Cleanup(ctx context.Context, policy *networking.NetworkPolicy) error
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

const (
	ingressShift = 2
	egressShift  = 1
	psShift      = 0

	ingBit = 4
	egBit  = 2
	psBit  = 1

	reasonBinPacking = "PEBinPacked"
	reasonPatching   = "PEPatched"
)

var _ PolicyEndpointsManager = (*policyEndpointsManager)(nil)

type policyEndpointsManager struct {
	k8sClient         client.Client
	endpointsResolver resolvers.EndpointsResolver
	endpointChunkSize int
	logger            logr.Logger
}

func (m *policyEndpointsManager) Reconcile(ctx context.Context, policy *networking.NetworkPolicy) error {
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

	createList, updateList, deleteList, packed, err := m.computePolicyEndpoints(policy, policyEndpointList.Items, ingressRules, egressRules, podSelectorEndpoints)
	m.logger.Info("the controller is packing PE rules", "Packed", conversions.IntToBool(packed))
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
		peId := k8s.NamespacedName(&policyEndpoint)
		if err := m.k8sClient.Get(ctx, peId, oldRes); err != nil {
			return err
		}
		if equality.Semantic.DeepEqual(oldRes.Spec, policyEndpoint.Spec) {
			m.logger.V(1).Info("Policy endpoint already up to date", "id", peId)
			continue
		}

		if err := m.k8sClient.Patch(ctx, &policyEndpoint, client.MergeFrom(oldRes)); err != nil {
			if cErr := conditions.UpdatePEConditions(ctx, m.k8sClient,
				peId,
				m.logger,
				policyinfo.Updated,
				corev1.ConditionFalse,
				reasonPatching,
				fmt.Sprintf("patching policy endpoint failed: %s", err.Error()),
			); cErr != nil {
				m.logger.Error(cErr, "Adding PE patch failure condition updates to PE failed", "PENamespacedName", peId)
			}
			return err
		}
		m.logger.Info("Updated policy endpoint", "id", peId)

		if packed > 0 {
			if err := conditions.UpdatePEConditions(ctx, m.k8sClient,
				peId,
				m.logger,
				policyinfo.Packed,
				corev1.ConditionTrue,
				reasonBinPacking,
				fmt.Sprintf("binpacked network policy endpoint slices on Ingress - %t, Egress - %t, PodSelector - %t", packed&ingBit>>ingressShift == 1, packed&egBit>>egressShift == 1, packed&psBit>>psShift == 1),
			); err != nil {
				m.logger.Error(err, "Adding bingpacking condition updates to PE failed", "PENamespacedName", peId)
			}
		}
	}

	for _, policyEndpoint := range deleteList {
		if err := m.k8sClient.Delete(ctx, &policyEndpoint); err != nil {
			return err
		}
		m.logger.Info("Deleted policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}

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

// computePolicyEndpoints computes the policy endpoints for the given policy
// The return values are list of policy endpoints to create, update and delete
func (m *policyEndpointsManager) computePolicyEndpoints(policy *networking.NetworkPolicy,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint) ([]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint, int, error) {

	// Loop through ingressEndpoints, egressEndpoints and podSelectorEndpoints and put in map
	// also populate them into policy endpoints
	ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, modifiedEndpoints, potentialDeletes := m.processExistingPolicyEndpoints(
		policy, existingPolicyEndpoints, ingressEndpoints, egressEndpoints, podSelectorEndpoints,
	)

	doNotDelete := sets.Set[types.NamespacedName]{}

	var createPolicyEndpoints []policyinfo.PolicyEndpoint
	var updatePolicyEndpoints []policyinfo.PolicyEndpoint
	var deletePolicyEndpoints []policyinfo.PolicyEndpoint

	// packing new ingress rules
	createPolicyEndpoints, doNotDeleteIngress, ingPacked := m.packingIngressRules(policy, ingressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes)
	// packing new egress rules
	createPolicyEndpoints, doNotDeleteEgress, egPacked := m.packingEgressRules(policy, egressEndpointsMap, createPolicyEndpoints, modifiedEndpoints, potentialDeletes)
	// packing new pod selector
	createPolicyEndpoints, doNotDeletePs, psPacked := m.packingPodSelectorEndpoints(policy, podSelectorEndpointSet.UnsortedList(), createPolicyEndpoints, modifiedEndpoints, potentialDeletes)

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
			newEP := m.newPolicyEndpoint(policy, nil, nil, nil)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		} else {
			ep := deletePolicyEndpoints[0]
			updatePolicyEndpoints = append(updatePolicyEndpoints, ep)
			deletePolicyEndpoints = deletePolicyEndpoints[1:]
		}
	}

	return createPolicyEndpoints, updatePolicyEndpoints, deletePolicyEndpoints, (conversions.BoolToint(ingPacked) << ingressShift) | (conversions.BoolToint(egPacked) << egressShift) | (conversions.BoolToint(psPacked) << psShift), nil
}

func (m *policyEndpointsManager) newPolicyEndpoint(policy *networking.NetworkPolicy,
	ingressRules []policyinfo.EndpointInfo, egressRules []policyinfo.EndpointInfo,
	podSelectorEndpoints []policyinfo.PodEndpoint) policyinfo.PolicyEndpoint {
	blockOwnerDeletion := true
	isController := true
	policyEndpoint := policyinfo.PolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    policy.Namespace,
			GenerateName: policy.Name + "-",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "networking.k8s.io/v1",
					Kind:               "NetworkPolicy",
					Name:               policy.Name,
					UID:                policy.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
					Controller:         &isController,
				},
			},
		},
		Spec: policyinfo.PolicyEndpointSpec{
			PodSelector:          &policy.Spec.PodSelector,
			PodSelectorEndpoints: podSelectorEndpoints,
			PolicyRef: policyinfo.PolicyReference{
				Namespace: policy.Namespace,
				Name:      policy.Name,
			},
			PodIsolation: policy.Spec.PolicyTypes,
			Ingress:      ingressRules,
			Egress:       egressRules,
		},
	}

	// if no pod selector is specified, the controller adds a boolean value true to AllPodsInNamespace
	if policy.Spec.PodSelector.Size() == 0 {
		m.logger.Info("Creating a new PE but requested NP doesn't have pod selector", "NPName", policy.Name, "NPNamespace", policy.Namespace)
		policyEndpoint.Spec.AllPodsInNamespace = true
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
	policy *networking.NetworkPolicy,
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
		if !equality.Semantic.DeepEqual(policy.Spec.PolicyTypes, existingPolicyEndpoints[i].Spec.PodIsolation) {
			existingPolicyEndpoints[i].Spec.PodIsolation = policy.Spec.PolicyTypes
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
func (m *policyEndpointsManager) packingIngressRules(policy *networking.NetworkPolicy,
	rulesMap map[string]policyinfo.EndpointInfo,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName], bool) {
	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0
	ingressList := maps.Keys(rulesMap)

	packed := false

	// try to fill existing polciy endpoints first and then new ones if needed
	for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{modifiedEndpoints, potentialDeletes, createPolicyEndpoints} {
		for i := range sliceToCheck {
			// reset start pointer if end pointer is updated
			chunkStartIdx = chunkEndIdx

			// Instead of adding the entire chunk we should try to add to full the slice
			// when new ingress rule list is greater than available spots in current non-empty PE rule's list, we do binpacking
			spots := m.endpointChunkSize - len(sliceToCheck[i].Spec.Ingress)
			packed = spots > 0 && len(sliceToCheck[i].Spec.Ingress) > 0 && spots < len(ingressList)

			if spots > 0 && chunkEndIdx < len(ingressList) {
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
			newEP := m.newPolicyEndpoint(policy, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil, nil)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete, packed
}

// packingEgressRules iterates over egress rules across available policy endpoints and required egress rule changes.
// it returns the egress rules packed in policy endpoints and a set of policy endpoints that need to be kept.
func (m *policyEndpointsManager) packingEgressRules(policy *networking.NetworkPolicy,
	rulesMap map[string]policyinfo.EndpointInfo,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName], bool) {
	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0
	egressList := maps.Keys(rulesMap)

	packed := false

	// try to fill existing polciy endpoints first and then new ones if needed
	for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{modifiedEndpoints, potentialDeletes, createPolicyEndpoints} {
		for i := range sliceToCheck {
			// reset start pointer if end pointer is updated
			chunkStartIdx = chunkEndIdx

			// Instead of adding the entire chunk we should try to add to full the slice
			// when new egress rule list is greater than available spots in current non-empty PE rule's list, we do binpacking
			spots := m.endpointChunkSize - len(sliceToCheck[i].Spec.Egress)
			packed = spots > 0 && len(sliceToCheck[i].Spec.Egress) > 0 && spots < len(egressList)

			if spots > 0 && chunkEndIdx < len(egressList) {
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
			newEP := m.newPolicyEndpoint(policy, nil, m.getListOfEndpointInfoFromHash(chunk, rulesMap), nil)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete, packed
}

// packingPodSelectorEndpoints iterates over pod selectors across available policy endpoints and required pod selector changes.
// it returns the pod selectors packed in policy endpoints and a set of policy endpoints that need to be kept.
func (m *policyEndpointsManager) packingPodSelectorEndpoints(policy *networking.NetworkPolicy,
	psList []policyinfo.PodEndpoint,
	createPolicyEndpoints, modifiedEndpoints, potentialDeletes []policyinfo.PolicyEndpoint) ([]policyinfo.PolicyEndpoint, sets.Set[types.NamespacedName], bool) {

	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0
	packed := false

	// try to fill existing polciy endpoints first and then new ones if needed
	for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{modifiedEndpoints, potentialDeletes, createPolicyEndpoints} {
		for i := range sliceToCheck {
			// reset start pointer if end pointer is updated
			chunkStartIdx = chunkEndIdx

			// Instead of adding the entire chunk we should try to add to full the slice
			// when new pods list is greater than available spots in current non-empty PS list, we do binpacking
			spots := m.endpointChunkSize - len(sliceToCheck[i].Spec.PodSelectorEndpoints)
			packed = spots > 0 && len(sliceToCheck[i].Spec.PodSelectorEndpoints) > 0 && spots < len(psList)

			if spots > 0 && chunkEndIdx < len(psList) {
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
			newEP := m.newPolicyEndpoint(policy, nil, nil, chunk)
			createPolicyEndpoints = append(createPolicyEndpoints, newEP)
		}
	}
	return createPolicyEndpoints, doNotDelete, packed
}
