package equality

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

func TestEqualPolicyEndpointSpec_IngressOrderInsensitive(t *testing.T) {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port80 := int32(80)
	port443 := int32(443)

	a := policyinfo.PolicyEndpointSpec{
		PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		PolicyRef:   policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		PodIsolation: []networking.PolicyType{
			networking.PolicyTypeIngress,
		},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "10.0.0.0/8", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port80}}},
			{CIDR: "172.16.0.0/12", Ports: []policyinfo.Port{{Protocol: &udp, Port: &port443}}},
		},
	}

	// Same content, reversed order.
	b := policyinfo.PolicyEndpointSpec{
		PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		PolicyRef:   policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		PodIsolation: []networking.PolicyType{
			networking.PolicyTypeIngress,
		},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "172.16.0.0/12", Ports: []policyinfo.Port{{Protocol: &udp, Port: &port443}}},
			{CIDR: "10.0.0.0/8", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port80}}},
		},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "should be equal regardless of Ingress order")
	assert.False(t, equality.Semantic.DeepEqual(a, b), "k8s semantic equality should be order-sensitive for Ingress")
}

func TestEqualPolicyEndpointSpec_PortOrderInsensitive(t *testing.T) {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port80 := int32(80)
	port443 := int32(443)

	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{
				CIDR: "10.0.0.0/8",
				Ports: []policyinfo.Port{
					{Protocol: &tcp, Port: &port80},
					{Protocol: &udp, Port: &port443},
				},
			},
		},
	}

	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{
				CIDR: "10.0.0.0/8",
				Ports: []policyinfo.Port{
					{Protocol: &udp, Port: &port443},
					{Protocol: &tcp, Port: &port80},
				},
			},
		},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "should be equal regardless of Ports order within EndpointInfo")
	assert.False(t, equality.Semantic.DeepEqual(a, b), "k8s semantic equality should be order-sensitive for Ports")
}

func TestEqualPolicyEndpointSpec_ExceptOrderInsensitive(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{
				CIDR:   "10.0.0.0/8",
				Except: []policyinfo.NetworkAddress{"10.1.0.0/16", "10.2.0.0/16"},
			},
		},
	}

	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{
				CIDR:   "10.0.0.0/8",
				Except: []policyinfo.NetworkAddress{"10.2.0.0/16", "10.1.0.0/16"},
			},
		},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "should be equal regardless of Except order")
	assert.False(t, equality.Semantic.DeepEqual(a, b), "k8s semantic equality should be order-sensitive for Except")
}

func TestEqualPolicyEndpointSpec_PodSelectorEndpointsOrderInsensitive(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		PodSelectorEndpoints: []policyinfo.PodEndpoint{
			{Name: "pod-a", Namespace: "ns", PodIP: "10.0.0.1", HostIP: "192.168.0.1"},
			{Name: "pod-b", Namespace: "ns", PodIP: "10.0.0.2", HostIP: "192.168.0.2"},
		},
	}

	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		PodSelectorEndpoints: []policyinfo.PodEndpoint{
			{Name: "pod-b", Namespace: "ns", PodIP: "10.0.0.2", HostIP: "192.168.0.2"},
			{Name: "pod-a", Namespace: "ns", PodIP: "10.0.0.1", HostIP: "192.168.0.1"},
		},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "should be equal regardless of PodSelectorEndpoints order")
	assert.False(t, equality.Semantic.DeepEqual(a, b), "k8s semantic equality should be order-sensitive for PodSelectorEndpoints")
}

func TestEqualPolicyEndpointSpec_NotEqual(t *testing.T) {
	tcp := corev1.ProtocolTCP
	port80 := int32(80)
	port443 := int32(443)

	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "10.0.0.0/8", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port80}}},
		},
	}

	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "10.0.0.0/8", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port443}}},
		},
	}

	assert.False(t, EqualPolicyEndpointSpec(a, b), "different port values should not be equal")
}

func TestEqualPolicyEndpointSpec_DifferentLengths(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "10.0.0.0/8"},
			{CIDR: "172.16.0.0/12"},
		},
	}

	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "10.0.0.0/8"},
		},
	}

	assert.False(t, EqualPolicyEndpointSpec(a, b), "different lengths should not be equal")
}

func TestEqualPolicyEndpointSpec_BothEmpty(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
	}
	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "both empty should be equal")
}

