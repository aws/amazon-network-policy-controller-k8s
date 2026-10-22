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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
)

func TestPolicyReferenceResolver_GetReferredPoliciesForPod(t *testing.T) {
	type namespaceGetCall struct {
		nsRef types.NamespacedName
		ns    *corev1.Namespace
		err   error
	}
	type policyGetCall struct {
		policyRef types.NamespacedName
		policy    *networking.NetworkPolicy
		err       error
	}
	type policyListCall struct {
		policies []networking.NetworkPolicy
		err      error
	}
	accessPolicy := networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "access-policy",
			Namespace: "metaverse",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"select": "pods",
					"track":  "stable",
				},
			},
			Ingress: []networking.NetworkPolicyIngressRule{
				{
					From: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"access": "ingress",
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
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"access": "egress",
								},
							},
						},
					},
				},
			},
		},
	}
	ingressPolicy := networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-ingress",
			Namespace: "ing",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"direction": "ingress",
				},
			},
			Ingress: []networking.NetworkPolicyIngressRule{
				{
					From: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"ingress": "allow",
								},
							},
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"scope": "ns",
								},
							},
						},
					},
				},
			},
		},
	}
	egressPolicy := networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-egress",
			Namespace: "egr",
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"direction": "egress",
				},
			},
			Egress: []networking.NetworkPolicyEgressRule{
				{
					To: []networking.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"egress": "allow",
								},
							},
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"scope": "ns",
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
		nsGetCalls      []namespaceGetCall
		policyGetCalls  []policyGetCall
		policyListCalls []policyListCall
		trackedPolicies []types.NamespacedName
		pod             *corev1.Pod
		podOld          *corev1.Pod
		want            []networking.NetworkPolicy
		wantErr         string
	}{
		{
			name: "no policies defined",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
				},
			},
			policyListCalls: []policyListCall{
				{
					policies: []networking.NetworkPolicy{},
				},
			},
		},
		{
			name: "no matching policies",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
				},
			},
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "allow-ingress",
					Namespace: "ing",
				},
				{
					Name:      "allow-egress",
					Namespace: "egr",
				},
			},
			policyListCalls: []policyListCall{
				{
					policies: []networking.NetworkPolicy{
						accessPolicy,
					},
				},
			},
			policyGetCalls: []policyGetCall{
				{
					policyRef: types.NamespacedName{
						Name:      "allow-ingress",
						Namespace: "ing",
					},
					policy: &ingressPolicy,
				},
				{
					policyRef: types.NamespacedName{
						Name:      "allow-egress",
						Namespace: "egr",
					},
					policy: &egressPolicy,
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "metaverse",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "metaverse",
							Labels: map[string]string{
								"scope": "ns",
							},
						},
					},
				},
			},
		},
		{
			name: "no matching policies for pod or podOld",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
				},
			},
			podOld: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
					Labels: map[string]string{
						"age": "old",
					},
				},
			},
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "allow-ingress",
					Namespace: "ing",
				},
				{
					Name:      "allow-egress",
					Namespace: "egr",
				},
			},
			policyListCalls: []policyListCall{
				{
					policies: []networking.NetworkPolicy{
						accessPolicy,
					},
				},
			},
			policyGetCalls: []policyGetCall{
				{
					policyRef: types.NamespacedName{
						Name:      "allow-ingress",
						Namespace: "ing",
					},
					policy: &ingressPolicy,
				},
				{
					policyRef: types.NamespacedName{
						Name:      "allow-egress",
						Namespace: "egr",
					},
					policy: &egressPolicy,
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "metaverse",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "metaverse",
							Labels: map[string]string{
								"scope": "ns",
							},
						},
					},
				},
			},
		},
		{
			name: "pod labels match policy spec.PodSelector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
					Labels: map[string]string{
						"select":     "pods",
						"track":      "stable",
						"deployment": "staging",
					},
				},
			},
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "allow-ingress",
					Namespace: "ing",
				},
				{
					Name:      "allow-egress",
					Namespace: "egr",
				},
				{
					Name:      "access-policy",
					Namespace: "metaverse",
				},
			},
			policyListCalls: []policyListCall{
				{
					policies: []networking.NetworkPolicy{
						accessPolicy,
					},
				},
			},
			policyGetCalls: []policyGetCall{
				{
					policyRef: types.NamespacedName{
						Name:      "allow-ingress",
						Namespace: "ing",
					},
					policy: &ingressPolicy,
				},
				{
					policyRef: types.NamespacedName{
						Name:      "allow-egress",
						Namespace: "egr",
					},
					policy: &egressPolicy,
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "metaverse",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "metaverse",
							Labels: map[string]string{
								"scope": "ns",
							},
						},
					},
				},
			},
			want: []networking.NetworkPolicy{
				accessPolicy,
			},
		},
		{
			name: "policy list returns error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
					Labels: map[string]string{
						"select":     "pods",
						"track":      "stable",
						"deployment": "staging",
					},
				},
			},
			policyListCalls: []policyListCall{
				{
					err: errors.New("list error"),
				},
			},
			wantErr: "failed to fetch policies: list error",
		},
		{
			name: "pod selected by the defined policies",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
					Labels: map[string]string{
						"select":     "pods",
						"track":      "stable",
						"deployment": "staging",
						"ingress":    "allow",
						"egress":     "allow",
					},
				},
			},
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "allow-ingress",
					Namespace: "ing",
				},
				{
					Name:      "allow-egress",
					Namespace: "egr",
				},
			},
			policyListCalls: []policyListCall{
				{
					policies: []networking.NetworkPolicy{
						accessPolicy,
					},
				},
			},
			policyGetCalls: []policyGetCall{
				{
					policyRef: types.NamespacedName{
						Name:      "allow-ingress",
						Namespace: "ing",
					},
					policy: &ingressPolicy,
				},
				{
					policyRef: types.NamespacedName{
						Name:      "allow-egress",
						Namespace: "egr",
					},
					policy: &egressPolicy,
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "metaverse",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "metaverse",
							Labels: map[string]string{
								"scope": "ns",
							},
						},
					},
				},
			},
			want: []networking.NetworkPolicy{
				accessPolicy,
				ingressPolicy,
				egressPolicy,
			},
		},
		{
			name: "podOld selected by the defined policies",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
					Labels: map[string]string{
						"new": "labels",
					},
				},
			},
			podOld: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
					Labels: map[string]string{
						"select":     "pods",
						"track":      "stable",
						"deployment": "staging",
						"ingress":    "allow",
						"egress":     "allow",
					},
				},
			},
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "allow-ingress",
					Namespace: "ing",
				},
				{
					Name:      "allow-egress",
					Namespace: "egr",
				},
			},
			policyListCalls: []policyListCall{
				{
					policies: []networking.NetworkPolicy{
						accessPolicy,
					},
				},
			},
			policyGetCalls: []policyGetCall{
				{
					policyRef: types.NamespacedName{
						Name:      "allow-ingress",
						Namespace: "ing",
					},
					policy: &ingressPolicy,
				},
				{
					policyRef: types.NamespacedName{
						Name:      "allow-egress",
						Namespace: "egr",
					},
					policy: &egressPolicy,
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "metaverse",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "metaverse",
							Labels: map[string]string{
								"scope": "ns",
							},
						},
					},
				},
			},
			want: []networking.NetworkPolicy{
				accessPolicy,
				ingressPolicy,
				egressPolicy,
			},
		},
		{
			name: "policy get error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "frontend",
					Namespace: "metaverse",
					Labels: map[string]string{
						"select":     "pods",
						"track":      "stable",
						"deployment": "staging",
						"ingress":    "allow",
						"egress":     "allow",
					},
				},
			},
			trackedPolicies: []types.NamespacedName{
				{
					Name:      "allow-ingress",
					Namespace: "ing",
				},
				{
					Name:      "allow-egress",
					Namespace: "egr",
				},
			},
			policyListCalls: []policyListCall{
				{
					policies: []networking.NetworkPolicy{
						accessPolicy,
					},
				},
			},
			policyGetCalls: []policyGetCall{
				{
					policyRef: types.NamespacedName{
						Name:      "allow-ingress",
						Namespace: "ing",
					},
					policy: &ingressPolicy,
				},
				{
					policyRef: types.NamespacedName{
						Name:      "allow-egress",
						Namespace: "egr",
					},
					err: errors.New("get error"),
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "metaverse",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "metaverse",
							Labels: map[string]string{
								"scope": "ns",
							},
						},
					},
				},
			},
			wantErr: "failed to get policy: get error",
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
			for _, item := range tt.nsGetCalls {
				call := item
				mockClient.EXPECT().Get(gomock.Any(), call.nsRef, gomock.Any(), client.UnsafeDisableDeepCopy).DoAndReturn(
					func(ctx context.Context, key types.NamespacedName, ns *corev1.Namespace, opts ...client.GetOption) error {
						if call.ns != nil {
							*ns = *call.ns
						}
						return call.err
					},
				).AnyTimes()
			}
			for _, item := range tt.policyGetCalls {
				call := item
				mockClient.EXPECT().Get(gomock.Any(), call.policyRef, gomock.Any(), client.UnsafeDisableDeepCopy).DoAndReturn(
					func(ctx context.Context, key types.NamespacedName, policy *networking.NetworkPolicy, opts ...client.GetOption) error {
						if call.policy != nil {
							*policy = *call.policy
						}
						return call.err
					},
				).AnyTimes()
			}
			for _, item := range tt.policyListCalls {
				call := item
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), client.InNamespace(tt.pod.Namespace), client.UnsafeDisableDeepCopy).DoAndReturn(
					func(ctx context.Context, policyList *networking.NetworkPolicyList, opts ...client.ListOption) error {
						for _, policy := range call.policies {
							policyList.Items = append(policyList.Items, *(policy.DeepCopy()))
						}
						return call.err
					},
				)
			}

			got, err := policyResolver.GetReferredPoliciesForPod(context.Background(), tt.pod, tt.podOld)
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

