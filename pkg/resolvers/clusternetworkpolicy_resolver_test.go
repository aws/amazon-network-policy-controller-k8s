package resolvers

import (
	"context"
	"fmt"
	"testing"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// MockEndpointsResolver is a mock for the base EndpointsResolver
type MockEndpointsResolver struct {
	mock.Mock
}

func (m *MockEndpointsResolver) Resolve(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error) {
	args := m.Called(ctx, policy)
	return args.Get(0).([]policyinfo.EndpointInfo), args.Get(1).([]policyinfo.EndpointInfo), args.Get(2).([]policyinfo.PodEndpoint), args.Error(3)
}

func TestClusterNetworkPolicyEndpointsResolver_mergeClusterEndpointInfo(t *testing.T) {
	resolver := &clusterNetworkPolicyEndpointsResolver{
		logger: logr.Discard(),
	}

	tcpProtocol := corev1.ProtocolTCP
	port443 := int32(443)
	port80 := int32(80)

	tests := []struct {
		name     string
		input    []policyinfo.ClusterEndpointInfo
		expected int
	}{
		{
			name: "merge duplicate CIDR entries",
			input: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
				{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
				{CIDR: "0.0.0.0/0", Action: policyinfo.ClusterNetworkPolicyRuleActionDeny, Ports: []policyinfo.Port{}},
				{CIDR: "0.0.0.0/0", Action: policyinfo.ClusterNetworkPolicyRuleActionDeny, Ports: []policyinfo.Port{}},
			},
			expected: 2,
		},
		{
			name: "merge duplicate domain entries",
			input: []policyinfo.ClusterEndpointInfo{
				{DomainName: "example.com", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
				{DomainName: "example.com", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
				{DomainName: "google.com", Action: policyinfo.ClusterNetworkPolicyRuleActionPass, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
			},
			expected: 2,
		},
		{
			name: "different ports should not merge",
			input: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port80}}},
				{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
			},
			expected: 2,
		},
		{
			name: "different actions should not merge",
			input: []policyinfo.ClusterEndpointInfo{
				{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
				{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionDeny, Ports: []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}}},
			},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolver.mergeClusterEndpointInfo(tt.input)
			assert.Len(t, result, tt.expected)
		})
	}
}

func TestClusterNetworkPolicyEndpointsResolver_portsToString(t *testing.T) {
	resolver := &clusterNetworkPolicyEndpointsResolver{}

	tcpProtocol := corev1.ProtocolTCP
	port443 := int32(443)
	port80 := int32(80)

	tests := []struct {
		name     string
		ports    []policyinfo.Port
		expected string
	}{
		{
			name:     "empty ports",
			ports:    []policyinfo.Port{},
			expected: "all",
		},
		{
			name:     "single port",
			ports:    []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port443}},
			expected: "TCP:443",
		},
		{
			name:     "multiple ports",
			ports:    []policyinfo.Port{{Protocol: &tcpProtocol, Port: &port80}, {Protocol: &tcpProtocol, Port: &port443}},
			expected: "TCP:80,TCP:443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolver.portsToString(tt.ports)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestClusterNetworkPolicyEndpointsResolver_convertSingleCNPEgressRuleToNP(t *testing.T) {
	resolver := &clusterNetworkPolicyEndpointsResolver{
		logger: logr.Discard(),
	}

	tests := []struct {
		name          string
		cnp           *policyinfo.ClusterNetworkPolicy
		rule          policyinfo.ClusterNetworkPolicyEgressRule
		namespace     string
		expectedPeers int
		expectedCIDRs []string
	}{
		{
			name: "multiple CIDRs in single peer",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
				},
			},
			rule: policyinfo.ClusterNetworkPolicyEgressRule{
				Name:   "multi-cidr-rule",
				Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
				To: []policyinfo.ClusterNetworkPolicyEgressPeer{
					{Networks: []policyinfo.CIDR{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
				},
			},
			namespace:     "default",
			expectedPeers: 3, // One peer per CIDR
			expectedCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
		},
		{
			name: "mixed CIDR and namespace peers",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
				},
			},
			rule: policyinfo.ClusterNetworkPolicyEgressRule{
				Name:   "mixed-rule",
				Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
				To: []policyinfo.ClusterNetworkPolicyEgressPeer{
					{Networks: []policyinfo.CIDR{"10.0.0.0/8"}},
					{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}},
				},
			},
			namespace:     "default",
			expectedPeers: 2, // One CIDR peer + one namespace peer
			expectedCIDRs: []string{"10.0.0.0/8"},
		},
		{
			name: "pod selector peer",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Pods: &policyinfo.NamespacedPod{
							NamespaceSelector: metav1.LabelSelector{},
							PodSelector:       metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
						},
					},
				},
			},
			rule: policyinfo.ClusterNetworkPolicyEgressRule{
				Name:   "pod-rule",
				Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
				To: []policyinfo.ClusterNetworkPolicyEgressPeer{
					{Pods: &policyinfo.NamespacedPod{
						NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "backend"}},
						PodSelector:       metav1.LabelSelector{MatchLabels: map[string]string{"component": "database"}},
					}},
				},
			},
			namespace:     "default",
			expectedPeers: 1,
			expectedCIDRs: []string{}, // No CIDRs
		},
		{
			name: "domain names peer - should be ignored",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
				},
			},
			rule: policyinfo.ClusterNetworkPolicyEgressRule{
				Name:   "domain-rule",
				Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
				To: []policyinfo.ClusterNetworkPolicyEgressPeer{
					{DomainNames: []policyinfo.DomainName{"example.com", "google.com"}},
					{Networks: []policyinfo.CIDR{"10.0.0.0/8"}},
				},
			},
			namespace:     "default",
			expectedPeers: 1, // Only CIDR peer, domain names ignored
			expectedCIDRs: []string{"10.0.0.0/8"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			np := resolver.convertSingleCNPEgressRuleToNP(tt.cnp, tt.rule, tt.namespace)

			assert.NotNil(t, np)
			assert.Equal(t, tt.namespace, np.Namespace)
			assert.Len(t, np.Spec.Egress, 1, "should have exactly one egress rule")

			egressRule := np.Spec.Egress[0]
			assert.Len(t, egressRule.To, tt.expectedPeers, "peer count mismatch")

			// Check CIDRs
			foundCIDRs := []string{}
			for _, peer := range egressRule.To {
				if peer.IPBlock != nil {
					foundCIDRs = append(foundCIDRs, peer.IPBlock.CIDR)
				}
			}
			assert.ElementsMatch(t, tt.expectedCIDRs, foundCIDRs, "CIDR mismatch")

			// Verify pod selector is empty since temp NP is only for peer resolution
			assert.Equal(t, metav1.LabelSelector{}, np.Spec.PodSelector)
		})
	}
}

