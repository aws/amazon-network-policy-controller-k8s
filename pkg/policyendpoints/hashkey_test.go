package policyendpoints

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

func cnpInfo(port int32) policyinfo.ClusterEndpointInfo {
	tcp, p := corev1.ProtocolTCP, port
	return policyinfo.ClusterEndpointInfo{
		CIDR: "10.200.0.0/16", Action: "Deny",
		Ports: []policyinfo.Port{{Protocol: &tcp, Port: &p}},
	}
}

func TestClusterKeyInjectiveOverPortSpace(t *testing.T) {
	m := &policyEndpointsManager{}
	seen := make(map[string]int32, 65535)
	for p := int32(1); p <= 65535; p++ {
		k := m.getClusterEndpointInfoKey(cnpInfo(p))
		if prev, dup := seen[k]; dup {
			t.Fatalf("ports %d and %d share a key", prev, p)
		}
		seen[k] = p
	}
	if len(seen) != 65535 {
		t.Fatalf("got %d distinct keys, want 65535", len(seen))
	}
}

func TestClusterKeyHighPortsDiffer(t *testing.T) {
	m := &policyEndpointsManager{}
	for _, tc := range [][2]int32{{55555, 56666}, {56666, 57000}, {55300, 57000}, {55555, 65535}} {
		if m.getClusterEndpointInfoKey(cnpInfo(tc[0])) == m.getClusterEndpointInfoKey(cnpInfo(tc[1])) {
			t.Errorf("ports %d and %d share a key", tc[0], tc[1])
		}
	}
}

func TestKeyFieldsCannotAlias(t *testing.T) {
	m := &policyEndpointsManager{}
	pairs := [][2]policyinfo.ClusterEndpointInfo{
		{{CIDR: "10.0.0.0/8", DomainName: "", Action: "Deny"},
			{CIDR: "10.0.0.0/", DomainName: "8", Action: "Deny"}},
		{{CIDR: "10.0.0.0/8", Action: "Deny"},
			{CIDR: "10.0.0.0/8De", Action: "ny"}},
	}
	for i, p := range pairs {
		if m.getClusterEndpointInfoKey(p[0]) == m.getClusterEndpointInfoKey(p[1]) {
			t.Errorf("pair %d aliases", i)
		}
	}
}

func TestNamespacedKeyPortRangesCannotAlias(t *testing.T) {
	m := &policyEndpointsManager{}
	rng := func(lo, hi int32) policyinfo.EndpointInfo {
		tcp, l, h := corev1.ProtocolTCP, lo, hi
		return policyinfo.EndpointInfo{
			CIDR:  "10.200.0.0/16",
			Ports: []policyinfo.Port{{Protocol: &tcp, Port: &l, EndPort: &h}},
		}
	}
	for _, tc := range [][4]int32{{1, 234, 12, 34}, {1, 2345, 12, 345}} {
		if m.getEndpointInfoKey(rng(tc[0], tc[1])) == m.getEndpointInfoKey(rng(tc[2], tc[3])) {
			t.Errorf("ranges %d-%d and %d-%d share a key", tc[0], tc[1], tc[2], tc[3])
		}
	}

	a := policyinfo.EndpointInfo{CIDR: "10.0.0.0/8", Except: []policyinfo.NetworkAddress{"10.1.0.0/16"}}
	b := policyinfo.EndpointInfo{CIDR: "10.0.0.0/810.1.0.0/16"}
	if m.getEndpointInfoKey(a) == m.getEndpointInfoKey(b) {
		t.Error("CIDR absorbed an Except entry")
	}
}