func TestEqualPolicyEndpointSpec_NilVsEmptySlice(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress:   nil,
	}
	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress:   []policyinfo.EndpointInfo{},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "nil and empty slice should be equal")
}

func TestEqualPolicyEndpointSpec_PodIsolationOrderInsensitive(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{
		PolicyRef:    policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		PodIsolation: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
	}
	b := policyinfo.PolicyEndpointSpec{
		PolicyRef:    policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		PodIsolation: []networking.PolicyType{networking.PolicyTypeEgress, networking.PolicyTypeIngress},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "PodIsolation order should not matter")
	assert.False(t, equality.Semantic.DeepEqual(a, b), "k8s semantic equality should be order-sensitive for PodIsolation")
}

func TestEqualPolicyEndpointSpec_DomainNameEndpoints(t *testing.T) {
	tcp := corev1.ProtocolTCP
	port443 := int32(443)

	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Egress: []policyinfo.EndpointInfo{
			{DomainName: "example.com", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port443}}},
			{DomainName: "other.com", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port443}}},
		},
	}

	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Egress: []policyinfo.EndpointInfo{
			{DomainName: "other.com", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port443}}},
			{DomainName: "example.com", Ports: []policyinfo.Port{{Protocol: &tcp, Port: &port443}}},
		},
	}

	assert.True(t, EqualPolicyEndpointSpec(a, b), "DomainName egress order should not matter")
	assert.False(t, equality.Semantic.DeepEqual(a, b), "k8s semantic equality should be order-sensitive for Egress")
}

// --- ClusterPolicyEndpointSpec tests ---

func TestEqualClusterPolicyEndpointSpec_IngressOrderInsensitive(t *testing.T) {
	accept := policyinfo.ClusterNetworkPolicyRuleActionAccept

	a := policyinfo.ClusterPolicyEndpointSpec{
		PolicyRef: policyinfo.ClusterPolicyReference{Name: "cnp"},
		Tier:      policyinfo.AdminTier,
		Priority:  100,
		Ingress: []policyinfo.ClusterEndpointInfo{
			{CIDR: "10.0.0.0/8", Action: accept},
			{CIDR: "172.16.0.0/12", Action: accept},
		},
	}

	b := policyinfo.ClusterPolicyEndpointSpec{
		PolicyRef: policyinfo.ClusterPolicyReference{Name: "cnp"},
		Tier:      policyinfo.AdminTier,
		Priority:  100,
		Ingress: []policyinfo.ClusterEndpointInfo{
			{CIDR: "172.16.0.0/12", Action: accept},
			{CIDR: "10.0.0.0/8", Action: accept},
		},
	}

	assert.True(t, EqualClusterPolicyEndpointSpec(a, b), "should be equal regardless of Ingress order")
	assert.False(t, equality.Semantic.DeepEqual(a, b), "k8s semantic equality should be order-sensitive for ClusterEndpointInfo Ingress")
}

func TestEqualClusterPolicyEndpointSpec_DifferentAction(t *testing.T) {
	a := policyinfo.ClusterPolicyEndpointSpec{
		PolicyRef: policyinfo.ClusterPolicyReference{Name: "cnp"},
		Tier:      policyinfo.AdminTier,
		Priority:  100,
		Ingress: []policyinfo.ClusterEndpointInfo{
			{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionAccept},
		},
	}

	b := policyinfo.ClusterPolicyEndpointSpec{
		PolicyRef: policyinfo.ClusterPolicyReference{Name: "cnp"},
		Tier:      policyinfo.AdminTier,
		Priority:  100,
		Ingress: []policyinfo.ClusterEndpointInfo{
			{CIDR: "10.0.0.0/8", Action: policyinfo.ClusterNetworkPolicyRuleActionDeny},
		},
	}

	assert.False(t, EqualClusterPolicyEndpointSpec(a, b), "different Action should not be equal")
}

func TestEqualClusterPolicyEndpointSpec_DifferentPriority(t *testing.T) {
	a := policyinfo.ClusterPolicyEndpointSpec{
		PolicyRef: policyinfo.ClusterPolicyReference{Name: "cnp"},
		Tier:      policyinfo.AdminTier,
		Priority:  100,
	}

	b := policyinfo.ClusterPolicyEndpointSpec{
		PolicyRef: policyinfo.ClusterPolicyReference{Name: "cnp"},
		Tier:      policyinfo.AdminTier,
		Priority:  200,
	}

	assert.False(t, EqualClusterPolicyEndpointSpec(a, b), "different Priority should not be equal")
}