func TestClusterNetworkPolicyEndpointsResolver_resolveCNPEgressRules(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	// Create test namespaces
	defaultNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
	}
	prodNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "prod-ns",
			Labels: map[string]string{"env": "prod"},
		},
	}
	devNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "dev-ns",
			Labels: map[string]string{"env": "dev"},
		},
	}

	// Create test pods
	prodPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prod-pod",
			Namespace: "prod-ns",
			Labels:    map[string]string{"app": "web"},
		},
		Status: corev1.PodStatus{
			PodIP: "10.1.1.1",
		},
	}
	devPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dev-pod",
			Namespace: "dev-ns",
			Labels:    map[string]string{"app": "api"},
		},
		Status: corev1.PodStatus{
			PodIP: "10.1.1.2",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(defaultNS, prodNS, devNS, prodPod, devPod).
		Build()
	baseResolver := NewEndpointsResolver(fakeClient, logr.Discard())
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: baseResolver,
		logger:       logr.Discard(),
	}

	tcpProtocol := corev1.ProtocolTCP
	port443 := int32(443)

	tests := []struct {
		name             string
		cnp              *policyinfo.ClusterNetworkPolicy
		targetNamespaces []string
		expectedCount    int
		expectedDomains  []string
		expectedCIDRs    []string
	}{
		{
			name: "CIDR egress rules",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "cidr-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{Networks: []policyinfo.CIDR{"10.0.0.0/8", "172.16.0.0/12"}},
							},
						},
					},
				},
			},
			targetNamespaces: []string{"ns1", "ns2"},
			expectedCount:    2, // 2 CIDRs (not multiplied by namespaces)
			expectedCIDRs:    []string{"10.0.0.0/8", "172.16.0.0/12"},
		},
		{
			name: "domain name egress rules",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "domain-accept",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{DomainNames: []policyinfo.DomainName{"example.com"}},
							},
							Ports: &[]policyinfo.ClusterNetworkPolicyPort{
								{PortNumber: &policyinfo.CNPPort{Protocol: tcpProtocol, Port: port443}},
							},
						},
						{
							Name:   "domain-deny",
							Action: policyinfo.ClusterNetworkPolicyRuleActionDeny,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{DomainNames: []policyinfo.DomainName{"blocked.com"}},
							},
						},
					},
				},
			},
			targetNamespaces: []string{"ns1"},
			expectedCount:    1, // Only Accept action, Deny ignored
			expectedDomains:  []string{"example.com"},
		},
		{
			name: "mixed CIDR and domain rules",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "mixed-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{Networks: []policyinfo.CIDR{"10.0.0.0/8"}},
								{DomainNames: []policyinfo.DomainName{"example.com"}},
							},
						},
					},
				},
			},
			targetNamespaces: []string{"ns1"},
			expectedCount:    2, // 1 CIDR + 1 domain
			expectedCIDRs:    []string{"10.0.0.0/8"},
			expectedDomains:  []string{"example.com"},
		},
		{
			name: "namespace selector all namespaces",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "namespace-all",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{Namespaces: &metav1.LabelSelector{}}, // Empty = all namespaces
							},
						},
					},
				},
			},
			targetNamespaces: []string{"ns1"},
			expectedCount:    2, // 2 pods from all namespaces (prod-pod + dev-pod)
			expectedCIDRs:    []string{"10.1.1.1", "10.1.1.2"},
		},
		{
			name: "namespace selector specific label",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "namespace-prod",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}},
							},
						},
					},
				},
			},
			targetNamespaces: []string{"ns1"},
			expectedCount:    1, // 1 pod from prod-ns only
			expectedCIDRs:    []string{"10.1.1.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolver.resolveCNPEgressRules(context.Background(), tt.cnp)

			assert.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)

			// Check CIDRs
			foundCIDRs := []string{}
			for _, endpoint := range result {
				if endpoint.CIDR != "" {
					foundCIDRs = append(foundCIDRs, string(endpoint.CIDR))
				}
			}
			if len(tt.expectedCIDRs) > 0 {
				for _, expectedCIDR := range tt.expectedCIDRs {
					assert.Contains(t, foundCIDRs, expectedCIDR)
				}
			}

			// Check domains
			foundDomains := []string{}
			for _, endpoint := range result {
				if endpoint.DomainName != "" {
					foundDomains = append(foundDomains, string(endpoint.DomainName))
				}
			}
			assert.ElementsMatch(t, tt.expectedDomains, foundDomains)
		})
	}
}