func TestKeyNilAndBoundaryPortFields(t *testing.T) {
	m := &policyEndpointsManager{}
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	one, max := int32(1), int32(65535)

	protos := []*corev1.Protocol{nil, &tcp, &udp}
	nums := []*int32{nil, &one, &max}

	seen := map[string]policyinfo.Port{}
	for _, proto := range protos {
		for _, port := range nums {
			for _, endPort := range nums {
				p := policyinfo.Port{Protocol: proto, Port: port, EndPort: endPort}
				info := policyinfo.ClusterEndpointInfo{
					CIDR: "10.200.0.0/16", Action: "Deny",
					Ports: []policyinfo.Port{p},
				}
				k := m.getClusterEndpointInfoKey(info)
				if prev, dup := seen[k]; dup {
					t.Errorf("%+v and %+v share a key", prev, p)
				}
				seen[k] = p
				if again := m.getClusterEndpointInfoKey(info); again != k {
					t.Errorf("%+v is not deterministic", p)
				}
			}
		}
	}
	if len(seen) != len(protos)*len(nums)*len(nums) {
		t.Fatalf("got %d distinct keys, want %d", len(seen), len(protos)*len(nums)*len(nums))
	}

	noPorts := policyinfo.ClusterEndpointInfo{CIDR: "10.200.0.0/16", Action: "Deny"}
	emptyPorts := noPorts
	emptyPorts.Ports = []policyinfo.Port{}
	if m.getClusterEndpointInfoKey(noPorts) != m.getClusterEndpointInfoKey(emptyPorts) {
		t.Error("nil and empty Ports should be equivalent")
	}
	if _, dup := seen[m.getClusterEndpointInfoKey(noPorts)]; dup {
		t.Error("no ports collides with a single-port rule")
	}
}

func TestKeyIgnoresFieldOrder(t *testing.T) {
	m := &policyEndpointsManager{}
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	p80, p443 := int32(80), int32(443)

	a := []policyinfo.Port{
		{Protocol: &tcp, Port: &p80},
		{Protocol: &udp, Port: &p443},
		{Protocol: &tcp},
	}
	b := []policyinfo.Port{a[2], a[1], a[0]}
	infoA := policyinfo.ClusterEndpointInfo{CIDR: "10.200.0.0/16", Action: "Deny", Ports: a}
	infoB := policyinfo.ClusterEndpointInfo{CIDR: "10.200.0.0/16", Action: "Deny", Ports: b}
	if m.getClusterEndpointInfoKey(infoA) != m.getClusterEndpointInfoKey(infoB) {
		t.Error("permuted ports produced different keys")
	}
	if a[0].Port != &p80 || a[2].Port != nil {
		t.Error("caller's port slice was reordered")
	}

	exA := policyinfo.EndpointInfo{CIDR: "10.0.0.0/8",
		Except: []policyinfo.NetworkAddress{"10.1.0.0/16", "10.2.0.0/16"}}
	exB := policyinfo.EndpointInfo{CIDR: "10.0.0.0/8",
		Except: []policyinfo.NetworkAddress{"10.2.0.0/16", "10.1.0.0/16"}}
	if m.getEndpointInfoKey(exA) != m.getEndpointInfoKey(exB) {
		t.Error("permuted Except produced different keys")
	}
	if exA.Except[0] != "10.1.0.0/16" {
		t.Error("caller's Except slice was reordered")
	}
}

func TestSubstitutedRuleIsIsolated(t *testing.T) {
	m := &policyEndpointsManager{}
	desired := cnpInfo(56666)
	stored := []policyinfo.ClusterPolicyEndpoint{{
		Spec: policyinfo.ClusterPolicyEndpointSpec{
			Tier:   "Admin",
			Egress: []policyinfo.ClusterEndpointInfo{cnpInfo(56666)},
		},
	}}

	// A tier change forces the spec to be rebuilt from the desired rules.
	_, _, _, modified, _ := m.processExistingClusterPolicyEndpoints(
		stored, nil, []policyinfo.ClusterEndpointInfo{desired}, nil,
		ClusterPolicyMetadata{Tier: "Baseline"})

	if len(modified) != 1 || len(modified[0].Spec.Egress) != 1 {
		t.Fatalf("expected one retained egress rule, got %+v", modified)
	}
	*modified[0].Spec.Egress[0].Ports[0].Port = 1
	if *desired.Ports[0].Port != 56666 {
		t.Error("mutating the persisted rule reached the desired rule")
	}
}

