package policyendpoints

import (
	"context"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
)

func Test_policyEndpointsManager_computePolicyEndpoints(t *testing.T) {
	type fields struct {
		endpointChunkSize int
	}
	type args struct {
		policy               *networking.NetworkPolicy
		policyEndpoints      []policyinfo.PolicyEndpoint
		ingressRules         []policyinfo.EndpointInfo
		egressRules          []policyinfo.EndpointInfo
		podselectorEndpoints []policyinfo.PodEndpoint
		epValidator          func(netpol *networking.NetworkPolicy, ep *policyinfo.PolicyEndpoint) bool
	}
	type want struct {
		createCount int
		updateCount int
		deleteCount int
	}
	protocolTCP := corev1.ProtocolTCP
	blockOwnerDeletion := true
	isController := true

	defaultEPValidator := func(netpol *networking.NetworkPolicy, ep *policyinfo.PolicyEndpoint) bool {
		if len(ep.GenerateName) != 0 {
			if ep.GenerateName != netpol.Name+"-" {
				return false
			}
		} else {
			if ep.Name != netpol.Name {
				return false
			}
		}
		return ep.Namespace == netpol.Namespace &&
			equality.Semantic.DeepEqual(ep.OwnerReferences, []metav1.OwnerReference{
				{
					APIVersion:         "networking.k8s.io/v1",
					Name:               netpol.Name,
					Kind:               "NetworkPolicy",
					BlockOwnerDeletion: &blockOwnerDeletion,
					Controller:         &isController,
				},
			}) && equality.Semantic.DeepEqual(ep.Spec.PolicyRef,
			policyinfo.PolicyReference{
				Namespace: netpol.Namespace,
				Name:      netpol.Name,
			}) &&
			equality.Semantic.DeepEqual(ep.Spec.PodSelector, &netpol.Spec.PodSelector) &&
			equality.Semantic.DeepEqual(ep.Spec.PodIsolation, netpol.Spec.PolicyTypes)
	}

	getEPInfoHelper := func(cidrs []policyinfo.NetworkAddress, except []policyinfo.NetworkAddress, portCount int) []policyinfo.EndpointInfo {
		var ports []policyinfo.Port
		for i := 0; i < portCount; i++ {
			portVal := int32(80 + i)
			ports = append(ports, policyinfo.Port{
				Protocol: &protocolTCP,
				Port:     &portVal,
			})
		}
		var epInfo []policyinfo.EndpointInfo
		for _, cidr := range cidrs {
			epInfo = append(epInfo, policyinfo.EndpointInfo{
				CIDR:   cidr,
				Except: except,
				Ports:  ports,
			})
		}
		return epInfo
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    want
		wantErr string
	}{
		{
			name: "no existing endpoints, create one",
			fields: fields{
				endpointChunkSize: 2,
			},
			args: args{
				policy: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "policy-namespace",
						Name:      "policy-name",
					},
					Spec: networking.NetworkPolicySpec{},
				},
			},
			want: want{
				createCount: 1,
			},
		},
		{
			name: "no existing endpoints, create two",
			fields: fields{
				endpointChunkSize: 2,
			},
			args: args{
				policy: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "policy-namespace",
						Name:      "policy-name",
					},
					Spec: networking.NetworkPolicySpec{},
				},
				ingressRules: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5", "2.3.4.5"}, nil, 3),
				egressRules:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
			},
			want: want{
				createCount: 2,
			},
		},
		{
			name: "existing endpoints, update",
			fields: fields{
				endpointChunkSize: 10,
			},
			args: args{
				policy: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "policy-namespace",
						Name:      "policy-name",
					},
					Spec: networking.NetworkPolicySpec{},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod1",
								},
								{
									Name: "pod2",
								},
							},
						},
					},
				},
				ingressRules: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5", "2.3.4.5"}, nil, 3),
				egressRules:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 3),
				podselectorEndpoints: []policyinfo.PodEndpoint{
					{
						Name: "pod1",
					},
				},
				epValidator: func(_ *networking.NetworkPolicy, _ *policyinfo.PolicyEndpoint) bool {
					return true
				},
			},
			want: want{
				updateCount: 1,
			},
		},
		{
			name: "delete unneeded endpoints",
			fields: fields{
				endpointChunkSize: 10,
			},
			args: args{
				policy: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "policy-namespace",
						Name:      "policy-name",
					},
					Spec: networking.NetworkPolicySpec{},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "policy-namespace",
							Name:      "policy-name-1",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod1",
								},
								{
									Name: "pod2",
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "policy-namespace",
							Name:      "policy-name-2",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.6", "1.2.3.7"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.3.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.5"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod3",
								},
								{
									Name: "pod4",
								},
							},
						},
					},
				},
				ingressRules: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5", "2.3.4.5"}, nil, 3),
				egressRules:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16", "2.3.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
				podselectorEndpoints: []policyinfo.PodEndpoint{
					{
						Name: "pod1",
					},
					{
						Name: "pod5",
					},
				},
				epValidator: func(_ *networking.NetworkPolicy, _ *policyinfo.PolicyEndpoint) bool {
					return true
				},
			},
			want: want{
				updateCount: 1,
				deleteCount: 1,
			},
		},
		{
			name: "create new to fit endpoints",
			fields: fields{
				endpointChunkSize: 3,
			},
			args: args{
				policy: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "policy-namespace",
						Name:      "policy-name",
					},
					Spec: networking.NetworkPolicySpec{},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "policy-namespace",
							Name:      "policy-name-1",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16", "2.3.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod1",
								},
								{
									Name: "pod2",
								},
							},
						},
					},
				},
				ingressRules: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5", "2.3.4.5", "2.3.4.6"}, nil, 3),
				egressRules:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16", "2.3.0.0/16", "2.4.0.0/16", "2.5.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
				podselectorEndpoints: []policyinfo.PodEndpoint{
					{
						Name: "pod1",
					},
					{
						Name: "pod2",
					},
					{
						Name: "pod3",
					},
					{
						Name: "pod4",
					},
				},
				epValidator: func(_ *networking.NetworkPolicy, _ *policyinfo.PolicyEndpoint) bool {
					return true
				},
			},
			want: want{
				createCount: 1,
				updateCount: 1,
			},
		},
		{
			name: "reuse to be deleted endpoints",
			fields: fields{
				endpointChunkSize: 3,
			},
			args: args{
				policy: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "policy-namespace",
						Name:      "policy-name",
					},
					Spec: networking.NetworkPolicySpec{},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "policy-namespace",
							Name:      "policy-name-1",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16", "2.3.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod1",
								},
								{
									Name: "pod2",
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "policy-namespace",
							Name:      "policy-name-2",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"4.2.3.6", "4.2.3.7"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"4.3.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.5"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod1-1",
								},
								{
									Name: "pod1-2",
								},
							},
						},
					},
				},
				ingressRules: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5", "2.3.4.5", "2.3.4.6"}, nil, 3),
				egressRules:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16", "2.3.0.0/16", "2.4.0.0/16", "2.5.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
				podselectorEndpoints: []policyinfo.PodEndpoint{
					{
						Name: "pod1",
					},
					{
						Name: "pod2",
					},
					{
						Name: "pod3",
					},
					{
						Name: "pod4",
					},
				},
				epValidator: func(_ *networking.NetworkPolicy, _ *policyinfo.PolicyEndpoint) bool {
					return true
				},
			},
			want: want{
				updateCount: 2,
			},
		},
		{
			name: "cleanup endpoints with same entries",
			fields: fields{
				endpointChunkSize: 3,
			},
			args: args{
				policy: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "policy-namespace",
						Name:      "policy-name",
					},
					Spec: networking.NetworkPolicySpec{},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "policy-namespace",
							Name:      "policy-name-1",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16", "2.3.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod1",
								},
								{
									Name: "pod2",
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "policy-namespace",
							Name:      "policy-name-dup",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Ingress: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.5", "1.2.3.4"}, nil, 3),
							Egress:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.3.0.0/16", "2.2.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
							PodSelectorEndpoints: []policyinfo.PodEndpoint{
								{
									Name: "pod1",
								},
								{
									Name: "pod2",
								},
							},
						},
					},
				},
				ingressRules: getEPInfoHelper([]policyinfo.NetworkAddress{"1.2.3.4", "1.2.3.5"}, nil, 3),
				egressRules:  getEPInfoHelper([]policyinfo.NetworkAddress{"2.2.0.0/16", "2.3.0.0/16"}, []policyinfo.NetworkAddress{"2.2.3.4"}, 1),
				podselectorEndpoints: []policyinfo.PodEndpoint{
					{
						Name: "pod1",
					},
					{
						Name: "pod2",
					},
				},
				epValidator: func(_ *networking.NetworkPolicy, _ *policyinfo.PolicyEndpoint) bool {
					return true
				},
			},
			want: want{
				deleteCount: 1,
				updateCount: 1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &policyEndpointsManager{
				endpointChunkSize: tt.fields.endpointChunkSize,
			}
			createList, updateList, deleteList, err := m.computePolicyEndpoints(tt.args.policy, tt.args.policyEndpoints,
				tt.args.ingressRules, tt.args.egressRules, tt.args.podselectorEndpoints)

			if len(tt.wantErr) > 0 {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want.createCount, len(createList))
				assert.Equal(t, tt.want.updateCount, len(updateList))
				assert.Equal(t, tt.want.deleteCount, len(deleteList))

				var gotIngressRules []policyinfo.EndpointInfo
				var gotEgressRules []policyinfo.EndpointInfo
				var gotPodSelectorEndpoints []policyinfo.PodEndpoint

				for _, epList := range [][]policyinfo.PolicyEndpoint{createList, updateList, deleteList} {
					for _, ep := range epList {
						epValidator := tt.args.epValidator
						if epValidator == nil {
							epValidator = defaultEPValidator
						}
						ret := epValidator(tt.args.policy, &ep)
						assert.True(t, ret)
						//assert.True(t, defaultEPValidator(tt.args.policy, &ep))
						gotIngressRules = append(gotIngressRules, ep.Spec.Ingress...)
						gotEgressRules = append(gotEgressRules, ep.Spec.Egress...)
						gotPodSelectorEndpoints = append(gotPodSelectorEndpoints, ep.Spec.PodSelectorEndpoints...)
					}
				}
				for _, lst := range [][]policyinfo.EndpointInfo{tt.args.ingressRules, tt.args.egressRules, gotIngressRules, gotEgressRules} {
					sort.Slice(lst, func(i, j int) bool {
						return lst[i].CIDR < lst[j].CIDR
					})
				}
				for _, lst := range [][]policyinfo.PodEndpoint{tt.args.podselectorEndpoints, gotPodSelectorEndpoints} {
					sort.Slice(lst, func(i, j int) bool {
						return lst[i].Name < lst[j].Name
					})
				}
				assert.Equal(t, tt.args.ingressRules, gotIngressRules)
				assert.Equal(t, tt.args.egressRules, gotEgressRules)
				assert.Equal(t, tt.args.podselectorEndpoints, gotPodSelectorEndpoints)
			}
		})
	}
}

