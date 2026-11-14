package resolvers

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
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

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
)

// The tests in this file cover the namespaceSelector peer index used by the pod event path. Every expectation is
// checked against both the indexed resolver and referenceGetReferredPoliciesForPod / referenceGetReferredANPsForPod,
// the per-event scan the index replaces, so a case documents behaviour that was already there rather than behaviour
// the index introduced.

// referenceGetReferredPoliciesForPod is the NetworkPolicy pod path as it was before the index: every policy tracked
// with namespace references is read from the cache and every peer is matched from the object, reading the pod's
// namespace once per peer.
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

// referenceGetReferredANPsForPod is referenceGetReferredPoliciesForPod for ApplicationNetworkPolicies.
func referenceGetReferredANPsForPod(ctx context.Context, r *defaultPolicyReferenceResolver, pod, podOld *corev1.Pod) ([]policyinfo.ApplicationNetworkPolicy, error) {
	policyList := &policyinfo.ApplicationNetworkPolicyList{}
	if err := r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	processed := sets.Set[types.NamespacedName]{}
	var out []policyinfo.ApplicationNetworkPolicy
	for _, pol := range policyList.Items {
		if r.isPodMatchesANPPolicySelector(pod, podOld, &pol) || r.isPodReferredOnANPIngressEgress(ctx, pod, &pol) ||
			(podOld != nil && r.isPodReferredOnANPIngressEgress(ctx, podOld, &pol)) {
			out = append(out, pol)
			processed.Insert(k8s.NamespacedName(&pol))
		}
	}
	for _, ref := range r.policyTracker.GetApplicationNetworkPoliciesWithNamespaceReferences().UnsortedList() {
		if processed.Has(ref) {
			continue
		}
		policy := &policyinfo.ApplicationNetworkPolicy{}
		if err := r.k8sClient.Get(ctx, ref, policy, client.UnsafeDisableDeepCopy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			continue
		}
		if r.isPodReferredOnANPIngressEgress(ctx, pod, policy) || (podOld != nil && r.isPodReferredOnANPIngressEgress(ctx, podOld, policy)) {
			out = append(out, *policy)
			processed.Insert(k8s.NamespacedName(policy))
		}
	}
	return out, nil
}

func policyNames(ps []networking.NetworkPolicy) []string {
	out := make([]string, 0, len(ps))
	for i := range ps {
		out = append(out, k8s.NamespacedName(&ps[i]).String())
	}
	sort.Strings(out)
	return out
}

func anpNames(ps []policyinfo.ApplicationNetworkPolicy) []string {
	out := make([]string, 0, len(ps))
	for i := range ps {
		out = append(out, k8s.NamespacedName(&ps[i]).String())
	}
	sort.Strings(out)
	return out
}

func newFakeResolver(t testing.TB, objs ...client.Object) (*defaultPolicyReferenceResolver, *defaultPolicyTracker) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, policyinfo.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	nullLogger := logr.New(&log.NullLogSink{})
	tracker := &defaultPolicyTracker{logger: nullLogger}
	return &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: nullLogger}, tracker
}

func matchLabels(kv ...string) *metav1.LabelSelector {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return &metav1.LabelSelector{MatchLabels: m}
}

func matchExpression(key string, op metav1.LabelSelectorOperator, values ...string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: key, Operator: op, Values: values}}}
}

func newTestNamespace(name string, l map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l}}
}

func newTestPod(namespace, name string, l map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: l},
		Status: corev1.PodStatus{PodIP: "10.0.0.1"}}
}

// newTestNetworkPolicy builds a policy that selects app=server pods in its namespace and has one ingress rule per
// entry of ingress and one egress rule per entry of egress.
func newTestNetworkPolicy(namespace, name string, ingress, egress [][]networking.NetworkPolicyPeer) *networking.NetworkPolicy {
	p := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networking.NetworkPolicySpec{PodSelector: *matchLabels("app", "server")}}
	for _, from := range ingress {
		p.Spec.Ingress = append(p.Spec.Ingress, networking.NetworkPolicyIngressRule{From: from})
	}
	for _, to := range egress {
		p.Spec.Egress = append(p.Spec.Egress, networking.NetworkPolicyEgressRule{To: to})
	}
	return p
}