func TestPolicyReferenceResolver_isPodMatchesPolicySelector(t *testing.T) {
	tests := []struct {
		name   string
		pod    *corev1.Pod
		podOld *corev1.Pod
		policy *networking.NetworkPolicy
		want   bool
	}{
		{
			name: "empty pod labels and policy pod selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "access-policy",
				},
				Spec: networking.NetworkPolicySpec{},
			},
			want: true,
		},
		{
			name: "pod labels and empty policy pod selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "stable",
						"tier":   "backend",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "access-policy",
				},
				Spec: networking.NetworkPolicySpec{},
			},
			want: true,
		},
		{
			name: "pod labels match policy pod selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "stable",
						"tier":   "backend",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "access-policy",
				},
				Spec: networking.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"select": "pods",
							"track":  "stable",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "pod labels mismatch policy pod selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "stable",
						"tier":   "backend",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "access-policy",
				},
				Spec: networking.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"select": "pods",
							"track":  "stable",
						},
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "track",
								Operator: "NotIn",
								Values:   []string{"stable", "prerelease", "beta"},
							},
						},
					},
				},
			},
		},
		{
			name: "policy label selector invalid",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "stable",
						"tier":   "backend",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "access-policy",
				},
				Spec: networking.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"select":        "pods",
							"invalid track": "stable",
						},
					},
				},
			},
		},
		{
			name: "old pod matches policy podSelector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "stable",
						"tier":   "backend",
					},
				},
			},
			podOld: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "pre-release",
						"tier":   "backend",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "access-policy",
				},
				Spec: networking.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"select": "pods",
							"track":  "pre-release",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "either pods don't match policy podSelector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "stable",
						"tier":   "backend",
					},
				},
			},
			podOld: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "app-pod",
					Labels: map[string]string{
						"select": "pods",
						"track":  "pre-release",
						"tier":   "backend",
					},
				},
			},
			policy: &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "access-policy",
				},
				Spec: networking.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"select": "pods",
							"track":  "alpha",
						},
					},
				},
			},
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
			got := policyResolver.isPodMatchesPolicySelector(tt.pod, tt.podOld, tt.policy)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPolicyReferenceResolver_isPodLabelMatchPeer(t *testing.T) {
	type namespaceGetCall struct {
		nsRef types.NamespacedName
		ns    *corev1.Namespace
		err   error
	}
	tests := []struct {
		name       string
		nsGetCalls []namespaceGetCall
		pod        *corev1.Pod
		peer       *networking.NetworkPolicyPeer
		namespace  string
		want       bool
	}{
		{
			name: "no match",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "no-selector",
				},
			},
			peer: &networking.NetworkPolicyPeer{},
		},
		{
			name: "no ns selector and pod in a different namespace",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				PodSelector: &metav1.LabelSelector{},
			},
		},
		{
			name: "no ns selector and pod the same ns",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "pod",
					},
				},
			},
			namespace: "application",
			want:      true,
		},
		{
			name: "with ns selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "namespace",
					},
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "application",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "application",
							Labels: map[string]string{
								"select": "namespace",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "with ns selector, get error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "namespace",
					},
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "application",
					},
					err: errors.New("unable to get namespace"),
				},
			},
		},
		{
			name: "with no matching ns selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "something else",
					},
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "application",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "application",
							Labels: map[string]string{
								"select": "namespace",
							},
						},
					},
				},
			},
		},
		{
			name: "with matching pod and namespace selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "namespace",
					},
				},
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "pod",
					},
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "application",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "application",
							Labels: map[string]string{
								"select": "namespace",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "namespace selector matches but pod selector doesn't",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "app.pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "namespace",
					},
				},
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "pod",
					},
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "application",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "application",
							Labels: map[string]string{
								"select": "namespace",
							},
						},
					},
				},
			},
		},
		{
			name: "namespace selector matches but pod label selector error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "app.pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select": "namespace",
					},
				},
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select invalid pod": "pod",
					},
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "application",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "application",
							Labels: map[string]string{
								"select": "namespace",
							},
						},
					},
				},
			},
		},
		{
			name: "namespace selector error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "application",
					Labels: map[string]string{
						"select": "app.pod",
					},
				},
			},
			peer: &networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"select invalid": "namespace",
					},
				},
			},
			nsGetCalls: []namespaceGetCall{
				{
					nsRef: types.NamespacedName{
						Name: "application",
					},
					ns: &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "application",
							Labels: map[string]string{
								"select": "namespace",
							},
						},
					},
				},
			},
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

			policyReferenceResolver := &defaultPolicyReferenceResolver{
				k8sClient:     mockClient,
				policyTracker: policyTracker,
				logger:        nullLogger,
			}
			for _, item := range tt.nsGetCalls {
				call := item
				mockClient.EXPECT().Get(gomock.Any(), call.nsRef, gomock.Any(), client.UnsafeDisableDeepCopy).DoAndReturn(
					func(ctx context.Context, key types.NamespacedName, ns *corev1.Namespace, opts ...client.GetOption) error {
						if call.ns != nil {
							*ns = *call.ns
						}
						return call.err
					},
				)
			}
			got := policyReferenceResolver.isPodLabelMatchPeer(context.Background(), tt.pod, tt.peer, tt.namespace)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPolicyReferenceResolver_GetReferredClusterPoliciesForPod(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	nullLogger := logr.New(logr.Discard().GetSink())

	cnp1 := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "cnp1"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{},
			},
		},
	}
	cnp2 := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "cnp2"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Pods: &policyinfo.NamespacedPod{
					NamespaceSelector: metav1.LabelSelector{},
					PodSelector:       metav1.LabelSelector{},
				},
			},
		},
	}

	cnpList := &policyinfo.ClusterNetworkPolicyList{
		Items: []policyinfo.ClusterNetworkPolicy{*cnp1, *cnp2},
	}

	mockClient.EXPECT().List(gomock.Any(), gomock.Any(), client.UnsafeDisableDeepCopy).DoAndReturn(
		func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			list.(*policyinfo.ClusterNetworkPolicyList).Items = cnpList.Items
			return nil
		})

	resolver := &defaultPolicyReferenceResolver{
		k8sClient: mockClient,
		logger:    nullLogger,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	policies, err := resolver.GetReferredClusterPoliciesForPod(context.Background(), pod, nil)

	assert.NoError(t, err)
	assert.Len(t, policies, 2)

	policyNames := make([]string, len(policies))
	for i, p := range policies {
		policyNames[i] = p.Name
	}
	assert.Contains(t, policyNames, "cnp1")
	assert.Contains(t, policyNames, "cnp2")
}
func TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod(t *testing.T) {
	type policyGetCall struct {
		policyRef types.NamespacedName
		policy    *policyinfo.ApplicationNetworkPolicy
		err       error
	}

	tests := []struct {
		name                      string
		pod                       *corev1.Pod
		podOld                    *corev1.Pod
		policyList                *policyinfo.ApplicationNetworkPolicyList
		listErr                   error
		trackedNamespacedPolicies []types.NamespacedName
		policyGetCalls            []policyGetCall
		want                      []policyinfo.ApplicationNetworkPolicy
		wantErr                   string
	}{
		{
			name: "no policies defined",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
			},
			policyList: &policyinfo.ApplicationNetworkPolicyList{},
		},
		{
			name: "no matching policies",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policyList: &policyinfo.ApplicationNetworkPolicyList{
				Items: []policyinfo.ApplicationNetworkPolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: "default"},
						Spec: policyinfo.ApplicationNetworkPolicySpec{
							PodSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{"app": "different"},
							},
						},
					},
				},
			},
		},
		{
			name: "pod labels match policy spec.PodSelector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policyList: &policyinfo.ApplicationNetworkPolicyList{
				Items: []policyinfo.ApplicationNetworkPolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: "default"},
						Spec: policyinfo.ApplicationNetworkPolicySpec{
							PodSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{"app": "test"},
							},
						},
					},
				},
			},
			want: []policyinfo.ApplicationNetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: "default"},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test"},
						},
					},
				},
			},
		},
		{
			name: "policy list returns error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
			},
			listErr: errors.New("list error"),
			wantErr: "failed to fetch ANP policies: list error",
		},
		{
			name: "pod selected by the defined policies",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policyList: &policyinfo.ApplicationNetworkPolicyList{
				Items: []policyinfo.ApplicationNetworkPolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: "default"},
						Spec: policyinfo.ApplicationNetworkPolicySpec{
							PodSelector: metav1.LabelSelector{},
							Ingress: []networking.NetworkPolicyIngressRule{
								{
									From: []networking.NetworkPolicyPeer{
										{
											PodSelector: &metav1.LabelSelector{
												MatchLabels: map[string]string{"app": "test"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: []policyinfo.ApplicationNetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: "default"},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{},
						Ingress: []networking.NetworkPolicyIngressRule{
							{
								From: []networking.NetworkPolicyPeer{
									{
										PodSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{"app": "test"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "podOld selected by the defined policies",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "new"},
				},
			},
			podOld: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policyList: &policyinfo.ApplicationNetworkPolicyList{
				Items: []policyinfo.ApplicationNetworkPolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: "default"},
						Spec: policyinfo.ApplicationNetworkPolicySpec{
							PodSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{"app": "test"},
							},
						},
					},
				},
			},
			want: []policyinfo.ApplicationNetworkPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: "default"},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test"},
						},
					},
				},
			},
		},
		{
			name: "policy get error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policyList: &policyinfo.ApplicationNetworkPolicyList{},
			trackedNamespacedPolicies: []types.NamespacedName{
				{Namespace: "default", Name: "policy1"},
			},
			policyGetCalls: []policyGetCall{
				{
					policyRef: types.NamespacedName{Namespace: "default", Name: "policy1"},
					err:       errors.New("get error"),
				},
			},
			wantErr: "failed to get ANP policy: get error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockClient := mock_client.NewMockClient(ctrl)
			policyTracker := NewPolicyTracker(log.Log)

			// Setup policy tracker
			for _, policy := range tt.trackedNamespacedPolicies {
				anp := &policyinfo.ApplicationNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: policy.Name, Namespace: policy.Namespace},
					Spec: policyinfo.ApplicationNetworkPolicySpec{
						Ingress: []networking.NetworkPolicyIngressRule{
							{
								From: []networking.NetworkPolicyPeer{
									{NamespaceSelector: &metav1.LabelSelector{}},
								},
							},
						},
					},
				}
				policyTracker.UpdateGenericPolicy(anp)
			}

			mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any(), client.UnsafeDisableDeepCopy).DoAndReturn(
				func(ctx context.Context, list *policyinfo.ApplicationNetworkPolicyList, opts ...client.ListOption) error {
					if tt.policyList != nil {
						*list = *tt.policyList
					}
					return tt.listErr
				},
			).AnyTimes()

			for _, call := range tt.policyGetCalls {
				mockClient.EXPECT().Get(gomock.Any(), call.policyRef, gomock.Any(), client.UnsafeDisableDeepCopy).DoAndReturn(
					func(ctx context.Context, key types.NamespacedName, obj *policyinfo.ApplicationNetworkPolicy, opts ...client.GetOption) error {
						if call.policy != nil {
							*obj = *call.policy
						}
						return call.err
					},
				).AnyTimes()
			}

			policyResolver := NewPolicyReferenceResolver(mockClient, policyTracker, log.Log)
			got, err := policyResolver.GetReferredApplicationNetworkPoliciesForPod(context.Background(), tt.pod, tt.podOld)
			if len(tt.wantErr) > 0 {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestPolicyReferenceResolver_isPodMatchesANPPolicySelector(t *testing.T) {
	tests := []struct {
		name   string
		pod    *corev1.Pod
		podOld *corev1.Pod
		policy *policyinfo.ApplicationNetworkPolicy
		want   bool
	}{
		{
			name: "empty pod labels and policy pod selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
			},
			policy: &policyinfo.ApplicationNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
				Spec: policyinfo.ApplicationNetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
				},
			},
			want: true,
		},
		{
			name: "pod labels match policy pod selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policy: &policyinfo.ApplicationNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
				Spec: policyinfo.ApplicationNetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
				},
			},
			want: true,
		},
		{
			name: "pod labels mismatch policy pod selector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policy: &policyinfo.ApplicationNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
				Spec: policyinfo.ApplicationNetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "different"},
					},
				},
			},
			want: false,
		},
		{
			name: "old pod matches policy podSelector",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "new"},
				},
			},
			podOld: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "default",
					Labels:    map[string]string{"app": "test"},
				},
			},
			policy: &policyinfo.ApplicationNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
				Spec: policyinfo.ApplicationNetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policyResolver := &defaultPolicyReferenceResolver{
				logger: log.Log,
			}
			got := policyResolver.isPodMatchesANPPolicySelector(tt.pod, tt.podOld, tt.policy)
			assert.Equal(t, tt.want, got)
		})
	}
}
