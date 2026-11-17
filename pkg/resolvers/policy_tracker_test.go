package resolvers

import (
	"sort"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
)

func TestDefaultPolicyTracker_UpdatePolicy(t *testing.T) {
	tests := []struct {
		name           string
		policies       []networking.NetworkPolicy
		wantNsList     []types.NamespacedName
		wantEgressList []types.NamespacedName
	}{
		{
			name: "no namespace selector",
			policies: []networking.NetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "no-ns-selector",
					},
				},
			},
			wantEgressList: []types.NamespacedName{},
			wantNsList:     []types.NamespacedName{},
		},
		{
			name: "ns selector in ingress",
			policies: []networking.NetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "ingress-selector",
						Namespace: "app",
					},
					Spec: networking.NetworkPolicySpec{
						Ingress: []networking.NetworkPolicyIngressRule{
							{
								From: []networking.NetworkPolicyPeer{
									{
										NamespaceSelector: &metav1.LabelSelector{},
									},
								},
							},
						},
					},
				},
			},
			wantNsList: []types.NamespacedName{
				{
					Namespace: "app",
					Name:      "ingress-selector",
				},
			},
			wantEgressList: []types.NamespacedName{},
		},
		{
			name: "ns selector in egress",
			policies: []networking.NetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "egress-selector",
						Namespace: "egress",
					},
					Spec: networking.NetworkPolicySpec{
						Egress: []networking.NetworkPolicyEgressRule{
							{
								To: []networking.NetworkPolicyPeer{
									{
										NamespaceSelector: &metav1.LabelSelector{},
									},
								},
							},
						},
					},
				},
			},
			wantNsList: []types.NamespacedName{
				{
					Namespace: "egress",
					Name:      "egress-selector",
				},
			},
			wantEgressList: []types.NamespacedName{
				{
					Namespace: "egress",
					Name:      "egress-selector",
				},
			},
		},
		{
			name: "multiple entries",
			policies: []networking.NetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "no-ns-selector",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "ingress-selector",
						Namespace: "ing",
					},
					Spec: networking.NetworkPolicySpec{
						Ingress: []networking.NetworkPolicyIngressRule{
							{
								From: []networking.NetworkPolicyPeer{
									{
										NamespaceSelector: &metav1.LabelSelector{},
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "egress-selector",
					},
					Spec: networking.NetworkPolicySpec{
						Egress: []networking.NetworkPolicyEgressRule{
							{
								To: []networking.NetworkPolicyPeer{
									{
										NamespaceSelector: &metav1.LabelSelector{},
									},
								},
							},
						},
					},
				},
			},
			wantNsList: []types.NamespacedName{
				{
					Namespace: "ing",
					Name:      "ingress-selector",
				},
				{
					Name: "egress-selector",
				},
			},
			wantEgressList: []types.NamespacedName{
				{
					Name: "egress-selector",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policyTracker := &defaultPolicyTracker{
				logger: logr.New(&log.NullLogSink{}),
			}
			for _, policy := range tt.policies {
				policyTracker.UpdatePolicy(&policy)
			}
			gotNsList := policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList()
			gotEgressList := policyTracker.GetPoliciesWithEgressRules().UnsortedList()
			for _, lst := range [][]types.NamespacedName{gotNsList, gotEgressList, tt.wantNsList, tt.wantEgressList} {
				sort.Slice(lst, func(i, j int) bool {
					return lst[i].String() < lst[j].String()
				})
			}
			assert.Equal(t, tt.wantNsList, gotNsList)
			assert.Equal(t, tt.wantEgressList, gotEgressList)
		})
	}
}