func newTestANP(namespace, name string, ingress [][]networking.NetworkPolicyPeer, egress [][]policyinfo.ApplicationNetworkPolicyPeer) *policyinfo.ApplicationNetworkPolicy {
	p := &policyinfo.ApplicationNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: policyinfo.ApplicationNetworkPolicySpec{PodSelector: *matchLabels("app", "server")}}
	for _, from := range ingress {
		p.Spec.Ingress = append(p.Spec.Ingress, networking.NetworkPolicyIngressRule{From: from})
	}
	for _, to := range egress {
		p.Spec.Egress = append(p.Spec.Egress, policyinfo.ApplicationNetworkPolicyEgressRule{To: to})
	}
	return p
}

func ruleWith(p ...networking.NetworkPolicyPeer) [][]networking.NetworkPolicyPeer {
	return [][]networking.NetworkPolicyPeer{p}
}

func TestPolicyReferenceResolver_GetReferredPoliciesForPod_namespaceSelectorPeers(t *testing.T) {
	teamA := newTestNamespace("clients", map[string]string{"team": "a", "env": "prod"})
	bare := newTestNamespace("bare", nil)
	web := map[string]string{"app": "web"}
	invalid := matchExpression("team", metav1.LabelSelectorOpIn) // In without values does not parse

	tests := []struct {
		name       string
		namespaces []*corev1.Namespace
		policies   []*networking.NetworkPolicy
		pod        *corev1.Pod
		podOld     *corev1.Pod
		want       []string
	}{
		{
			name:     "namespaceSelector and podSelector both match",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "web")}), nil)},
			pod:      newTestPod("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "namespaceSelector matches, podSelector does not",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "db")}), nil)},
			pod:      newTestPod("clients", "c", web),
		},
		{
			name:     "podSelector matches, namespaceSelector does not",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "b"), PodSelector: matchLabels("app", "web")}), nil)},
			pod:      newTestPod("clients", "c", web),
		},
		{
			name:     "namespaceSelector without podSelector selects every pod in the namespace",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)},
			pod:      newTestPod("clients", "c", nil),
			want:     []string{"servers/p"},
		},
		{
			name:     "empty podSelector selects every pod in the namespace",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: &metav1.LabelSelector{}}), nil)},
			pod:      newTestPod("clients", "c", nil),
			want:     []string{"servers/p"},
		},
		{
			name:     "empty namespaceSelector selects a namespace with no labels",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}), nil)},
			pod:      newTestPod("bare", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "namespaceSelector with labels does not select a namespace with no labels",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)},
			pod:      newTestPod("bare", "c", web),
		},
		{
			name:     "pod in a namespace missing from the cache matches no namespaceSelector peer",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}), nil)},
			pod:      newTestPod("missing", "c", web),
		},
		{
			name:     "matchExpressions In",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchExpression("team", metav1.LabelSelectorOpIn, "a", "b")}), nil)},
			pod:      newTestPod("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "matchExpressions NotIn excludes the namespace",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchExpression("team", metav1.LabelSelectorOpNotIn, "a")}), nil)},
			pod:      newTestPod("clients", "c", web),
		},
		{
			name:     "matchExpressions Exists",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchExpression("env", metav1.LabelSelectorOpExists)}), nil)},
			pod:      newTestPod("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "matchExpressions DoesNotExist on the pod selector",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchExpression("tier", metav1.LabelSelectorOpDoesNotExist)}), nil)},
			pod:      newTestPod("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "invalid namespaceSelector never matches",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: invalid}), nil)},
			pod:      newTestPod("clients", "c", web),
		},
		{
			name:     "invalid podSelector never matches",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: invalid}), nil)},
			pod:      newTestPod("clients", "c", web),
		},
		{
			name: "invalid peer does not hide a valid peer in the same rule",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(
				networking.NetworkPolicyPeer{NamespaceSelector: invalid},
				networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)},
			pod:  newTestPod("clients", "c", web),
			want: []string{"servers/p"},
		},
		{
			name:     "egress peer",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", nil, ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "web")}))},
			pod:      newTestPod("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name: "match on a later rule and a later peer",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", [][]networking.NetworkPolicyPeer{
				{{NamespaceSelector: matchLabels("team", "b")}, {IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"}}},
				{{PodSelector: matchLabels("app", "web")}, {NamespaceSelector: matchLabels("env", "prod"), PodSelector: matchLabels("app", "web")}},
			}, nil)},
			pod:  newTestPod("clients", "c", web),
			want: []string{"servers/p"},
		},
		{
			name:     "only the old labels match, so the pod must be removed from the policy",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "web")}), nil)},
			pod:      newTestPod("clients", "c", map[string]string{"app": "other"}),
			podOld:   newTestPod("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "neither the old nor the new labels match",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "web")}), nil)},
			pod:      newTestPod("clients", "c", map[string]string{"app": "other"}),
			podOld:   newTestPod("clients", "c", map[string]string{"app": "older"}),
		},
		{
			name:     "podSelector-only peer of a policy in another namespace does not match",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "b")}, networking.NetworkPolicyPeer{PodSelector: matchLabels("app", "web")}), nil)},
			pod:      newTestPod("clients", "c", web),
		},
		{
			name:     "policy in the pod's own namespace is returned once",
			policies: []*networking.NetworkPolicy{newTestNetworkPolicy("clients", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)},
			pod:      newTestPod("clients", "c", web),
			want:     []string{"clients/p"},
		},
		{
			name: "only the matching policies are returned",
			policies: []*networking.NetworkPolicy{
				newTestNetworkPolicy("servers", "match-ingress", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil),
				newTestNetworkPolicy("servers", "other-team", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "b")}), nil),
				newTestNetworkPolicy("db", "match-egress", nil, ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: matchLabels("app", "web")})),
				newTestNetworkPolicy("db", "other-app", nil, ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: matchLabels("app", "api")})),
			},
			pod:  newTestPod("clients", "c", web),
			want: []string{"db/match-egress", "servers/match-ingress"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{teamA, bare}
			for _, ns := range tt.namespaces {
				objs = append(objs, ns)
			}
			for _, p := range tt.policies {
				objs = append(objs, p)
			}
			r, tracker := newFakeResolver(t, objs...)
			for _, p := range tt.policies {
				tracker.UpdatePolicy(p)
			}
			want := tt.want
			if want == nil {
				want = []string{}
			}

			before, err := referenceGetReferredPoliciesForPod(context.Background(), r, tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, policyNames(before), "per-event scan")

			got, err := r.GetReferredPoliciesForPod(context.Background(), tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, policyNames(got), "indexed")
		})
	}
}

func TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_namespaceSelectorPeers(t *testing.T) {
	teamA := newTestNamespace("clients", map[string]string{"team": "a"})
	bare := newTestNamespace("bare", nil)
	web := map[string]string{"app": "web"}

	tests := []struct {
		name     string
		policies []*policyinfo.ApplicationNetworkPolicy
		pod      *corev1.Pod
		podOld   *corev1.Pod
		want     []string
	}{
		{
			name:     "ingress peer",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "web")}), nil)},
			pod:      newTestPod("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name: "egress peer",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", nil, [][]policyinfo.ApplicationNetworkPolicyPeer{{
				{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "web")}}})},
			pod:  newTestPod("clients", "c", web),
			want: []string{"servers/p"},
		},
		{
			name: "egress peer with a podSelector that does not match",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", nil, [][]policyinfo.ApplicationNetworkPolicyPeer{{
				{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "db")}}})},
			pod: newTestPod("clients", "c", web),
		},
		{
			name: "egress namespaceSelector without podSelector selects every pod in the namespace",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", nil, [][]policyinfo.ApplicationNetworkPolicyPeer{{
				{NamespaceSelector: matchLabels("team", "a")}}})},
			pod:  newTestPod("clients", "c", nil),
			want: []string{"servers/p"},
		},
		{
			name: "empty namespaceSelector selects a namespace with no labels",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", nil, [][]policyinfo.ApplicationNetworkPolicyPeer{{
				{NamespaceSelector: &metav1.LabelSelector{}}}})},
			pod:  newTestPod("bare", "c", web),
			want: []string{"servers/p"},
		},
		{
			name:     "pod in a namespace missing from the cache matches no namespaceSelector peer",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}), nil)},
			pod:      newTestPod("missing", "c", web),
		},
		{
			name: "domainNames peer is not a namespace reference",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", nil, [][]policyinfo.ApplicationNetworkPolicyPeer{{
				{DomainNames: []policyinfo.DomainName{"example.com"}}}})},
			pod: newTestPod("clients", "c", web),
		},
		{
			name: "only the old labels match",
			policies: []*policyinfo.ApplicationNetworkPolicy{newTestANP("servers", "p", nil, [][]policyinfo.ApplicationNetworkPolicyPeer{{
				{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "web")}}})},
			pod:    newTestPod("clients", "c", map[string]string{"app": "other"}),
			podOld: newTestPod("clients", "c", web),
			want:   []string{"servers/p"},
		},
		{
			name: "only the matching policies are returned",
			policies: []*policyinfo.ApplicationNetworkPolicy{
				newTestANP("servers", "match", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil),
				newTestANP("servers", "other-team", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "b")}), nil),
			},
			pod:  newTestPod("clients", "c", web),
			want: []string{"servers/match"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{teamA, bare}
			for _, p := range tt.policies {
				objs = append(objs, p)
			}
			r, tracker := newFakeResolver(t, objs...)
			for _, p := range tt.policies {
				tracker.UpdateGenericPolicy(p)
			}
			want := tt.want
			if want == nil {
				want = []string{}
			}

			before, err := referenceGetReferredANPsForPod(context.Background(), r, tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, anpNames(before), "per-event scan")

			got, err := r.GetReferredApplicationNetworkPoliciesForPod(context.Background(), tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, anpNames(got), "indexed")
		})
	}
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_matchesPerEventScan compares the indexed path with the
// per-event scan over random policy sets: mixed peer shapes (namespaceSelector only, with a podSelector, podSelector
// only, ipBlock, empty and invalid selectors, matchExpressions), unlabelled namespaces, a namespace the cache does not
// have, and label changes between the old and new pod.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_matchesPerEventScan(t *testing.T) {
	rng := rand.New(rand.NewSource(20261113))
	for round := 0; round < 40; round++ {
		r, tracker, nsList := randomResolver(t, rng)
		policies := randomPolicySet(rng, nsList, 30)
		for _, p := range policies {
			require.NoError(t, r.k8sClient.Create(context.Background(), p))
			tracker.UpdatePolicy(p)
		}
		for i, pod := range randomPods(rng, nsList, 30) {
			want, err := referenceGetReferredPoliciesForPod(context.Background(), r, pod.pod, pod.old)
			require.NoError(t, err)
			got, err := r.GetReferredPoliciesForPod(context.Background(), pod.pod, pod.old)
			require.NoError(t, err)
			assert.Equal(t, policyNames(want), policyNames(got), "round %d pod %d %s labels=%v old=%v", round, i,
				k8s.NamespacedName(pod.pod), pod.pod.Labels, pod.old != nil)
		}
	}
}

// TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_matchesPerEventScan is the ANP version,
// where egress peers are ApplicationNetworkPolicyPeers and may carry domainNames.
func TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_matchesPerEventScan(t *testing.T) {
	rng := rand.New(rand.NewSource(20261114))
	for round := 0; round < 40; round++ {
		r, tracker, nsList := randomResolver(t, rng)
		for _, np := range randomPolicySet(rng, nsList, 30) {
			anp := toANP(rng, np)
			require.NoError(t, r.k8sClient.Create(context.Background(), anp))
			tracker.UpdateGenericPolicy(anp)
		}
		for i, pod := range randomPods(rng, nsList, 30) {
			want, err := referenceGetReferredANPsForPod(context.Background(), r, pod.pod, pod.old)
			require.NoError(t, err)
			got, err := r.GetReferredApplicationNetworkPoliciesForPod(context.Background(), pod.pod, pod.old)
			require.NoError(t, err)
			assert.Equal(t, anpNames(want), anpNames(got), "round %d pod %d %s labels=%v old=%v", round, i,
				k8s.NamespacedName(pod.pod), pod.pod.Labels, pod.old != nil)
		}
	}
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_indexFollowsPolicyChanges: the index tracks spec updates
// (peers widened, namespaceSelector dropped) and deletions, so a stale entry can neither hide a new peer nor keep a
// removed one.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_indexFollowsPolicyChanges(t *testing.T) {
	ctx := context.Background()
	target := newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a"), PodSelector: matchLabels("app", "job")}), nil)
	r, tracker := newFakeResolver(t, newTestNamespace("clients", map[string]string{"team": "a"}), target)
	tracker.UpdatePolicy(target)
	other := newTestPod("clients", "o", map[string]string{"app": "other"})

	got, err := r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "non-peer pod is not matched")

	widened := target.DeepCopy()
	widened.Spec.Ingress[0].From[0].PodSelector = nil
	require.NoError(t, r.k8sClient.Update(ctx, widened))
	tracker.UpdatePolicy(widened)
	got, err = r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/p"}, policyNames(got), "index follows a widened peer")

	sameNamespaceOnly := widened.DeepCopy()
	sameNamespaceOnly.Spec.Ingress[0].From[0] = networking.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	require.NoError(t, r.k8sClient.Update(ctx, sameNamespaceOnly))
	tracker.UpdatePolicy(sameNamespaceOnly)
	_, indexed := tracker.GetNamespaceSelectorPeers(k8s.NamespacedName(sameNamespaceOnly))
	assert.False(t, indexed, "a policy without namespaceSelector peers leaves the index")
	got, err = r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got)

	// Deleted from the cache before the tracker hears about it: skipped, not an error.
	tracker.UpdatePolicy(widened)
	require.NoError(t, r.k8sClient.Delete(ctx, widened))
	got, err = r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	tracker.RemovePolicy(widened)
	_, indexed = tracker.GetNamespaceSelectorPeers(k8s.NamespacedName(widened))
	assert.False(t, indexed)
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_withoutIndexedPeers: a policy tracked by reference only, with
// no parsed peers, is matched from the policy object as before.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_withoutIndexedPeers(t *testing.T) {
	np := newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)
	anp := newTestANP("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)
	r, tracker := newFakeResolver(t, newTestNamespace("clients", map[string]string{"team": "a"}), np, anp)
	tracker.namespacedPolicies.Store(k8s.NamespacedName(np), true)
	tracker.namespacedANP.Store(k8s.NamespacedName(anp), true)
	pod := newTestPod("clients", "c", nil)

	got, err := r.GetReferredPoliciesForPod(context.Background(), pod, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/p"}, policyNames(got))
	gotANP, err := r.GetReferredApplicationNetworkPoliciesForPod(context.Background(), pod, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/p"}, anpNames(gotANP))
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_concurrentPolicyUpdates runs the pod path while the policy
// event handler updates and removes the same policies, as the two informer goroutines do. Run with -race.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_concurrentPolicyUpdates(t *testing.T) {
	ctx := context.Background()
	matching := newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)
	notMatching := newTestNetworkPolicy("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "b")}), nil)
	anp := newTestANP("servers", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("team", "a")}), nil)
	r, tracker := newFakeResolver(t, newTestNamespace("clients", map[string]string{"team": "a"}), matching, anp)
	pod := newTestPod("clients", "c", nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			switch i % 4 {
			case 0:
				tracker.UpdatePolicy(matching)
				tracker.UpdateGenericPolicy(anp)
			case 1:
				tracker.UpdatePolicy(notMatching)
			case 2:
				tracker.RemovePolicy(matching)
				tracker.RemoveGenericPolicy(anp)
			default:
				tracker.namespacedPolicies.Store(k8s.NamespacedName(matching), true) // ref without parsed peers
			}
		}
	}()
	for i := 0; i < 2000; i++ {
		got, err := r.GetReferredPoliciesForPod(ctx, pod, nil)
		require.NoError(t, err)
		assert.Subset(t, []string{"servers/p"}, policyNames(got))
		gotANP, err := r.GetReferredApplicationNetworkPoliciesForPod(ctx, pod, nil)
		require.NoError(t, err)
		assert.Subset(t, []string{"servers/p"}, anpNames(gotANP))
	}
	wg.Wait()
}

