package policyendpoints

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

func Test_policyEndpointsManager_computeClusterPolicyEndpoints_chunking(t *testing.T) {
	tests := []struct {
		name                 string
		cnp                  *policyinfo.ClusterNetworkPolicy
		existingCPEs         []policyinfo.ClusterPolicyEndpoint
		ingressRules         []policyinfo.ClusterEndpointInfo
		egressRules          []policyinfo.ClusterEndpointInfo
		podSelectorEndpoints []policyinfo.PodEndpoint
		chunkSize            int
		expectedCreateCount  int
		expectedUpdateCount  int
		expectedDeleteCount  int
	}{
		{
			name: "no existing CPEs, create one with small data",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp", UID: "test-uid"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     "Admin",
					Priority: 100,
					Subject:  policyinfo.ClusterNetworkPolicySubject{},
				},
			},
			existingCPEs: []policyinfo.ClusterPolicyEndpoint{},
			ingressRules: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: "Allow"},
			},
			egressRules:          []policyinfo.ClusterEndpointInfo{},
			podSelectorEndpoints: []policyinfo.PodEndpoint{},
			chunkSize:            200,
			expectedCreateCount:  1,
			expectedUpdateCount:  0,
			expectedDeleteCount:  0,
		},
		{
			name: "large ingress rules requiring multiple CPEs",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp", UID: "test-uid"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     "Admin",
					Priority: 100,
					Subject:  policyinfo.ClusterNetworkPolicySubject{},
				},
			},
			existingCPEs:         []policyinfo.ClusterPolicyEndpoint{},
			ingressRules:         generateClusterEndpointInfos(250), // More than chunk size
			egressRules:          []policyinfo.ClusterEndpointInfo{},
			podSelectorEndpoints: []policyinfo.PodEndpoint{},
			chunkSize:            200,
			expectedCreateCount:  2, // 200 + 50
			expectedUpdateCount:  0,
			expectedDeleteCount:  0,
		},
		{
			name: "large pod endpoints requiring multiple CPEs",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp", UID: "test-uid"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     "Admin",
					Priority: 100,
					Subject:  policyinfo.ClusterNetworkPolicySubject{},
				},
			},
			existingCPEs:         []policyinfo.ClusterPolicyEndpoint{},
			ingressRules:         []policyinfo.ClusterEndpointInfo{},
			egressRules:          []policyinfo.ClusterEndpointInfo{},
			podSelectorEndpoints: generatePodEndpoints(300), // More than chunk size
			chunkSize:            200,
			expectedCreateCount:  2, // 200 + 100
			expectedUpdateCount:  0,
			expectedDeleteCount:  0,
		},
		{
			name: "existing CPE gets updated",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp", UID: "test-uid"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     "Admin",
					Priority: 100,
					Subject:  policyinfo.ClusterNetworkPolicySubject{},
				},
			},
			existingCPEs: []policyinfo.ClusterPolicyEndpoint{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-cnp-existing"},
					Spec: policyinfo.ClusterPolicyEndpointSpec{
						Ingress: []policyinfo.ClusterEndpointInfo{
							{CIDR: "10.0.0.0/8", Action: "Allow"},
						},
					},
				},
			},
			ingressRules: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: "Allow"},     // Same rule
				{CIDR: "192.168.0.0/16", Action: "Allow"}, // New rule
			},
			egressRules:          []policyinfo.ClusterEndpointInfo{},
			podSelectorEndpoints: []policyinfo.PodEndpoint{},
			chunkSize:            200,
			expectedCreateCount:  0,
			expectedUpdateCount:  1,
			expectedDeleteCount:  0,
		},
		{
			name: "unused CPE gets reused for new rules",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp", UID: "test-uid"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     "Admin",
					Priority: 100,
					Subject:  policyinfo.ClusterNetworkPolicySubject{},
				},
			},
			existingCPEs: []policyinfo.ClusterPolicyEndpoint{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-cnp-unused"},
					Spec: policyinfo.ClusterPolicyEndpointSpec{
						Ingress: []policyinfo.ClusterEndpointInfo{
							{CIDR: "172.16.0.0/12", Action: "Allow"}, // Rule no longer needed
						},
					},
				},
			},
			ingressRules: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: "Allow"}, // Different rule
			},
			egressRules:          []policyinfo.ClusterEndpointInfo{},
			podSelectorEndpoints: []policyinfo.PodEndpoint{},
			chunkSize:            200,
			expectedCreateCount:  0, // Existing CPE is reused
			expectedUpdateCount:  1, // Existing CPE updated with new rule
			expectedDeleteCount:  0, // No deletion, CPE is reused
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &policyEndpointsManager{
				endpointChunkSize: tt.chunkSize,
			}

			createList, updateList, deleteList, err := m.computeClusterPolicyEndpoints(
				tt.cnp, tt.existingCPEs, tt.ingressRules, tt.egressRules, tt.podSelectorEndpoints)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCreateCount, len(createList), "create count mismatch")
			assert.Equal(t, tt.expectedUpdateCount, len(updateList), "update count mismatch")
			assert.Equal(t, tt.expectedDeleteCount, len(deleteList), "delete count mismatch")

			// Verify CPE structure for created objects
			for _, cpe := range createList {
				assert.Equal(t, tt.cnp.Name+"-", cpe.GenerateName)
				assert.Equal(t, tt.cnp.Name, cpe.Spec.PolicyRef.Name)
				assert.Equal(t, tt.cnp.Spec.Tier, cpe.Spec.Tier)
				assert.Equal(t, tt.cnp.Spec.Priority, cpe.Spec.Priority)

				// Verify chunking limits
				assert.LessOrEqual(t, len(cpe.Spec.Ingress), tt.chunkSize)
				assert.LessOrEqual(t, len(cpe.Spec.Egress), tt.chunkSize)
				assert.LessOrEqual(t, len(cpe.Spec.PodSelectorEndpoints), tt.chunkSize)
			}
		})
	}
}

