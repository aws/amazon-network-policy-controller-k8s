package resolvers

import (
	"testing"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Test_EndpointsResolver_getAllowAllNetworkPeers(t *testing.T) {
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
