package policyendpoints

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// Regression tests for V2339293193: a separator-free rule-identity hash let
// the controller treat two semantically different rules as one. Narrowing a
// NetworkPolicy from the port range 1-23 to the single port 123 produced the
// same digest, so the controller kept the revoked range and dropped the newly
// declared port.

func i32(v int32) *int32 { return &v }

func Test_getEndpointInfoKey_portRangeVsSinglePort_noCollision(t *testing.T) {
	m := &policyEndpointsManager{}
	tcp := corev1.ProtocolTCP

	// {TCP, port 1, endPort 23} -- the port RANGE 1-23
	rangeRule := policyinfo.EndpointInfo{
		CIDR:  "10.0.0.0/24",
		Ports: []policyinfo.Port{{Protocol: &tcp, Port: i32(1), EndPort: i32(23)}},
	}
	// {TCP, port 123} -- a single port whose digits concatenate to the range's
	singleRule := policyinfo.EndpointInfo{
		CIDR:  "10.0.0.0/24",
		Ports: []policyinfo.Port{{Protocol: &tcp, Port: i32(123)}},
	}

	assert.NotEqual(t, m.getEndpointInfoKey(rangeRule), m.getEndpointInfoKey(singleRule),
		"range 1-23 and single port 123 must not share a hash key")
}

func Test_getEndpointInfoKey_deterministicAndDistinct(t *testing.T) {
	m := &policyEndpointsManager{}
	tcp := corev1.ProtocolTCP

	base := policyinfo.EndpointInfo{
		CIDR:  "10.0.0.0/24",
		Ports: []policyinfo.Port{{Protocol: &tcp, Port: i32(80)}},
	}
	// same input -> same key
	assert.Equal(t, m.getEndpointInfoKey(base), m.getEndpointInfoKey(base))

	// a nil port must not collide with port 0 (the "nil" sentinel guards this)
	nilPort := policyinfo.EndpointInfo{CIDR: "10.0.0.0/24", Ports: []policyinfo.Port{{Protocol: &tcp}}}
	zeroPort := policyinfo.EndpointInfo{CIDR: "10.0.0.0/24", Ports: []policyinfo.Port{{Protocol: &tcp, Port: i32(0)}}}
	assert.NotEqual(t, m.getEndpointInfoKey(nilPort), m.getEndpointInfoKey(zeroPort),
		"absent port and port 0 must be distinguishable")

	// CIDR boundary that a separator-free hash would blur:
	// {cidr 10.0.0.0/2, except 4} vs {cidr 10.0.0.0/24}
	a := policyinfo.EndpointInfo{CIDR: "10.0.0.0/2", Except: []policyinfo.NetworkAddress{"4"}}
	b := policyinfo.EndpointInfo{CIDR: "10.0.0.0/24"}
	assert.NotEqual(t, m.getEndpointInfoKey(a), m.getEndpointInfoKey(b))
}

func Test_getClusterEndpointInfoKey_portRangeVsSinglePort_noCollision(t *testing.T) {
	m := &policyEndpointsManager{}
	tcp := corev1.ProtocolTCP

	rangeRule := policyinfo.ClusterEndpointInfo{
		CIDR:   "10.0.0.0/24",
		Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
		Ports:  []policyinfo.Port{{Protocol: &tcp, Port: i32(1), EndPort: i32(23)}},
	}
	singleRule := policyinfo.ClusterEndpointInfo{
		CIDR:   "10.0.0.0/24",
		Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
		Ports:  []policyinfo.Port{{Protocol: &tcp, Port: i32(123)}},
	}

	assert.NotEqual(t, m.getClusterEndpointInfoKey(rangeRule), m.getClusterEndpointInfoKey(singleRule),
		"CNP range 1-23 and single port 123 must not share a hash key")
}

// The previous CNP key used string(rune(port)), the Unicode code point rather
// than the decimal text. Ports 80 and 443 both had to encode distinctly; more
// importantly, values that map to the same rune arithmetic must not collide.
func Test_getClusterEndpointInfoKey_distinctPorts(t *testing.T) {
	m := &policyEndpointsManager{}
	tcp := corev1.ProtocolTCP

	p80 := policyinfo.ClusterEndpointInfo{
		CIDR:   "10.0.0.0/24",
		Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
		Ports:  []policyinfo.Port{{Protocol: &tcp, Port: i32(80)}},
	}
	p443 := policyinfo.ClusterEndpointInfo{
		CIDR:   "10.0.0.0/24",
		Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
		Ports:  []policyinfo.Port{{Protocol: &tcp, Port: i32(443)}},
	}
	assert.NotEqual(t, m.getClusterEndpointInfoKey(p80), m.getClusterEndpointInfoKey(p443))

	// Action must remain part of the identity: same CIDR/ports, different action.
	deny := p80
	deny.Action = policyinfo.ClusterNetworkPolicyRuleActionDeny
	assert.NotEqual(t, m.getClusterEndpointInfoKey(p80), m.getClusterEndpointInfoKey(deny),
		"differing Action must produce a different key")
}

// string(rune(n)) is not injective over the port range: every n in the UTF-16
// surrogate block U+D800-U+DFFF (55296-57343) is not a valid rune, so Go
// encodes all 2048 of them as the replacement character U+FFFD. Under the old
// encoding every port in that block hashed identically, so changing a policy
// from one such port to another was silently ignored.
func Test_getClusterEndpointInfoKey_surrogateBlockPorts_noCollision(t *testing.T) {
	m := &policyEndpointsManager{}
	tcp := corev1.ProtocolTCP

	cpe := func(port int32) policyinfo.ClusterEndpointInfo {
		return policyinfo.ClusterEndpointInfo{
			CIDR:   "10.0.0.0/24",
			Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
			Ports:  []policyinfo.Port{{Protocol: &tcp, Port: i32(port)}},
		}
	}

	// Both ends of the surrogate block, plus an interior value.
	for _, other := range []int32{55297, 57000, 57343} {
		assert.NotEqual(t, m.getClusterEndpointInfoKey(cpe(55296)), m.getClusterEndpointInfoKey(cpe(other)),
			"ports 55296 and %d must not share a hash key", other)
	}

	// A surrogate-block port must also stay distinct from a port outside it.
	assert.NotEqual(t, m.getClusterEndpointInfoKey(cpe(55296)), m.getClusterEndpointInfoKey(cpe(40000)))
}
