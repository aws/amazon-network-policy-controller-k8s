package resolvers

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
)

func TestEndpointsResolver_buildAllowAllEgress(t *testing.T) {
	protocolTCP := corev1.ProtocolTCP
	protocolUDP := corev1.ProtocolUDP
	policyInfoProtocolTCP := corev1.ProtocolTCP
	policyInfoProtocolUDP := corev1.ProtocolUDP
	var port53 int32 = 53
	var port80 int32 = 80
	intOrStrPort53 := intstr.FromInt(int(port53))
	namedPort := intstr.FromString("named-port")
	type args struct {
		ports []networking.NetworkPolicyPort
	}
	tests := []struct {
		name string
		args args
		want []policyinfo.EndpointInfo
	}{
		{
			name: "empty ports",
			want: []policyinfo.EndpointInfo{
				{
					CIDR: "0.0.0.0/0",
				},
				{
					CIDR: "::/0",
				},
			},
		},
		{
			name: "no port protocol only",
			args: args{
				ports: []networking.NetworkPolicyPort{
					{
						Protocol: &protocolTCP,
					},
				},
			},
			want: []policyinfo.EndpointInfo{
				{
					CIDR: "0.0.0.0/0",
					Ports: []policyinfo.Port{
						{
							Protocol: &policyInfoProtocolTCP,
						},
					},
				},
				{
					CIDR: "::/0",
					Ports: []policyinfo.Port{
						{
							Protocol: &policyInfoProtocolTCP,
						},
					},
				},
			},
		},
		{
			name: "both port and protocol",
			args: args{
				ports: []networking.NetworkPolicyPort{
					{
						Protocol: &protocolTCP,
						Port:     &intOrStrPort53,
					},
					{
						Protocol: &protocolUDP,
						Port:     &intOrStrPort53,
					},
				},
			},
			want: []policyinfo.EndpointInfo{
				{
					CIDR: "0.0.0.0/0",
					Ports: []policyinfo.Port{
						{
							Protocol: &policyInfoProtocolTCP,
							Port:     &port53,
						},
						{
							Protocol: &policyInfoProtocolUDP,
							Port:     &port53,
						},
					},
				},
				{
					CIDR: "::/0",
					Ports: []policyinfo.Port{
						{
							Protocol: &policyInfoProtocolTCP,
							Port:     &port53,
						},
						{
							Protocol: &policyInfoProtocolUDP,
							Port:     &port53,
						},
					},
				},
			},
		},
		{
			name: "named port and port ranges",
			args: args{
				ports: []networking.NetworkPolicyPort{
					{
						Protocol: &protocolTCP,
						Port:     &namedPort,
					},
					{
						Protocol: &protocolUDP,
						Port:     &intOrStrPort53,
						EndPort:  &port80,
					},
				},
			},
			want: []policyinfo.EndpointInfo{
				{
					CIDR: "0.0.0.0/0",
					Ports: []policyinfo.Port{
						{
							Protocol: &policyInfoProtocolUDP,
							Port:     &port53,
							EndPort:  &port80,
						},
					},
				},
				{
					CIDR: "::/0",
					Ports: []policyinfo.Port{
						{
							Protocol: &policyInfoProtocolUDP,
							Port:     &port53,
							EndPort:  &port80,
						},
					},
				},
			},
		},
		{
			name: "named port only",
			args: args{
				ports: []networking.NetworkPolicyPort{
					{
						Protocol: &protocolTCP,
						Port:     &namedPort,
					},
					{
						Protocol: &protocolUDP,
						Port:     &namedPort,
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &defaultEndpointsResolver{}
			got := resolver.buildAllowAllEgress(tt.args.ports)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEndpointsResolver_Resolve(t *testing.T) {
	type podListCall struct {
		pods []corev1.Pod
		err  error
	}
	type serviceListCall struct {
		services []corev1.Service
		err      error
	}
	type args struct {
		netpol           *networking.NetworkPolicy
		podListCalls     []podListCall
		serviceListCalls []serviceListCall
	}
	protocolTCP := corev1.ProtocolTCP
	protocolUDP := corev1.ProtocolUDP
	port80 := int32(80)
	intOrStrPort80 := intstr.FromInt(int(port80))
	intOrStrPortName := intstr.FromString("port-name")
	port443 := int32(443)
	denyAll := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deny-all",
			Namespace: "ns",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networking.PolicyType{
				networking.PolicyTypeIngress,
				networking.PolicyTypeEgress,
			},
		},
	}
	ingressPolicy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ingress-policy",
			Namespace: "ns",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			Ingress: []networking.NetworkPolicyIngressRule{
				{
					From: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{},
						},
					},
				},
			},
		},
	}
	egressPolicy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "egress-policy",
			Namespace: "ns",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			Egress: []networking.NetworkPolicyEgressRule{
				{
					To: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{},
						},
					},
				},
			},
		},
	}
	pod1 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "ns",
		},
		Status: corev1.PodStatus{
			PodIP: "1.0.0.1",
		},
	}
	pod2 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod2",
			Namespace: "ns",
		},
		Status: corev1.PodStatus{
			PodIP: "1.0.0.2",
		},
	}
	pod3 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod3",
			Namespace: "ns",
			Annotations: map[string]string{
				"vpc.amazonaws.com/pod-ips": "1.0.0.3",
			},
		},
	}
	podNoIP := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-no-ip",
			Namespace: "ns",
		},
	}
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc",
			Namespace: "ns",
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "100.0.10.20",
		},
	}

	tests := []struct {
		name                 string
		args                 args
		wantErr              string
		wantIngressEndpoints []policyinfo.EndpointInfo
		wantEgressEndpoints  []policyinfo.EndpointInfo
		wantPodEndpoints     []policyinfo.PodEndpoint
	}{
		{
			name: "deny all policy no pods",
			args: args{
				netpol: denyAll,
				podListCalls: []podListCall{
					{},
				},
			},
		},
		{
			name: "multiple isolated pods",
			args: args{
				netpol: denyAll,
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{pod1, pod3, podNoIP},
					},
				},
			},
			wantPodEndpoints: []policyinfo.PodEndpoint{
				{PodIP: "1.0.0.1", Name: "pod1", Namespace: "ns"},
				{PodIP: "1.0.0.3", Name: "pod3", Namespace: "ns"},
			},
		},
		{
			name: "ingress rules",
			args: args{
				netpol: ingressPolicy,
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{pod1, pod2, pod3},
					},
				},
			},
			wantIngressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "1.0.0.1"},
				{CIDR: "1.0.0.2"},
				{CIDR: "1.0.0.3"},
			},
			wantPodEndpoints: []policyinfo.PodEndpoint{
				{PodIP: "1.0.0.1", Name: "pod1", Namespace: "ns"},
				{PodIP: "1.0.0.2", Name: "pod2", Namespace: "ns"},
				{PodIP: "1.0.0.3", Name: "pod3", Namespace: "ns"},
			},
		},
		{
			name: "egress rules",
			args: args{
				netpol: egressPolicy,
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{pod2, podNoIP, pod3},
					},
				},
				serviceListCalls: []serviceListCall{
					{
						services: []corev1.Service{svc},
					},
				},
			},
			wantEgressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "1.0.0.2"},
				{CIDR: "1.0.0.3"},
				{CIDR: "100.0.10.20"},
			},
			wantPodEndpoints: []policyinfo.PodEndpoint{
				{PodIP: "1.0.0.2", Name: "pod2", Namespace: "ns"},
				{PodIP: "1.0.0.3", Name: "pod3", Namespace: "ns"},
			},
		},
		{
			name: "exclude headless service",
			args: args{
				netpol: egressPolicy,
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{pod2, podNoIP, pod3},
					},
				},
				serviceListCalls: []serviceListCall{
					{
						services: []corev1.Service{
							{
								Spec: corev1.ServiceSpec{
									ClusterIP: "None",
								},
							},
						},
					},
				},
			},
			wantEgressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "1.0.0.2"},
				{CIDR: "1.0.0.3"},
			},
			wantPodEndpoints: []policyinfo.PodEndpoint{
				{PodIP: "1.0.0.2", Name: "pod2", Namespace: "ns"},
				{PodIP: "1.0.0.3", Name: "pod3", Namespace: "ns"},
			},
		},
		{
			name: "resolve network peers, ingress/egress",
			args: args{
				netpol: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "netpol",
						Namespace: "ns",
					},
					Spec: networking.NetworkPolicySpec{
						PodSelector: metav1.LabelSelector{},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
						Ingress: []networking.NetworkPolicyIngressRule{
							{
								From: []networking.NetworkPolicyPeer{
									{
										IPBlock: &networking.IPBlock{
											CIDR:   "10.20.0.0/16",
											Except: []string{"10.20.0.5", "10.20.0.6"},
										},
									},
								},
								Ports: []networking.NetworkPolicyPort{
									{
										Protocol: &protocolTCP,
										Port:     &intOrStrPort80,
									},
								},
							},
							{
								From: []networking.NetworkPolicyPeer{
									{
										IPBlock: &networking.IPBlock{
											CIDR:   "20.51.78.0/24",
											Except: []string{"20.51.78.5"},
										},
									},
								},
								Ports: []networking.NetworkPolicyPort{
									{
										Protocol: &protocolUDP,
										Port:     &intOrStrPortName,
									},
								},
							},
						},
						Egress: []networking.NetworkPolicyEgressRule{
							{
								To: []networking.NetworkPolicyPeer{
									{
										IPBlock: &networking.IPBlock{
											CIDR: "192.168.33.0/24",
										},
									},
								},
							},
							{
								To: []networking.NetworkPolicyPeer{
									{
										IPBlock: &networking.IPBlock{
											CIDR:   "10.30.0.0/16",
											Except: []string{"10.30.0.5", "10.30.0.6"},
										},
									},
								},
								Ports: []networking.NetworkPolicyPort{
									{
										Protocol: &protocolTCP,
										Port:     &intOrStrPort80,
										EndPort:  &port443,
									},
								},
							},
						},
					},
				},
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{podNoIP, pod3},
					},
				},
			},
			wantIngressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "10.20.0.0/16", Except: []policyinfo.NetworkAddress{"10.20.0.5", "10.20.0.6"}, Ports: []policyinfo.Port{{Protocol: &protocolTCP, Port: &port80}}},
			},
			wantEgressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "10.30.0.0/16", Except: []policyinfo.NetworkAddress{"10.30.0.5", "10.30.0.6"}, Ports: []policyinfo.Port{{Protocol: &protocolTCP, Port: &port80, EndPort: &port443}}},
				{CIDR: "192.168.33.0/24"},
			},
			wantPodEndpoints: []policyinfo.PodEndpoint{
				{PodIP: "1.0.0.3", Name: "pod3", Namespace: "ns"},
			},
		},
		{
			name: "allow all, ingress/egress to specific ports",
			args: args{
				netpol: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "netpol",
						Namespace: "ns",
					},
					Spec: networking.NetworkPolicySpec{
						PodSelector: metav1.LabelSelector{},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
						Ingress: []networking.NetworkPolicyIngressRule{
							{
								Ports: []networking.NetworkPolicyPort{
									{
										Protocol: &protocolTCP,
										Port:     &intOrStrPort80,
									},
								},
							},
						},
						Egress: []networking.NetworkPolicyEgressRule{
							{
								Ports: []networking.NetworkPolicyPort{
									{
										Protocol: &protocolTCP,
										Port:     &intOrStrPort80,
										EndPort:  &port443,
									},
								},
							},
						},
					},
				},
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{podNoIP},
					},
				},
			},
			wantIngressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "0.0.0.0/0", Ports: []policyinfo.Port{{Protocol: &protocolTCP, Port: &port80}}},
				{CIDR: "::/0", Ports: []policyinfo.Port{{Protocol: &protocolTCP, Port: &port80}}},
			},
			wantEgressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "0.0.0.0/0", Ports: []policyinfo.Port{{Protocol: &protocolTCP, Port: &port80, EndPort: &port443}}},
				{CIDR: "::/0", Ports: []policyinfo.Port{{Protocol: &protocolTCP, Port: &port80, EndPort: &port443}}},
			},
		},
		{
			name: "allow all, ingress all pods / egress unable to resolve named ports",
			args: args{
				netpol: &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "netpol",
						Namespace: "ns",
					},
					Spec: networking.NetworkPolicySpec{
						PodSelector: metav1.LabelSelector{},
						PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
						Ingress: []networking.NetworkPolicyIngressRule{
							{},
						},
						Egress: []networking.NetworkPolicyEgressRule{
							{
								Ports: []networking.NetworkPolicyPort{
									{
										Protocol: &protocolTCP,
										Port:     &intOrStrPortName,
									},
								},
							},
						},
					},
				},
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{podNoIP},
					},
				},
			},
			wantIngressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "0.0.0.0/0"},
				{CIDR: "::/0"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := mock_client.NewMockClient(ctrl)
			resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

			for _, item := range tt.args.podListCalls {
				call := item
				podList := &corev1.PodList{}
				mockClient.EXPECT().List(gomock.Any(), podList, gomock.Any()).DoAndReturn(
					func(ctx context.Context, podList *corev1.PodList, opts ...client.ListOption) error {
						for _, pod := range call.pods {
							podList.Items = append(podList.Items, *(pod.DeepCopy()))
						}
						return call.err
					},
				).AnyTimes()
			}
			for _, item := range tt.args.serviceListCalls {
				call := item
				serviceList := &corev1.ServiceList{}
				mockClient.EXPECT().List(gomock.Any(), serviceList, gomock.Any()).DoAndReturn(
					func(ctx context.Context, serviceList *corev1.ServiceList, opts ...client.ListOption) error {
						for _, svc := range call.services {
							serviceList.Items = append(serviceList.Items, *(svc.DeepCopy()))
						}
						return call.err
					},
				).AnyTimes()
			}

			ingressEndpoints, egressEndpoints, podEndpoints, err := resolver.Resolve(context.Background(), tt.args.netpol)

			if len(tt.wantErr) > 0 {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				for _, lst := range [][]policyinfo.EndpointInfo{tt.wantEgressEndpoints, tt.wantEgressEndpoints, ingressEndpoints, egressEndpoints} {
					sort.Slice(lst, func(i, j int) bool {
						return lst[i].CIDR < lst[j].CIDR
					})
				}
				for _, lst := range [][]policyinfo.PodEndpoint{tt.wantPodEndpoints, podEndpoints} {
					sort.Slice(lst, func(i, j int) bool {
						return lst[i].Name < lst[j].Name
					})
				}

				assert.Equal(t, tt.wantIngressEndpoints, ingressEndpoints)
				assert.Equal(t, tt.wantEgressEndpoints, egressEndpoints)
				assert.Equal(t, tt.wantPodEndpoints, podEndpoints)
			}
		})
	}
}