func TestDefaultPolicyTracker_NamespaceSelectorPeers(t *testing.T) {
	invalid := matchExpression("k", metav1.LabelSelectorOpIn) // In without values does not parse
	type wantPeer struct {
		nsLabels    labels.Set
		nsMatch     bool
		podSelector bool // false when the peer has no podSelector
	}
	tests := []struct {
		name        string
		policy      client.Object
		wantIndexed bool
		wantPeers   []wantPeer
	}{
		{
			name:   "NetworkPolicy without namespaceSelector peers is not indexed",
			policy: newTestNetworkPolicy("n", "p", ruleWith(networking.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}, networking.NetworkPolicyPeer{IPBlock: &networking.IPBlock{CIDR: "0.0.0.0/0"}}), nil),
		},
		{
			name: "NetworkPolicy ingress and egress namespaceSelector peers are indexed in order",
			policy: newTestNetworkPolicy("n", "p",
				ruleWith(networking.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}, networking.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}),
				ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("a", "b"), PodSelector: matchLabels("c", "d")})),
			wantIndexed: true,
			wantPeers: []wantPeer{
				{nsLabels: labels.Set{}, nsMatch: true},
				{nsLabels: labels.Set{"a": "b"}, nsMatch: true, podSelector: true},
			},
		},
		{
			name: "NetworkPolicy peer with an invalid selector is dropped",
			policy: newTestNetworkPolicy("n", "p", ruleWith(
				networking.NetworkPolicyPeer{NamespaceSelector: invalid},
				networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("a", "b"), PodSelector: invalid},
				networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("a", "b")}), nil),
			wantIndexed: true,
			wantPeers:   []wantPeer{{nsLabels: labels.Set{"a": "x"}, nsMatch: false}},
		},
		{
			name:        "NetworkPolicy whose only namespaceSelector peer is invalid is indexed with no peers",
			policy:      newTestNetworkPolicy("n", "p", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: invalid}), nil),
			wantIndexed: true,
		},
		{
			name:   "ApplicationNetworkPolicy without namespaceSelector peers is not indexed",
			policy: newTestANP("n", "p", nil, [][]policyinfo.ApplicationNetworkPolicyPeer{{{DomainNames: []policyinfo.DomainName{"example.com"}}, {PodSelector: &metav1.LabelSelector{}}}}),
		},
		{
			name: "ApplicationNetworkPolicy ingress and egress namespaceSelector peers are indexed",
			policy: newTestANP("n", "p",
				ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("a", "b")}),
				[][]policyinfo.ApplicationNetworkPolicyPeer{{{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: matchLabels("c", "d")}}}),
			wantIndexed: true,
			wantPeers: []wantPeer{
				{nsLabels: labels.Set{"a": "b"}, nsMatch: true},
				{nsLabels: labels.Set{}, nsMatch: true, podSelector: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := &defaultPolicyTracker{logger: logr.New(&log.NullLogSink{})}
			ref := k8s.NamespacedName(tt.policy)
			get := tracker.GetNamespaceSelectorPeers
			if _, ok := tt.policy.(*policyinfo.ApplicationNetworkPolicy); ok {
				get = tracker.GetANPNamespaceSelectorPeers
			}

			tracker.UpdateGenericPolicy(tt.policy)
			got, indexed := get(ref)
			require.Equal(t, tt.wantIndexed, indexed)
			require.Len(t, got, len(tt.wantPeers))
			for i, want := range tt.wantPeers {
				assert.Equal(t, want.nsMatch, got[i].Namespace.Matches(want.nsLabels), "peer %d namespace selector", i)
				assert.Equal(t, want.podSelector, got[i].Pod != nil, "peer %d pod selector", i)
			}

			tracker.RemoveGenericPolicy(tt.policy)
			_, indexed = get(ref)
			assert.False(t, indexed, "removed with the policy")
		})
	}
}

