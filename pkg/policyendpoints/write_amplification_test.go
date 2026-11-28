package policyendpoints

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	policyequality "github.com/aws/amazon-network-policy-controller-k8s/pkg/equality"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

// These tests drive the compute functions through repeated reconciles against an in-memory store
// and count the API calls each reconcile would make. The store hands back its objects in a new
// random order every time, because the informer cache does not keep list order stable.

// writeCounter stands in for the API server. A write is a create, a delete, or an update whose
// spec differs from what is stored; Reconcile skips the other updates.
type writeCounter[T any] struct {
	objs    map[string]T
	created int
	rng     *rand.Rand
	name    func(*T) string
	setName func(*T, string)
	equal   func(a, b *T) bool
	clone   func(*T) T
}

func (s *writeCounter[T]) list() []T {
	names := make([]string, 0, len(s.objs))
	for n := range s.objs {
		names = append(names, n)
	}
	sort.Strings(names)
	s.rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	out := make([]T, 0, len(names))
	for _, n := range names {
		obj := s.objs[n]
		out = append(out, s.clone(&obj))
	}
	return out
}

func (s *writeCounter[T]) apply(t *testing.T, create, update, del []T) int {
	t.Helper()
	writes := 0
	for i := range create {
		s.created++
		s.setName(&create[i], fmt.Sprintf("obj-%03d", s.created))
		s.objs[s.name(&create[i])] = create[i]
		writes++
	}
	for i := range update {
		stored, ok := s.objs[s.name(&update[i])]
		require.True(t, ok, "update of an object that does not exist: %q", s.name(&update[i]))
		if !s.equal(&stored, &update[i]) {
			s.objs[s.name(&update[i])] = update[i]
			writes++
		}
	}
	for i := range del {
		delete(s.objs, s.name(&del[i]))
		writes++
	}
	return writes
}

func newPECounter(seed int64) *writeCounter[policyinfo.PolicyEndpoint] {
	return &writeCounter[policyinfo.PolicyEndpoint]{
		objs:    map[string]policyinfo.PolicyEndpoint{},
		rng:     rand.New(rand.NewSource(seed)),
		name:    func(pe *policyinfo.PolicyEndpoint) string { return pe.Name },
		setName: func(pe *policyinfo.PolicyEndpoint, n string) { pe.Name = n },
		equal: func(a, b *policyinfo.PolicyEndpoint) bool {
			return policyequality.EqualPolicyEndpointSpec(a.Spec, b.Spec)
		},
		clone: func(pe *policyinfo.PolicyEndpoint) policyinfo.PolicyEndpoint { return *pe.DeepCopy() },
	}
}

func newCPECounter(seed int64) *writeCounter[policyinfo.ClusterPolicyEndpoint] {
	return &writeCounter[policyinfo.ClusterPolicyEndpoint]{
		objs:    map[string]policyinfo.ClusterPolicyEndpoint{},
		rng:     rand.New(rand.NewSource(seed)),
		name:    func(cpe *policyinfo.ClusterPolicyEndpoint) string { return cpe.Name },
		setName: func(cpe *policyinfo.ClusterPolicyEndpoint, n string) { cpe.Name = n },
		equal: func(a, b *policyinfo.ClusterPolicyEndpoint) bool {
			return policyequality.EqualClusterPolicyEndpointSpec(a.Spec, b.Spec)
		},
		clone: func(cpe *policyinfo.ClusterPolicyEndpoint) policyinfo.ClusterPolicyEndpoint {
			return *cpe.DeepCopy()
		},
	}
}

var writeTestNP = &networking.NetworkPolicy{
	ObjectMeta: metav1.ObjectMeta{Name: "np", Namespace: "ns"},
	Spec:       networking.NetworkPolicySpec{PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress}},
}

var writeTestANP = &policyinfo.ApplicationNetworkPolicy{
	ObjectMeta: metav1.ObjectMeta{Name: "anp", Namespace: "ns"},
	Spec: policyinfo.ApplicationNetworkPolicySpec{
		PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress},
	},
}

var writeTestCNP = &policyinfo.ClusterNetworkPolicy{
	ObjectMeta: metav1.ObjectMeta{Name: "cnp"},
	Spec:       policyinfo.ClusterNetworkPolicySpec{Tier: "Admin", Priority: 10},
}