func TestEndpointsResolver_ResolveNetworkPeers(t *testing.T) {
	protocolTCP := corev1.ProtocolTCP
	port80 := int32(80)
	port8080 := int32(8080)
	port9090 := int32(9090)

	srcPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "src",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "pod1",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: port80,
							Protocol:      corev1.ProtocolTCP,
							Name:          "src-port",
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			PodIP: "1.0.0.1",
		},
	}

	dstPodOne := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod2",
			Namespace: "dst",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "pod2",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: port8080,
							Protocol:      corev1.ProtocolTCP,
							Name:          "dst-port",
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			PodIP: "1.0.0.2",
		},
	}
	dstPodTwo := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod3",
			Namespace: "dst",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "pod3",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: port8080,
							Protocol:      corev1.ProtocolTCP,
							Name:          "test-port",
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			PodIP: "1.0.0.3",
		},
	}

	// the policy is applied to dst namespace on dst pod
	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "netpol",
			Namespace: "dst",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
			Ingress: []networking.NetworkPolicyIngressRule{
				{
					From: []networking.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "src",
								},
							},
						},
					},
					Ports: []networking.NetworkPolicyPort{
						{
							Protocol: &protocolTCP,
							Port:     &intstr.IntOrString{Type: intstr.String, StrVal: "dst-port"},
						},
					},
				},
			},
			Egress: []networking.NetworkPolicyEgressRule{
				{
					To: []networking.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "src",
								},
							},
						},
					},
					Ports: []networking.NetworkPolicyPort{
						{
							Protocol: &protocolTCP,
							Port:     &intstr.IntOrString{Type: intstr.String, StrVal: "src-port"},
							EndPort:  &port9090,
						},
					},
				},
			},
		},
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

	srcNS := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "src",
		},
	}

	// Resolve runs ingress, egress, and podSelector in parallel, so dispatch by
	// list type + namespace + selector instead of by call order. Unknown
	// combinations fail the test to catch regressions where the resolver queries
	// an unexpected namespace or selector.
	mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			listOpts := &client.ListOptions{}
			for _, opt := range opts {
				opt.ApplyToList(listOpts)
			}
			sel := ""
			if listOpts.LabelSelector != nil {
				sel = listOpts.LabelSelector.String()
			}
			switch typedList := list.(type) {
			case *corev1.NamespaceList:
				// Ingress and egress peers both select namespace "src" by name label.
				if sel != "kubernetes.io/metadata.name=src" {
					t.Errorf("unexpected namespace list selector: %q", sel)
					return fmt.Errorf("unexpected namespace list selector: %q", sel)
				}
				typedList.Items = []corev1.Namespace{srcNS}
			case *corev1.ServiceList:
				// Egress service ClusterIP lookup in the resolved peer namespace ("src").
				if listOpts.Namespace != "src" {
					t.Errorf("unexpected service list namespace: %q", listOpts.Namespace)
					return fmt.Errorf("unexpected service list namespace: %q", listOpts.Namespace)
				}
			case *corev1.PodList:
				// Policy has empty podSelector on both sides, so all pod lists use the empty selector.
				if sel != "" {
					t.Errorf("unexpected pod list selector: %q", sel)
					return fmt.Errorf("unexpected pod list selector: %q", sel)
				}
				switch listOpts.Namespace {
				case "dst":
					// Ingress named-port resolution + podSelector endpoints (policy namespace).
					typedList.Items = []corev1.Pod{dstPodOne, dstPodTwo}
				case "src":
					// Ingress peer pods + egress peer pods.
					typedList.Items = []corev1.Pod{srcPod}
				default:
					t.Errorf("unexpected pod list namespace: %q", listOpts.Namespace)
					return fmt.Errorf("unexpected pod list namespace: %q", listOpts.Namespace)
				}
			default:
				t.Errorf("unexpected list type: %T", list)
				return fmt.Errorf("unexpected list type: %T", list)
			}
			return nil
		})

	ingressEndpoints, egressEndpoints, podEndpoints, err := resolver.Resolve(context.TODO(), policy)
	assert.NoError(t, err)

	// the policy is applied to dst namespace
	// the ingress should have cidr from src pod and ports from dst pod
	require.Len(t, ingressEndpoints, 1)
	assert.Equal(t, srcPod.Status.PodIP, string(ingressEndpoints[0].CIDR))
	assert.Len(t, ingressEndpoints[0].Ports, 1)
	assert.Equal(t, dstPodOne.Spec.Containers[0].Ports[0].ContainerPort, *ingressEndpoints[0].Ports[0].Port)

	// the egress should have cidr from src pod and ports from src pod
	require.Len(t, egressEndpoints, 1)
	assert.Equal(t, srcPod.Status.PodIP, string(egressEndpoints[0].CIDR))
	assert.Len(t, egressEndpoints[0].Ports, 1)
	assert.Equal(t, port80, *egressEndpoints[0].Ports[0].Port)
	assert.Equal(t, port9090, *egressEndpoints[0].Ports[0].EndPort)

	// PodSelector: dst pods
	sort.Slice(podEndpoints, func(i, j int) bool {
		return podEndpoints[i].Name < podEndpoints[j].Name
	})
	require.Len(t, podEndpoints, 2)
	assert.Equal(t, "pod2", podEndpoints[0].Name)
	assert.Equal(t, "pod3", podEndpoints[1].Name)
}

