package policyendpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/algorithm"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

const labelPolicyName = "networking.k8s.aws/policy-name"

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
		client.MatchingLabels{labelPolicyName: policy.Name}); err != nil {
		return err
	}
	existingPolicyEndpoints := make([]policyinfo.PolicyEndpoint, 0, len(policyEndpointList.Items))
	for _, policyEndpoint := range policyEndpointList.Items {
		existingPolicyEndpoints = append(existingPolicyEndpoints, policyEndpoint)
	}

	createList, updateList, deleteList, err := m.getPolicyEndpoints(policy, existingPolicyEndpoints, ingressRules, egressRules, podSelectorEndpoints)
	if err != nil {
		return err
	}
	m.logger.V(1).Info("Got policy endpoints lists", "create", len(createList), "update", len(updateList), "delete", len(deleteList))
	for _, policyEndpoint := range createList {
		if err := m.k8sClient.Create(ctx, &policyEndpoint); err != nil {
			return err
		}
		m.logger.Info("Created policy endpoint", "id", k8s.NamespacedName(&policyEndpoint))
	}

	for _, policyEndpoint := range updateList {
		oldRes := &policyinfo.PolicyEndpoint{}
		if err := m.k8sClient.Get(ctx, k8s.NamespacedName(&policyEndpoint), oldRes); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
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

func (m *policyEndpointsManager) Cleanup(ctx context.Context, policy *networking.NetworkPolicy) error {
	policyEndpointList := &policyinfo.PolicyEndpointList{}
	if err := m.k8sClient.List(ctx, policyEndpointList,
		client.InNamespace(policy.Namespace),
		client.MatchingLabels{labelPolicyName: policy.Name}); err != nil {
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

// getPolicyEndpoints returns the policy endpoints for the given policy
// The return values are list of policy endpoints to create, update and delete
func (m *policyEndpointsManager) getPolicyEndpoints(policy *networking.NetworkPolicy,
	existingPolicyEndpoints []policyinfo.PolicyEndpoint, ingressEndpoints []policyinfo.EndpointInfo,
	egressEndpoints []policyinfo.EndpointInfo, podSelectorEndpoints []policyinfo.PodEndpoint) ([]policyinfo.PolicyEndpoint,
	[]policyinfo.PolicyEndpoint, []policyinfo.PolicyEndpoint, error) {
	var createPolicyEndpoints []policyinfo.PolicyEndpoint
	var updatePolicyEndpoints []policyinfo.PolicyEndpoint
	var deletePolicyEndpoints []policyinfo.PolicyEndpoint

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
	usedIngressRuleKeys := sets.Set[string]{}
	usedEgressRulesKeys := sets.Set[string]{}
	usedPodEndpoints := sets.Set[policyinfo.PodEndpoint]{}
	for i := range existingPolicyEndpoints {
		ingEndpointList := make([]policyinfo.EndpointInfo, 0, len(existingPolicyEndpoints[i].Spec.Ingress))
		for _, ingRule := range existingPolicyEndpoints[i].Spec.Ingress {
			ruleKey := m.getEndpointInfoKey(ingRule)
			if _, exists := ingressEndpointsMap[ruleKey]; exists {
				ingEndpointList = append(ingEndpointList, ingRule)
				usedIngressRuleKeys.Insert(ruleKey)
			}
		}
		egEndpointList := make([]policyinfo.EndpointInfo, 0, len(existingPolicyEndpoints[i].Spec.Egress))
		for _, egRule := range existingPolicyEndpoints[i].Spec.Egress {
			ruleKey := m.getEndpointInfoKey(egRule)
			if _, exists := egressEndpointsMap[ruleKey]; exists {
				egEndpointList = append(egEndpointList, egRule)
				usedEgressRulesKeys.Insert(ruleKey)
			}
		}
		podSelectorEndpointList := make([]policyinfo.PodEndpoint, 0, len(existingPolicyEndpoints[i].Spec.PodSelectorEndpoints))
		for _, ps := range existingPolicyEndpoints[i].Spec.PodSelectorEndpoints {
			if podSelectorEndpointSet.Has(ps) {
				podSelectorEndpointList = append(podSelectorEndpointList, ps)
				usedPodEndpoints.Insert(ps)
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

	remainingIngressRuleKeys := sets.Set[string]{}
	remainingEgressRulesKeys := sets.Set[string]{}
	remainingPodEndpoints := podSelectorEndpointSet.Difference(usedPodEndpoints)

	for key := range ingressEndpointsMap {
		if !usedIngressRuleKeys.Has(key) {
			remainingIngressRuleKeys.Insert(key)
		}
	}
	for key := range egressEndpointsMap {
		if !usedEgressRulesKeys.Has(key) {
			remainingEgressRulesKeys.Insert(key)
		}
	}

	ingressRuleChunks := algorithm.ChunkSlice(remainingIngressRuleKeys.UnsortedList(), m.endpointChunkSize)
	doNotDelete := sets.Set[types.NamespacedName]{}
	for _, chunk := range ingressRuleChunks {
		// check in the existing lists if chunk fits, otherwise allocate a new ep
		var assigned bool
		for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{createPolicyEndpoints, modifiedEndpoints, potentialDeletes} {
			for i := range sliceToCheck {
				if m.endpointChunkSize-len(sliceToCheck[i].Spec.Ingress) >= len(chunk) {
					sliceToCheck[i].Spec.Ingress = append(sliceToCheck[i].Spec.Ingress, m.getListOfEndpointInfoFromHash(chunk, ingressEndpointsMap)...)
					doNotDelete.Insert(k8s.NamespacedName(&sliceToCheck[i]))
					assigned = true
					break
				}
			}
			if assigned {
				break
			}
		}
		if assigned {
			continue
		}
		newEP := m.newPolicyEndpoint(policy, m.getListOfEndpointInfoFromHash(chunk, ingressEndpointsMap), nil, nil)
		createPolicyEndpoints = append(createPolicyEndpoints, newEP)
	}

	egressRuleChunks := algorithm.ChunkSlice(remainingEgressRulesKeys.UnsortedList(), m.endpointChunkSize)
	for _, chunk := range egressRuleChunks {
		// check in the existing to-update/to-delete list if chunk fits, otherwise allocate a new ep
		var assigned bool
		for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{createPolicyEndpoints, modifiedEndpoints, potentialDeletes} {
			for i := range sliceToCheck {
				if m.endpointChunkSize-len(sliceToCheck[i].Spec.Egress) >= len(chunk) {
					sliceToCheck[i].Spec.Egress = append(sliceToCheck[i].Spec.Egress, m.getListOfEndpointInfoFromHash(chunk, egressEndpointsMap)...)
					doNotDelete.Insert(k8s.NamespacedName(&sliceToCheck[i]))
					assigned = true
					break
				}
			}
			if assigned {
				break
			}
		}
		if assigned {
			continue
		}
		newEP := m.newPolicyEndpoint(policy, nil, m.getListOfEndpointInfoFromHash(chunk, egressEndpointsMap), nil)
		createPolicyEndpoints = append(createPolicyEndpoints, newEP)
	}
	podEndpointChunks := algorithm.ChunkSlice(remainingPodEndpoints.UnsortedList(), m.endpointChunkSize)
	for _, chunk := range podEndpointChunks {
		var assigned bool
		for _, sliceToCheck := range [][]policyinfo.PolicyEndpoint{createPolicyEndpoints, modifiedEndpoints, potentialDeletes} {
			for i := range sliceToCheck {
				if m.endpointChunkSize-len(sliceToCheck[i].Spec.PodSelectorEndpoints) >= len(chunk) {
					sliceToCheck[i].Spec.PodSelectorEndpoints = append(sliceToCheck[i].Spec.PodSelectorEndpoints, chunk...)
					doNotDelete.Insert(k8s.NamespacedName(&sliceToCheck[i]))
					assigned = true
					break
				}
			}
			if assigned {
				break
			}
		}
		if assigned {
			continue
		}
		newEP := m.newPolicyEndpoint(policy, nil, nil, chunk)
		createPolicyEndpoints = append(createPolicyEndpoints, newEP)
	}
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

	return createPolicyEndpoints, updatePolicyEndpoints, deletePolicyEndpoints, nil
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
			Labels: map[string]string{
				labelPolicyName: policy.Name,
			},
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
