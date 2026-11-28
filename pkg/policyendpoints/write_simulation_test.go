package policyendpoints

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	"golang.org/x/exp/maps"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

// TestWriteSimulation prints the write counts behind the proposal in
// proposals/reduce-policyendpoint-writes.md. It needs this file and write_amplification_test.go,
// and runs on any build that has them, so copying both onto main gives main's column:
//
//	NPC_WRITE_SIMULATION=1 go test ./pkg/policyendpoints/ -run TestWriteSimulation -v
//
// Ports come in the order main's dedupPorts gives them (values of a fresh map), and the store
// lists objects in Go map order, as the informer's indexed List does. Each scenario runs ten
// times; the lower bound is computed per reconcile on the build's own state.
func TestWriteSimulation(t *testing.T) {
	if os.Getenv("NPC_WRITE_SIMULATION") == "" {
		t.Skip("set NPC_WRITE_SIMULATION=1 to run")
	}
	p80 := []policyinfo.Port{tcpPort(80)}
	twoRules := func(n int) []policyinfo.EndpointInfo { return peers(n, p80, []policyinfo.Port{tcpPort(443)}) }

	for _, ports := range [][]int32{{8080, 9090}, {80, 443, 8080}} {
		simulate(t, fmt.Sprintf("50 no-op reconciles, 1000 pods, 900 peers, named ports resolving to %d ports", len(ports)), func(s *simState) {
			pods := podEndpoints(seq(1000))
			s.reconcile(peers(900, dedupOrder(ports...)), pods, false)
			s.counting = true
			for range 50 {
				s.reconcile(peers(900, dedupOrder(ports...)), pods, false)
			}
		})
	}
	for _, k := range []int{1, 10} {
		simulate(t, fmt.Sprintf("100 reconciles, 900 pods, %d replaced per reconcile in one batch", k), func(s *simState) {
			ids, next := seq(900), 900
			s.reconcile(peers(10, p80), podEndpoints(ids), false)
			s.counting = true
			for range 100 {
				for range k {
					replaceOne(s.rng, ids, &next)
				}
				s.reconcile(peers(10, p80), podEndpoints(ids), true)
			}
		})
	}
	splitChurn := func(ingress func() []policyinfo.EndpointInfo) func(*simState) {
		return func(s *simState) {
			ids, next := seq(900), 900
			s.reconcile(ingress(), podEndpoints(ids), false)
			s.counting = true
			for range 100 {
				ids = removeRandom(s.rng, ids, 10)
				s.reconcile(ingress(), podEndpoints(ids), false)
				for range 10 {
					ids = append(ids, next)
					next++
				}
				s.reconcile(ingress(), podEndpoints(ids), true)
			}
		}
	}
	simulate(t, "100 rounds (200 reconciles), 900 pods: remove 10, then add 10 in a separate reconcile",
		splitChurn(func() []policyinfo.EndpointInfo { return peers(10, p80) }))
	simulate(t, "50 rounds (100 reconciles), 900 pods: scale up by 20, then down by 20", func(s *simState) {
		ids, next := seq(900), 900
		s.reconcile(peers(10, p80), podEndpoints(ids), false)
		s.counting = true
		for range 50 {
			for range 20 {
				ids = append(ids, next)
				next++
			}
			s.reconcile(peers(10, p80), podEndpoints(ids), true)
			ids = removeRandom(s.rng, ids, 20)
			s.reconcile(peers(10, p80), podEndpoints(ids), false)
		}
	})
	simulate(t, "100 reconciles, 1000 pods (5 full PEs), 1 replaced per reconcile; control", func(s *simState) {
		ids, next := seq(1000), 1000
		s.reconcile(peers(10, p80), podEndpoints(ids), false)
		s.counting = true
		for range 100 {
			replaceOne(s.rng, ids, &next)
			s.reconcile(peers(10, p80), podEndpoints(ids), true)
		}
	})
	for _, n := range []int{500, 450} {
		for _, window := range [][2]int{{0, 50}, {50, 300}} {
			name := fmt.Sprintf("no-op reconciles %d-%d after creation, 1000 pods, %d peers each in two rules", window[0]+1, window[1], n)
			simulate(t, name, func(s *simState) {
				pods := podEndpoints(seq(1000))
				s.reconcile(twoRules(n), pods, false)
				for r := range window[1] {
					s.counting = r >= window[0]
					s.reconcile(twoRules(n), pods, false)
				}
			})
		}
	}
	simulate(t, "100 rounds (200 reconciles), 900 pods that are also the peers of two rules: remove 10, then add 10 separately", func(s *simState) {
		ids, next := seq(900), 900
		selfPeers := func() []policyinfo.EndpointInfo {
			var out []policyinfo.EndpointInfo
			for _, pod := range podEndpoints(ids) {
				out = append(out, policyinfo.EndpointInfo{CIDR: pod.PodIP, Ports: p80},
					policyinfo.EndpointInfo{CIDR: pod.PodIP, Ports: []policyinfo.Port{tcpPort(443)}})
			}
			return out
		}
		s.reconcile(selfPeers(), podEndpoints(ids), false)
		s.counting = true
		for range 100 {
			ids = removeRandom(s.rng, ids, 10)
			s.reconcile(selfPeers(), podEndpoints(ids), false)
			for range 10 {
				ids = append(ids, next)
				next++
			}
			s.reconcile(selfPeers(), podEndpoints(ids), true)
		}
	})
}