func Test_policyEndpointsManager_packingClusterIngressRules(t *testing.T) {
	tests := []struct {
		name                string
		rulesMap            map[string]policyinfo.ClusterEndpointInfo
		existingCPEs        []policyinfo.ClusterPolicyEndpoint
		chunkSize           int
		expectedCPEs        int
		expectedDoNotDelete int
	}{
		{
			name: "pack rules into new CPEs",
			rulesMap: map[string]policyinfo.ClusterEndpointInfo{
				"rule1": {CIDR: "10.0.0.0/8", Action: "Allow"},
				"rule2": {CIDR: "192.168.0.0/16", Action: "Allow"},
			},
			existingCPEs:        []policyinfo.ClusterPolicyEndpoint{},
			chunkSize:           200,
			expectedCPEs:        1,
			expectedDoNotDelete: 0,
		},
		{
			name:                "pack rules exceeding chunk size",
			rulesMap:            generateClusterEndpointInfoMap(250),
			existingCPEs:        []policyinfo.ClusterPolicyEndpoint{},
			chunkSize:           200,
			expectedCPEs:        2, // 200 + 50
			expectedDoNotDelete: 0,
		},
		{
			name: "reuse existing CPE",
			rulesMap: map[string]policyinfo.ClusterEndpointInfo{
				"rule1": {CIDR: "10.0.0.0/8", Action: "Allow"},
			},
			existingCPEs: []policyinfo.ClusterPolicyEndpoint{
				{ObjectMeta: metav1.ObjectMeta{Name: "existing-cpe"}},
			},
			chunkSize:           200,
			expectedCPEs:        0, // No new CPEs created, existing one is reused
			expectedDoNotDelete: 1, // Existing CPE is marked as do not delete
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &policyEndpointsManager{
				endpointChunkSize: tt.chunkSize,
			}

			metadata := ClusterPolicyMetadata{
				Name: "test-cnp",
				UID:  "test-uid",
			}

			createCPEs := []policyinfo.ClusterPolicyEndpoint{}
			modifiedCPEs := tt.existingCPEs
			potentialDeletes := []policyinfo.ClusterPolicyEndpoint{}

			resultCPEs, doNotDelete := m.packingClusterIngressRules(
				metadata, tt.rulesMap, createCPEs, modifiedCPEs, potentialDeletes)

			assert.Equal(t, tt.expectedCPEs, len(resultCPEs))
			assert.Equal(t, tt.expectedDoNotDelete, doNotDelete.Len())
		})
	}
}