func Test_processPolicyEndpoints(t *testing.T) {
	m := &policyEndpointsManager{
		logger: zap.New(),
	}

	p80 := int32(80)
	p8080 := int32(8080)
	pTCP := corev1.ProtocolTCP
	pUDP := corev1.ProtocolUDP

	pes := m.processPolicyEndpoints([]policyinfo.PolicyEndpoint{
		{
			Spec: policyinfo.PolicyEndpointSpec{
				Ingress: []policyinfo.EndpointInfo{
					{
						CIDR: "1.2.3.4",
						Ports: []policyinfo.Port{
							{Port: &p80, Protocol: &pTCP},
						},
					},
					{
						CIDR: "1.2.3.4",
						Ports: []policyinfo.Port{
							{Port: &p8080, Protocol: &pTCP},
						},
					},
					{
						CIDR: "1.2.3.4",
						Ports: []policyinfo.Port{
							{Protocol: &pUDP},
						},
					},
				},
				Egress: []policyinfo.EndpointInfo{
					{
						CIDR: "1.2.3.5",
						Ports: []policyinfo.Port{
							{Port: &p80, Protocol: &pTCP},
						},
					},
					{
						CIDR: "1.2.3.5",
						Ports: []policyinfo.Port{
							{Port: &p8080, Protocol: &pTCP},
						},
					},
				},
			},
		},
	})
	assert.Equal(t, 1, len(pes[0].Spec.Ingress))
	assert.Equal(t, 1, len(pes[0].Spec.Egress))
	assert.Equal(t, "1.2.3.4", string(pes[0].Spec.Ingress[0].CIDR))
	assert.Equal(t, "1.2.3.5", string(pes[0].Spec.Egress[0].CIDR))
	assert.Equal(t, 3, len(pes[0].Spec.Ingress[0].Ports))
	assert.Equal(t, 2, len(pes[0].Spec.Egress[0].Ports))
}

