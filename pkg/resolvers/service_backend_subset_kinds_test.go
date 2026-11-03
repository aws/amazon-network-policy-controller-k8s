package resolvers

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

const workerClusterIP = policyinfo.NetworkAddress("10.100.134.183")

func optInFixture(withUnlabeledBackend bool) []client.Object {
	optIn := workerPod("w1", true, corev1.PodRunning)
	svc := workerService()
	objs := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "restate", Labels: map[string]string{"kubernetes.io/metadata.name": "restate"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "intro-dp", Labels: map[string]string{"kubernetes.io/metadata.name": "intro-dp"}}},
		&optIn,
		&svc,
	}
	if withUnlabeledBackend {
		other := workerPod("w2", false, corev1.PodRunning)
		other.Status.PodIP = "192.168.57.43"
		objs = append(objs, &other)
	}
	return objs
}

func newFixtureClient(t *testing.T, objs []client.Object) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, networking.AddToScheme(scheme))
	require.NoError(t, policyinfo.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func hasCIDR(cidrs []policyinfo.NetworkAddress, want policyinfo.NetworkAddress) bool {
	for _, c := range cidrs {
		if c == want {
			return true
		}
	}
	return false
}

// The ClusterIP decision for NetworkPolicy, ApplicationNetworkPolicy and ClusterNetworkPolicy all
// flow through the same endpoints resolver. Each kind expresses the issue #572 peer:
// any namespace AND pods carrying the opt-in label.
func TestServiceClusterIPOptInPeer_AllPolicyKinds(t *testing.T) {
	subject := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "restate"}}

	resolveNP := func(t *testing.T, c client.Client) []policyinfo.NetworkAddress {
		np := &networking.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "allow-restate-egress", Namespace: "restate"},
			Spec: networking.NetworkPolicySpec{
				PodSelector: subject,
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
				Egress: []networking.NetworkPolicyEgressRule{{
					To: []networking.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: optInPeerSelector()}},
				}},
			},
		}
		_, egress, _, err := NewEndpointsResolver(c, logr.Discard()).Resolve(context.Background(), np)
		require.NoError(t, err)
		var out []policyinfo.NetworkAddress
		for _, e := range egress {
			out = append(out, e.CIDR)
		}
		return out
	}
	resolveANP := func(t *testing.T, c client.Client) []policyinfo.NetworkAddress {
		anp := &policyinfo.ApplicationNetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "allow-restate-egress", Namespace: "restate"},
			Spec: policyinfo.ApplicationNetworkPolicySpec{
				PodSelector: subject,
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
				Egress: []policyinfo.ApplicationNetworkPolicyEgressRule{{
					To: []policyinfo.ApplicationNetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: optInPeerSelector()}},
				}},
			},
		}
		_, egress, _, err := NewApplicationNetworkPolicyEndpointsResolver(c, logr.Discard()).ResolveApplicationNetworkPolicy(context.Background(), anp)
		require.NoError(t, err)
		var out []policyinfo.NetworkAddress
		for _, e := range egress {
			out = append(out, e.CIDR)
		}
		return out
	}
	resolveCNP := func(t *testing.T, c client.Client) []policyinfo.NetworkAddress {
		cnp := &policyinfo.ClusterNetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "allow-restate-egress"},
			Spec: policyinfo.ClusterNetworkPolicySpec{
				Tier:     policyinfo.AdminTier,
				Priority: 10,
				Subject: policyinfo.ClusterNetworkPolicySubject{
					Pods: &policyinfo.NamespacedPod{
						NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "restate"}},
						PodSelector:       subject,
					},
				},
				Egress: []policyinfo.ClusterNetworkPolicyEgressRule{{
					Name:   "allow-opted-in",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					To: []policyinfo.ClusterNetworkPolicyEgressPeer{{
						Pods: &policyinfo.NamespacedPod{NamespaceSelector: metav1.LabelSelector{}, PodSelector: *optInPeerSelector()},
					}},
				}},
			},
		}
		_, egress, _, err := NewClusterNetworkPolicyEndpointsResolver(c, logr.Discard()).ResolveClusterNetworkPolicy(context.Background(), cnp)
		require.NoError(t, err)
		var out []policyinfo.NetworkAddress
		for _, e := range egress {
			out = append(out, e.CIDR)
		}
		return out
	}

	kinds := map[string]func(*testing.T, client.Client) []policyinfo.NetworkAddress{
		"NetworkPolicy":            resolveNP,
		"ApplicationNetworkPolicy": resolveANP,
		"ClusterNetworkPolicy":     resolveCNP,
	}
	for kind, resolve := range kinds {
		t.Run(kind+"/every backend opted in allows pod IP and ClusterIP", func(t *testing.T) {
			got := resolve(t, newFixtureClient(t, optInFixture(false)))
			assert.True(t, hasCIDR(got, "192.168.57.42"), "pod IP missing: %v", got)
			assert.True(t, hasCIDR(got, workerClusterIP), "ClusterIP missing: %v", got)
		})
		t.Run(kind+"/unlabeled backend keeps ClusterIP out", func(t *testing.T) {
			got := resolve(t, newFixtureClient(t, optInFixture(true)))
			assert.True(t, hasCIDR(got, "192.168.57.42"), "pod IP missing: %v", got)
			assert.False(t, hasCIDR(got, "192.168.57.43"), "unlabeled pod IP allowed: %v", got)
			assert.False(t, hasCIDR(got, workerClusterIP), "ClusterIP allowed with unlabeled backend: %v", got)
		})
	}
}

