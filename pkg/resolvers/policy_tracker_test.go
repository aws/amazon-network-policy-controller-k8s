package resolvers

import (
	"sort"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
