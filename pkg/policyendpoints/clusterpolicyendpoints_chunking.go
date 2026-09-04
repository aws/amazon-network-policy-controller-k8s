package policyendpoints

import (
	"github.com/samber/lo"
	"golang.org/x/exp/maps"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// ClusterPolicyMetadata contains metadata for creating ClusterPolicyEndpoints
type ClusterPolicyMetadata struct {
	Name     string
	UID      types.UID
	Tier     policyinfo.Tier
	Priority int32
	Subject  policyinfo.ClusterNetworkPolicySubject
}

// processExistingClusterPolicyEndpoints processes existing CPE objects and converts them to maps for efficient chunking
func (m *policyEndpointsManager) processExistingClusterPolicyEndpoints(
	existingCPEs []policyinfo.ClusterPolicyEndpoint,
	ingressRules []policyinfo.ClusterEndpointInfo,
	egressRules []policyinfo.ClusterEndpointInfo,
	podSelectorEndpoints []policyinfo.PodEndpoint,
	metadata ClusterPolicyMetadata) (
	map[string]policyinfo.ClusterEndpointInfo,
	map[string]policyinfo.ClusterEndpointInfo,
	sets.Set[policyinfo.PodEndpoint],
	[]policyinfo.ClusterPolicyEndpoint,
	[]policyinfo.ClusterPolicyEndpoint) {

	// Create maps for new rules
	ingressEndpointsMap := make(map[string]policyinfo.ClusterEndpointInfo)
	for _, rule := range ingressRules {
		key := m.getClusterEndpointInfoKey(rule)
		ingressEndpointsMap[key] = rule
	}

	egressEndpointsMap := make(map[string]policyinfo.ClusterEndpointInfo)
	for _, rule := range egressRules {
		key := m.getClusterEndpointInfoKey(rule)
		egressEndpointsMap[key] = rule
	}

	podSelectorEndpointSet := sets.New(podSelectorEndpoints...)

	var modifiedCPEs []policyinfo.ClusterPolicyEndpoint
	var potentialDeletes []policyinfo.ClusterPolicyEndpoint

	// Process existing CPEs (following the same logic as regular PE processing)
	for i := range existingCPEs {
		// Filter ingress rules - keep only those that exist in the new rules
		// Keep the desired copy; a key match need not imply equality.
		ingEndpointList := make([]policyinfo.ClusterEndpointInfo, 0, len(existingCPEs[i].Spec.Ingress))
		for _, ingRule := range existingCPEs[i].Spec.Ingress {
			ruleKey := m.getClusterEndpointInfoKey(ingRule)
			if desired, exists := ingressEndpointsMap[ruleKey]; exists {
				ingEndpointList = append(ingEndpointList, *desired.DeepCopy())
				delete(ingressEndpointsMap, ruleKey)
			}
		}

		// Filter egress rules - keep only those that exist in the new rules
		egEndpointList := make([]policyinfo.ClusterEndpointInfo, 0, len(existingCPEs[i].Spec.Egress))
		for _, egRule := range existingCPEs[i].Spec.Egress {
			ruleKey := m.getClusterEndpointInfoKey(egRule)
			if desired, exists := egressEndpointsMap[ruleKey]; exists {
				egEndpointList = append(egEndpointList, *desired.DeepCopy())
				delete(egressEndpointsMap, ruleKey)
			}
		}

		// Filter pod selector endpoints - keep only those that exist in the new endpoints
		podSelectorEndpointList := make([]policyinfo.PodEndpoint, 0, len(existingCPEs[i].Spec.PodSelectorEndpoints))
		for _, ps := range existingCPEs[i].Spec.PodSelectorEndpoints {
			if podSelectorEndpointSet.Has(ps) {
				podSelectorEndpointList = append(podSelectorEndpointList, ps)
				podSelectorEndpointSet.Delete(ps)
			}
		}

		// Check if tier, priority, or subject changed (subject change will trigger pod endpoint changes)
		tierChanged := existingCPEs[i].Spec.Tier != metadata.Tier
		priorityChanged := existingCPEs[i].Spec.Priority != metadata.Priority
		subjectChanged := !equality.Semantic.DeepEqual(existingCPEs[i].Spec.Subject, metadata.Subject)

		// Update tier, priority, and subject to match current CNP spec
		existingCPEs[i].Spec.Tier = metadata.Tier
		existingCPEs[i].Spec.Priority = metadata.Priority
		existingCPEs[i].Spec.Subject = metadata.Subject

		// Determine if CPE should be modified or potentially deleted
		if len(ingEndpointList) == 0 && len(egEndpointList) == 0 && len(podSelectorEndpointList) == 0 {
			// CPE has no matching rules - mark for potential deletion
			existingCPEs[i].Spec.Ingress = ingEndpointList
			existingCPEs[i].Spec.Egress = egEndpointList
			existingCPEs[i].Spec.PodSelectorEndpoints = podSelectorEndpointList
			potentialDeletes = append(potentialDeletes, existingCPEs[i])
		} else if !equality.Semantic.DeepEqual(existingCPEs[i].Spec.Ingress, ingEndpointList) ||
			!equality.Semantic.DeepEqual(existingCPEs[i].Spec.Egress, egEndpointList) ||
			!equality.Semantic.DeepEqual(existingCPEs[i].Spec.PodSelectorEndpoints, podSelectorEndpointList) ||
			tierChanged || priorityChanged || subjectChanged {
			// CPE has changed - update it. Content, not length: an in-place
			// edit keeps the count.
			existingCPEs[i].Spec.Ingress = ingEndpointList
			existingCPEs[i].Spec.Egress = egEndpointList
			existingCPEs[i].Spec.PodSelectorEndpoints = podSelectorEndpointList
			modifiedCPEs = append(modifiedCPEs, existingCPEs[i])
		} else {
			// CPE unchanged - keep it as is
			modifiedCPEs = append(modifiedCPEs, existingCPEs[i])
		}
	}

	return ingressEndpointsMap, egressEndpointsMap, podSelectorEndpointSet, modifiedCPEs, potentialDeletes
}

// packingClusterIngressRules chunks cluster ingress rules across CPE objects
func (m *policyEndpointsManager) packingClusterIngressRules(metadata ClusterPolicyMetadata,
	rulesMap map[string]policyinfo.ClusterEndpointInfo,
	createCPEs, modifiedCPEs, potentialDeletes []policyinfo.ClusterPolicyEndpoint) ([]policyinfo.ClusterPolicyEndpoint, sets.Set[types.NamespacedName]) {

	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0
	ingressList := maps.Keys(rulesMap)

	// Fill existing CPEs first
	for _, sliceToCheck := range [][]policyinfo.ClusterPolicyEndpoint{modifiedCPEs, potentialDeletes, createCPEs} {
		for i := range sliceToCheck {
			chunkStartIdx = chunkEndIdx
			if len(sliceToCheck[i].Spec.Ingress) < m.endpointChunkSize && chunkEndIdx < len(ingressList) {
				for len(sliceToCheck[i].Spec.Ingress)+(chunkEndIdx-chunkStartIdx+1) < m.endpointChunkSize && chunkEndIdx < len(ingressList)-1 {
					chunkEndIdx++
				}
				sliceToCheck[i].Spec.Ingress = append(sliceToCheck[i].Spec.Ingress, m.getClusterEndpointInfoFromHashes(lo.Slice(ingressList, chunkStartIdx, chunkEndIdx+1), rulesMap)...)
				chunkEndIdx++
			}
			if chunkStartIdx != chunkEndIdx {
				doNotDelete.Insert(types.NamespacedName{Name: sliceToCheck[i].Name})
			}
		}
	}

	// Create new CPEs for remaining rules
	if chunkEndIdx < len(ingressList) && m.endpointChunkSize > 0 {
		ingressRuleChunks := lo.Chunk(ingressList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range ingressRuleChunks {
			newCPE := m.newClusterPolicyEndpoint(metadata, m.getClusterEndpointInfoFromHashes(chunk, rulesMap), nil, nil)
			createCPEs = append(createCPEs, newCPE)
		}
	}
	return createCPEs, doNotDelete
}

// packingClusterEgressRules chunks cluster egress rules across CPE objects
func (m *policyEndpointsManager) packingClusterEgressRules(metadata ClusterPolicyMetadata,
	rulesMap map[string]policyinfo.ClusterEndpointInfo,
	createCPEs, modifiedCPEs, potentialDeletes []policyinfo.ClusterPolicyEndpoint) ([]policyinfo.ClusterPolicyEndpoint, sets.Set[types.NamespacedName]) {

	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0
	egressList := maps.Keys(rulesMap)

	// Fill existing CPEs first
	for _, sliceToCheck := range [][]policyinfo.ClusterPolicyEndpoint{modifiedCPEs, potentialDeletes, createCPEs} {
		for i := range sliceToCheck {
			chunkStartIdx = chunkEndIdx
			if len(sliceToCheck[i].Spec.Egress) < m.endpointChunkSize && chunkEndIdx < len(egressList) {
				for len(sliceToCheck[i].Spec.Egress)+(chunkEndIdx-chunkStartIdx+1) < m.endpointChunkSize && chunkEndIdx < len(egressList)-1 {
					chunkEndIdx++
				}
				sliceToCheck[i].Spec.Egress = append(sliceToCheck[i].Spec.Egress, m.getClusterEndpointInfoFromHashes(lo.Slice(egressList, chunkStartIdx, chunkEndIdx+1), rulesMap)...)
				chunkEndIdx++
			}
			if chunkStartIdx != chunkEndIdx {
				doNotDelete.Insert(types.NamespacedName{Name: sliceToCheck[i].Name})
			}
		}
	}

	// Create new CPEs for remaining rules
	if chunkEndIdx < len(egressList) && m.endpointChunkSize > 0 {
		egressRuleChunks := lo.Chunk(egressList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range egressRuleChunks {
			newCPE := m.newClusterPolicyEndpoint(metadata, nil, m.getClusterEndpointInfoFromHashes(chunk, rulesMap), nil)
			createCPEs = append(createCPEs, newCPE)
		}
	}
	return createCPEs, doNotDelete
}

// packingClusterPodSelectorEndpoints chunks pod selector endpoints across CPE objects
func (m *policyEndpointsManager) packingClusterPodSelectorEndpoints(metadata ClusterPolicyMetadata,
	psList []policyinfo.PodEndpoint,
	createCPEs, modifiedCPEs, potentialDeletes []policyinfo.ClusterPolicyEndpoint) ([]policyinfo.ClusterPolicyEndpoint, sets.Set[types.NamespacedName]) {

	doNotDelete := sets.Set[types.NamespacedName]{}
	chunkStartIdx := 0
	chunkEndIdx := 0

	// Fill existing CPEs first
	for _, sliceToCheck := range [][]policyinfo.ClusterPolicyEndpoint{modifiedCPEs, potentialDeletes, createCPEs} {
		for i := range sliceToCheck {
			chunkStartIdx = chunkEndIdx
			if len(sliceToCheck[i].Spec.PodSelectorEndpoints) < m.endpointChunkSize && chunkEndIdx < len(psList) {
				for len(sliceToCheck[i].Spec.PodSelectorEndpoints)+(chunkEndIdx-chunkStartIdx+1) < m.endpointChunkSize && chunkEndIdx < len(psList)-1 {
					chunkEndIdx++
				}
				sliceToCheck[i].Spec.PodSelectorEndpoints = append(sliceToCheck[i].Spec.PodSelectorEndpoints, lo.Slice(psList, chunkStartIdx, chunkEndIdx+1)...)
				chunkEndIdx++
			}
			if chunkStartIdx != chunkEndIdx {
				doNotDelete.Insert(types.NamespacedName{Name: sliceToCheck[i].Name})
			}
		}
	}

	// Create new CPEs for remaining pod selectors
	if chunkEndIdx < len(psList) && m.endpointChunkSize > 0 {
		psChunks := lo.Chunk(psList[chunkEndIdx:], m.endpointChunkSize)
		for _, chunk := range psChunks {
			newCPE := m.newClusterPolicyEndpoint(metadata, nil, nil, chunk)
			createCPEs = append(createCPEs, newCPE)
		}
	}
	return createCPEs, doNotDelete
}

// newClusterPolicyEndpoint creates a new ClusterPolicyEndpoint object
func (m *policyEndpointsManager) newClusterPolicyEndpoint(metadata ClusterPolicyMetadata,
	ingressRules []policyinfo.ClusterEndpointInfo, egressRules []policyinfo.ClusterEndpointInfo,
	podSelectorEndpoints []policyinfo.PodEndpoint) policyinfo.ClusterPolicyEndpoint {

	blockOwnerDeletion := true
	isController := true
	return policyinfo.ClusterPolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: metadata.Name + "-",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "networking.k8s.aws/v1alpha1",
					Kind:               "ClusterNetworkPolicy",
					Name:               metadata.Name,
					UID:                metadata.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
					Controller:         &isController,
				},
			},
		},
		Spec: policyinfo.ClusterPolicyEndpointSpec{
			PolicyRef: policyinfo.ClusterPolicyReference{
				Name: metadata.Name,
			},
			Tier:                 metadata.Tier,
			Priority:             metadata.Priority,
			Subject:              metadata.Subject,
			PodSelectorEndpoints: podSelectorEndpoints,
			Ingress:              ingressRules,
			Egress:               egressRules,
		},
	}
}

// getClusterEndpointInfoFromHashes converts hash keys back to ClusterEndpointInfo objects
func (m *policyEndpointsManager) getClusterEndpointInfoFromHashes(hashes []string, epInfo map[string]policyinfo.ClusterEndpointInfo) []policyinfo.ClusterEndpointInfo {
	var ruleList []policyinfo.ClusterEndpointInfo
	for _, key := range hashes {
		ruleList = append(ruleList, epInfo[key])
	}
	return ruleList
}

// getClusterEndpointInfoKey generates a hash key for ClusterEndpointInfo
func (m *policyEndpointsManager) getClusterEndpointInfoKey(info policyinfo.ClusterEndpointInfo) string {
	w := newHashKeyWriter()
	w.str("cidr", string(info.CIDR))
	w.str("domain", string(info.DomainName))
	w.str("action", string(info.Action))
	w.ports(info.Ports)
	return w.sum()
}
