package resolvers

import (
	"context"
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

func TestEndpointsResolver_getAllowAllNetworkPeers(t *testing.T) {
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
			got := resolver.getAllowAllNetworkPeers(tt.args.ports)
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
	ingressPolicyOnHostNetwork := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host-network-policy",
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
	podHostNetwork := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-host-network",
			Namespace: "ns",
			Annotations: map[string]string{
				"vpc.amazonaws.com/pod-ips": "1.0.0.4",
			},
		},
		Spec: corev1.PodSpec{
			HostNetwork: true,
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
			name: "host network pods should be ignored",
			args: args{
				netpol: ingressPolicyOnHostNetwork,
				podListCalls: []podListCall{
					{
						pods: []corev1.Pod{pod1, podHostNetwork},
					},
				},
			},
			wantIngressEndpoints: []policyinfo.EndpointInfo{
				{CIDR: "1.0.0.1"},
			},
			wantPodEndpoints: []policyinfo.PodEndpoint{
				{PodIP: "1.0.0.1", Name: "pod1", Namespace: "ns"},
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

	portsMap := map[string]int32{
		"src-port": port80,
		"dst-port": port8080,
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

	var ingressEndpoints []policyinfo.EndpointInfo
	var egressEndpoints []policyinfo.EndpointInfo
	ctx := context.TODO()
	for _, rule := range policy.Spec.Ingress {
		namespaces := []corev1.Namespace{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "src",
				},
			},
		}

		nsList := &corev1.NamespaceList{}
		podList := &corev1.PodList{}

		gomock.InOrder(
			mockClient.EXPECT().List(gomock.Any(), nsList, gomock.Any()).DoAndReturn(
				func(ctx context.Context, nsList *corev1.NamespaceList, opts ...client.ListOption) error {
					for _, ns := range namespaces {
						nsList.Items = append(nsList.Items, *(ns.DeepCopy()))
					}
					return nil
				},
			),
			// getting ingress endpoint calls listing pods with dst NS first
			mockClient.EXPECT().List(gomock.Any(), podList, gomock.Any()).DoAndReturn(
				func(ctx context.Context, podList *corev1.PodList, opts ...client.ListOption) error {
					podList.Items = []corev1.Pod{dstPodOne, dstPodTwo}
					podList.Items = []corev1.Pod{dstPodOne, dstPodTwo}
					return nil
				},
			),
			// getting ingress endpoint calls then listing pods with src NS for CIDRs
			mockClient.EXPECT().List(gomock.Any(), podList, gomock.Any()).DoAndReturn(
				func(ctx context.Context, podList *corev1.PodList, opts ...client.ListOption) error {
					podList.Items = []corev1.Pod{srcPod}
					return nil
				},
			),
		)
		if rule.From == nil {
			ingressEndpoints = append(ingressEndpoints, resolver.getAllowAllNetworkPeers(rule.Ports)...)
			continue
		}
		resolvedPeers, err := resolver.resolveNetworkPeers(ctx, policy, rule.From, rule.Ports, networking.PolicyTypeIngress)
		assert.NoError(t, err)
		ingressEndpoints = append(ingressEndpoints, resolvedPeers...)

		dstNS := corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "src",
			},
		}

		svcList := &corev1.ServiceList{}
		gomock.InOrder(
			mockClient.EXPECT().List(gomock.Any(), nsList, gomock.Any()).DoAndReturn(
				func(ctx context.Context, nsList *corev1.NamespaceList, opts ...client.ListOption) error {
					nsList.Items = []corev1.Namespace{dstNS}
					return nil
				},
			),
			mockClient.EXPECT().List(gomock.Any(), podList, gomock.Any()).DoAndReturn(
				func(ctx context.Context, podList *corev1.PodList, opts ...client.ListOption) error {
					podList.Items = []corev1.Pod{dstPodOne, dstPodTwo}
					podList.Items = []corev1.Pod{dstPodOne, dstPodTwo}
					return nil
				},
			),
			mockClient.EXPECT().List(gomock.Any(), nsList, gomock.Any()).DoAndReturn(
				func(ctx context.Context, nsList *corev1.NamespaceList, opts ...client.ListOption) error {
					nsList.Items = []corev1.Namespace{dstNS}
					return nil
				},
			),
			mockClient.EXPECT().List(gomock.Any(), svcList, gomock.Any()).DoAndReturn(
				func(ctx context.Context, svcList *corev1.ServiceList, opts ...client.ListOption) error {
					svcList.Items = []corev1.Service{}
					return nil
				},
			),
		)

		for _, rule := range policy.Spec.Egress {
			if rule.To == nil {
				egressEndpoints = append(egressEndpoints, resolver.getAllowAllNetworkPeers(rule.Ports)...)
				continue
			}
			resolvedPeers, err := resolver.resolveNetworkPeers(ctx, policy, rule.To, rule.Ports, networking.PolicyTypeEgress)
			assert.NoError(t, err)
			resolvedClusterIPs, err := resolver.resolveServiceClusterIPs(ctx, rule.To, policy.Namespace, rule.Ports)
			assert.NoError(t, err)
			egressEndpoints = append(egressEndpoints, resolvedPeers...)
			egressEndpoints = append(egressEndpoints, resolvedClusterIPs...)
		}
	}

	// the policy is applied to dst namespace
	// the ingress should have cidr from src pod and ports from dst pod
	// the egress should have cidr from src pod and ports from src pod
	for _, ingPE := range ingressEndpoints {
		assert.Equal(t, srcPod.Status.PodIP, string(ingPE.CIDR))
		assert.Equal(t, dstPodOne.Spec.Containers[0].Ports[0].ContainerPort, *ingPE.Ports[0].Port)
		assert.Equal(t, 1, len(ingPE.Ports))
		assert.Equal(t, dstPodOne.Spec.Containers[0].Ports[0].ContainerPort, *ingPE.Ports[0].Port)
		assert.Equal(t, 1, len(ingPE.Ports))
	}

	for _, egPE := range egressEndpoints {
		assert.True(t, string(egPE.CIDR) == dstPodOne.Status.PodIP || string(egPE.CIDR) == dstPodTwo.Status.PodIP)
		assert.Equal(t, dstPodOne.Spec.Containers[0].Ports[0].ContainerPort, *egPE.Ports[0].Port)
		assert.Equal(t, srcPod.Status.PodIP, string(egPE.CIDR))
		assert.Equal(t, srcPod.Spec.Containers[0].Ports[0].ContainerPort, *egPE.Ports[0].Port)
		assert.Equal(t, portsMap[policy.Spec.Egress[0].Ports[0].Port.StrVal], *egPE.Ports[0].Port)
		assert.Equal(t, *policy.Spec.Egress[0].Ports[0].EndPort, *egPE.Ports[0].EndPort)
	}
}