func TestInPlacePortEditReachesCPE(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 100}
	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cnp"},
		Spec:       policyinfo.ClusterNetworkPolicySpec{Tier: "Admin", Priority: 10},
	}
	stored := []policyinfo.ClusterPolicyEndpoint{{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cnp-abc12"},
		Spec: policyinfo.ClusterPolicyEndpointSpec{
			PolicyRef: policyinfo.ClusterPolicyReference{Name: "test-cnp"},
			Tier:      "Admin", Priority: 10,
			Egress: []policyinfo.ClusterEndpointInfo{cnpInfo(55555)},
		},
	}}

	create, update, _, err := m.computeClusterPolicyEndpoints(
		cnp, stored, nil, []policyinfo.ClusterEndpointInfo{cnpInfo(56666)}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var ports []int32
	for _, cpe := range append(append([]policyinfo.ClusterPolicyEndpoint{}, create...), update...) {
		for _, e := range cpe.Spec.Egress {
			ports = append(ports, *e.Ports[0].Port)
		}
	}
	if len(ports) != 1 || ports[0] != 56666 {
		t.Fatalf("persisted egress ports = %v, want [56666]", ports)
	}
}

func TestKeyResistsSeparatorsAndMultibyteValues(t *testing.T) {
	m := &policyEndpointsManager{}
	long := strings.Repeat("x", 1500)
	values := []string{
		"", "a", "a;", ";a", "a=1", "=1:a;", "cidr=1:a;", "1:a", "a[b]", "a,b", "a];",
		"\u65e5\u672c\u8a9e", "\u65e5", "\u00e9", "e\u0301", long, long + "y",
	}

	seen := map[string]string{}
	add := func(label string, info policyinfo.EndpointInfo) {
		k := m.getEndpointInfoKey(info)
		if prev, dup := seen[k]; dup {
			t.Errorf("%s collides with %s", label, prev)
		}
		seen[k] = label
		if again := m.getEndpointInfoKey(info); again != k {
			t.Errorf("%s is not deterministic", label)
		}
	}

	for i, v := range values {
		add(fmt.Sprintf("cidr[%d]", i), policyinfo.EndpointInfo{CIDR: policyinfo.NetworkAddress(v)})
		add(fmt.Sprintf("except[%d]", i), policyinfo.EndpointInfo{
			Except: []policyinfo.NetworkAddress{policyinfo.NetworkAddress(v)}})
		if v != "" {
			add(fmt.Sprintf("domain[%d]", i), policyinfo.EndpointInfo{DomainName: policyinfo.DomainName(v)})
		}
	}

	nilExcept := policyinfo.EndpointInfo{CIDR: "10.0.0.0/8"}
	emptyExcept := policyinfo.EndpointInfo{CIDR: "10.0.0.0/8", Except: []policyinfo.NetworkAddress{}}
	if m.getEndpointInfoKey(nilExcept) != m.getEndpointInfoKey(emptyExcept) {
		t.Error("nil and empty Except should be equivalent")
	}
}

