package resolvers

import (
	"context"
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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
)

// referenceGetReferredPoliciesForPod is the pre-index algorithm, kept verbatim as the oracle: for every tracked
// policy, read it and match every peer from the object, re-reading the namespace per peer.
func referenceGetReferredPoliciesForPod(ctx context.Context, r *defaultPolicyReferenceResolver, pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
	policyList := &networking.NetworkPolicyList{}
	if err := r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	processed := sets.Set[types.NamespacedName]{}
	var out []networking.NetworkPolicy
	for _, pol := range policyList.Items {
		if r.isPodMatchesPolicySelector(pod, podOld, &pol) || r.isPodReferredOnIngressEgress(ctx, pod, &pol) ||
			(podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, &pol)) {
			out = append(out, pol)
			processed.Insert(k8s.NamespacedName(&pol))
		}
	}
	for _, ref := range r.policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList() {
		if processed.Has(ref) {
			continue
		}
		policy := &networking.NetworkPolicy{}
		if err := r.k8sClient.Get(ctx, ref, policy, client.UnsafeDisableDeepCopy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			continue
		}
		if r.isPodReferredOnIngressEgress(ctx, pod, policy) || (podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, policy)) {
			out = append(out, *policy)
			processed.Insert(k8s.NamespacedName(policy))
		}
	}
	return out, nil
}

func names(ps []networking.NetworkPolicy) []string {
	out := make([]string, 0, len(ps))
	for i := range ps {
		out = append(out, k8s.NamespacedName(&ps[i]).String())
	}
	sort.Strings(out)
	return out
}

func newFakeResolver(t *testing.T, objs ...client.Object) (*defaultPolicyReferenceResolver, *defaultPolicyTracker) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	nullLogger := logr.New(&log.NullLogSink{})
	tracker := &defaultPolicyTracker{logger: nullLogger}
	return &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: nullLogger}, tracker
}

// randomPolicySet builds policies in several namespaces with a mix of peer shapes: namespaceSelector only,
// namespaceSelector + podSelector, podSelector only (same namespace), ipBlock only, empty selectors, matchExpressions,
// and one deliberately invalid selector.
func randomPolicySet(rng *rand.Rand, nsList []string, n int) []*networking.NetworkPolicy {
	keys := []string{"team", "tier", "env"}
	vals := []string{"a", "b", "c"}
	randSel := func() *metav1.LabelSelector {
		switch rng.Intn(5) {
		case 0:
			return &metav1.LabelSelector{} // everything
		case 1:
			return &metav1.LabelSelector{MatchLabels: map[string]string{keys[rng.Intn(3)]: vals[rng.Intn(3)]}}
		case 2:
			return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: keys[rng.Intn(3)], Operator: metav1.LabelSelectorOpIn, Values: []string{vals[rng.Intn(3)], vals[rng.Intn(3)]}}}}
		case 3:
			return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: keys[rng.Intn(3)], Operator: metav1.LabelSelectorOpDoesNotExist}}}
		default:
			return &metav1.LabelSelector{MatchLabels: map[string]string{keys[rng.Intn(3)]: vals[rng.Intn(3)], keys[rng.Intn(3)]: vals[rng.Intn(3)]}}
		}
	}
	randPeer := func() networking.NetworkPolicyPeer {
		switch rng.Intn(6) {
		case 0:
			return networking.NetworkPolicyPeer{NamespaceSelector: randSel()}
		case 1, 2:
			return networking.NetworkPolicyPeer{NamespaceSelector: randSel(), PodSelector: randSel()}
		case 3:
			return networking.NetworkPolicyPeer{PodSelector: randSel()}
		case 4:
			return networking.NetworkPolicyPeer{IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"}}
		default:
			// invalid: In with no values
			return networking.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "team", Operator: metav1.LabelSelectorOpIn}}}, PodSelector: randSel()}
		}
	}
	var out []*networking.NetworkPolicy
	for i := 0; i < n; i++ {
		p := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i), Namespace: nsList[rng.Intn(len(nsList))]},
			Spec: networking.NetworkPolicySpec{PodSelector: *randSel()}}
		for k := 0; k < 1+rng.Intn(3); k++ {
			rule := networking.NetworkPolicyIngressRule{Ports: []networking.NetworkPolicyPort{{Port: &[]intstr.IntOrString{intstr.FromInt(8080)}[0]}}}
			for j := 0; j < 1+rng.Intn(3); j++ {
				rule.From = append(rule.From, randPeer())
			}
			p.Spec.Ingress = append(p.Spec.Ingress, rule)
		}
		if rng.Intn(2) == 0 {
			rule := networking.NetworkPolicyEgressRule{}
			for j := 0; j < 1+rng.Intn(2); j++ {
				rule.To = append(rule.To, randPeer())
			}
			p.Spec.Egress = append(p.Spec.Egress, rule)
		}
		out = append(out, p)
	}
	return out
}