func TestClusterNetworkPolicyEndpointsResolver_ResolveClusterNetworkPolicy(t *testing.T) {
	// Integration test with real resolver to catch duplication issues
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	// Create test namespaces
	ns1 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	ns2 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns2"}}
	kubeSystemNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", Labels: map[string]string{"name": "kube-system"}}}

	// Add coredns pods for ingress test
	coredns1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns-1", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-dns"}},
		Status:     corev1.PodStatus{PodIP: "192.168.1.1"},
	}
	coredns2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns-2", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-dns"}},
		Status:     corev1.PodStatus{PodIP: "192.168.1.2"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns1, ns2, kubeSystemNs, coredns1, coredns2).
		Build()

	// Create real base resolver
	baseResolver := NewEndpointsResolver(fakeClient, logr.Discard())

	// Create CNP resolver with real base resolver
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: baseResolver,
		logger:       logr.Discard(),
	}

	tests := []struct {
		name                  string
		cnp                   *policyinfo.ClusterNetworkPolicy
		expectedIngressCount  int
		expectedEgressCount   int
		expectedPodCount      int
		expectedEgressDomains []string
	}{
		{
			name: "egress CIDR only - no duplicates",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp-egress-cidr"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     policyinfo.AdminTier,
					Priority: 100,
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{}, // All namespaces
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "allow-internal",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{Networks: []policyinfo.CIDR{"10.0.0.0/8", "172.16.0.0/12"}},
							},
							Ports: &[]policyinfo.ClusterNetworkPolicyPort{
								{PortNumber: &policyinfo.CNPPort{Protocol: corev1.ProtocolTCP, Port: 443}},
							},
						},
						{
							Name:   "deny-external",
							Action: policyinfo.ClusterNetworkPolicyRuleActionDeny,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{Networks: []policyinfo.CIDR{"0.0.0.0/0"}},
							},
						},
					},
				},
			},
			expectedIngressCount: 0,
			expectedEgressCount:  3, // 2 from first rule + 1 from second rule
			expectedPodCount:     2, // coredns pods from kube-system namespace
		},
		{
			name: "egress with FQDN Accept and Pass",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp-fqdn"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     policyinfo.AdminTier,
					Priority: 100,
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "fqdn-accept-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{DomainNames: []policyinfo.DomainName{"example.com"}},
							},
						},
						{
							Name:   "fqdn-pass-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionPass,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{DomainNames: []policyinfo.DomainName{"google.com"}},
							},
						},
						{
							Name:   "fqdn-deny-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionDeny,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{DomainNames: []policyinfo.DomainName{"blocked.com"}},
							},
						},
					},
				},
			},
			expectedIngressCount:  0,
			expectedEgressCount:   2, // Only Accept and Pass, Deny should be ignored
			expectedPodCount:      2, // coredns pods from kube-system namespace
			expectedEgressDomains: []string{"example.com", "google.com"},
		},
		{
			name: "mixed CIDR and FQDN egress",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp-mixed"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     policyinfo.AdminTier,
					Priority: 100,
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{},
					},
					Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
						{
							Name:   "cidr-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{Networks: []policyinfo.CIDR{"10.0.0.0/8"}},
							},
						},
						{
							Name:   "fqdn-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							To: []policyinfo.ClusterNetworkPolicyEgressPeer{
								{DomainNames: []policyinfo.DomainName{"example.com"}},
							},
						},
					},
				},
			},
			expectedIngressCount:  0,
			expectedEgressCount:   2,
			expectedPodCount:      2, // coredns pods from kube-system namespace
			expectedEgressDomains: []string{"example.com"},
		},
		{
			name: "ingress port deduplication",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp-ingress-dedup"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     policyinfo.AdminTier,
					Priority: 80,
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Pods: &policyinfo.NamespacedPod{
							NamespaceSelector: metav1.LabelSelector{},
							PodSelector:       metav1.LabelSelector{},
						},
					},
					Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
						{
							Name:   "dns-rule",
							Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
							From: []policyinfo.ClusterNetworkPolicyIngressPeer{
								{Pods: &policyinfo.NamespacedPod{
									NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"name": "kube-system"}},
									PodSelector:       metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
								}},
							},
							Ports: &[]policyinfo.ClusterNetworkPolicyPort{
								{PortNumber: &policyinfo.CNPPort{Protocol: corev1.ProtocolTCP, Port: 53}},
								{PortNumber: &policyinfo.CNPPort{Protocol: corev1.ProtocolUDP, Port: 53}},
							},
						},
					},
				},
			},
			expectedIngressCount: 2, // 2 coredns pods should create 2 ingress rules
			expectedEgressCount:  0,
			expectedPodCount:     2, // Only 2 coredns pods in kube-system
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ingressRules, egressRules, podEndpoints, err := resolver.ResolveClusterNetworkPolicy(context.Background(), tt.cnp)

			assert.NoError(t, err)
			assert.Len(t, ingressRules, tt.expectedIngressCount, "ingress rules count mismatch")
			assert.Len(t, egressRules, tt.expectedEgressCount, "egress rules count mismatch")
			assert.Len(t, podEndpoints, tt.expectedPodCount, "pod endpoints count mismatch")

			// Check egress domains
			if len(tt.expectedEgressDomains) > 0 {
				foundDomains := []string{}
				for _, rule := range egressRules {
					if rule.DomainName != "" {
						foundDomains = append(foundDomains, string(rule.DomainName))
					}
				}
				assert.ElementsMatch(t, tt.expectedEgressDomains, foundDomains, "egress domains mismatch")
			}

			// Verify no duplicates by checking unique CIDR+Action combinations
			seen := make(map[string]bool)
			for _, rule := range egressRules {
				var key string
				if rule.CIDR != "" {
					key = fmt.Sprintf("cidr:%s:%s", rule.CIDR, rule.Action)
				} else if rule.DomainName != "" {
					key = fmt.Sprintf("domain:%s:%s", rule.DomainName, rule.Action)
				}
				if key != "" {
					assert.False(t, seen[key], "found duplicate egress rule: %s", key)
					seen[key] = true
				}
			}

			// For ingress port deduplication test, verify no duplicate ports per CIDR
			if tt.name == "ingress port deduplication" {
				cidrPortCount := make(map[string]int)
				for _, rule := range ingressRules {
					key := fmt.Sprintf("%s:%s", rule.CIDR, rule.Action)
					cidrPortCount[key] += len(rule.Ports)
				}
				// Each CIDR should have exactly 2 unique ports (TCP:53, UDP:53)
				for cidr, portCount := range cidrPortCount {
					assert.Equal(t, 2, portCount, "CIDR %s should have exactly 2 unique ports, got %d", cidr, portCount)
				}
			}
		})
	}
}
func TestCNPIngressResolution_MultiplePods(t *testing.T) {
	// Test case for the DNS CNP bug - ingress should resolve all matching pods
	tests := []struct {
		name                 string
		cnp                  *policyinfo.ClusterNetworkPolicy
		pods                 []corev1.Pod
		namespaces           []corev1.Namespace
		expectedIngressCount int
		expectedPodIPs       []string
	}{
		{
			name: "ingress rule with podSelector should resolve all matching pods",
			cnp: &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
				Spec: policyinfo.ClusterNetworkPolicySpec{
					Tier:     "Admin",
					Priority: 100,
					Subject: policyinfo.ClusterNetworkPolicySubject{
						Namespaces: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"kubernetes.io/metadata.name": "default",
							},
						},
					},
					Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
						{
							Name:   "allow-coredns",
							Action: "Accept",
							From: []policyinfo.ClusterNetworkPolicyIngressPeer{
								{
									Pods: &policyinfo.NamespacedPod{
										NamespaceSelector: metav1.LabelSelector{
											MatchLabels: map[string]string{
												"kubernetes.io/metadata.name": "kube-system",
											},
										},
										PodSelector: metav1.LabelSelector{
											MatchLabels: map[string]string{
												"k8s-app": "kube-dns",
											},
										},
									},
								},
							},
							Ports: &[]policyinfo.ClusterNetworkPolicyPort{
								{
									PortNumber: &policyinfo.CNPPort{
										Protocol: corev1.ProtocolUDP,
										Port:     53,
									},
								},
							},
						},
					},
				},
			},
			namespaces: []corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
						Labels: map[string]string{
							"kubernetes.io/metadata.name": "default",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kube-system",
						Labels: map[string]string{
							"kubernetes.io/metadata.name": "kube-system",
						},
					},
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Status: corev1.PodStatus{
						PodIP: "192.168.1.100",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "coredns-1",
						Namespace: "kube-system",
						Labels: map[string]string{
							"k8s-app": "kube-dns",
						},
					},
					Status: corev1.PodStatus{
						PodIP: "192.168.25.168", // First CoreDNS pod
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "coredns-2",
						Namespace: "kube-system",
						Labels: map[string]string{
							"k8s-app": "kube-dns",
						},
					},
					Status: corev1.PodStatus{
						PodIP: "192.168.13.154", // Second CoreDNS pod
					},
				},
			},
			expectedIngressCount: 2, // Should resolve both CoreDNS pods
			expectedPodIPs:       []string{"192.168.25.168", "192.168.13.154"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with test objects
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = policyinfo.AddToScheme(scheme)

			objs := []runtime.Object{}
			for _, ns := range tt.namespaces {
				objs = append(objs, &ns)
			}
			for _, pod := range tt.pods {
				objs = append(objs, &pod)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			// Create resolver
			resolver := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

			// Test the resolution
			ingressRules, egressRules, podEndpoints, err := resolver.ResolveClusterNetworkPolicy(context.Background(), tt.cnp)

			// Verify results
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedIngressCount, len(ingressRules), "Should resolve all matching pods for ingress")
			assert.Empty(t, egressRules, "No egress rules expected")
			assert.NotEmpty(t, podEndpoints, "Should have pod endpoints")

			// Verify all expected pod IPs are present in ingress rules
			actualIPs := make([]string, len(ingressRules))
			for i, rule := range ingressRules {
				actualIPs[i] = string(rule.CIDR)
				assert.Equal(t, policyinfo.ClusterNetworkPolicyRuleAction("Accept"), rule.Action)
			}

			for _, expectedIP := range tt.expectedPodIPs {
				assert.Contains(t, actualIPs, expectedIP, "Expected pod IP should be in ingress rules")
			}
		})
	}
}