func reconcileNP(t *testing.T, m *policyEndpointsManager, s *writeCounter[policyinfo.PolicyEndpoint],
	ingress []policyinfo.EndpointInfo, pods []policyinfo.PodEndpoint) int {
	t.Helper()
	groups := []resolvers.IngressGroup{{Ingress: ingress, PodEndpoints: pods}}
	c, u, d, err := m.computePolicyEndpoints(writeTestNP, s.list(), groups, nil, nil)
	require.NoError(t, err)
	return s.apply(t, c, u, d)
}

func reconcileANP(t *testing.T, m *policyEndpointsManager, s *writeCounter[policyinfo.PolicyEndpoint],
	ingress []policyinfo.EndpointInfo, pods []policyinfo.PodEndpoint) int {
	t.Helper()
	c, u, d, err := m.computeApplicationNetworkPolicyEndpoints(writeTestANP, s.list(), ingress, nil, pods)
	require.NoError(t, err)
	return s.apply(t, c, u, d)
}

func reconcileCNP(t *testing.T, m *policyEndpointsManager, s *writeCounter[policyinfo.ClusterPolicyEndpoint],
	ingress []policyinfo.ClusterEndpointInfo, pods []policyinfo.PodEndpoint) int {
	t.Helper()
	c, u, d, err := m.computeClusterPolicyEndpoints(writeTestCNP, s.list(), ingress, nil, pods)
	require.NoError(t, err)
	return s.apply(t, c, u, d)
}