func Test_combineRulesEndpoints(t *testing.T) {
	p53 := int32(53)
	pTCP := corev1.ProtocolTCP
	pUDP := corev1.ProtocolUDP

	tests := []struct {
		name      string
		endpoints []policyinfo.EndpointInfo
		wantCount int
		// wantPorts maps CIDR string → expected port count; -1 means expect nil/empty (allow all)
		wantPorts map[string]int
	}{
		{
			name: "same CIDR different exception lists are not merged",
			endpoints: []policyinfo.EndpointInfo{
				// DNS rule: allow port 53 to anywhere (no exceptions)
				{
					CIDR:  "0.0.0.0/0",
					Ports: []policyinfo.Port{{Protocol: &pTCP, Port: &p53}, {Protocol: &pUDP, Port: &p53}},
				},
				// Broad allow: all ports to 0.0.0.0/0 except private ranges
				{
					CIDR:   "0.0.0.0/0",
					Except: []policyinfo.NetworkAddress{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
				},
			},
			wantCount: 2,
		},
		{
			name: "allow-all ports semantics preserved when merging with specific-port rule",
			endpoints: []policyinfo.EndpointInfo{
				// Rule with specific ports
				{
					CIDR:  "172.16.0.0/12",
					Ports: []policyinfo.Port{{Protocol: &pTCP, Port: &p53}, {Protocol: &pUDP, Port: &p53}},
				},
				// Rule with no port restriction (allow all) for same CIDR
				{
					CIDR: "172.16.0.0/12",
				},
			},
			wantCount: 1,
			wantPorts: map[string]int{"172.16.0.0/12": -1}, // -1 = expect nil ports (allow all)
		},
		{
			name: "same CIDR same exceptions different ports are merged",
			endpoints: []policyinfo.EndpointInfo{
				{CIDR: "1.2.3.4", Ports: []policyinfo.Port{{Protocol: &pTCP, Port: &p53}}},
				{CIDR: "1.2.3.4", Ports: []policyinfo.Port{{Protocol: &pUDP, Port: &p53}}},
			},
			wantCount: 1,
			wantPorts: map[string]int{"1.2.3.4": 2},
		},
		{
			name: "exception list order does not affect key stability",
			endpoints: []policyinfo.EndpointInfo{
				{
					CIDR:   "0.0.0.0/0",
					Except: []policyinfo.NetworkAddress{"10.0.0.0/8", "172.16.0.0/12"},
					Ports:  []policyinfo.Port{{Protocol: &pTCP, Port: &p53}},
				},
				{
					// Same CIDR and same excepts but in reverse order — should still merge
					CIDR:   "0.0.0.0/0",
					Except: []policyinfo.NetworkAddress{"172.16.0.0/12", "10.0.0.0/8"},
					Ports:  []policyinfo.Port{{Protocol: &pUDP, Port: &p53}},
				},
			},
			wantCount: 1,
			wantPorts: map[string]int{"0.0.0.0/0": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := combineRulesEndpoints(tt.endpoints)
			assert.Equal(t, tt.wantCount, len(result))
			if tt.wantPorts != nil {
				for _, ep := range result {
					expected, ok := tt.wantPorts[string(ep.CIDR)]
					if !ok {
						continue
					}
					if expected == -1 {
						assert.Empty(t, ep.Ports, "expected allow-all (nil/empty) ports for CIDR %s", ep.CIDR)
					} else {
						assert.Equal(t, expected, len(ep.Ports), "unexpected port count for CIDR %s", ep.CIDR)
					}
				}
			}
		})
	}
}

func Test_policyEndpointsManager_computeApplicationNetworkPolicyEndpoints(t *testing.T) {
	type args struct {
		anp                  *policyinfo.ApplicationNetworkPolicy
		policyEndpoints      []policyinfo.PolicyEndpoint
		ingressRules         []policyinfo.EndpointInfo
		egressRules          []policyinfo.EndpointInfo
		podselectorEndpoints []policyinfo.PodEndpoint
	}
	type want struct {
		createCount int
		updateCount int
		deleteCount int
	}

	protocolTCP := corev1.ProtocolTCP
	port443 := int32(443)
	blockOwnerDeletion := true
	isController := true

	tests := []struct {
		name string
		args args
		want want
	}{
		{
			name: "ANP with FQDN egress rules",
			args: args{
				anp: &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-anp",
						Namespace: "default",
					},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "web"},
						},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
					},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{},
				ingressRules:    []policyinfo.EndpointInfo{},
				egressRules: []policyinfo.EndpointInfo{
					{
						DomainName: "example.com",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				podselectorEndpoints: []policyinfo.PodEndpoint{},
			},
			want: want{
				createCount: 1,
				updateCount: 0,
				deleteCount: 0,
			},
		},
		{
			name: "ANP with ingress and egress CIDR rules",
			args: args{
				anp: &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-anp-cidr",
						Namespace: "default",
					},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "web"},
						},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
					},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{},
				ingressRules: []policyinfo.EndpointInfo{
					{
						CIDR: "172.17.0.0/16",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				egressRules: []policyinfo.EndpointInfo{
					{
						CIDR: "10.0.0.0/8",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				podselectorEndpoints: []policyinfo.PodEndpoint{},
			},
			want: want{
				createCount: 1,
				updateCount: 0,
				deleteCount: 0,
			},
		},
		{
			name: "ANP with ingress and egress FQDN rules",
			args: args{
				anp: &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-anp-fqdn",
						Namespace: "default",
					},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "web"},
						},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
					},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{},
				ingressRules: []policyinfo.EndpointInfo{
					{
						CIDR: "172.17.0.0/16",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				egressRules: []policyinfo.EndpointInfo{
					{
						DomainName: "*.amazonaws.com",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				podselectorEndpoints: []policyinfo.PodEndpoint{},
			},
			want: want{
				createCount: 1,
				updateCount: 0,
				deleteCount: 0,
			},
		},
		{
			name: "ANP with only ingress rules",
			args: args{
				anp: &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-anp-ingress",
						Namespace: "default",
					},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "web"},
						},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress},
					},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{},
				ingressRules: []policyinfo.EndpointInfo{
					{
						CIDR: "172.17.0.0/16",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				egressRules:          []policyinfo.EndpointInfo{},
				podselectorEndpoints: []policyinfo.PodEndpoint{},
			},
			want: want{
				createCount: 1,
				updateCount: 0,
				deleteCount: 0,
			},
		},
		{
			name: "ANP update existing endpoint",
			args: args{
				anp: &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-anp-update",
						Namespace: "default",
						UID:       "test-uid",
					},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "web"},
						},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
					},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "existing-endpoint",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "networking.k8s.aws/v1alpha1",
									Name:       "test-anp-update",
									UID:        "test-uid",
								},
							},
						},
						Spec: policyinfo.PolicyEndpointSpec{
							PolicyRef: policyinfo.PolicyReference{
								Name:      "test-anp-update",
								Namespace: "default",
							},
						},
					},
				},
				ingressRules: []policyinfo.EndpointInfo{},
				egressRules: []policyinfo.EndpointInfo{
					{
						DomainName: "example.com",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				podselectorEndpoints: []policyinfo.PodEndpoint{},
			},
			want: want{
				createCount: 0,
				updateCount: 1,
				deleteCount: 0,
			},
		},
		{
			name: "ANP delete unused endpoint",
			args: args{
				anp: &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-anp-delete",
						Namespace: "default",
						UID:       "test-uid",
					},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "web"},
						},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
					},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "unused-endpoint",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "networking.k8s.aws/v1alpha1",
									Name:       "test-anp-delete",
									UID:        "test-uid",
								},
							},
						},
						Spec: policyinfo.PolicyEndpointSpec{
							PolicyRef: policyinfo.PolicyReference{
								Name:      "test-anp-delete",
								Namespace: "default",
							},
							Egress: []policyinfo.EndpointInfo{
								{
									DomainName: "old-domain.com",
								},
							},
						},
					},
				},
				ingressRules: []policyinfo.EndpointInfo{},
				egressRules: []policyinfo.EndpointInfo{
					{
						DomainName: "new-domain.com",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &protocolTCP},
						},
					},
				},
				podselectorEndpoints: []policyinfo.PodEndpoint{},
			},
			want: want{
				createCount: 0,
				updateCount: 1, // The unused endpoint gets converted to update (policy invariant)
				deleteCount: 0,
			},
		},
		{
			name: "ANP with multiple endpoints - delete extra",
			args: args{
				anp: &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-anp-multi",
						Namespace: "default",
						UID:       "test-uid",
					},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "web"},
						},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
					},
				},
				policyEndpoints: []policyinfo.PolicyEndpoint{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "endpoint-1",
							Namespace: "default",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Egress: []policyinfo.EndpointInfo{
								{DomainName: "keep.com"},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "endpoint-2",
							Namespace: "default",
						},
						Spec: policyinfo.PolicyEndpointSpec{
							Egress: []policyinfo.EndpointInfo{
								{DomainName: "delete.com"},
							},
						},
					},
				},
				ingressRules: []policyinfo.EndpointInfo{},
				egressRules: []policyinfo.EndpointInfo{
					{DomainName: "keep.com"},
				},
				podselectorEndpoints: []policyinfo.PodEndpoint{},
			},
			want: want{
				createCount: 0,
				updateCount: 1,
				deleteCount: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &policyEndpointsManager{
				endpointChunkSize: 100,
				logger:            zap.New(),
			}
			createList, updateList, deleteList, err := m.computeApplicationNetworkPolicyEndpoints(
				tt.args.anp, tt.args.policyEndpoints, tt.args.ingressRules, tt.args.egressRules, tt.args.podselectorEndpoints)

			assert.NoError(t, err)
			assert.Equal(t, tt.want.createCount, len(createList))
			assert.Equal(t, tt.want.updateCount, len(updateList))
			assert.Equal(t, tt.want.deleteCount, len(deleteList))

			if len(createList) > 0 {
				ep := createList[0]
				assert.Equal(t, tt.args.anp.Namespace, ep.Namespace)
				assert.Equal(t, tt.args.anp.Name+"-", ep.GenerateName)
				assert.Equal(t, []metav1.OwnerReference{
					{
						APIVersion:         "networking.k8s.aws/v1alpha1",
						Name:               tt.args.anp.Name,
						UID:                tt.args.anp.UID,
						BlockOwnerDeletion: &blockOwnerDeletion,
						Kind:               "ApplicationNetworkPolicy",
						Controller:         &isController,
					},
				}, ep.OwnerReferences)
			}
		})
	}
}