func Test_policyEndpointsManager_getClusterEndpointInfoKey(t *testing.T) {
	m := &policyEndpointsManager{}

	tests := []struct {
		name        string
		info1       policyinfo.ClusterEndpointInfo
		info2       policyinfo.ClusterEndpointInfo
		shouldMatch bool
	}{
		{
			name:        "identical rules should have same key",
			info1:       policyinfo.ClusterEndpointInfo{CIDR: "10.0.0.0/8", Action: "Allow"},
			info2:       policyinfo.ClusterEndpointInfo{CIDR: "10.0.0.0/8", Action: "Allow"},
			shouldMatch: true,
		},
		{
			name:        "different CIDR should have different key",
			info1:       policyinfo.ClusterEndpointInfo{CIDR: "10.0.0.0/8", Action: "Allow"},
			info2:       policyinfo.ClusterEndpointInfo{CIDR: "192.168.0.0/16", Action: "Allow"},
			shouldMatch: false,
		},
		{
			name:        "different action should have different key",
			info1:       policyinfo.ClusterEndpointInfo{CIDR: "10.0.0.0/8", Action: "Allow"},
			info2:       policyinfo.ClusterEndpointInfo{CIDR: "10.0.0.0/8", Action: "Deny"},
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key1 := m.getClusterEndpointInfoKey(tt.info1)
			key2 := m.getClusterEndpointInfoKey(tt.info2)

			if tt.shouldMatch {
				assert.Equal(t, key1, key2)
			} else {
				assert.NotEqual(t, key1, key2)
			}
		})
	}
}

func Test_policyEndpointsManager_getClusterEndpointInfoKey_portCollisions(t *testing.T) {
	m := &policyEndpointsManager{}

	rule := func(ports ...policyinfo.Port) policyinfo.ClusterEndpointInfo {
		return policyinfo.ClusterEndpointInfo{CIDR: "10.200.0.0/16", Action: "Deny", Ports: ports}
	}
	single := func(port int32) policyinfo.Port {
		p := port
		return policyinfo.Port{Port: &p}
	}
	portRange := func(start, end int32) policyinfo.Port {
		s, e := start, end
		return policyinfo.Port{Port: &s, EndPort: &e}
	}

	// Every port across the surrogate band must hash to a distinct key.
	surrogateBand := []int32{55296, 55555, 56666, 57000, 57343}
	keys := make(map[string]int32, len(surrogateBand))
	for _, p := range surrogateBand {
		k := m.getClusterEndpointInfoKey(rule(single(p)))
		if prev, dup := keys[k]; dup {
			t.Fatalf("surrogate-band ports %d and %d hashed to the same key", prev, p)
		}
		keys[k] = p
	}

	// Editing the port 55555 -> 56666 must change the key.
	assert.NotEqual(t,
		m.getClusterEndpointInfoKey(rule(single(55555))),
		m.getClusterEndpointInfoKey(rule(single(56666))),
		"55555 and 56666 must not collide")

	// Delimiter: a port range must not alias two discrete ports.
	assert.NotEqual(t,
		m.getClusterEndpointInfoKey(rule(portRange(1, 23))),
		m.getClusterEndpointInfoKey(rule(single(1), single(23))),
		"range 1-23 must not hash the same as discrete ports 1 and 23")

	// Delimiter: adjacent-digit boundaries must not alias.
	assert.NotEqual(t,
		m.getClusterEndpointInfoKey(rule(portRange(1, 23))),
		m.getClusterEndpointInfoKey(rule(portRange(12, 3))),
		"range 1-23 must not hash the same as range 12-3")

	// Sanity: identical rules still produce the same key.
	assert.Equal(t,
		m.getClusterEndpointInfoKey(rule(single(80))),
		m.getClusterEndpointInfoKey(rule(single(80))))
}

// Helper functions for test data generation
func generateClusterEndpointInfos(count int) []policyinfo.ClusterEndpointInfo {
	rules := make([]policyinfo.ClusterEndpointInfo, count)
	for i := 0; i < count; i++ {
		// Create unique CIDR for each rule
		rules[i] = policyinfo.ClusterEndpointInfo{
			CIDR:   policyinfo.NetworkAddress(fmt.Sprintf("10.0.%d.%d/32", i/256, i%256)),
			Action: "Allow",
		}
	}
	return rules
}

func generateClusterEndpointInfoMap(count int) map[string]policyinfo.ClusterEndpointInfo {
	m := &policyEndpointsManager{}
	rules := generateClusterEndpointInfos(count)
	rulesMap := make(map[string]policyinfo.ClusterEndpointInfo)
	for _, rule := range rules {
		key := m.getClusterEndpointInfoKey(rule)
		rulesMap[key] = rule
	}
	return rulesMap
}

func generatePodEndpoints(count int) []policyinfo.PodEndpoint {
	pods := make([]policyinfo.PodEndpoint, count)
	for i := 0; i < count; i++ {
		// Create unique pod names and IPs
		pods[i] = policyinfo.PodEndpoint{
			Name:      fmt.Sprintf("pod-%d", i),
			Namespace: "default",
			PodIP:     policyinfo.NetworkAddress(fmt.Sprintf("10.0.%d.%d", i/256, i%256)),
			HostIP:    policyinfo.NetworkAddress("192.168.1.1"),
		}
	}
	return pods
}