func TestEndpointsResolver_ResolveNetworkPeers_NamedIngressPortsIPBlocks(t *testing.T) {
	protocolTCP := corev1.ProtocolTCP
	port8080 := int32(8080)
	port9090 := int32(9090)

	dstPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "dst",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "pod1",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: port8080,
							Protocol:      corev1.ProtocolTCP,
							Name:          "src-port",
						},
						{
							ContainerPort: port9090,
							Protocol:      corev1.ProtocolTCP,
							Name:          "src-port2",
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			PodIP: "1.0.0.1",
		},
	}

	portsMap := map[string]int32{
		"src-port":  port8080,
		"src-port2": port9090,
	}

	t.Run("IPBlock with named ports", func(t *testing.T) {
		policy := &networking.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netpol",
				Namespace: "dst",
			},
			Spec: networking.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{},
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress},
				Ingress: []networking.NetworkPolicyIngressRule{
					{
						From: []networking.NetworkPolicyPeer{
							{
								IPBlock: &networking.IPBlock{
									CIDR: "100.64.0.0/16",
								},
							},
						},
						Ports: []networking.NetworkPolicyPort{
							{
								Protocol: &protocolTCP,
								Port:     &intstr.IntOrString{Type: intstr.String, StrVal: "src-port"},
							},
							{
								Protocol: &protocolTCP,
								Port:     &intstr.IntOrString{Type: intstr.String, StrVal: "src-port2"},
							},
						},
					},
				},
			},
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mock_client.NewMockClient(ctrl)
		resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

		// V2 resolves all named ports in one getIngressRulesPorts call
		mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any()).
			DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				list.(*corev1.PodList).Items = []corev1.Pod{dstPod}
				return nil
			})

		ingressEndpoints, err := resolver.computeIngressEndpoints(context.TODO(), policy)
		assert.NoError(t, err)

		// Should allow ingress from 100.64.0.0/16 on ports 8080 and 9090
		require.Len(t, ingressEndpoints, 1)
		ingPE := ingressEndpoints[0]
		assert.Equal(t, "100.64.0.0/16", string(ingPE.CIDR))
		assert.Len(t, ingPE.Ports, 2)
		gotPorts := []int32{*ingPE.Ports[0].Port, *ingPE.Ports[1].Port}
		wantPorts := []int32{
			portsMap[policy.Spec.Ingress[0].Ports[0].Port.StrVal],
			portsMap[policy.Spec.Ingress[0].Ports[1].Port.StrVal],
		}
		sort.Slice(gotPorts, func(i, j int) bool { return gotPorts[i] < gotPorts[j] })
		sort.Slice(wantPorts, func(i, j int) bool { return wantPorts[i] < wantPorts[j] })
		assert.Equal(t, wantPorts, gotPorts)
	})

	t.Run("allow-all with named ports", func(t *testing.T) {
		dstPod2 := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod2",
				Namespace: "dst2",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "pod2",
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: port8080,
								Protocol:      corev1.ProtocolTCP,
								Name:          "src-port",
							},
							{
								ContainerPort: port9090,
								Protocol:      corev1.ProtocolTCP,
								Name:          "src-port2",
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{
				PodIP: "1.0.0.2",
			},
		}

		policyAll := &networking.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netpolAll",
				Namespace: "dst2",
			},
			Spec: networking.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{},
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress},
				Ingress: []networking.NetworkPolicyIngressRule{
					{
						Ports: []networking.NetworkPolicyPort{
							{
								Protocol: &protocolTCP,
								Port:     &intstr.IntOrString{Type: intstr.String, StrVal: "src-port"},
							},
							{
								Protocol: &protocolTCP,
								Port:     &intstr.IntOrString{Type: intstr.String, StrVal: "src-port2"},
							},
						},
					},
				},
			},
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mock_client.NewMockClient(ctrl)
		resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

		// V2 resolves all named ports in one getIngressRulesPorts call
		mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any()).
			DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				list.(*corev1.PodList).Items = []corev1.Pod{dstPod2}
				return nil
			})

		ingressEndpoints, err := resolver.computeIngressEndpoints(context.TODO(), policyAll)
		assert.NoError(t, err)

		// Should allow ingress from all addresses on ports 8080 and 9090
		require.Len(t, ingressEndpoints, 2)
		for _, ingPE := range ingressEndpoints {
			assert.True(t, "0.0.0.0/0" == string(ingPE.CIDR) || "::/0" == string(ingPE.CIDR))
			assert.Len(t, ingPE.Ports, 2)
			gotPorts := []int32{*ingPE.Ports[0].Port, *ingPE.Ports[1].Port}
			wantPorts := []int32{
				portsMap[policyAll.Spec.Ingress[0].Ports[0].Port.StrVal],
				portsMap[policyAll.Spec.Ingress[0].Ports[1].Port.StrVal],
			}
			sort.Slice(gotPorts, func(i, j int) bool { return gotPorts[i] < gotPorts[j] })
			sort.Slice(wantPorts, func(i, j int) bool { return wantPorts[i] < wantPorts[j] })
			assert.Equal(t, wantPorts, gotPorts)
		}
	})
}