func TestCNPIngressResolution_SingleRule(t *testing.T) {
	// Test that single pod resolution still works
	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-single"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Tier:     "Admin",
			Priority: 100,
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "target",
					},
				},
			},
			Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
				{
					Name:   "allow-single",
					Action: "Accept",
					From: []policyinfo.ClusterNetworkPolicyIngressPeer{
						{
							Namespaces: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"source": "allowed",
								},
							},
						},
					},
				},
			},
		},
	}

	namespaces := []corev1.Namespace{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "target-ns",
				Labels: map[string]string{"test": "target"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "source-ns",
				Labels: map[string]string{"source": "allowed"},
			},
		},
	}

	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "target-pod",
				Namespace: "target-ns",
			},
			Status: corev1.PodStatus{PodIP: "10.0.1.1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "source-pod",
				Namespace: "source-ns",
			},
			Status: corev1.PodStatus{PodIP: "10.0.2.1"},
		},
	}

	// Create fake client
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	objs := []runtime.Object{}
	for _, ns := range namespaces {
		objs = append(objs, &ns)
	}
	for _, pod := range pods {
		objs = append(objs, &pod)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()

	resolver := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	ingressRules, _, _, err := resolver.ResolveClusterNetworkPolicy(context.Background(), cnp)

	assert.NoError(t, err)
	assert.Equal(t, 1, len(ingressRules))
	assert.Equal(t, "10.0.2.1", string(ingressRules[0].CIDR))
	assert.Equal(t, policyinfo.ClusterNetworkPolicyRuleAction("Accept"), ingressRules[0].Action)
}
func TestClusterNetworkPolicyEndpointsResolver_resolveNamespacesBySelector(t *testing.T) {
	tests := []struct {
		name       string
		selector   metav1.LabelSelector
		namespaces []corev1.Namespace
		expected   []string
	}{
		{
			name:     "empty selector returns all namespaces",
			selector: metav1.LabelSelector{},
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "test-ns"}},
			},
			expected: []string{"default", "kube-system", "test-ns"},
		},
		{
			name: "label selector matches specific namespaces",
			selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "prod"},
			},
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "prod-ns", Labels: map[string]string{"env": "prod"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-ns", Labels: map[string]string{"env": "dev"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: map[string]string{"env": "test"}}},
			},
			expected: []string{"prod-ns"},
		},
		{
			name: "no matching namespaces",
			selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "staging"},
			},
			namespaces: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "prod-ns", Labels: map[string]string{"env": "prod"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "dev-ns", Labels: map[string]string{"env": "dev"}}},
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			corev1.AddToScheme(scheme)

			var objects []runtime.Object
			for _, ns := range tt.namespaces {
				objects = append(objects, &ns)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			resolver := &clusterNetworkPolicyEndpointsResolver{
				k8sClient: fakeClient,
				logger:    logr.Discard(),
			}

			result, err := resolver.resolveNamespacesBySelector(context.Background(), tt.selector)

			assert.NoError(t, err)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}