func optInANP() *policyinfo.ApplicationNetworkPolicy {
	return &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-restate-egress", Namespace: "restate"},
		Spec: policyinfo.ApplicationNetworkPolicySpec{
			PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
			Egress: []policyinfo.ApplicationNetworkPolicyEgressRule{{
				To: []policyinfo.ApplicationNetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: optInPeerSelector()}},
			}},
		},
	}
}

func newANPReferenceResolver(c client.Client, track bool) *defaultPolicyReferenceResolver {
	tracker := &defaultPolicyTracker{logger: logr.Discard()}
	if track {
		ref := types.NamespacedName{Namespace: "restate", Name: "allow-restate-egress"}
		tracker.namespacedANP.Store(ref, true)
		tracker.egressRulesANP.Store(ref, true)
	}
	return &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: logr.Discard()}
}

// The ApplicationNetworkPolicy service trigger must see a service whose selector does not carry
// the opt-in label but whose backends do.
func TestIsServiceMatchANPPeer_BackendFallback(t *testing.T) {
	peer := &optInANP().Spec.Egress[0].To[0]
	svc := workerService()

	c := newFixtureClient(t, optInFixture(false))
	assert.True(t, newANPReferenceResolver(c, true).isServiceMatchANPPeer(context.Background(), &svc, peer, "restate"))

	objs := optInFixture(false)[:2]
	objs = append(objs, &svc, func() *corev1.Pod { p := workerPod("w2", false, corev1.PodRunning); return &p }())
	c = newFixtureClient(t, objs)
	assert.False(t, newANPReferenceResolver(c, true).isServiceMatchANPPeer(context.Background(), &svc, peer, "restate"))
}

// An unlabeled backend joining the service must re-resolve the ApplicationNetworkPolicy so the
// ClusterIP allowed earlier is withdrawn.
func TestGetReferredANPsForPodServices(t *testing.T) {
	objs := append(optInFixture(true), optInANP())
	joiner := workerPod("w2", false, corev1.PodRunning)

	got, err := newANPReferenceResolver(newFixtureClient(t, objs), true).
		getReferredANPsForPodServices(context.Background(), &joiner, nil, sets.Set[types.NamespacedName]{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "allow-restate-egress", got[0].Name)

	got, err = newANPReferenceResolver(newFixtureClient(t, objs), false).
		getReferredANPsForPodServices(context.Background(), &joiner, nil, sets.Set[types.NamespacedName]{})
	require.NoError(t, err)
	assert.Empty(t, got)

	outsider := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "intro-dp", Labels: map[string]string{"app": "other"}}}
	got, err = newANPReferenceResolver(newFixtureClient(t, objs), true).
		getReferredANPsForPodServices(context.Background(), &outsider, nil, sets.Set[types.NamespacedName]{})
	require.NoError(t, err)
	assert.Empty(t, got)
}