func TestEndpointsResolver_ExcludesTerminalPods(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

	// Create pods in different phases
	runningPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "running-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			PodIP: "10.0.0.1",
			Phase: corev1.PodRunning,
		},
	}

	succeededPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "succeeded-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			PodIP: "10.0.0.2",
			Phase: corev1.PodSucceeded,
		},
	}

	failedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "failed-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			PodIP: "10.0.0.3",
			Phase: corev1.PodFailed,
		},
	}

	podList := &corev1.PodList{
		Items: []corev1.Pod{*runningPod, *succeededPod, *failedPod},
	}

	// Mock the List call for pod selector endpoints
	mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			list.(*corev1.PodList).Items = podList.Items
			return nil
		})

	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-policy",
			Namespace: "test-ns",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
		},
	}

	_, _, podEndpoints, err := resolver.Resolve(context.Background(), policy)

	assert.NoError(t, err)
	assert.Len(t, podEndpoints, 1, "Should only include running pod in PolicyEndpoints")
	assert.Equal(t, "10.0.0.1", string(podEndpoints[0].PodIP))
	assert.Equal(t, "running-pod", podEndpoints[0].Name)
}

func TestEndpointsResolver_ExcludesHostNetworkPods_Integration(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

	// Create a regular pod (should be included)
	regularPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "regular-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: false,
		},
		Status: corev1.PodStatus{
			PodIP:  "10.0.0.1",
			HostIP: "192.168.1.1",
			Phase:  corev1.PodRunning,
		},
	}

	// Create a hostNetwork pod (should be excluded)
	hostNetworkPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostnetwork-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: true,
		},
		Status: corev1.PodStatus{
			PodIP:  "192.168.1.1",
			HostIP: "192.168.1.1",
			Phase:  corev1.PodRunning,
		},
	}

	// Create another regular pod (should be included)
	regularPod2 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "regular-pod-2",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: false,
		},
		Status: corev1.PodStatus{
			PodIP:  "10.0.0.2",
			HostIP: "192.168.1.2",
			Phase:  corev1.PodRunning,
		},
	}

	mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			podList := list.(*corev1.PodList)
			podList.Items = []corev1.Pod{regularPod, hostNetworkPod, regularPod2}
			return nil
		})

	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-policy",
			Namespace: "test-ns",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			PolicyTypes: []networking.PolicyType{
				networking.PolicyTypeIngress,
				networking.PolicyTypeEgress,
			},
		},
	}

	_, _, podEndpoints, err := resolver.Resolve(context.Background(), policy)

	require.NoError(t, err)
	assert.Len(t, podEndpoints, 2, "Should only include non-hostNetwork pods")

	podNames := make(map[string]bool)
	for _, pe := range podEndpoints {
		podNames[pe.Name] = true
	}

	assert.True(t, podNames["regular-pod"], "regular-pod should be included")
	assert.True(t, podNames["regular-pod-2"], "regular-pod-2 should be included")
	assert.False(t, podNames["hostnetwork-pod"], "hostnetwork-pod should be excluded")

	podIPs := make(map[string]bool)
	for _, pe := range podEndpoints {
		podIPs[string(pe.PodIP)] = true
	}

	assert.True(t, podIPs["10.0.0.1"], "regular-pod IP should be included")
	assert.True(t, podIPs["10.0.0.2"], "regular-pod-2 IP should be included")
	assert.False(t, podIPs["192.168.1.1"], "hostNetwork pod IP should not be included")
}

