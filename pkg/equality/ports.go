package equality

import (
	"cmp"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// ComparePorts orders ports by protocol, then port, then end port, with an absent value before any
// present one. Every field takes part, so sorting by it gives one order for any set of ports.
func ComparePorts(a, b policyinfo.Port) int {
	if c := compareOptional(a.Protocol, b.Protocol); c != 0 {
		return c
	}
	if c := compareOptional(a.Port, b.Port); c != 0 {
		return c
	}
	return compareOptional(a.EndPort, b.EndPort)
}

func compareOptional[T cmp.Ordered](a, b *T) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return cmp.Compare(*a, *b)
}