func TestClusterNetworkPolicyEndpointsResolver_resolveCNPIngressRules(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	// Create test namespaces and pods (same as egress test)
	prodNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "prod-ns",
			Labels: map[string]string{"env": "prod"},
		},
	}
	prodPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prod-pod",
			Namespace: "prod-ns",
		},
		Status: corev1.PodStatus{
			PodIP: "10.1.1.1",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(prodNS, prodPod).
		Build()
	baseResolver := NewEndpointsResolver(fakeClient, logr.Discard())
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: baseResolver,
		logger:       logr.Discard(),
	}

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{},
			},
			Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
				{
					Name:   "ingress-rule",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					From: []policyinfo.ClusterNetworkPolicyIngressPeer{
						{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}},
					},
				},
			},
		},
	}

	result, err := resolver.resolveCNPIngressRules(context.Background(), cnp)

	assert.NoError(t, err)
	assert.Len(t, result, 1) // 1 pod from prod-ns
	assert.Equal(t, "10.1.1.1", string(result[0].CIDR))
	assert.Equal(t, policyinfo.ClusterNetworkPolicyRuleActionAccept, result[0].Action)
}

// Asserts Resolve() is called once per ingress rule, not per matched namespace.
func TestResolveCNPIngressRules_SingleResolveCallPerRule(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	// 5 namespaces match the peer selector.
	namespaces := make([]runtime.Object, 0, 5)
	for i := 0; i < 5; i++ {
		namespaces = append(namespaces, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   fmt.Sprintf("prod-ns-%d", i),
				Labels: map[string]string{"env": "prod"},
			},
		})
	}
	subjectNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "subject-ns", Labels: map[string]string{"role": "subject"}},
	}
	namespaces = append(namespaces, subjectNS)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(namespaces...).
		Build()

	mockResolver := new(MockEndpointsResolver)
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: mockResolver,
		logger:       logr.Discard(),
	}

	mockResolver.On("Resolve", mock.Anything, mock.Anything).Return(
		[]policyinfo.EndpointInfo{{CIDR: "10.0.0.1"}},
		[]policyinfo.EndpointInfo(nil),
		[]policyinfo.PodEndpoint(nil),
		nil,
	)

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "subject"}},
			},
			Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
				{
					Name:   "rule-a",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					From:   []policyinfo.ClusterNetworkPolicyIngressPeer{{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}}},
				},
				{
					Name:   "rule-b",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					From:   []policyinfo.ClusterNetworkPolicyIngressPeer{{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}}},
				},
			},
		},
	}

	_, err := resolver.resolveCNPIngressRules(context.Background(), cnp)
	assert.NoError(t, err)
	// 2 rules × 5 matched namespaces → expect 2 calls (one per rule), not 10.
	mockResolver.AssertNumberOfCalls(t, "Resolve", 2)
}