func TestEndpointsResolver_IncludesHostNetworkPodsInIngressEgressRules(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

	targetPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "target-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"role": "backend"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: false,
		},
		Status: corev1.PodStatus{
			PodIP:  "10.0.0.1",
			HostIP: "192.168.1.1",
			Phase:  corev1.PodRunning,
		},
	}

	hostNetworkMonitoringPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostnetwork-monitoring-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"role": "monitoring"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: true,
		},
		Status: corev1.PodStatus{
			PodIP:  "192.168.1.1",
			HostIP: "192.168.1.1",
			Phase:  corev1.PodRunning,
		},
	}

	regularMonitoringPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "regular-monitoring-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"role": "monitoring"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: false,
		},
		Status: corev1.PodStatus{
			PodIP:  "10.0.0.2",
			HostIP: "192.168.1.2",
			Phase:  corev1.PodRunning,
		},
	}

	hostNetworkDatabasePod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostnetwork-database-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"role": "database"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: true,
		},
		Status: corev1.PodStatus{
			PodIP:  "192.168.1.3",
			HostIP: "192.168.1.3",
			Phase:  corev1.PodRunning,
		},
	}

	regularDatabasePod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "regular-database-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"role": "database"},
		},
		Spec: corev1.PodSpec{
			HostNetwork: false,
		},
		Status: corev1.PodStatus{
			PodIP:  "10.0.0.3",
			HostIP: "192.168.1.4",
			Phase:  corev1.PodRunning,
		},
	}

	// Mock List calls - with parallel Resolve, call order is non-deterministic,
	// so dispatch by list type and label selector instead of using gomock.InOrder.
	// Unknown selectors fail the test to catch regressions.
	mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			listOpts := &client.ListOptions{}
			for _, opt := range opts {
				opt.ApplyToList(listOpts)
			}
			switch typedList := list.(type) {
			case *corev1.ServiceList:
				return nil
			case *corev1.PodList:
				sel := ""
				if listOpts.LabelSelector != nil {
					sel = listOpts.LabelSelector.String()
				}
				switch sel {
				case "role=monitoring":
					typedList.Items = []corev1.Pod{hostNetworkMonitoringPod, regularMonitoringPod}
				case "role=database":
					typedList.Items = []corev1.Pod{hostNetworkDatabasePod, regularDatabasePod}
				case "role=backend":
					// Used by both getIngressRulesPorts and computePodSelectorEndpoints.
					typedList.Items = []corev1.Pod{targetPod}
				default:
					t.Errorf("unexpected pod list selector: %q", sel)
					return fmt.Errorf("unexpected pod list selector: %q", sel)
				}
			default:
				t.Errorf("unexpected list type: %T", list)
				return fmt.Errorf("unexpected list type: %T", list)
			}
			return nil
		})

	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend-policy",
			Namespace: "test-ns",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"role": "backend"},
			},
			PolicyTypes: []networking.PolicyType{
				networking.PolicyTypeIngress,
				networking.PolicyTypeEgress,
			},
			Ingress: []networking.NetworkPolicyIngressRule{
				{
					From: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"role": "monitoring"},
							},
						},
					},
				},
			},
			Egress: []networking.NetworkPolicyEgressRule{
				{
					To: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"role": "database"},
							},
						},
					},
				},
			},
		},
	}

	ingressEndpoints, egressEndpoints, podEndpoints, err := resolver.Resolve(context.Background(), policy)

	require.NoError(t, err)

	// 1. PodSelectorEndpoints should only include the target pod (not hostNetwork)
	assert.Len(t, podEndpoints, 1, "Should only include non-hostNetwork pods in podSelectorEndpoints")
	assert.Equal(t, "target-pod", podEndpoints[0].Name)
	assert.Equal(t, "10.0.0.1", string(podEndpoints[0].PodIP))

	// 2. IngressEndpoints should include BOTH hostNetwork and regular monitoring pods
	assert.Len(t, ingressEndpoints, 2, "Should include both hostNetwork and regular pods in ingress rules")

	ingressCIDRs := make(map[string]bool)
	for _, ep := range ingressEndpoints {
		ingressCIDRs[string(ep.CIDR)] = true
	}

	assert.True(t, ingressCIDRs["192.168.1.1"], "hostNetwork monitoring pod IP should be included in ingress rules")
	assert.True(t, ingressCIDRs["10.0.0.2"], "regular monitoring pod IP should be included in ingress rules")

	// 3. EgressEndpoints should include BOTH hostNetwork and regular database pods
	assert.Len(t, egressEndpoints, 2, "Should include both hostNetwork and regular pods in egress rules")

	egressCIDRs := make(map[string]bool)
	for _, ep := range egressEndpoints {
		egressCIDRs[string(ep.CIDR)] = true
	}

	assert.True(t, egressCIDRs["192.168.1.3"], "hostNetwork database pod IP should be included in egress rules")
	assert.True(t, egressCIDRs["10.0.0.3"], "regular database pod IP should be included in egress rules")
}