func Test_processANPPolicyEndpoints(t *testing.T) {
	m := &policyEndpointsManager{
		logger: zap.New(),
	}

	port443 := int32(443)
	pTCP := corev1.ProtocolTCP

	pes := m.processANPPolicyEndpoints([]policyinfo.PolicyEndpoint{
		{
			Spec: policyinfo.PolicyEndpointSpec{
				Egress: []policyinfo.EndpointInfo{
					{
						DomainName: "example.com",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &pTCP},
						},
					},
					{
						DomainName: "example.com",
						Ports: []policyinfo.Port{
							{Port: &port443, Protocol: &pTCP},
						},
					},
				},
			},
		},
	})

	assert.Equal(t, 1, len(pes))
	assert.Equal(t, 1, len(pes[0].Spec.Egress))
	assert.Equal(t, policyinfo.DomainName("example.com"), pes[0].Spec.Egress[0].DomainName)
	assert.Equal(t, 2, len(pes[0].Spec.Egress[0].Ports))
}

func Test_getEndpointInfoKey(t *testing.T) {
	m := &policyEndpointsManager{}
	port443 := int32(443)
	pTCP := corev1.ProtocolTCP

	tests := []struct {
		name string
		info policyinfo.EndpointInfo
		want string
	}{
		{
			name: "FQDN endpoint",
			info: policyinfo.EndpointInfo{
				DomainName: "example.com",
				Ports: []policyinfo.Port{
					{Port: &port443, Protocol: &pTCP},
				},
			},
		},
		{
			name: "CIDR endpoint",
			info: policyinfo.EndpointInfo{
				CIDR: "10.0.0.0/8",
				Ports: []policyinfo.Port{
					{Port: &port443, Protocol: &pTCP},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := m.getEndpointInfoKey(tt.info)
			assert.NotEmpty(t, key)
			// Verify same input produces same key
			key2 := m.getEndpointInfoKey(tt.info)
			assert.Equal(t, key, key2)
		})
	}

	// Verify different inputs produce different keys
	fqdnKey := m.getEndpointInfoKey(policyinfo.EndpointInfo{DomainName: "example.com"})
	cidrKey := m.getEndpointInfoKey(policyinfo.EndpointInfo{CIDR: "10.0.0.0/8"})
	assert.NotEqual(t, fqdnKey, cidrKey)
}

func TestPolicyEndpointsManager_ReconcileCNP(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	mockCNPResolver := &mockCNPEndpointsResolver{}

	manager := &policyEndpointsManager{
		k8sClient:            mockClient,
		cnpEndpointsResolver: mockCNPResolver,
		logger:               logr.Discard(),
	}

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cnp",
			UID:  "test-uid",
		},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Tier:     policyinfo.AdminTier,
			Priority: 100,
		},
	}

	// Mock resolver response
	mockCNPResolver.ingressRules = []policyinfo.ClusterEndpointInfo{
		{CIDR: "10.0.0.0/8"},
	}
	mockCNPResolver.egressRules = []policyinfo.ClusterEndpointInfo{
		{DomainName: "example.com"},
	}
	mockCNPResolver.podEndpoints = []policyinfo.PodEndpoint{
		{Name: "pod1", Namespace: "default", PodIP: "10.0.1.1", HostIP: "10.1.1.1"},
	}

	// Mock client calls
	mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	mockClient.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	err := manager.ReconcileCNP(context.Background(), cnp)
	assert.NoError(t, err)
	assert.True(t, mockCNPResolver.called)
}

