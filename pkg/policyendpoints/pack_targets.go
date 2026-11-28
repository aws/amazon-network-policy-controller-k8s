package policyendpoints

import (
	"sort"

	"k8s.io/apimachinery/pkg/util/sets"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// packTargets holds the objects one reconcile can place endpoints into, and which of the existing
// ones it already has to write. T is PolicyEndpoint or ClusterPolicyEndpoint.
type packTargets[T any] struct {
	// modified are existing objects that kept at least one endpoint.
	modified []T
	// potentialDeletes are existing objects left with no endpoints. Each is deleted unless refilled.
	potentialDeletes []T
	// created are the objects this reconcile creates.
	created []T
	// written names the existing objects this reconcile writes so far. It only orders the targets;
	// whether an emptied object is kept depends on what it holds, see result.
	written sets.Set[string]
	name    func(*T) string
	empty   func(*T) bool
}

func newPackTargets[T any](name func(*T) string, empty func(*T) bool) *packTargets[T] {
	return &packTargets[T]{written: sets.New[string](), name: name, empty: empty}
}

func newPolicyEndpointTargets() *packTargets[policyinfo.PolicyEndpoint] {
	return newPackTargets(
		func(pe *policyinfo.PolicyEndpoint) string { return pe.Name },
		func(pe *policyinfo.PolicyEndpoint) bool {
			return len(pe.Spec.Ingress) == 0 && len(pe.Spec.Egress) == 0 && len(pe.Spec.PodSelectorEndpoints) == 0
		})
}

func newClusterPolicyEndpointTargets() *packTargets[policyinfo.ClusterPolicyEndpoint] {
	return newPackTargets(
		func(cpe *policyinfo.ClusterPolicyEndpoint) string { return cpe.Name },
		func(cpe *policyinfo.ClusterPolicyEndpoint) bool {
			return len(cpe.Spec.Ingress) == 0 && len(cpe.Spec.Egress) == 0 && len(cpe.Spec.PodSelectorEndpoints) == 0
		})
}

// markWritten records that obj is written by this reconcile. An object being created has no name
// yet and is written anyway.
func (t *packTargets[T]) markWritten(obj *T) {
	if n := t.name(obj); n != "" {
		t.written.Insert(n)
	}
}

// fillOrder lists the targets cheapest first. An object costs one API call however many of its
// endpoints change, so an endpoint is free in an object already being written, in an emptied one
// (written either way, as an update or a delete) and in one being created. Anywhere else it costs
// a write, so untouched objects go most room first, by held, the entries an object already has in
// the field being filled: a batch then lands in as few of them as possible, and room does not pile
// up in one object while the others stay full. Ties, and the objects being written, go by name, so
// the order does not depend on list order.
func (t *packTargets[T]) fillOrder(held func(*T) int) [][]T {
	sort.Slice(t.modified, func(i, j int) bool {
		ni, nj := t.name(&t.modified[i]), t.name(&t.modified[j])
		wi, wj := t.written.Has(ni), t.written.Has(nj)
		if wi != wj {
			return wi
		}
		if !wi {
			if hi, hj := held(&t.modified[i]), held(&t.modified[j]); hi != hj {
				return hi < hj
			}
		}
		return ni < nj
	})
	n := 0
	for n < len(t.modified) && t.written.Has(t.name(&t.modified[n])) {
		n++
	}
	return [][]T{t.modified[:n], t.potentialDeletes, t.created, t.modified[n:]}
}

// result returns the objects to create, update and delete once packing is done. Every modified
// object is an update candidate; the write step skips the ones whose spec did not change. An
// emptied object is kept if packing put anything into it.
func (t *packTargets[T]) result() (create, update, del []T) {
	update = append(update, t.modified...)
	for i := range t.potentialDeletes {
		if !t.empty(&t.potentialDeletes[i]) {
			update = append(update, t.potentialDeletes[i])
		} else {
			del = append(del, t.potentialDeletes[i])
		}
	}
	return t.created, update, del
}

// addExisting files an existing object after the purge step. changed reports whether purging
// altered it, so it has to be written whatever else this reconcile does.
func (t *packTargets[T]) addExisting(obj T, empty, changed bool) {
	switch {
	case empty:
		t.potentialDeletes = append(t.potentialDeletes, obj)
	case changed:
		t.modified = append(t.modified, obj)
		t.markWritten(&obj)
	default:
		t.modified = append(t.modified, obj)
	}
}

// sortPotentialDeletes puts the emptied objects in name order, so which one is refilled does not
// depend on list order.
func (t *packTargets[T]) sortPotentialDeletes() {
	sort.Slice(t.potentialDeletes, func(i, j int) bool {
		return t.name(&t.potentialDeletes[i]) < t.name(&t.potentialDeletes[j])
	})
}