// TestDefaultPolicyTracker_NamespaceSelectorPeers_kindsDoNotCollide: a NetworkPolicy and an ApplicationNetworkPolicy
// with the same namespace and name are indexed independently.
func TestDefaultPolicyTracker_NamespaceSelectorPeers_kindsDoNotCollide(t *testing.T) {
	tracker := &defaultPolicyTracker{logger: logr.New(&log.NullLogSink{})}
	np := newTestNetworkPolicy("n", "same", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("kind", "np")}), nil)
	anp := newTestANP("n", "same", ruleWith(networking.NetworkPolicyPeer{NamespaceSelector: matchLabels("kind", "anp")}), nil)
	tracker.UpdateGenericPolicy(np)
	tracker.UpdateGenericPolicy(anp)

	npPeers, ok := tracker.GetNamespaceSelectorPeers(k8s.NamespacedName(np))
	require.True(t, ok)
	require.Len(t, npPeers, 1)
	assert.True(t, npPeers[0].Namespace.Matches(labels.Set{"kind": "np"}))
	anpPeers, ok := tracker.GetANPNamespaceSelectorPeers(k8s.NamespacedName(anp))
	require.True(t, ok)
	require.Len(t, anpPeers, 1)
	assert.True(t, anpPeers[0].Namespace.Matches(labels.Set{"kind": "anp"}))

	tracker.RemoveGenericPolicy(np)
	_, ok = tracker.GetNamespaceSelectorPeers(k8s.NamespacedName(np))
	assert.False(t, ok)
	_, ok = tracker.GetANPNamespaceSelectorPeers(k8s.NamespacedName(anp))
	assert.True(t, ok, "removing the NetworkPolicy keeps the ApplicationNetworkPolicy")
}