func Test_policyEndpointsManager_processExistingClusterPolicyEndpoints_preserveMatchingRules(t *testing.T) {
	manager := &policyEndpointsManager{endpointChunkSize: 200}

	// Create existing CPE with some rules
	existingCPE := policyinfo.ClusterPolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-cpe"},
		Spec: policyinfo.ClusterPolicyEndpointSpec{
			Ingress: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: "Accept"},
				{CIDR: "192.168.1.0/24", Action: "Deny"}, // This should be removed
			},
			Egress: []policyinfo.ClusterEndpointInfo{
				{CIDR: "172.16.0.0/12", Action: "Accept"},
			},
			PodSelectorEndpoints: []policyinfo.PodEndpoint{
				{Name: "pod1", Namespace: "ns1", PodIP: "10.0.0.1", HostIP: "10.0.1.1"},
			},
		},
	}

	// New rules - some match existing, some don't
	newIngressRules := []policyinfo.ClusterEndpointInfo{
		{CIDR: "10.0.0.0/8", Action: "Accept"},  // Matches existing
		{CIDR: "10.1.0.0/16", Action: "Accept"}, // New rule
	}
	newEgressRules := []policyinfo.ClusterEndpointInfo{
		{CIDR: "172.16.0.0/12", Action: "Accept"}, // Matches existing
		{CIDR: "192.168.0.0/16", Action: "Deny"},  // New rule
	}
	newPodEndpoints := []policyinfo.PodEndpoint{
		{Name: "pod1", Namespace: "ns1", PodIP: "10.0.0.1", HostIP: "10.0.1.1"}, // Matches existing
		{Name: "pod2", Namespace: "ns2", PodIP: "10.0.0.2", HostIP: "10.0.1.2"}, // New pod
	}

	// Process existing CPEs
	testMetadata := ClusterPolicyMetadata{
		Name:     "test-cnp",
		UID:      "test-uid",
		Tier:     "Admin",
		Priority: 100,
		Subject: policyinfo.ClusterNetworkPolicySubject{
			Pods: &policyinfo.NamespacedPod{
				NamespaceSelector: metav1.LabelSelector{},
				PodSelector:       metav1.LabelSelector{},
			},
		},
	}

	ingressMap, egressMap, podSet, modifiedCPEs, potentialDeletes := manager.processExistingClusterPolicyEndpoints(
		[]policyinfo.ClusterPolicyEndpoint{existingCPE},
		newIngressRules,
		newEgressRules,
		newPodEndpoints,
		testMetadata,
	)

	// Verify results
	assert.Len(t, modifiedCPEs, 1, "Should have one modified CPE")
	assert.Len(t, potentialDeletes, 0, "Should have no potential deletes")

	modifiedCPE := modifiedCPEs[0]

	// Verify matching rules are preserved in CPE
	assert.Len(t, modifiedCPE.Spec.Ingress, 1, "Should keep only matching ingress rule")
	assert.Equal(t, policyinfo.NetworkAddress("10.0.0.0/8"), modifiedCPE.Spec.Ingress[0].CIDR, "Should keep matching ingress rule")

	assert.Len(t, modifiedCPE.Spec.Egress, 1, "Should keep only matching egress rule")
	assert.Equal(t, policyinfo.NetworkAddress("172.16.0.0/12"), modifiedCPE.Spec.Egress[0].CIDR, "Should keep matching egress rule")

	assert.Len(t, modifiedCPE.Spec.PodSelectorEndpoints, 1, "Should keep only matching pod endpoint")
	assert.Equal(t, "pod1", modifiedCPE.Spec.PodSelectorEndpoints[0].Name, "Should keep matching pod endpoint")

	// Verify non-matching rules are removed from maps (available for new CPEs)
	assert.Len(t, ingressMap, 1, "Should have one remaining ingress rule in map")
	assert.Contains(t, ingressMap, manager.getClusterEndpointInfoKey(policyinfo.ClusterEndpointInfo{CIDR: "10.1.0.0/16", Action: "Accept"}))

	assert.Len(t, egressMap, 1, "Should have one remaining egress rule in map")
	assert.Contains(t, egressMap, manager.getClusterEndpointInfoKey(policyinfo.ClusterEndpointInfo{CIDR: "192.168.0.0/16", Action: "Deny"}))

	assert.Len(t, podSet, 1, "Should have one remaining pod endpoint in set")
	assert.True(t, podSet.Has(policyinfo.PodEndpoint{Name: "pod2", Namespace: "ns2", PodIP: "10.0.0.2", HostIP: "10.0.1.2"}))
}
func Test_policyEndpointsManager_processExistingClusterPolicyEndpoints_updateTierAndPriority(t *testing.T) {
	manager := &policyEndpointsManager{
		endpointChunkSize: 100,
	}

	// Create existing CPE with old tier and priority
	existingCPE := policyinfo.ClusterPolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cpe-1",
		},
		Spec: policyinfo.ClusterPolicyEndpointSpec{
			Tier:     policyinfo.Tier("System"), // Old tier
			Priority: 50,                        // Old priority
			Ingress: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: "Accept"},
			},
		},
	}

	// New metadata with updated tier and priority
	newMetadata := ClusterPolicyMetadata{
		Name:     "test-cnp",
		UID:      "test-uid",
		Tier:     "Admin", // New tier
		Priority: 100,     // New priority
		Subject: policyinfo.ClusterNetworkPolicySubject{
			Pods: &policyinfo.NamespacedPod{
				NamespaceSelector: metav1.LabelSelector{},
				PodSelector:       metav1.LabelSelector{},
			},
		},
	}

	// Same ingress rules (no change in rules)
	ingressRules := []policyinfo.ClusterEndpointInfo{
		{CIDR: "10.0.0.0/8", Action: "Accept"},
	}

	_, _, _, modifiedCPEs, _ := manager.processExistingClusterPolicyEndpoints(
		[]policyinfo.ClusterPolicyEndpoint{existingCPE},
		ingressRules,
		[]policyinfo.ClusterEndpointInfo{},
		[]policyinfo.PodEndpoint{},
		newMetadata,
	)

	// Verify that CPE was marked as modified due to tier/priority change
	assert.Len(t, modifiedCPEs, 1, "Should have one modified CPE")
	assert.Equal(t, policyinfo.Tier("Admin"), modifiedCPEs[0].Spec.Tier, "Tier should be updated")
	assert.Equal(t, int32(100), modifiedCPEs[0].Spec.Priority, "Priority should be updated")
}
func Test_policyEndpointsManager_processExistingClusterPolicyEndpoints_updateSubject(t *testing.T) {
	manager := &policyEndpointsManager{
		endpointChunkSize: 100,
	}

	// Create existing CPE with old subject
	existingCPE := policyinfo.ClusterPolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cpe-1",
		},
		Spec: policyinfo.ClusterPolicyEndpointSpec{
			Tier:     policyinfo.Tier("Admin"),
			Priority: 100,
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Pods: &policyinfo.NamespacedPod{
					NamespaceSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
					},
					PodSelector: metav1.LabelSelector{},
				},
			},
			Ingress: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: "Accept"},
			},
		},
	}

	// New metadata with updated subject (all namespaces instead of just kube-system)
	newMetadata := ClusterPolicyMetadata{
		Name:     "test-cnp",
		UID:      "test-uid",
		Tier:     "Admin",
		Priority: 100,
		Subject: policyinfo.ClusterNetworkPolicySubject{
			Pods: &policyinfo.NamespacedPod{
				NamespaceSelector: metav1.LabelSelector{}, // Empty = all namespaces
				PodSelector:       metav1.LabelSelector{},
			},
		},
	}

	// Same ingress rules (no change in rules)
	ingressRules := []policyinfo.ClusterEndpointInfo{
		{CIDR: "10.0.0.0/8", Action: "Accept"},
	}

	_, _, _, modifiedCPEs, _ := manager.processExistingClusterPolicyEndpoints(
		[]policyinfo.ClusterPolicyEndpoint{existingCPE},
		ingressRules,
		[]policyinfo.ClusterEndpointInfo{},
		[]policyinfo.PodEndpoint{},
		newMetadata,
	)

	// Verify that CPE was marked as modified due to subject change
	assert.Len(t, modifiedCPEs, 1, "Should have one modified CPE")
	assert.Equal(t, policyinfo.Tier("Admin"), modifiedCPEs[0].Spec.Tier, "Tier should be updated")
	assert.Equal(t, int32(100), modifiedCPEs[0].Spec.Priority, "Priority should be updated")
	assert.Empty(t, modifiedCPEs[0].Spec.Subject.Pods.NamespaceSelector.MatchLabels, "Subject should be updated to all namespaces")
}