func randomLabels(rng *rand.Rand) map[string]string {
	keys := []string{"team", "tier", "env"}
	vals := []string{"a", "b", "c"}
	m := map[string]string{}
	for _, k := range keys {
		if rng.Intn(2) == 0 {
			m[k] = vals[rng.Intn(3)]
		}
	}
	return m
}

// TestGetReferredPoliciesForPod_IndexMatchesReference: the indexed path returns exactly what the per-event scan
// returned, over random policies, namespaces, pods and old-pod label changes, including a namespace the cache
// does not have.
func TestGetReferredPoliciesForPod_IndexMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261113))
	for round := 0; round < 40; round++ {
		nsList := []string{"ns-a", "ns-b", "ns-c", "ns-d"}
		var objs []client.Object
		for _, n := range nsList {
			objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n, Labels: randomLabels(rng)}})
		}
		policies := randomPolicySet(rng, nsList, 30)
		for _, p := range policies {
			objs = append(objs, p)
		}
		r, tracker := newFakeResolver(t, objs...)
		for _, p := range policies {
			tracker.UpdatePolicy(p)
		}
		// pods in known namespaces and one in a namespace that is not in the cache
		podNS := append([]string{"ns-missing"}, nsList...)
		for i := 0; i < 30; i++ {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod%d", i), Namespace: podNS[rng.Intn(len(podNS))], Labels: randomLabels(rng)},
				Status: corev1.PodStatus{PodIP: "10.1.1.1"}}
			var podOld *corev1.Pod
			if rng.Intn(2) == 0 {
				podOld = pod.DeepCopy()
				podOld.Labels = randomLabels(rng)
			}
			want, err := referenceGetReferredPoliciesForPod(context.Background(), r, pod, podOld)
			require.NoError(t, err)
			got, err := r.GetReferredPoliciesForPod(context.Background(), pod, podOld)
			require.NoError(t, err)
			assert.Equal(t, names(want), names(got), "round %d pod %s/%s labels=%v old=%v", round, pod.Namespace, pod.Name, pod.Labels, podOld != nil)
		}
	}
}

// TestGetReferredPoliciesForPod_IndexFollowsPolicyChanges: the index tracks spec updates (peers added, removed,
// namespaceSelector dropped) and deletions, so a stale entry can neither hide a new peer nor keep a removed one.
func TestGetReferredPoliciesForPod_IndexFollowsPolicyChanges(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "clients", Labels: map[string]string{"team": "a"}}}
	target := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "kh-ingress", Namespace: "servers"},
		Spec: networking.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "kh"}},
			Ingress: []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "job-client"}}}}}}}}
	r, tracker := newFakeResolver(t, ns, target)
	tracker.UpdatePolicy(target)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: "clients", Labels: map[string]string{"app": "job-client"}}, Status: corev1.PodStatus{PodIP: "10.0.0.1"}}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "o1", Namespace: "clients", Labels: map[string]string{"app": "other"}}, Status: corev1.PodStatus{PodIP: "10.0.0.2"}}

	got, err := r.GetReferredPoliciesForPod(context.Background(), pod, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/kh-ingress"}, names(got), "peer pod is matched through the index")
	got, err = r.GetReferredPoliciesForPod(context.Background(), other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "non-peer pod is not matched")

	// old labels matched, new do not: the policy must still be enqueued so the pod is removed from it
	got, err = r.GetReferredPoliciesForPod(context.Background(), other, pod)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/kh-ingress"}, names(got), "podOld match is honoured")

	// widen the peer: drop the podSelector -> every pod in team=a namespaces matches
	updated := target.DeepCopy()
	updated.Spec.Ingress[0].From[0].PodSelector = nil
	require.NoError(t, r.k8sClient.Update(context.Background(), updated))
	tracker.UpdatePolicy(updated)
	got, err = r.GetReferredPoliciesForPod(context.Background(), other, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/kh-ingress"}, names(got), "index follows a widened peer")

	// drop the namespaceSelector: the policy leaves the cross-namespace index entirely
	updated2 := updated.DeepCopy()
	updated2.Spec.Ingress[0].From[0] = networking.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	require.NoError(t, r.k8sClient.Update(context.Background(), updated2))
	tracker.UpdatePolicy(updated2)
	_, indexed := tracker.GetNamespaceSelectorPeers(k8s.NamespacedName(updated2))
	assert.False(t, indexed)
	assert.False(t, tracker.GetPoliciesWithNamespaceReferences().Has(k8s.NamespacedName(updated2)))
	got, err = r.GetReferredPoliciesForPod(context.Background(), other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "a same-namespace-only policy in another namespace does not match")

	// delete: index entry gone, and a stale tracker ref (policy deleted from the cache first) is skipped quietly
	tracker.UpdatePolicy(updated)
	require.NoError(t, r.k8sClient.Delete(context.Background(), updated))
	got, err = r.GetReferredPoliciesForPod(context.Background(), other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "deleted policy still in the tracker is skipped, not an error")
	tracker.RemovePolicy(updated)
	_, indexed = tracker.GetNamespaceSelectorPeers(k8s.NamespacedName(updated))
	assert.False(t, indexed)
}

// TestGetReferredPoliciesForPod_FallbackWithoutIndex: a policy tracked by reference only (no UpdatePolicy call, as
// older code paths and the existing tests do) is still matched the slow way.
func TestGetReferredPoliciesForPod_FallbackWithoutIndex(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "clients", Labels: map[string]string{"team": "a"}}}
	target := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "servers"},
		Spec: networking.NetworkPolicySpec{Ingress: []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}}}}}}}
	r, tracker := newFakeResolver(t, ns, target)
	tracker.namespacedPolicies.Store(k8s.NamespacedName(target), true) // ref only, no peers indexed
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "clients"}, Status: corev1.PodStatus{PodIP: "10.0.0.1"}}
	got, err := r.GetReferredPoliciesForPod(context.Background(), pod, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/p"}, names(got))
}