var writeTestNPMetadata = PolicyMetadata{Name: "np", Namespace: "ns", PolicyTypes: writeTestNP.Spec.PolicyTypes,
	APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"}

// seedPE stores an existing PolicyEndpoint of writeTestNP with the given content.
func seedPE(s *writeCounter[policyinfo.PolicyEndpoint], m *policyEndpointsManager, name, targetGroup string,
	ingress, egress []policyinfo.EndpointInfo, pods []policyinfo.PodEndpoint) {
	pe := m.newPolicyEndpoint(writeTestNPMetadata, ingress, egress, pods)
	pe.Name = name
	pe.Spec.TargetGroup = targetGroup
	s.objs[name] = pe
}

// peerSet maps each peer to the ports it is allowed on, the way the node agent unions a policy's
// entries across all of its objects. A peer allowed on every port maps to ["all"], so a stored
// entry narrowed to specific ports does not compare equal to it.
func peerSet[E policyinfo.EndpointInfo | policyinfo.ClusterEndpointInfo](entries []E) map[string][]string {
	out := map[string]map[string]bool{}
	for _, e := range entries {
		var peer string
		var ports []policyinfo.Port
		switch v := any(e).(type) {
		case policyinfo.EndpointInfo:
			peer, ports = string(v.CIDR), v.Ports
		case policyinfo.ClusterEndpointInfo:
			peer, ports = string(v.CIDR)+"/"+string(v.Action), v.Ports
		}
		if out[peer] == nil {
			out[peer] = map[string]bool{}
		}
		// No ports means every port, which covers any specific ones.
		if len(ports) == 0 || out[peer]["all"] {
			out[peer] = map[string]bool{"all": true}
			continue
		}
		for _, p := range ports {
			out[peer][fmt.Sprintf("%s/%s/%s", protocolKeyPart(p.Protocol), int32PtrKeyPart(p.Port), int32PtrKeyPart(p.EndPort))] = true
		}
	}
	sorted := map[string][]string{}
	for peer, ports := range out {
		sorted[peer] = []string{}
		for p := range ports {
			sorted[peer] = append(sorted[peer], p)
		}
		sort.Strings(sorted[peer])
	}
	return sorted
}

// assertStoresDesired checks that the stored objects, taken together, carry exactly the desired
// ingress peers and pods. A test that only counts writes would pass a packer that drops endpoints.
func assertStoresDesired(t *testing.T, objs map[string]policyinfo.PolicyEndpoint,
	ingress []policyinfo.EndpointInfo, pods []policyinfo.PodEndpoint) {
	t.Helper()
	var storedIngress []policyinfo.EndpointInfo
	var storedPods []policyinfo.PodEndpoint
	for _, pe := range objs {
		storedIngress = append(storedIngress, pe.Spec.Ingress...)
		storedPods = append(storedPods, pe.Spec.PodSelectorEndpoints...)
	}
	require.Equal(t, peerSet(ingress), peerSet(storedIngress))
	require.ElementsMatch(t, pods, storedPods)
}

func assertStoresDesiredCNP(t *testing.T, objs map[string]policyinfo.ClusterPolicyEndpoint,
	ingress []policyinfo.ClusterEndpointInfo, pods []policyinfo.PodEndpoint) {
	t.Helper()
	var storedIngress []policyinfo.ClusterEndpointInfo
	var storedPods []policyinfo.PodEndpoint
	for _, cpe := range objs {
		storedIngress = append(storedIngress, cpe.Spec.Ingress...)
		storedPods = append(storedPods, cpe.Spec.PodSelectorEndpoints...)
	}
	require.Equal(t, peerSet(ingress), peerSet(storedIngress))
	require.ElementsMatch(t, pods, storedPods)
}

func tcpPort(p int32) policyinfo.Port {
	proto := corev1.ProtocolTCP
	return policyinfo.Port{Protocol: &proto, Port: &p}
}

func peerIP(i int) policyinfo.NetworkAddress {
	return policyinfo.NetworkAddress(fmt.Sprintf("10.9.%d.%d", i/250, i%250))
}

func podEndpoint(i int) policyinfo.PodEndpoint {
	return policyinfo.PodEndpoint{Name: fmt.Sprintf("pod-%d", i), Namespace: "ns",
		PodIP: policyinfo.NetworkAddress(fmt.Sprintf("10.1.%d.%d", i/250, i%250)), HostIP: "192.168.0.1"}
}

func podEndpoints(ids []int) []policyinfo.PodEndpoint {
	out := make([]policyinfo.PodEndpoint, 0, len(ids))
	for _, id := range ids {
		out = append(out, podEndpoint(id))
	}
	return out
}

// peers returns one ingress entry per peer and port set, in the order a resolver walks the rules.
func peers(n int, portSets ...[]policyinfo.Port) []policyinfo.EndpointInfo {
	var out []policyinfo.EndpointInfo
	for _, ports := range portSets {
		for i := range n {
			out = append(out, policyinfo.EndpointInfo{CIDR: peerIP(i), Ports: ports})
		}
	}
	return out
}

func clusterPeers(n int, portSets ...[]policyinfo.Port) []policyinfo.ClusterEndpointInfo {
	var out []policyinfo.ClusterEndpointInfo
	for _, ports := range portSets {
		for i := range n {
			out = append(out, policyinfo.ClusterEndpointInfo{CIDR: peerIP(i), Ports: ports,
				Action: policyinfo.ClusterNetworkPolicyRuleActionAccept})
		}
	}
	return out
}

// shuffledPorts is what dedupPorts hands back for a named port: the same set in any order.
func shuffledPorts(rng *rand.Rand) []policyinfo.Port {
	ports := []policyinfo.Port{tcpPort(80), tcpPort(443), tcpPort(8080)}
	rng.Shuffle(len(ports), func(i, j int) { ports[i], ports[j] = ports[j], ports[i] })
	return ports
}

func seq(n int) []int {
	ids := make([]int, n)
	for i := range ids {
		ids[i] = i
	}
	return ids
}

const noopRounds = 30

func TestWrites_NoOpReconcile_NamedPortsInAnotherOrder(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	s := newPECounter(1)
	rng := rand.New(rand.NewSource(2))
	pods := podEndpoints(seq(7))

	reconcileNP(t, m, s, peers(8, shuffledPorts(rng)), pods)
	require.Greater(t, len(s.objs), 1, "the policy must span several objects for placement to matter")

	writes := 0
	for range noopRounds {
		ingress := peers(8, shuffledPorts(rng))
		writes += reconcileNP(t, m, s, ingress, pods)
		assertStoresDesired(t, s.objs, ingress, pods)
	}
	assert.Zero(t, writes, "the same ports in another order are the same rule")
}

// ApplicationNetworkPolicy ingress resolves named ports on the same path as NetworkPolicy, so it
// sees the same reordering.
func TestWrites_NoOpReconcile_NamedPortsInAnotherOrder_ANP(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	s := newPECounter(1)
	rng := rand.New(rand.NewSource(2))
	pods := podEndpoints(seq(7))

	reconcileANP(t, m, s, peers(8, shuffledPorts(rng)), pods)
	require.Greater(t, len(s.objs), 1)

	writes := 0
	for range noopRounds {
		ingress := peers(8, shuffledPorts(rng))
		writes += reconcileANP(t, m, s, ingress, pods)
		assertStoresDesired(t, s.objs, ingress, pods)
	}
	assert.Zero(t, writes)
}

func TestWrites_NoOpReconcile_CNP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ingress func(rng *rand.Rand) []policyinfo.ClusterEndpointInfo
	}{
		{"ports in another order", func(rng *rand.Rand) []policyinfo.ClusterEndpointInfo {
			return clusterPeers(8, shuffledPorts(rng))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
			s := newCPECounter(1)
			rng := rand.New(rand.NewSource(2))
			pods := podEndpoints(seq(7))

			reconcileCNP(t, m, s, tc.ingress(rng), pods)
			require.Greater(t, len(s.objs), 1)

			writes := 0
			for range noopRounds {
				ingress := tc.ingress(rng)
				writes += reconcileCNP(t, m, s, ingress, pods)
				assertStoresDesiredCNP(t, s.objs, ingress, pods)
			}
			assert.Zero(t, writes)
		})
	}
}

