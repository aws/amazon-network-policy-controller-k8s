package resolvers

import (
	"context"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
)

func TestPolicyReferenceResolver_GetReferredPoliciesForNamespace(t *testing.T) {
	type policyGetCall struct {
		ref    types.NamespacedName
		policy *networking.NetworkPolicy
		err    error
	}
	policyIng := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-allow",
			Namespace: "ing",
		},
		Spec: networking.NetworkPolicySpec{
			Ingress: []networking.NetworkPolicyIngressRule{
				{
					From: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"key": "value",
								},
							},
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"ns": "select",
								},
							},
						},
					},
				},
			},
		},
	}
	policyEgr := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "policy",
			Namespace: "egr",
		},
		Spec: networking.NetworkPolicySpec{
			Egress: []networking.NetworkPolicyEgressRule{
				{
					To: []networking.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"ns": "select",
								},
							},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name            string
		trackedPolicies []types.NamespacedName
		policyGetCalls  []policyGetCall
		namespace       *corev1.Namespace
		namespaceOld    *corev1.Namespace
		want            []networking.NetworkPolicy
		wantErr         string
	}{
		{
			name: "no x namespace policies",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
				},
			},
		},
		{
			name: "namespace policies no match",
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "app-allow",
					Namespace: "ing",
				},
			},
			policyGetCalls: []policyGetCall{
				{
					ref: types.NamespacedName{
						Namespace: "ing",
						Name:      "app-allow",
					},
					policy: &networking.NetworkPolicy{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "policy",
							Namespace: "ing",
						},
						Spec: networking.NetworkPolicySpec{
							Ingress: []networking.NetworkPolicyIngressRule{
								{
									From: []networking.NetworkPolicyPeer{
										{
											PodSelector: &metav1.LabelSelector{
												MatchLabels: map[string]string{
													"key": "value",
												},
											},
											NamespaceSelector: &metav1.LabelSelector{
												MatchLabels: map[string]string{
													"ns": "select",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
				},
			},
		},
		{
			name: "namespace ingress rule match",
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "app-allow",
					Namespace: "ing",
				},
			},
			policyGetCalls: []policyGetCall{
				{
					ref: types.NamespacedName{
						Namespace: "ing",
						Name:      "app-allow",
					},
					policy: policyIng,
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
					Labels: map[string]string{
						"ns": "select",
					},
				},
			},
			want: []networking.NetworkPolicy{*policyIng},
		},
		{
			name: "namespace egress rule match",
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "policy",
					Namespace: "egr",
				},
			},
			policyGetCalls: []policyGetCall{
				{
					ref: types.NamespacedName{
						Namespace: "egr",
						Name:      "policy",
					},
					policy: policyEgr,
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
					Labels: map[string]string{
						"ns": "select",
					},
				},
			},
			want: []networking.NetworkPolicy{*policyEgr},
		},
		{
			name: "old namespace label match",
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "policy",
					Namespace: "egr",
				},
			},
			policyGetCalls: []policyGetCall{
				{
					ref: types.NamespacedName{
						Namespace: "egr",
						Name:      "policy",
					},
					policy: policyEgr,
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
					Labels: map[string]string{
						"ns-new": "select",
					},
				},
			},
			namespaceOld: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
					Labels: map[string]string{
						"ns": "select",
					},
				},
			},
			want: []networking.NetworkPolicy{*policyEgr},
		},
		{
			name: "multiple matches",
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "app-allow",
					Namespace: "ing",
				},
				{
					Name:      "policy",
					Namespace: "egr",
				},
			},
			policyGetCalls: []policyGetCall{
				{
					ref: types.NamespacedName{
						Namespace: "ing",
						Name:      "app-allow",
					},
					policy: policyIng,
				},
				{
					ref: types.NamespacedName{
						Namespace: "egr",
						Name:      "policy",
					},
					policy: policyEgr,
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
					Labels: map[string]string{
						"ns": "select",
					},
				},
			},
			want: []networking.NetworkPolicy{*policyIng, *policyEgr},
		},
		{
			name: "Ignore not found",
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "app-allow",
					Namespace: "ing",
				},
				{
					Name:      "policy",
					Namespace: "egr",
				},
			},
			policyGetCalls: []policyGetCall{
				{
					ref: types.NamespacedName{
						Namespace: "egr",
						Name:      "policy",
					},
					err: apierrors.NewNotFound(schema.GroupResource{}, "egr"),
				},
				{
					ref: types.NamespacedName{
						Namespace: "ing",
						Name:      "app-allow",
					},
					policy: policyIng,
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
					Labels: map[string]string{
						"ns": "select",
					},
				},
			},
			want: []networking.NetworkPolicy{*policyIng},
		},
		{
			name: "policy get returns error",
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "app-allow",
					Namespace: "ing",
				},
				{
					Name:      "policy",
					Namespace: "egr",
				},
			},
			policyGetCalls: []policyGetCall{
				{
					ref: types.NamespacedName{
						Namespace: "egr",
						Name:      "policy",
					},
					err: errors.New("some random error"),
				},
				{
					ref: types.NamespacedName{
						Namespace: "ing",
						Name:      "app-allow",
					},
					policy: policyIng,
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "xyz",
					Labels: map[string]string{
						"ns": "select",
					},
				},
			},
			wantErr: "failed to get policies: some random error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := mock_client.NewMockClient(ctrl)
			nullLogger := logr.New(&log.NullLogSink{})
			policyTracker := &defaultPolicyTracker{
				logger: nullLogger,
			}

			policyResolver := &defaultPolicyReferenceResolver{
				k8sClient:     mockClient,
				policyTracker: policyTracker,
				logger:        nullLogger,
			}
			for _, ref := range tt.trackedPolicies {
				policyTracker.namespacedPolicies.Store(ref, true)
			}
			for _, item := range tt.policyGetCalls {
				call := item
				mockClient.EXPECT().Get(gomock.Any(), call.ref, gomock.Any()).DoAndReturn(
					func(ctx context.Context, key types.NamespacedName, policy *networking.NetworkPolicy, opts ...client.GetOption) error {
						if call.policy != nil {
							*policy = *call.policy
						}
						return call.err
					},
				)
			}
			got, err := policyResolver.GetReferredPoliciesForNamespace(context.Background(), tt.namespace, tt.namespaceOld)
			if len(tt.wantErr) > 0 {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				sort.Slice(tt.want, func(i, j int) bool {
					return tt.want[i].String() < tt.want[j].String()
				})
				sort.Slice(got, func(i, j int) bool {
					return got[i].String() < got[j].String()
				})
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestDefaultNamespaceUtils_isNamespaceReferredInPolicy(t *testing.T) {
	tests := []struct {
		name      string
		namespace *corev1.Namespace
		policy    *networking.NetworkPolicy
		want      bool
	}{
		{
			name: "nomatch",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "awesome",
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "policy",
					Namespace: "ing",
				},
			},
		},
		{
			name: "all match",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "awesome",
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "policy",
					Namespace: "ing",
				},
				Spec: networking.NetworkPolicySpec{
					Ingress: []networking.NetworkPolicyIngressRule{
						{
							From: []networking.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{},
									},
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "namespace label match ingress",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "awesome",
					Labels: map[string]string{
						"label.one.value":              "ai",
						"second.label":                 "matches.policy",
						"runs.app/systemCriticalLevel": "medium",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "policy",
					Namespace: "ing",
				},
				Spec: networking.NetworkPolicySpec{
					Ingress: []networking.NetworkPolicyIngressRule{
						{
							From: []networking.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"key": "value",
										},
									},
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"ns": "select",
										},
									},
								},
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "second.label",
												Operator: "In",
												Values:   []string{"matches.policy"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "namespace label match egress",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "awesome",
					Labels: map[string]string{
						"label.one.value":              "ai",
						"second.label":                 "matches.policy",
						"runs.app/systemCriticalLevel": "medium",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "policy",
					Namespace: "ing",
				},
				Spec: networking.NetworkPolicySpec{
					Ingress: []networking.NetworkPolicyIngressRule{
						{
							From: []networking.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"key": "value",
										},
									},
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"ns": "select",
										},
									},
								},
							},
						},
					},
					Egress: []networking.NetworkPolicyEgressRule{
						{
							To: []networking.NetworkPolicyPeer{
								{
									IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"},
								},
							},
						},
						{
							To: []networking.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"runs.app/systemCriticalLevel": "medium",
										},
									},
								},
							},
						},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			policyResolver := &defaultPolicyReferenceResolver{
				k8sClient: mock_client.NewMockClient(ctrl),
				logger:    logr.New(&log.NullLogSink{}),
			}

			got := policyResolver.isNamespaceReferredInPolicy(tt.namespace, tt.policy)
			assert.Equal(t, tt.want, got)
		})
	}
}