func TestGetMatchingServiceClusterIPs_NamedPortBypass(t *testing.T) {
	protocolTCP := corev1.ProtocolTCP
	intOrStrPort80 := intstr.FromInt(80)
	intOrStrPort8080 := intstr.FromInt(8080)
	intOrStrPort6379 := intstr.FromInt(6379)
	namedPortHTTP := intstr.FromString("http")

	nginxPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-pod", Namespace: "test-ns", Labels: map[string]string{"shared-backend": "true"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80, Protocol: corev1.ProtocolTCP}}},
			},
		},
	}
	pythonPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "python-pod", Namespace: "test-ns", Labels: map[string]string{"shared-backend": "true"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}},
			},
		},
	}
	multiPortPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-port-pod", Namespace: "test-ns", Labels: map[string]string{"app": "backend"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Ports: []corev1.ContainerPort{
					{Name: "app-port", ContainerPort: 6379, Protocol: corev1.ProtocolTCP},
					{Name: "metrics-port", ContainerPort: 9101, Protocol: corev1.ProtocolTCP},
				}},
			},
		},
	}

	tests := []struct {
		name                     string
		npPodSelector            *metav1.LabelSelector
		npPorts                  []networking.NetworkPolicyPort
		services                 []corev1.Service
		pods                     []corev1.Pod
		expectServiceClusterIPs  []string
		excludeServiceClusterIPs []string
	}{
		{
			name:          "bypass: named targetPort with inconsistent container ports",
			npPodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shared-backend": "true"}},
			npPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test-ns"},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.100.76.99",
						Selector:  map[string]string{"shared-backend": "true"},
						Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}},
					},
				},
			},
			pods:                     []corev1.Pod{nginxPod, pythonPod},
			excludeServiceClusterIPs: []string{"10.100.76.99"},
		},
		{
			name:          "safe: named NP port skips bypass check",
			npPodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shared-backend": "true"}},
			npPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &namedPortHTTP},
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test-ns"},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.100.76.99",
						Selector:  map[string]string{"shared-backend": "true"},
						Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}},
					},
				},
			},
			pods:                    []corev1.Pod{nginxPod, pythonPod},
			expectServiceClusterIPs: []string{"10.100.76.99"},
		},
		{
			name:          "safe: numeric targetPort in service",
			npPodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shared-backend": "true"}},
			npPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test-ns"},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.100.76.100",
						Selector:  map[string]string{"shared-backend": "true"},
						Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(80), Protocol: corev1.ProtocolTCP}},
					},
				},
			},
			pods:                    []corev1.Pod{nginxPod, pythonPod},
			expectServiceClusterIPs: []string{"10.100.76.100"},
		},
		{
			name:          "safe: policy allows all remapped container ports",
			npPodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shared-backend": "true"}},
			npPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
				{Protocol: &protocolTCP, Port: &intOrStrPort8080},
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test-ns"},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.100.76.99",
						Selector:  map[string]string{"shared-backend": "true"},
						Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}},
					},
				},
			},
			pods:                    []corev1.Pod{nginxPod, pythonPod},
			expectServiceClusterIPs: []string{"10.100.76.99"},
		},
		{
			name:          "bypass: nil NP Protocol and omitted ServicePort Protocol default to TCP",
			npPodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shared-backend": "true"}},
			npPorts: []networking.NetworkPolicyPort{
				// Protocol nil — defaults to TCP
				{Port: &intOrStrPort80},
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test-ns"},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.100.76.102",
						Selector:  map[string]string{"shared-backend": "true"},
						// Protocol omitted (zero value "") — defaults to TCP
						Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("http")}},
					},
				},
			},
			pods:                     []corev1.Pod{nginxPod, pythonPod},
			excludeServiceClusterIPs: []string{"10.100.76.102"},
		},
		{
			name:          "safe: multi-port service only checks matched port (bug fix for false positive)",
			npPodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "backend"}},
			npPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort6379},
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test-ns"},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.100.76.101",
						Selector:  map[string]string{"app": "backend"},
						Ports: []corev1.ServicePort{
							{Port: 6379, TargetPort: intstr.FromString("app-port"), Protocol: corev1.ProtocolTCP},
							{Port: 9101, TargetPort: intstr.FromString("metrics-port"), Protocol: corev1.ProtocolTCP},
						},
					},
				},
			},
			pods:                    []corev1.Pod{multiPortPod},
			expectServiceClusterIPs: []string{"10.100.76.101"},
		},
		{
			name:          "safe: mixed numeric+named NP ports — named port keeps ClusterIP even though numeric is skipped",
			npPodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shared-backend": "true"}},
			npPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80}, // numeric: bypass detected, skipped
				{Protocol: &protocolTCP, Port: &namedPortHTTP},  // named: bypass check not applicable, included
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test-ns"},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.100.76.103",
						Selector:  map[string]string{"shared-backend": "true"},
						Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}},
					},
				},
			},
			pods: []corev1.Pod{nginxPod, pythonPod},
			// ClusterIP is still included because the named NP port "http" adds it.
			// The numeric port 80 is correctly skipped (bypass risk), but the named
			// port entry keeps the ClusterIP in the result — user explicitly opted into
			// named port resolution which includes 8080.
			expectServiceClusterIPs: []string{"10.100.76.103"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := mock_client.NewMockClient(ctrl)
			resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

			mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.ServiceList{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					list.(*corev1.ServiceList).Items = tt.services
					return nil
				})
			mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					list.(*corev1.PodList).Items = tt.pods
					return nil
				}).AnyTimes()

			result := resolver.getMatchingServiceClusterIPs(context.Background(), tt.npPodSelector, "test-ns", tt.npPorts)

			foundIPs := make(map[string]bool)
			for _, ep := range result {
				foundIPs[string(ep.CIDR)] = true
			}
			for _, expectedIP := range tt.expectServiceClusterIPs {
				assert.True(t, foundIPs[expectedIP], "Expected ClusterIP %s to be present", expectedIP)
			}
			for _, excludedIP := range tt.excludeServiceClusterIPs {
				assert.False(t, foundIPs[excludedIP], "ClusterIP %s should NOT be present (bypass risk)", excludedIP)
			}
		})
	}
}