// replaceOne swaps one random pod for a new one, the way a rolling update does.
func replaceOne(rng *rand.Rand, ids []int, next *int) {
	ids[rng.Intn(len(ids))] = *next
	*next++
}

// Seven pods in chunks of three leave room, so a new pod can land in an object that nothing else
// touched. The object the old pod left is written anyway and has room for the new one.
func TestWrites_PodReplacement_WritesOneObject(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	s := newPECounter(1)
	rng := rand.New(rand.NewSource(2))
	ids, next := seq(7), 7
	ingress := peers(1, []policyinfo.Port{tcpPort(80)})

	reconcileNP(t, m, s, ingress, podEndpoints(ids))
	const rounds = 50
	writes := 0
	for range rounds {
		replaceOne(rng, ids, &next)
		writes += reconcileNP(t, m, s, ingress, podEndpoints(ids))
		assertStoresDesired(t, s.objs, ingress, podEndpoints(ids))
	}
	assert.Equal(t, rounds, writes, "each replacement should write only the object the old pod left")
	assert.Len(t, s.objs, 3)
}

func TestWrites_PodReplacement_WritesOneObject_CNP(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	s := newCPECounter(1)
	rng := rand.New(rand.NewSource(2))
	ids, next := seq(7), 7
	ingress := clusterPeers(1, []policyinfo.Port{tcpPort(80)})

	reconcileCNP(t, m, s, ingress, podEndpoints(ids))
	const rounds = 50
	writes := 0
	for range rounds {
		replaceOne(rng, ids, &next)
		writes += reconcileCNP(t, m, s, ingress, podEndpoints(ids))
		assertStoresDesiredCNP(t, s.objs, ingress, podEndpoints(ids))
	}
	assert.Equal(t, rounds, writes)
	assert.Len(t, s.objs, 3)
}