func TestPolicyEndpointsManager_CleanupCNP(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)

	manager := &policyEndpointsManager{
		k8sClient: mockClient,
		logger:    logr.Discard(),
	}

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cnp",
			UID:  "test-uid",
		},
	}

	// Mock existing CPE
	cpeList := &policyinfo.ClusterPolicyEndpointList{
		Items: []policyinfo.ClusterPolicyEndpoint{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-test-cnp-abc123"},
				Spec:       policyinfo.ClusterPolicyEndpointSpec{PolicyRef: policyinfo.ClusterPolicyReference{Name: "test-cnp"}},
			},
		},
	}

	mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			list.(*policyinfo.ClusterPolicyEndpointList).Items = cpeList.Items
			return nil
		})
	mockClient.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	err := manager.CleanupCNP(context.Background(), cnp)
	assert.NoError(t, err)
}

// Mock CNP resolver for testing
type mockCNPEndpointsResolver struct {
	called       bool
	ingressRules []policyinfo.ClusterEndpointInfo
	egressRules  []policyinfo.ClusterEndpointInfo
	podEndpoints []policyinfo.PodEndpoint
}

func (m *mockCNPEndpointsResolver) ResolveClusterNetworkPolicy(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) ([]policyinfo.ClusterEndpointInfo, []policyinfo.ClusterEndpointInfo, []policyinfo.PodEndpoint, error) {
	m.called = true
	return m.ingressRules, m.egressRules, m.podEndpoints, nil
}