func TestEndpointsResolver_hasTargetPortBypass(t *testing.T) {
	protocolTCP := corev1.ProtocolTCP
	protocolUDP := corev1.ProtocolUDP
	intOrStrPort80 := intstr.FromInt(80)
	intOrStrPort8080 := intstr.FromInt(8080)
	intOrStrPort6379 := intstr.FromInt(6379)

	tests := []struct {
		name              string
		service           *corev1.Service
		matchedListenPort int32
		protocol          corev1.Protocol
		policyPorts       []networking.NetworkPolicyPort
		pods              []corev1.Pod
		expected          bool
	}{
		{
			name: "problematic: named targetPort resolves to disallowed port on some pods",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "safe: numeric targetPort in service",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromInt(80), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "problematic: all pods resolve named port to disallowed port",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "safe: all pods resolve named port to allowed port",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "safe: policy allows all remapped container ports",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
				{Protocol: &protocolTCP, Port: &intOrStrPort8080},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "safe: multi-port service only checks matched port (not unrelated service ports)",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 6379, TargetPort: intstr.FromString("app-port"), Protocol: corev1.ProtocolTCP},
						{Port: 9101, TargetPort: intstr.FromString("metrics-port"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 6379,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort6379},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{
								{Name: "app-port", ContainerPort: 6379, Protocol: corev1.ProtocolTCP},
								{Name: "metrics-port", ContainerPort: 9101, Protocol: corev1.ProtocolTCP},
							}},
						},
					},
				},
			},
			expected: false, // Only checks "app-port"->6379 (allowed), ignores "metrics-port"->9101
		},
		{
			name: "problematic: protocol mismatch - policy allows TCP but container port is UDP",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("dns"), Protocol: corev1.ProtocolUDP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolUDP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "dns", ContainerPort: 80, Protocol: corev1.ProtocolUDP}}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "safe: protocol matches - policy allows UDP and container port is UDP",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("dns"), Protocol: corev1.ProtocolUDP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolUDP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolUDP, Port: &intOrStrPort80},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "dns", ContainerPort: 80, Protocol: corev1.ProtocolUDP}}},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "safe: protocol-only policy entry (allow all TCP) covers remapped port",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP},                        // allow ALL TCP ports
				{Protocol: &protocolTCP, Port: &intOrStrPort80}, // also explicitly allow 80
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
			},
			expected: false, // {Protocol: TCP} allows all TCP ports, so 8080 is allowed
		},
		{
			name: "safe: container port falls within EndPort range",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80, EndPort: int32Ptr(9000)}, // allow 80-9000
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
			},
			expected: false, // 8080 is within range 80-9000
		},
		{
			name: "problematic: nil NP Protocol and omitted ServicePort Protocol still detects bypass",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						// Protocol omitted (zero value "") — should default to TCP
						{Port: 80, TargetPort: intstr.FromString("http")},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				// Protocol nil — should default to TCP
				{Port: &intOrStrPort80},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							// ContainerPort.Protocol omitted (zero value "") — defaults to TCP
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
			},
			expected: true, // named port resolves to 8080 which is not allowed by port 80
		},
		{
			name: "problematic: container port outside EndPort range",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80, EndPort: int32Ptr(100)}, // allow 80-100
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}},
						},
					},
				},
			},
			expected: true, // 8080 is outside range 80-100
		},
		{
			name: "safe: multi-container pod — only first matching container port is checked",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.1",
					Selector:  map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP},
					},
				},
			},
			matchedListenPort: 80,
			protocol:          corev1.ProtocolTCP,
			policyPorts: []networking.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &intOrStrPort80},
				{Protocol: &protocolTCP, Port: &intOrStrPort8080},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							// First container: http=8080 (this is what the Service resolves to)
							{Name: "app", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}},
							// Second container: http=8090 (ignored by Service endpoint resolution)
							{Name: "sidecar", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8090, Protocol: corev1.ProtocolTCP}}},
						},
					},
				},
			},
			expected: false, // first container resolves http→8080 which is allowed; sidecar's 8090 is irrelevant
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := mock_client.NewMockClient(ctrl)
			resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

			result := resolver.hasTargetPortBypass(tt.service, tt.matchedListenPort, tt.protocol, tt.policyPorts, func() ([]corev1.Pod, error) {
				return tt.pods, nil
			})
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetMatchingServiceClusterIPs_LazyPodFetch(t *testing.T) {
	protocolTCP := corev1.ProtocolTCP
	port80 := intstr.FromInt(80)

	npPorts := []networking.NetworkPolicyPort{
		{Protocol: &protocolTCP, Port: &port80},
	}
	ls := &metav1.LabelSelector{}

	t.Run("no matching services skips pod list", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mock_client.NewMockClient(ctrl)
		resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

		// Only expect service list, no pod list call
		mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.ServiceList{}), gomock.Any()).
			DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				list.(*corev1.ServiceList).Items = []corev1.Service{
					{Spec: corev1.ServiceSpec{ClusterIP: "None"}}, // headless, will be skipped
				}
				return nil
			})

		result := resolver.getMatchingServiceClusterIPs(context.Background(), ls, "ns", npPorts)
		assert.Empty(t, result)
	})

	t.Run("matching service with direct port lookup skips pod list", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mock_client.NewMockClient(ctrl)
		resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

		// Service with numeric targetPort matching NP port — LookupServiceListenPort succeeds directly
		svc := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "svc1", Namespace: "ns"},
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.0.0.1",
				Selector:  map[string]string{"app": "web"},
				Ports: []corev1.ServicePort{
					{Port: 80, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt(80)},
				},
			},
		}

		mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.ServiceList{}), gomock.Any()).
			DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				list.(*corev1.ServiceList).Items = []corev1.Service{svc}
				return nil
			})
		// No pod list call expected

		result := resolver.getMatchingServiceClusterIPs(context.Background(), ls, "ns", npPorts)
		assert.Len(t, result, 1)
		assert.Equal(t, policyinfo.NetworkAddress("10.0.0.1"), result[0].CIDR)
	})

	t.Run("pod list fetched once when needed by both functions", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mock_client.NewMockClient(ctrl)
		resolver := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

		// Service with named targetPort + numeric NP port triggers both:
		// - hasTargetPortBypass needs pods (named targetPort + numeric policy port)
		// - getMatchingServicePort needs pods (direct lookup fails since targetPort is named)
		svc := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "svc1", Namespace: "ns"},
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.0.0.1",
				Selector:  map[string]string{"app": "web"},
				Ports: []corev1.ServicePort{
					{Port: 80, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromString("http")},
				},
			},
		}

		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns", Labels: map[string]string{"app": "web"}},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 80, Protocol: corev1.ProtocolTCP},
					}},
				},
			},
		}

		mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.ServiceList{}), gomock.Any()).
			DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				list.(*corev1.ServiceList).Items = []corev1.Service{svc}
				return nil
			})
		mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any()).
			DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				list.(*corev1.PodList).Items = []corev1.Pod{pod}
				return nil
			}).Times(1) // exactly once despite two consumers

		result := resolver.getMatchingServiceClusterIPs(context.Background(), ls, "ns", npPorts)
		assert.Len(t, result, 1)
		assert.Equal(t, policyinfo.NetworkAddress("10.0.0.1"), result[0].CIDR)
	})
}

func int32Ptr(i int32) *int32 {
	return &i
}