func TestDefaultPolicyTracker_RemovePolicy(t *testing.T) {
	tests := []struct {
		name                   string
		existingNsRefPolicies  []types.NamespacedName
		existingEgressPolicies []types.NamespacedName
		policies               []networking.NetworkPolicy
		wantNsList             []types.NamespacedName
		wantEgressList         []types.NamespacedName
	}{
		{
			name: "existing empty",
			policies: []networking.NetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "egress-selector",
					},
				},
			},
			wantNsList:     []types.NamespacedName{},
			wantEgressList: []types.NamespacedName{},
		},
		{
			name: "non tracked item delete",
			existingNsRefPolicies: []types.NamespacedName{
				{
					Namespace: "awesome",
					Name:      "app",
				},
			},
			existingEgressPolicies: []types.NamespacedName{
				{
					Namespace: "awesome",
					Name:      "egress",
				},
			},
			policies: []networking.NetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "egress-selector",
					},
				},
			},
			wantNsList: []types.NamespacedName{
				{
					Namespace: "awesome",
					Name:      "app",
				},
			},
			wantEgressList: []types.NamespacedName{
				{
					Namespace: "awesome",
					Name:      "egress",
				},
			},
		},
		{
			name: "tracked item delete",
			existingNsRefPolicies: []types.NamespacedName{
				{
					Namespace: "awesome",
					Name:      "app",
				},
				{
					Namespace: "ing",
					Name:      "policy",
				},
				{
					Name: "egr",
				},
			},
			existingEgressPolicies: []types.NamespacedName{
				{
					Name: "egr",
				},
				{
					Name: "app",
				},
			},
			policies: []networking.NetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "app",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ing",
						Name:      "policy",
					},
				},
			},
			wantNsList: []types.NamespacedName{
				{
					Name: "egr",
				},
				{
					Namespace: "awesome",
					Name:      "app",
				},
			},
			wantEgressList: []types.NamespacedName{
				{
					Name: "egr",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policyTracker := &defaultPolicyTracker{
				logger: logr.New(&log.NullLogSink{}),
			}
			for _, entry := range tt.existingNsRefPolicies {
				policyTracker.namespacedPolicies.Store(entry, true)
			}
			for _, entry := range tt.existingEgressPolicies {
				policyTracker.egressRulesPolicies.Store(entry, true)
			}
			for _, policy := range tt.policies {
				policyTracker.RemovePolicy(&policy)
			}
			gotNsList := policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList()
			gotEgressList := policyTracker.GetPoliciesWithEgressRules().UnsortedList()
			for _, lst := range [][]types.NamespacedName{gotNsList, gotEgressList, tt.wantNsList, tt.wantEgressList} {
				sort.Slice(lst, func(i, j int) bool {
					return lst[i].String() < lst[j].String()
				})
			}
			assert.Equal(t, tt.wantNsList, gotNsList)
			assert.Equal(t, tt.wantEgressList, gotEgressList)
		})
	}
}

func TestDefaultPolicyTracker_NamespaceSelectorPeers(t *testing.T) {
	invalid := expressionSelector("k", metav1.LabelSelectorOpIn) // In without values does not parse
	type wantPeer struct {
		matches    labels.Set // namespace labels the peer's namespaceSelector matches
		notMatches labels.Set // namespace labels it does not match
		hasPod     bool       // whether the peer has a podSelector
	}
	tests := []struct {
		name        string
		policy      client.Object
		wantTracked bool
		wantPeers   []wantPeer
	}{
		{
			name: "NetworkPolicy without namespaceSelector peers is not tracked",
			policy: networkPolicyWithPeers("n", "p", []networking.NetworkPolicyPeer{
				{PodSelector: &metav1.LabelSelector{}},
				{IPBlock: &networking.IPBlock{CIDR: "0.0.0.0/0"}},
			}, nil),
		},
		{
			name: "NetworkPolicy ingress and egress namespaceSelector peers, in order",
			policy: networkPolicyWithPeers("n", "p",
				policyPeers(networking.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}},
					namespacePeer(&metav1.LabelSelector{}, nil)),
				policyPeers(namespacePeer(labelSelector("a", "b"), labelSelector("c", "d")))),
			wantTracked: true,
			wantPeers: []wantPeer{
				{matches: labels.Set{}},
				{matches: labels.Set{"a": "b"}, notMatches: labels.Set{"a": "x"}, hasPod: true},
			},
		},
		{
			name: "NetworkPolicy peer with an invalid selector is left out",
			policy: networkPolicyWithPeers("n", "p", []networking.NetworkPolicyPeer{
				namespacePeer(invalid, nil),
				namespacePeer(labelSelector("a", "b"), invalid),
				namespacePeer(labelSelector("a", "b"), nil),
			}, nil),
			wantTracked: true,
			wantPeers:   []wantPeer{{matches: labels.Set{"a": "b"}, notMatches: labels.Set{"a": "x"}}},
		},
		{
			name:        "NetworkPolicy whose only namespaceSelector peer is invalid is tracked with no peers",
			policy:      networkPolicyWithPeers("n", "p", policyPeers(namespacePeer(invalid, nil)), nil),
			wantTracked: true,
		},
		{
			name: "ApplicationNetworkPolicy without namespaceSelector peers is not tracked",
			policy: anpWithPeers("n", "p", nil, []policyinfo.ApplicationNetworkPolicyPeer{
				{DomainNames: []policyinfo.DomainName{"example.com"}},
				{PodSelector: &metav1.LabelSelector{}},
			}),
		},
		{
			name: "ApplicationNetworkPolicy ingress and egress namespaceSelector peers",
			policy: anpWithPeers("n", "p",
				policyPeers(namespacePeer(labelSelector("a", "b"), nil)),
				[]policyinfo.ApplicationNetworkPolicyPeer{
					{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: labelSelector("c", "d")},
				}),
			wantTracked: true,
			wantPeers: []wantPeer{
				{matches: labels.Set{"a": "b"}, notMatches: labels.Set{}},
				{matches: labels.Set{}, hasPod: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := &defaultPolicyTracker{logger: logr.New(&log.NullLogSink{})}
			ref := k8s.NamespacedName(tt.policy)
			peersOf := tracker.GetPolicyNamespaceSelectorPeers
			if _, ok := tt.policy.(*policyinfo.ApplicationNetworkPolicy); ok {
				peersOf = tracker.GetApplicationNetworkPolicyNamespaceSelectorPeers
			}

			tracker.UpdateGenericPolicy(tt.policy)
			got, tracked := peersOf(ref)
			require.Equal(t, tt.wantTracked, tracked)
			require.Len(t, got, len(tt.wantPeers))
			for i, want := range tt.wantPeers {
				assert.True(t, got[i].Namespace.Matches(want.matches), "peer %d should match %v", i, want.matches)
				if want.notMatches != nil {
					assert.False(t, got[i].Namespace.Matches(want.notMatches), "peer %d should not match %v", i,
						want.notMatches)
				}
				assert.Equal(t, want.hasPod, got[i].Pod != nil, "peer %d podSelector", i)
			}

			tracker.RemoveGenericPolicy(tt.policy)
			_, tracked = peersOf(ref)
			assert.False(t, tracked, "removed with the policy")
		})
	}
}

