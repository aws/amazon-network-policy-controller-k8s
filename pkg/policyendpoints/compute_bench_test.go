package policyendpoints

import (
	"testing"

	"github.com/go-logr/logr"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

// BenchmarkComputePolicyEndpoints_NoOp measures the reconcile path the controller runs most: a
// policy of 25 objects (5,000 peers and 5,000 pods at the default chunk size) whose resolved
// endpoints have not changed. Port lists come single, sorted, or in non-canonical order.
func BenchmarkComputePolicyEndpoints_NoOp(b *testing.B) {
	for _, bc := range []struct {
		name  string
		ports []policyinfo.Port
	}{
		{"one port", []policyinfo.Port{tcpPort(80)}},
		{"two ports sorted", []policyinfo.Port{tcpPort(80), tcpPort(443)}},
		{"two ports unsorted", []policyinfo.Port{tcpPort(443), tcpPort(80)}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			m := &policyEndpointsManager{endpointChunkSize: 200, logger: logr.Discard()}
			groups := []resolvers.IngressGroup{{Ingress: peers(5000, bc.ports), PodEndpoints: podEndpoints(seq(5000))}}
			created, _, _, err := m.computePolicyEndpoints(writeTestNP, nil, groups, nil, nil)
			if err != nil {
				b.Fatal(err)
			}
			for i := range created {
				created[i].Name = "pe-" + string(rune('a'+i))
			}
			existing := make([]policyinfo.PolicyEndpoint, len(created))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				for i := range created {
					existing[i] = *created[i].DeepCopy()
				}
				b.StartTimer()
				if _, _, _, err := m.computePolicyEndpoints(writeTestNP, existing, groups, nil, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