func TestEqualPolicyEndpointSpec_DuplicateEntries(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "10.0.0.0/8"},
			{CIDR: "10.0.0.0/8"},
		},
	}

	b := policyinfo.PolicyEndpointSpec{
		PolicyRef: policyinfo.PolicyReference{Name: "pol", Namespace: "ns"},
		Ingress: []policyinfo.EndpointInfo{
			{CIDR: "10.0.0.0/8"},
			{CIDR: "172.16.0.0/12"},
		},
	}

	assert.False(t, EqualPolicyEndpointSpec(a, b), "duplicate vs distinct entries should not be equal")
}

// ---------------------------------------------------------------------------
// Field-coverage guards: these tests fail when a new field is added to a CRD
// type but the corresponding equality/key function in semantic.go is not
// updated. If a test below fails, update the equality function AND add the
// new field name to the expected list here.
// ---------------------------------------------------------------------------

func structFieldNames(v interface{}) []string {
	rt := reflect.TypeOf(v)
	fields := make([]string, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		fields[i] = rt.Field(i).Name
	}
	return fields
}

func TestEqualPolicyEndpointSpec_CoversAllFields(t *testing.T) {
	expected := []string{
		"PodSelector",
		"PolicyRef",
		"PodIsolation",
		"PodSelectorEndpoints",
		"Ingress",
		"Egress",
		"TargetGroup",
	}
	assert.ElementsMatch(t, expected, structFieldNames(policyinfo.PolicyEndpointSpec{}),
		"PolicyEndpointSpec fields changed — update EqualPolicyEndpointSpec in semantic.go")
}

func TestEqualPolicyEndpointSpec_TargetGroup(t *testing.T) {
	a := policyinfo.PolicyEndpointSpec{TargetGroup: "abc1234567"}
	b := policyinfo.PolicyEndpointSpec{TargetGroup: "def1234567"}

	// Regrouping can change only this field, so if it did not count the object would never be updated.
	assert.False(t, EqualPolicyEndpointSpec(a, b))
	assert.True(t, EqualPolicyEndpointSpec(a, a))
	// Ungrouped differs from grouped, so an upgrade restamps existing objects.
	assert.False(t, EqualPolicyEndpointSpec(policyinfo.PolicyEndpointSpec{}, a))
}

func TestEqualClusterPolicyEndpointSpec_CoversAllFields(t *testing.T) {
	expected := []string{
		"PolicyRef",
		"Tier",
		"Priority",
		"Subject",
		"PodSelectorEndpoints",
		"Ingress",
		"Egress",
	}
	assert.ElementsMatch(t, expected, structFieldNames(policyinfo.ClusterPolicyEndpointSpec{}),
		"ClusterPolicyEndpointSpec fields changed — update EqualClusterPolicyEndpointSpec in semantic.go")
}

func TestEndpointInfoKey_CoversAllFields(t *testing.T) {
	expected := []string{
		"CIDR",
		"Except",
		"Ports",
		"DomainName",
	}
	assert.ElementsMatch(t, expected, structFieldNames(policyinfo.EndpointInfo{}),
		"EndpointInfo fields changed — update endpointInfoKey in semantic.go")
}

func TestClusterEndpointInfoKey_CoversAllFields(t *testing.T) {
	expected := []string{
		"CIDR",
		"Ports",
		"DomainName",
		"Action",
	}
	assert.ElementsMatch(t, expected, structFieldNames(policyinfo.ClusterEndpointInfo{}),
		"ClusterEndpointInfo fields changed — update clusterEndpointInfoKey in semantic.go")
}

func TestPodEndpointKey_CoversAllFields(t *testing.T) {
	expected := []string{
		"HostIP",
		"PodIP",
		"Name",
		"Namespace",
	}
	assert.ElementsMatch(t, expected, structFieldNames(policyinfo.PodEndpoint{}),
		"PodEndpoint fields changed — update podEndpointKey in semantic.go")
}

func TestPortKey_CoversAllFields(t *testing.T) {
	expected := []string{
		"Protocol",
		"Port",
		"EndPort",
	}
	assert.ElementsMatch(t, expected, structFieldNames(policyinfo.Port{}),
		"Port fields changed — update portKey in semantic.go")
}