// BenchmarkPolicyReferenceResolver_GetReferredPoliciesForPod: one pod event against 5,000 cross-namespace policies
// with 10 namespaceSelector peers each, over 500 namespaces. reference-scan is the per-event scan the index replaces.
func BenchmarkPolicyReferenceResolver_GetReferredPoliciesForPod(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	var objs []client.Object
	nsList := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		ns := newTestNamespace(fmt.Sprintf("ns-%d", i), map[string]string{"team": fmt.Sprintf("t%d", i%20)})
		nsList = append(nsList, ns.Name)
		objs = append(objs, ns)
	}
	var policies []*networking.NetworkPolicy
	for i := 0; i < 5000; i++ {
		var from []networking.NetworkPolicyPeer
		for j := 0; j < 10; j++ {
			from = append(from, networking.NetworkPolicyPeer{
				NamespaceSelector: matchLabels("team", fmt.Sprintf("t%d", rng.Intn(20))),
				PodSelector:       matchLabels("app", fmt.Sprintf("a%d", rng.Intn(5000))),
			})
		}
		p := newTestNetworkPolicy(nsList[rng.Intn(500)], fmt.Sprintf("p%d", i), ruleWith(from...), nil)
		p.Spec.PodSelector = *matchLabels("app", fmt.Sprintf("a%d", i))
		policies = append(policies, p)
		objs = append(objs, p)
	}
	pod := newTestPod("ns-7", "j", map[string]string{"app": "a42"})

	b.Run("reference-scan", func(b *testing.B) {
		r, tracker := newFakeResolver(b, objs...)
		for _, p := range policies {
			tracker.namespacedPolicies.Store(k8s.NamespacedName(p), true)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := referenceGetReferredPoliciesForPod(context.Background(), r, pod, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("indexed", func(b *testing.B) {
		r, tracker := newFakeResolver(b, objs...)
		for _, p := range policies {
			tracker.UpdatePolicy(p)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := r.GetReferredPoliciesForPod(context.Background(), pod, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// randomResolver returns a fake resolver with four namespaces carrying random labels, some with none at all.
func randomResolver(t *testing.T, rng *rand.Rand) (*defaultPolicyReferenceResolver, *defaultPolicyTracker, []string) {
	nsList := []string{"ns-a", "ns-b", "ns-c", "ns-d"}
	var objs []client.Object
	for _, n := range nsList {
		objs = append(objs, newTestNamespace(n, randomLabels(rng)))
	}
	r, tracker := newFakeResolver(t, objs...)
	return r, tracker, nsList
}

type randomPodEvent struct {
	pod, old *corev1.Pod
}

// randomPods returns pod events in the given namespaces and in one the cache does not have, half of them updates
// whose old pod has different labels.
func randomPods(rng *rand.Rand, nsList []string, n int) []randomPodEvent {
	podNS := append([]string{"ns-missing"}, nsList...)
	out := make([]randomPodEvent, 0, n)
	for i := 0; i < n; i++ {
		ev := randomPodEvent{pod: newTestPod(podNS[rng.Intn(len(podNS))], fmt.Sprintf("pod%d", i), randomLabels(rng))}
		if rng.Intn(2) == 0 {
			ev.old = ev.pod.DeepCopy()
			ev.old.Labels = randomLabels(rng)
		}
		out = append(out, ev)
	}
	return out
}

func randomSelector(rng *rand.Rand) *metav1.LabelSelector {
	keys := []string{"team", "tier", "env"}
	vals := []string{"a", "b", "c"}
	switch rng.Intn(6) {
	case 0:
		return &metav1.LabelSelector{}
	case 1:
		return matchLabels(keys[rng.Intn(3)], vals[rng.Intn(3)])
	case 2:
		return matchExpression(keys[rng.Intn(3)], metav1.LabelSelectorOpIn, vals[rng.Intn(3)], vals[rng.Intn(3)])
	case 3:
		return matchExpression(keys[rng.Intn(3)], metav1.LabelSelectorOpNotIn, vals[rng.Intn(3)])
	case 4:
		return matchExpression(keys[rng.Intn(3)], metav1.LabelSelectorOpDoesNotExist)
	default:
		return matchLabels(keys[rng.Intn(3)], vals[rng.Intn(3)], keys[rng.Intn(3)], vals[rng.Intn(3)])
	}
}

func randomPeer(rng *rand.Rand) networking.NetworkPolicyPeer {
	switch rng.Intn(6) {
	case 0:
		return networking.NetworkPolicyPeer{NamespaceSelector: randomSelector(rng)}
	case 1, 2:
		return networking.NetworkPolicyPeer{NamespaceSelector: randomSelector(rng), PodSelector: randomSelector(rng)}
	case 3:
		return networking.NetworkPolicyPeer{PodSelector: randomSelector(rng)}
	case 4:
		return networking.NetworkPolicyPeer{IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"}}
	default:
		// In without values does not parse.
		return networking.NetworkPolicyPeer{NamespaceSelector: matchExpression("team", metav1.LabelSelectorOpIn), PodSelector: randomSelector(rng)}
	}
}

func randomPolicySet(rng *rand.Rand, nsList []string, n int) []*networking.NetworkPolicy {
	port := intstr.FromInt32(8080)
	out := make([]*networking.NetworkPolicy, 0, n)
	for i := 0; i < n; i++ {
		p := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i), Namespace: nsList[rng.Intn(len(nsList))]},
			Spec: networking.NetworkPolicySpec{PodSelector: *randomSelector(rng)}}
		for k := 0; k < 1+rng.Intn(3); k++ {
			rule := networking.NetworkPolicyIngressRule{Ports: []networking.NetworkPolicyPort{{Port: &port}}}
			for j := 0; j < 1+rng.Intn(3); j++ {
				rule.From = append(rule.From, randomPeer(rng))
			}
			p.Spec.Ingress = append(p.Spec.Ingress, rule)
		}
		if rng.Intn(2) == 0 {
			rule := networking.NetworkPolicyEgressRule{}
			for j := 0; j < 1+rng.Intn(2); j++ {
				rule.To = append(rule.To, randomPeer(rng))
			}
			p.Spec.Egress = append(p.Spec.Egress, rule)
		}
		out = append(out, p)
	}
	return out
}

// toANP converts a random NetworkPolicy into an ApplicationNetworkPolicy, turning some egress peers into
// domainNames-only peers.
func toANP(rng *rand.Rand, np *networking.NetworkPolicy) *policyinfo.ApplicationNetworkPolicy {
	anp := &policyinfo.ApplicationNetworkPolicy{ObjectMeta: *np.ObjectMeta.DeepCopy(),
		Spec: policyinfo.ApplicationNetworkPolicySpec{PodSelector: np.Spec.PodSelector, Ingress: np.Spec.Ingress}}
	for _, rule := range np.Spec.Egress {
		out := policyinfo.ApplicationNetworkPolicyEgressRule{Ports: rule.Ports}
		for _, peer := range rule.To {
			if rng.Intn(4) == 0 {
				out.To = append(out.To, policyinfo.ApplicationNetworkPolicyPeer{DomainNames: []policyinfo.DomainName{"example.com"}})
				continue
			}
			out.To = append(out.To, policyinfo.ApplicationNetworkPolicyPeer{
				NamespaceSelector: peer.NamespaceSelector, PodSelector: peer.PodSelector, IPBlock: peer.IPBlock})
		}
		anp.Spec.Egress = append(anp.Spec.Egress, out)
	}
	return anp
}

func randomLabels(rng *rand.Rand) map[string]string {
	m := map[string]string{}
	for _, k := range []string{"team", "tier", "env"} {
		if rng.Intn(2) == 0 {
			m[k] = []string{"a", "b", "c"}[rng.Intn(3)]
		}
	}
	return m
}
