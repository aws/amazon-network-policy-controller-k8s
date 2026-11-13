package resolvers

import (
	"context"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
)

func Test_resolveFQDNRules(t *testing.T) {
	resolver := &applicationNetworkPolicyEndpointsResolver{
		logger: zap.New(),
	}

	protocolTCP := corev1.ProtocolTCP
	protocolUDP := corev1.ProtocolUDP
	port443 := intstr.FromInt(443)
	port53 := intstr.FromInt(53)

	domainNames := []policyinfo.DomainName{"example.com", "*.amazonaws.com"}
	ports := []networking.NetworkPolicyPort{
		{
			Protocol: &protocolTCP,
			Port:     &port443,
		},
		{
			Protocol: &protocolUDP,
			Port:     &port53,
		},
	}

	endpoints := resolver.resolveFQDNRules(domainNames, ports)

	assert.Equal(t, 2, len(endpoints))
	assert.Equal(t, policyinfo.DomainName("example.com"), endpoints[0].DomainName)
	assert.Equal(t, policyinfo.DomainName("*.amazonaws.com"), endpoints[1].DomainName)
	assert.Equal(t, 2, len(endpoints[0].Ports))
	assert.Equal(t, &protocolTCP, endpoints[0].Ports[0].Protocol)
	assert.Equal(t, int32(443), *endpoints[0].Ports[0].Port)
	assert.Equal(t, &protocolUDP, endpoints[0].Ports[1].Protocol)
	assert.Equal(t, int32(53), *endpoints[0].Ports[1].Port)
}

func Test_convertApplicationNetworkPolicyToNetworkPolicy(t *testing.T) {
	resolver := &applicationNetworkPolicyEndpointsResolver{}

	anp := &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-anp",
			Namespace: "default",
		},
		Spec: policyinfo.ApplicationNetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
			PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
			Egress: []policyinfo.ApplicationNetworkPolicyEgressRule{
				{
					To: []policyinfo.ApplicationNetworkPolicyPeer{
						{
							IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"},
						},
					},
				},
				{
					To: []policyinfo.ApplicationNetworkPolicyPeer{
						{
							DomainNames: []policyinfo.DomainName{"example.com"},
						},
					},
				},
			},
		},
	}

	np := resolver.convertApplicationNetworkPolicyToNetworkPolicy(anp)

	assert.Equal(t, anp.Name, np.Name)
	assert.Equal(t, anp.Namespace, np.Namespace)
	assert.Equal(t, anp.Spec.PodSelector, np.Spec.PodSelector)
	assert.Equal(t, anp.Spec.PolicyTypes, np.Spec.PolicyTypes)
	// Should only have CIDR-based egress rules, FQDN rules filtered out
	assert.Equal(t, 1, len(np.Spec.Egress))
	assert.Equal(t, "10.0.0.0/8", np.Spec.Egress[0].To[0].IPBlock.CIDR)
}