func TestDuplicateStoredRulesCollapse(t *testing.T) {
	m := &policyEndpointsManager{}
	rewriteTier := ClusterPolicyMetadata{Tier: "Baseline"}
	mk := func(name string, rules ...policyinfo.ClusterEndpointInfo) policyinfo.ClusterPolicyEndpoint {
		return policyinfo.ClusterPolicyEndpoint{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       policyinfo.ClusterPolicyEndpointSpec{Tier: "Admin", Egress: rules},
		}
	}
	countEgress := func(sets ...[]policyinfo.ClusterPolicyEndpoint) int {
		n := 0
		for _, s := range sets {
			for _, cpe := range s {
				n += len(cpe.Spec.Egress)
			}
		}
		return n
	}

	t.Run("within one object", func(t *testing.T) {
		_, leftover, _, modified, deletes := m.processExistingClusterPolicyEndpoints(
			[]policyinfo.ClusterPolicyEndpoint{mk("a", cnpInfo(80), cnpInfo(80))},
			nil, []policyinfo.ClusterEndpointInfo{cnpInfo(80)}, nil, rewriteTier)

		if got := countEgress(modified, deletes); got != 1 {
			t.Errorf("egress rules retained = %d, want 1", got)
		}
		if len(leftover) != 0 {
			t.Errorf("unplaced desired rules = %d, want 0", len(leftover))
		}
	})

	t.Run("across two objects", func(t *testing.T) {
		_, leftover, _, modified, deletes := m.processExistingClusterPolicyEndpoints(
			[]policyinfo.ClusterPolicyEndpoint{mk("a", cnpInfo(80)), mk("b", cnpInfo(80))},
			nil, []policyinfo.ClusterEndpointInfo{cnpInfo(80)}, nil, rewriteTier)

		if got := countEgress(modified, deletes); got != 1 {
			t.Errorf("egress rules retained = %d, want 1", got)
		}
		if len(leftover) != 0 {
			t.Errorf("unplaced desired rules = %d, want 0", len(leftover))
		}
		if len(deletes) != 1 {
			t.Fatalf("emptied objects = %d, want 1", len(deletes))
		}
		if deletes[0].Name != "b" {
			t.Errorf("emptied object = %q, want \"b\"", deletes[0].Name)
		}
	})
}

func TestFQDNKeyCoversCIDRAndExcept(t *testing.T) {
	m := &policyEndpointsManager{}
	base := policyinfo.EndpointInfo{DomainName: "example.com"}

	withCIDR := base
	withCIDR.CIDR = "10.0.0.0/8"
	otherCIDR := base
	otherCIDR.CIDR = "192.168.0.0/16"
	withExcept := base
	withExcept.Except = []policyinfo.NetworkAddress{"10.1.0.0/16"}

	for _, tc := range []struct {
		name string
		a, b policyinfo.EndpointInfo
	}{
		{"domain only vs domain+cidr", base, withCIDR},
		{"differing cidr", withCIDR, otherCIDR},
		{"domain only vs domain+except", base, withExcept},
	} {
		if m.getEndpointInfoKey(tc.a) == m.getEndpointInfoKey(tc.b) {
			t.Errorf("%s: share an endpoint key", tc.name)
		}
		if getCombineKey(tc.a) == getCombineKey(tc.b) {
			t.Errorf("%s: share a combine key", tc.name)
		}
	}

	cidrOnly := policyinfo.EndpointInfo{CIDR: "example.com"}
	if m.getEndpointInfoKey(base) == m.getEndpointInfoKey(cidrOnly) {
		t.Error("domain and CIDR fields are interchangeable")
	}
	if getCombineKey(base) == getCombineKey(cidrOnly) {
		t.Error("domain and CIDR fields are interchangeable in the combine key")
	}
}

func BenchmarkClusterEndpointInfoKey(b *testing.B) {
	m := &policyEndpointsManager{}
	tcp := corev1.ProtocolTCP
	for _, n := range []int{1, 16} {
		ports := make([]policyinfo.Port, 0, n)
		for i := 0; i < n; i++ {
			p, e := int32(1000+i), int32(2000+i)
			ports = append(ports, policyinfo.Port{Protocol: &tcp, Port: &p, EndPort: &e})
		}
		info := policyinfo.ClusterEndpointInfo{CIDR: "10.200.0.0/16", Action: "Deny", Ports: ports}
		b.Run(fmt.Sprintf("%dports", n), func(b *testing.B) {
			for b.Loop() {
				m.getClusterEndpointInfoKey(info)
			}
		})
	}
}
