package policyendpoints

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

func Test_policyEndpointsManager_getgetPolicyEndpoints(t *testing.T) {
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
		return ep.Namespace == netpol.Namespace && ep.Labels["networking.k8s.aws/policy-name"] == netpol.Name &&
			equality.Semantic.DeepEqual(ep.OwnerReferences, []metav1.OwnerReference{
				{
					APIVersion:         "networking.k8s.io/v1",
					Kind:               "NetworkPolicy",
					Name:               netpol.Name,
					BlockOwnerDeletion: &blockOwnerDeletion,
					Controller:         &isController,
				},
			}) && equality.Semantic.DeepEqual(ep.Spec.PolicyRef,
			policyinfo.PolicyReference{
				Namespace: netpol.Namespace,
				Name:      netpol.Name}) &&
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &policyEndpointsManager{
				endpointChunkSize: tt.fields.endpointChunkSize,
			}
			createList, updateList, deleteList, err := m.getPolicyEndpoints(tt.args.policy, tt.args.policyEndpoints,
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