// Asserts Resolve() is called once per egress NS/Pod peer, not per matched namespace.
func TestResolveCNPEgressRules_SingleResolveCallForNamespacePeer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	namespaces := make([]runtime.Object, 0, 5)
	for i := 0; i < 5; i++ {
		namespaces = append(namespaces, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   fmt.Sprintf("prod-ns-%d", i),
				Labels: map[string]string{"env": "prod"},
			},
		})
	}
	subjectNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "subject-ns", Labels: map[string]string{"role": "subject"}},
	}
	namespaces = append(namespaces, subjectNS)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(namespaces...).
		Build()

	mockResolver := new(MockEndpointsResolver)
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: mockResolver,
		logger:       logr.Discard(),
	}

	mockResolver.On("Resolve", mock.Anything, mock.Anything).Return(
		[]policyinfo.EndpointInfo(nil),
		[]policyinfo.EndpointInfo{{CIDR: "10.0.0.1"}},
		[]policyinfo.PodEndpoint(nil),
		nil,
	)

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "subject"}},
			},
			Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
				{
					Name:   "rule-1",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					To:     []policyinfo.ClusterNetworkPolicyEgressPeer{{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}}},
				},
			},
		},
	}

	_, err := resolver.resolveCNPEgressRules(context.Background(), cnp)
	assert.NoError(t, err)
	// NS peer matches 5 namespaces → expect 1 call, not 5.
	mockResolver.AssertNumberOfCalls(t, "Resolve", 1)
}