func Test_combineClusterRulesEndpoints(t *testing.T) {
	protocol := corev1.ProtocolTCP
	port53 := int32(53)
	port80 := int32(80)

	tests := []struct {
		name      string
		endpoints []policyinfo.ClusterEndpointInfo
		expected  []policyinfo.ClusterEndpointInfo
	}{
		{
			name: "combine same CIDR and action",
			endpoints: []policyinfo.ClusterEndpointInfo{
				{
					CIDR:   "10.0.0.1/32",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports:  []policyinfo.Port{{Protocol: &protocol, Port: &port53}},
				},
				{
					CIDR:   "10.0.0.1/32",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports:  []policyinfo.Port{{Protocol: &protocol, Port: &port80}},
				},
			},
			expected: []policyinfo.ClusterEndpointInfo{
				{
					CIDR:   "10.0.0.1/32",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports: []policyinfo.Port{
						{Protocol: &protocol, Port: &port53},
						{Protocol: &protocol, Port: &port80},
					},
				},
			},
		},
		{
			name: "don't combine different actions",
			endpoints: []policyinfo.ClusterEndpointInfo{
				{
					CIDR:   "10.0.0.1/32",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports:  []policyinfo.Port{{Protocol: &protocol, Port: &port53}},
				},
				{
					CIDR:   "10.0.0.1/32",
					Action: policyinfo.ClusterNetworkPolicyRuleActionDeny,
					Ports:  []policyinfo.Port{{Protocol: &protocol, Port: &port80}},
				},
			},
			expected: []policyinfo.ClusterEndpointInfo{
				{
					CIDR:   "10.0.0.1/32",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports:  []policyinfo.Port{{Protocol: &protocol, Port: &port53}},
				},
				{
					CIDR:   "10.0.0.1/32",
					Action: policyinfo.ClusterNetworkPolicyRuleActionDeny,
					Ports:  []policyinfo.Port{{Protocol: &protocol, Port: &port80}},
				},
			},
		},
		{
			name: "combine same domain and action",
			endpoints: []policyinfo.ClusterEndpointInfo{
				{
					DomainName: "example.com",
					Action:     policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports:      []policyinfo.Port{{Protocol: &protocol, Port: &port53}},
				},
				{
					DomainName: "example.com",
					Action:     policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports:      []policyinfo.Port{{Protocol: &protocol, Port: &port80}},
				},
			},
			expected: []policyinfo.ClusterEndpointInfo{
				{
					DomainName: "example.com",
					Action:     policyinfo.ClusterNetworkPolicyRuleActionAccept,
					Ports: []policyinfo.Port{
						{Protocol: &protocol, Port: &port53},
						{Protocol: &protocol, Port: &port80},
					},
				},
			},
		},
		{
			name:      "empty input",
			endpoints: []policyinfo.ClusterEndpointInfo{},
			expected:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := combineClusterRulesEndpoints(tt.endpoints)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}