type simState struct {
	t        *testing.T
	m        *policyEndpointsManager
	store    *writeCounter[policyinfo.PolicyEndpoint]
	rng      *rand.Rand
	counting bool
	writes   int
	bound    int
}

// reconcile runs one reconcile and, while counting, adds its writes and its lower bound: every
// object holding an endpoint that went away, plus one write if there are additions and no such
// object.
func (s *simState) reconcile(ingress []policyinfo.EndpointInfo, pods []policyinfo.PodEndpoint, adds bool) {
	if s.counting {
		n := mustWrite(s.store.objs, pods, ingress)
		if adds && n == 0 {
			n = 1
		}
		s.bound += n
	}
	existing := make([]policyinfo.PolicyEndpoint, 0, len(s.store.objs))
	for _, pe := range s.store.objs {
		existing = append(existing, *pe.DeepCopy())
	}
	c, u, d, err := s.m.computePolicyEndpoints(writeTestNP, existing, []resolvers.IngressGroup{{Ingress: ingress, PodEndpoints: pods}}, nil, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	if w := s.store.apply(s.t, c, u, d); s.counting {
		s.writes += w
	}
}

func simulate(t *testing.T, name string, scenario func(*simState)) {
	var writes, bounds []int
	for seed := range int64(10) {
		s := &simState{t: t, m: &policyEndpointsManager{endpointChunkSize: 200, logger: logr.Discard()},
			store: newPECounter(seed), rng: rand.New(rand.NewSource(seed + 100))}
		scenario(s)
		writes, bounds = append(writes, s.writes), append(bounds, s.bound)
	}
	t.Logf("%s: writes %s, lower bound %s", name, spread(writes), spread(bounds))
}

func spread(xs []int) string {
	slices.Sort(xs)
	if xs[0] == xs[len(xs)-1] {
		return fmt.Sprint(xs[0])
	}
	return fmt.Sprintf("%d–%d (median %d)", xs[0], xs[len(xs)-1], xs[len(xs)/2])
}

// dedupOrder returns ports in the order main's dedupPorts gives them: the values of a fresh map.
func dedupOrder(ports ...int32) []policyinfo.Port {
	m := map[int32]policyinfo.Port{}
	for _, p := range ports {
		m[p] = tcpPort(p)
	}
	return maps.Values(m)
}