// An object whose pods all left is written either way, as an update or a delete, so refilling it
// is free. Putting the new pod into the untouched object instead costs a second write.
func TestWrites_EmptiedObjectIsRefilledBeforeAnUntouchedOne(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	for seed := range int64(10) {
		s := newPECounter(seed)
		for _, name := range []string{"a-untouched", "b-emptied"} {
			pe := m.newPolicyEndpoint(PolicyMetadata{Name: "np", Namespace: "ns",
				PolicyTypes: writeTestNP.Spec.PolicyTypes, APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"}, nil, nil, nil)
			pe.Name = name
			s.objs[name] = pe
		}
		untouched, emptied := s.objs["a-untouched"], s.objs["b-emptied"]
		untouched.Spec.PodSelectorEndpoints = podEndpoints([]int{1})
		emptied.Spec.PodSelectorEndpoints = podEndpoints([]int{2})
		s.objs["a-untouched"], s.objs["b-emptied"] = untouched, emptied

		writes := reconcileNP(t, m, s, nil, podEndpoints([]int{1, 3}))

		assert.Equal(t, 1, writes, "seed %d", seed)
		assert.ElementsMatch(t, podEndpoints([]int{1}), s.objs["a-untouched"].Spec.PodSelectorEndpoints, "seed %d", seed)
		assert.ElementsMatch(t, podEndpoints([]int{3}), s.objs["b-emptied"].Spec.PodSelectorEndpoints, "seed %d", seed)
	}
}

// mustWrite counts the objects that hold a pod or ingress peer that is no longer wanted. Those
// objects are written whatever the packer does, so it is the fewest writes a reconcile can make.
func mustWrite(objs map[string]policyinfo.PolicyEndpoint, pods []policyinfo.PodEndpoint,
	ingress []policyinfo.EndpointInfo) int {
	wantPod := map[policyinfo.PodEndpoint]bool{}
	for _, p := range pods {
		wantPod[p] = true
	}
	wantPeer := map[policyinfo.NetworkAddress]bool{}
	for _, e := range ingress {
		wantPeer[e.CIDR] = true
	}
	n := 0
	for _, pe := range objs {
		stale := false
		for _, p := range pe.Spec.PodSelectorEndpoints {
			stale = stale || !wantPod[p]
		}
		for _, e := range pe.Spec.Ingress {
			stale = stale || !wantPeer[e.CIDR]
		}
		if stale {
			n++
		}
	}
	return n
}

// A policy that selects app=x and allows ingress from app=x holds each pod twice: as a target and
// as a peer. Replacing a pod changes both, and the two halves can sit in different objects. The
// new pod's two halves should go into the objects being written, not into a third.
func TestWrites_PodThatIsTargetAndPeer_WritesOnlyObjectsItLeft(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	s := newPECounter(1)
	rng := rand.New(rand.NewSource(2))
	ids, next := seq(7), 7
	state := func() ([]policyinfo.EndpointInfo, []policyinfo.PodEndpoint) {
		pods := podEndpoints(ids)
		ingress := make([]policyinfo.EndpointInfo, 0, len(pods))
		for _, p := range pods {
			ingress = append(ingress, policyinfo.EndpointInfo{CIDR: p.PodIP, Ports: []policyinfo.Port{tcpPort(80)}})
		}
		return ingress, pods
	}

	ingress, pods := state()
	reconcileNP(t, m, s, ingress, pods)
	writes, floor := 0, 0
	for range 50 {
		replaceOne(rng, ids, &next)
		ingress, pods = state()
		floor += mustWrite(s.objs, pods, ingress)
		writes += reconcileNP(t, m, s, ingress, pods)
		assertStoresDesired(t, s.objs, ingress, pods)
	}
	assert.Equal(t, floor, writes)
}

// The set of objects being written carries from one packing pass to the next. Here the purge only
// marks a, which has no room. The new peer goes into u, the last object with ingress room, and from
// then on u is being written, so the new pod must follow it there rather than into b, which has
// pod room but would otherwise not be written at all.
func TestWrites_ObjectFilledInOnePassIsPreferredInTheNext(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 2, logger: logr.Discard()}
	s := newPECounter(1)
	p80 := []policyinfo.Port{tcpPort(80)}
	peer := func(i int) policyinfo.EndpointInfo { return policyinfo.EndpointInfo{CIDR: peerIP(i), Ports: p80} }
	gone := []policyinfo.EndpointInfo{{CIDR: "10.8.0.1", Ports: p80}}
	seedPE(s, m, "a", "", []policyinfo.EndpointInfo{peer(0), peer(1)}, gone, podEndpoints([]int{0, 1}))
	seedPE(s, m, "b", "", []policyinfo.EndpointInfo{peer(2), peer(3)}, nil, podEndpoints([]int{2}))
	seedPE(s, m, "u", "", []policyinfo.EndpointInfo{peer(4)}, nil, podEndpoints([]int{3}))

	ingress := peers(6, p80)
	pods := podEndpoints(seq(5))
	writes := reconcileNP(t, m, s, ingress, pods)

	assert.Equal(t, 2, writes, "only a, which lost its egress, and u, which takes the new peer and pod")
	assert.Equal(t, podEndpoints([]int{2}), s.objs["b"].Spec.PodSelectorEndpoints)
	assertStoresDesired(t, s.objs, ingress, pods)
}

// An object whose target group changes is written anyway, so it is the cheapest place for a new
// endpoint, ahead of an untouched object that sorts first.
func TestWrites_RegroupedObjectTakesNewEndpointsFirst(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	s := newPECounter(1)
	seedPE(s, m, "b-untouched", "", nil, nil, podEndpoints([]int{1}))
	seedPE(s, m, "z-regrouped", "g1", nil, nil, podEndpoints([]int{2, 3}))

	pods := podEndpoints([]int{1, 2, 3, 4})
	writes := reconcileNP(t, m, s, nil, pods)

	assert.Equal(t, 1, writes)
	assert.Equal(t, podEndpoints([]int{1}), s.objs["b-untouched"].Spec.PodSelectorEndpoints)
	assert.Empty(t, s.objs["z-regrouped"].Spec.TargetGroup)
	assertStoresDesired(t, s.objs, nil, pods)
}

// Whether an emptied object is kept must depend on what it holds after packing, not on its name.
// The API server always names objects, but nothing in the packer should be able to delete an
// object it just put an endpoint into.
func TestPack_RefilledEmptiedObjectIsNeverDeleted(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 1, logger: logr.Discard()}
	unnamed := func(pod int) policyinfo.PolicyEndpoint {
		return m.newPolicyEndpoint(writeTestNPMetadata, nil, nil, podEndpoints([]int{pod}))
	}
	groups := []resolvers.IngressGroup{{PodEndpoints: podEndpoints([]int{1, 2})}}

	c, u, d, err := m.computePolicyEndpoints(writeTestNP, []policyinfo.PolicyEndpoint{unnamed(1), unnamed(3)}, groups, nil, nil)
	require.NoError(t, err)

	var kept []policyinfo.PodEndpoint
	for _, pe := range append(c, u...) {
		kept = append(kept, pe.Spec.PodSelectorEndpoints...)
	}
	assert.ElementsMatch(t, podEndpoints([]int{1, 2}), kept)
	for _, pe := range d {
		assert.Empty(t, pe.Spec.PodSelectorEndpoints, "a deleted object must not carry a desired pod")
	}
}