func Test_applicationNetworkPolicyEndpointsResolver_ResolveApplicationNetworkPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	baseResolver := &mockEndpointsResolver{}
	resolver := &applicationNetworkPolicyEndpointsResolver{
		k8sClient:    mockClient,
		baseResolver: baseResolver,
		logger:       zap.New(),
	}

	tests := []struct {
		name            string
		anp             *policyinfo.ApplicationNetworkPolicy
		expectedIngress int
		expectedEgress  int
	}{
		{
			name: "FQDN egress only",
			anp: &policyinfo.ApplicationNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-anp", Namespace: "default"},
				Spec: policyinfo.ApplicationNetworkPolicySpec{
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
					PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
					Egress: []policyinfo.ApplicationNetworkPolicyEgressRule{
						{
							To: []policyinfo.ApplicationNetworkPolicyPeer{
								{
									DomainNames: []policyinfo.DomainName{"example.com"},
								},
							},
							Ports: []networking.NetworkPolicyPort{{Port: &intstr.IntOrString{IntVal: 443}}},
						},
					},
				},
			},
			expectedIngress: 0,
			expectedEgress:  1,
		},
		{
			name: "CIDR ingress and FQDN egress",
			anp: &policyinfo.ApplicationNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-anp", Namespace: "default"},
				Spec: policyinfo.ApplicationNetworkPolicySpec{
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
					PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
					Ingress: []networking.NetworkPolicyIngressRule{
						{
							From: []networking.NetworkPolicyPeer{
								{IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"}},
							},
						},
					},
					Egress: []policyinfo.ApplicationNetworkPolicyEgressRule{
						{
							To: []policyinfo.ApplicationNetworkPolicyPeer{
								{
									DomainNames: []policyinfo.DomainName{"api.example.com", "db.example.com"},
								},
							},
							Ports: []networking.NetworkPolicyPort{{Port: &intstr.IntOrString{IntVal: 443}}},
						},
					},
				},
			},
			expectedIngress: 1,
			expectedEgress:  2,
		},
		{
			name: "Multiple CIDR blocks ingress and egress",
			anp: &policyinfo.ApplicationNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-anp", Namespace: "default"},
				Spec: policyinfo.ApplicationNetworkPolicySpec{
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
					PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
					Ingress: []networking.NetworkPolicyIngressRule{
						{
							From: []networking.NetworkPolicyPeer{
								{IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"}},
								{IPBlock: &networking.IPBlock{CIDR: "192.168.0.0/16"}},
							},
						},
					},
					Egress: []policyinfo.ApplicationNetworkPolicyEgressRule{
						{
							To: []policyinfo.ApplicationNetworkPolicyPeer{
								{IPBlock: &networking.IPBlock{CIDR: "172.16.0.0/12"}},
							},
						},
					},
				},
			},
			expectedIngress: 2,
			expectedEgress:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock the base resolver to return converted CIDR results
			baseResolver.mockResolve = func(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error) {
				var ingressEndpoints, egressEndpoints []policyinfo.EndpointInfo

				// Convert ingress CIDR rules
				for _, rule := range policy.Spec.Ingress {
					for _, peer := range rule.From {
						if peer.IPBlock != nil {
							ingressEndpoints = append(ingressEndpoints, policyinfo.EndpointInfo{CIDR: policyinfo.NetworkAddress(peer.IPBlock.CIDR)})
						}
					}
				}

				// Convert egress CIDR rules
				for _, rule := range policy.Spec.Egress {
					for _, peer := range rule.To {
						if peer.IPBlock != nil {
							egressEndpoints = append(egressEndpoints, policyinfo.EndpointInfo{CIDR: policyinfo.NetworkAddress(peer.IPBlock.CIDR)})
						}
					}
				}

				return ingressEndpoints, egressEndpoints, []policyinfo.PodEndpoint{}, nil
			}

			ingress, egress, pods, err := resolver.ResolveApplicationNetworkPolicy(context.Background(), tt.anp)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedIngress, len(ingress))
			assert.Equal(t, tt.expectedEgress, len(egress))
			assert.Equal(t, 0, len(pods))
		})
	}
}

// Mock resolver for testing
type mockEndpointsResolver struct {
	mockResolve func(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error)
}

func (m *mockEndpointsResolver) Resolve(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error) {
	if m.mockResolve != nil {
		return m.mockResolve(ctx, policy)
	}
	return []policyinfo.EndpointInfo{}, []policyinfo.EndpointInfo{}, []policyinfo.PodEndpoint{}, nil
}

// The ANP resolver only uses Resolve. ResolveGroups is built from it so a stub serves both.
func (m *mockEndpointsResolver) ResolveGroups(ctx context.Context, policy *networking.NetworkPolicy) ([]IngressGroup, []policyinfo.EndpointInfo, error) {
	ingress, egress, podEndpoints, err := m.Resolve(ctx, policy)
	if err != nil {
		return nil, nil, err
	}
	return []IngressGroup{{Ingress: ingress, PodEndpoints: podEndpoints}}, egress, nil
}