// TestDefaultPolicyTracker_NamespaceSelectorPeers_kinds checks that a NetworkPolicy and an ApplicationNetworkPolicy
// with the same namespace and name are tracked independently.
func TestDefaultPolicyTracker_NamespaceSelectorPeers_kinds(t *testing.T) {
	tracker := &defaultPolicyTracker{logger: logr.New(&log.NullLogSink{})}
	np := networkPolicyWithPeers("n", "same", []networking.NetworkPolicyPeer{
		namespacePeer(labelSelector("kind", "np"), nil),
	}, nil)
	anp := anpWithPeers("n", "same", policyPeers(namespacePeer(labelSelector("kind", "anp"), nil)), nil)
	tracker.UpdateGenericPolicy(np)
	tracker.UpdateGenericPolicy(anp)

	npPeers, ok := tracker.GetPolicyNamespaceSelectorPeers(k8s.NamespacedName(np))
	require.True(t, ok)
	require.Len(t, npPeers, 1)
	assert.True(t, npPeers[0].Namespace.Matches(labels.Set{"kind": "np"}))
	anpPeers, ok := tracker.GetApplicationNetworkPolicyNamespaceSelectorPeers(k8s.NamespacedName(anp))
	require.True(t, ok)
	require.Len(t, anpPeers, 1)
	assert.True(t, anpPeers[0].Namespace.Matches(labels.Set{"kind": "anp"}))

	tracker.RemoveGenericPolicy(np)
	_, ok = tracker.GetPolicyNamespaceSelectorPeers(k8s.NamespacedName(np))
	assert.False(t, ok)
	_, ok = tracker.GetApplicationNetworkPolicyNamespaceSelectorPeers(k8s.NamespacedName(anp))
	assert.True(t, ok, "removing the NetworkPolicy keeps the ApplicationNetworkPolicy")
}

func TestNamespaceSelectorPeer_Matches(t *testing.T) {
	peer := NamespaceSelectorPeer{Namespace: labels.SelectorFromSet(labels.Set{"team": "a"})}
	assert.True(t, peer.Matches(labels.Set{"team": "a"}, nil), "no podSelector selects every pod")
	assert.False(t, peer.Matches(labels.Set{"team": "b"}, nil))
	peer.Pod = labels.SelectorFromSet(labels.Set{"app": "web"})
	assert.True(t, peer.Matches(labels.Set{"team": "a"}, labels.Set{"app": "web"}))
	assert.False(t, peer.Matches(labels.Set{"team": "a"}, labels.Set{"app": "db"}))
	assert.False(t, peer.Matches(labels.Set{"team": "b"}, labels.Set{"app": "web"}))
}