// TestPolicyTracker_ParseNamespaceSelectorPeers: only namespaceSelector peers are indexed, a nil podSelector stays
// nil, and an unparseable selector drops just that peer.
func TestPolicyTracker_ParseNamespaceSelectorPeers(t *testing.T) {
	tracker := &defaultPolicyTracker{logger: logr.New(&log.NullLogSink{})}
	p := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "n"}, Spec: networking.NetworkPolicySpec{
		Ingress: []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{
			{PodSelector: &metav1.LabelSelector{}},            // same-namespace only: not indexed
			{IPBlock: &networking.IPBlock{CIDR: "0.0.0.0/0"}}, // not indexed
			{NamespaceSelector: &metav1.LabelSelector{}},      // indexed, Pod nil
			{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"c": "d"}}},
			{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k", Operator: metav1.LabelSelectorOpIn}}}}, // invalid: dropped
		}}},
		Egress: []networking.NetworkPolicyEgressRule{{To: []networking.NetworkPolicyPeer{
			{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"e": "f"}}, PodSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k", Operator: metav1.LabelSelectorOpIn}}}}, // invalid pod selector: dropped
		}}}}}
	tracker.UpdatePolicy(p)
	peers, ok := tracker.GetNamespaceSelectorPeers(k8s.NamespacedName(p))
	require.True(t, ok)
	require.Len(t, peers, 2)
	assert.Nil(t, peers[0].Pod)
	assert.True(t, peers[0].Namespace.Empty(), "empty namespaceSelector matches everything")
	assert.NotNil(t, peers[1].Pod)
	assert.True(t, peers[1].Namespace.Matches(labels.Set{"a": "b"}))
	assert.False(t, peers[1].Namespace.Matches(labels.Set{"a": "x"}))
}

// BenchmarkGetReferredPoliciesForPod_CustomerShape: 5,000 cross-namespace policies with 10 namespaceSelector peers
// each, one pod event. Compares the indexed path with the per-event scan it replaces.
func BenchmarkGetReferredPoliciesForPod_CustomerShape(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	nsList := make([]string, 0, 500)
	var objs []client.Object
	for i := 0; i < 500; i++ {
		n := fmt.Sprintf("ns-%d", i)
		nsList = append(nsList, n)
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n, Labels: map[string]string{"team": fmt.Sprintf("t%d", i%20)}}})
	}
	var policies []*networking.NetworkPolicy
	for i := 0; i < 5000; i++ {
		p := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i), Namespace: nsList[rng.Intn(500)]},
			Spec: networking.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": fmt.Sprintf("a%d", i)}}}}
		rule := networking.NetworkPolicyIngressRule{}
		for j := 0; j < 10; j++ {
			rule.From = append(rule.From, networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": fmt.Sprintf("t%d", rng.Intn(20))}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": fmt.Sprintf("a%d", rng.Intn(5000))}}})
		}
		p.Spec.Ingress = []networking.NetworkPolicyIngressRule{rule}
		policies = append(policies, p)
		objs = append(objs, p)
	}
	scheme := runtime.NewScheme()
	require.NoError(b, clientgoscheme.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	nullLogger := logr.New(&log.NullLogSink{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns-7", Labels: map[string]string{"app": "a42"}}, Status: corev1.PodStatus{PodIP: "10.0.0.1"}}

	b.Run("reference-scan", func(b *testing.B) {
		tracker := &defaultPolicyTracker{logger: nullLogger}
		for _, p := range policies {
			tracker.namespacedPolicies.Store(k8s.NamespacedName(p), true)
		}
		r := &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: nullLogger}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := referenceGetReferredPoliciesForPod(context.Background(), r, pod, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("indexed", func(b *testing.B) {
		tracker := &defaultPolicyTracker{logger: nullLogger}
		for _, p := range policies {
			tracker.UpdatePolicy(p)
		}
		r := &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: nullLogger}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := r.GetReferredPoliciesForPod(context.Background(), pod, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}