// Covers getSubjectNamespace for Pods/Namespaces subject + no-match cases.
func TestGetSubjectNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	prodNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod-ns", Labels: map[string]string{"env": "prod"}}}
	devNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev-ns", Labels: map[string]string{"env": "dev"}}}

	tests := []struct {
		name        string
		subject     policyinfo.ClusterNetworkPolicySubject
		want        string // expected exact namespace (sort.Strings makes this deterministic)
		wantEmpty   bool
		expectError bool
	}{
		{
			name: "Pods subject with matching namespace",
			subject: policyinfo.ClusterNetworkPolicySubject{
				Pods: &policyinfo.NamespacedPod{
					NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
					PodSelector:       metav1.LabelSelector{},
				},
			},
			want: "prod-ns",
		},
		{
			name: "Namespaces subject with matching label",
			subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "dev"}},
			},
			want: "dev-ns",
		},
		{
			name: "Namespaces subject with empty selector returns alphabetically first",
			subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{},
			},
			want: "dev-ns", // sort.Strings(["prod-ns","dev-ns"])[0] == "dev-ns"
		},
		{
			name: "Namespaces subject matching multiple namespaces returns alphabetically first",
			subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"prod", "dev"}},
					},
				},
			},
			want: "dev-ns",
		},
		{
			name: "Pods subject matching multiple namespaces returns alphabetically first",
			subject: policyinfo.ClusterNetworkPolicySubject{
				Pods: &policyinfo.NamespacedPod{
					NamespaceSelector: metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"prod", "dev"}},
						},
					},
				},
			},
			want: "dev-ns",
		},
		{
			name: "Pods subject with no matching namespace returns empty",
			subject: policyinfo.ClusterNetworkPolicySubject{
				Pods: &policyinfo.NamespacedPod{
					NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"env": "nope"}},
				},
			},
			wantEmpty: true,
		},
		{
			name:      "empty subject returns empty",
			subject:   policyinfo.ClusterNetworkPolicySubject{},
			wantEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(prodNS, devNS).
				Build()
			resolver := &clusterNetworkPolicyEndpointsResolver{
				k8sClient: fakeClient,
				logger:    logr.Discard(),
			}
			cnp := &policyinfo.ClusterNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec:       policyinfo.ClusterNetworkPolicySpec{Subject: tc.subject},
			}
			got, err := resolver.getSubjectNamespace(context.Background(), cnp)
			assert.NoError(t, err)
			if tc.wantEmpty {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// Asserts temp NP namespace = subject namespace (for named-port resolution).
func TestResolveCNPIngressRules_TempNPNamespaceIsSubject(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	subjectNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sub-ns", Labels: map[string]string{"role": "subject"}}}
	peerNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "peer-ns", Labels: map[string]string{"env": "prod"}}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(subjectNS, peerNS).
		Build()

	mockResolver := new(MockEndpointsResolver)
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: mockResolver,
		logger:       logr.Discard(),
	}

	var capturedNS string
	mockResolver.On("Resolve", mock.Anything, mock.MatchedBy(func(np *networking.NetworkPolicy) bool {
		capturedNS = np.Namespace
		return true
	})).Return(
		[]policyinfo.EndpointInfo(nil),
		[]policyinfo.EndpointInfo(nil),
		[]policyinfo.PodEndpoint(nil),
		nil,
	)

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "subject"}},
			},
			Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
				{
					Name:   "rule-1",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					From:   []policyinfo.ClusterNetworkPolicyIngressPeer{{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}}},
				},
			},
		},
	}

	_, err := resolver.resolveCNPIngressRules(context.Background(), cnp)
	assert.NoError(t, err)
	assert.Equal(t, "sub-ns", capturedNS) // subject ns, not peer ns
}