// ApplicationNetworkPolicy packs through the same code as NetworkPolicy.
func TestWrites_PodReplacement_WritesOneObject_ANP(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 3, logger: logr.Discard()}
	s := newPECounter(1)
	rng := rand.New(rand.NewSource(2))
	ids, next := seq(7), 7
	ingress := peers(1, []policyinfo.Port{tcpPort(80)})

	reconcileANP(t, m, s, ingress, podEndpoints(ids))
	const rounds = 50
	writes := 0
	for range rounds {
		replaceOne(rng, ids, &next)
		writes += reconcileANP(t, m, s, ingress, podEndpoints(ids))
		assertStoresDesired(t, s.objs, ingress, podEndpoints(ids))
	}
	assert.Equal(t, rounds, writes, "each replacement should write only the object the old pod left")
	assert.Len(t, s.objs, 3)
}

// removeRandom drops n random ids, the way pods go away during a scale-down or before their
// replacements have an IP.
func removeRandom(rng *rand.Rand, ids []int, n int) []int {
	for range n {
		i := rng.Intn(len(ids))
		ids[i] = ids[len(ids)-1]
		ids = ids[:len(ids)-1]
	}
	return ids
}

// Additions often arrive in a reconcile of their own. The pod handler ignores a pod until it has
// an IP, so a replacement's removal and addition can land in different batches, and a scale-up
// only adds. With nothing being written, the new pods belong in the untouched object with the most
// room, so a batch that fits in one object costs one write. Filling untouched objects in name order
// instead keeps the first objects full and leaves the removals' holes scattered, so each batch is
// spread over several objects.
func TestWrites_AdditionsInTheirOwnReconcileWriteOneObject(t *testing.T) {
	for _, seed := range []int64{1, 2, 3} {
		m := &policyEndpointsManager{endpointChunkSize: 200, logger: logr.Discard()}
		s := newPECounter(seed)
		rng := rand.New(rand.NewSource(seed + 10))
		ids, next := seq(900), 900
		ingress := peers(10, []policyinfo.Port{tcpPort(80)})

		reconcileNP(t, m, s, ingress, podEndpoints(ids))
		const rounds = 50
		writes := 0
		for range rounds {
			ids = removeRandom(rng, ids, 10)
			reconcileNP(t, m, s, ingress, podEndpoints(ids))
			for range 10 {
				ids = append(ids, next)
				next++
			}
			writes += reconcileNP(t, m, s, ingress, podEndpoints(ids))
			assertStoresDesired(t, s.objs, ingress, podEndpoints(ids))
		}
		assert.Equal(t, rounds, writes, "seed %d: each batch of additions fits in one object", seed)
		assert.Len(t, s.objs, 5, "seed %d", seed)
	}
}