func TestResolveCNPIngressRules_NamedPortsWithoutSubjectNamespaceSkipsRule(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	peerNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "peer-ns",
			Labels: map[string]string{"env": "prod"},
		},
	}
	otherNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "other-ns",
			Labels: map[string]string{"env": "other"},
		},
	}
	peerPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "peer-pod",
			Namespace: "peer-ns",
		},
		Status: corev1.PodStatus{
			PodIP: "10.1.1.1",
		},
	}
	unrelatedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-pod",
			Namespace: "other-ns",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 8080,
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			PodIP: "10.2.2.2",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(peerNS, otherNS, peerPod, unrelatedPod).
		Build()
	baseResolver := NewEndpointsResolver(fakeClient, logr.Discard())
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: baseResolver,
		logger:       logr.Discard(),
	}

	namedPort := "http"
	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "subject"}},
			},
			Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
				{
					Name:   "ingress-rule",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					From: []policyinfo.ClusterNetworkPolicyIngressPeer{
						{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}},
					},
					Ports: &[]policyinfo.ClusterNetworkPolicyPort{
						{NamedPort: &namedPort},
					},
				},
			},
		},
	}

	result, err := resolver.resolveCNPIngressRules(context.Background(), cnp)

	assert.NoError(t, err)
	assert.Empty(t, result)
}

// Asserts named-port resolution honors Subject.Pods.PodSelector — non-subject
// pods in the subject namespace must not contribute their containerPort even
// if they declare the same named port.
func TestResolveCNPIngressRules_NamedPortRespectsSubjectPodSelector(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networking.AddToScheme(scheme)
	_ = policyinfo.AddToScheme(scheme)

	subjectNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "team-alpha", Labels: map[string]string{"team": "alpha"}},
	}
	peerNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "client-ns", Labels: map[string]string{"role": "client"}},
	}
	// Subject pod: matches Subject.Pods.PodSelector {app=web}, declares http=8080.
	webPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-1",
			Namespace: "team-alpha",
			Labels:    map[string]string{"app": "web"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "web",
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
				},
			},
		},
		Status: corev1.PodStatus{PodIP: "10.0.1.1"},
	}
	// Non-subject pod: same namespace, app=db, declares http=9999.
	dbPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-1",
			Namespace: "team-alpha",
			Labels:    map[string]string{"app": "db"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "db",
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 9999, Protocol: corev1.ProtocolTCP}},
				},
			},
		},
		Status: corev1.PodStatus{PodIP: "10.0.1.2"},
	}
	clientPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "client-1",
			Namespace: "client-ns",
			Labels:    map[string]string{"app": "client"},
		},
		Status: corev1.PodStatus{PodIP: "10.0.2.1"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(subjectNS, peerNS, webPod, dbPod, clientPod).
		Build()
	baseResolver := NewEndpointsResolver(fakeClient, logr.Discard())
	resolver := &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    fakeClient,
		baseResolver: baseResolver,
		logger:       logr.Discard(),
	}

	namedPort := "http"
	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "web-ingress-named-port"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Pods: &policyinfo.NamespacedPod{
					NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "alpha"}},
					PodSelector:       metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				},
			},
			Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{
				{
					Name:   "allow-http",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					From: []policyinfo.ClusterNetworkPolicyIngressPeer{
						{Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "client"}}},
					},
					Ports: &[]policyinfo.ClusterNetworkPolicyPort{{NamedPort: &namedPort}},
				},
			},
		},
	}

	result, err := resolver.resolveCNPIngressRules(context.Background(), cnp)
	assert.NoError(t, err)
	assert.Len(t, result, 1)

	// Resolved ports must include only 8080 (web pod), NOT 9999 (db pod).
	assert.Len(t, result[0].Ports, 1, "named-port resolution should not leak non-subject pod's port")
	if len(result[0].Ports) > 0 && result[0].Ports[0].Port != nil {
		assert.Equal(t, int32(8080), *result[0].Ports[0].Port)
	}
}
