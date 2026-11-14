package resolvers

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
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
				// Track each policy with its spec, as the policy event handler does. A policy the test cannot
				// return from Get is tracked as selecting every namespace, so that the resolver reads it.
				policy := &networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace},
					Spec: networking.NetworkPolicySpec{Ingress: []networking.NetworkPolicyIngressRule{{
						From: []networking.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}}},
					}}},
				}
				for _, call := range tt.policyGetCalls {
					if call.policyRef == ref && call.policy != nil {
						policy = call.policy
					}
				}
				require.Equal(t, ref, k8s.NamespacedName(policy))
				policyTracker.UpdatePolicy(policy)
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
			// Policies in other namespaces are matched against the pod's namespace labels, read once per call. An
			// unlabelled namespace is matched by the empty namespaceSelector the tracked ANPs are given above.
			mockClient.EXPECT().Get(gomock.Any(), types.NamespacedName{Name: tt.pod.Namespace},
				gomock.AssignableToTypeOf(&corev1.Namespace{}), client.UnsafeDisableDeepCopy).Return(nil).MaxTimes(1)

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

// The tests below cover matching a pod to policies in other namespaces through namespaceSelector peers, with a fake
// client. The table and randomized tests also run referenceGetReferredPoliciesForPod (or the ANP version), which is
// equivalent to the per-event scan this matching replaced, and require the same result.

// referenceGetReferredPoliciesForPod is equivalent to the NetworkPolicy pod path before namespaceSelector peers were
// parsed by the policy tracker: every policy tracked with namespace references is read, and every peer is matched from
// the policy object, reading the pod's namespace once per peer.
func referenceGetReferredPoliciesForPod(ctx context.Context, r *defaultPolicyReferenceResolver,
	pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
	policyList := &networking.NetworkPolicyList{}
	err := r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace), client.UnsafeDisableDeepCopy)
	if err != nil {
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
	for ref := range r.policyTracker.GetPoliciesWithNamespaceReferences() {
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
		if r.isPodReferredOnIngressEgress(ctx, pod, policy) ||
			(podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, policy)) {
			out = append(out, *policy)
		}
	}
	return out, nil
}

// referenceGetReferredANPsForPod is referenceGetReferredPoliciesForPod for ApplicationNetworkPolicies.
func referenceGetReferredANPsForPod(ctx context.Context, r *defaultPolicyReferenceResolver,
	pod, podOld *corev1.Pod) ([]policyinfo.ApplicationNetworkPolicy, error) {
	policyList := &policyinfo.ApplicationNetworkPolicyList{}
	err := r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace), client.UnsafeDisableDeepCopy)
	if err != nil {
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
	for ref := range r.policyTracker.GetApplicationNetworkPoliciesWithNamespaceReferences() {
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
		if r.isPodReferredOnANPIngressEgress(ctx, pod, policy) ||
			(podOld != nil && r.isPodReferredOnANPIngressEgress(ctx, podOld, policy)) {
			out = append(out, *policy)
		}
	}
	return out, nil
}

func newFakePolicyReferenceResolver(t testing.TB, interceptors interceptor.Funcs,
	objs ...client.Object) (*defaultPolicyReferenceResolver, *defaultPolicyTracker) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, policyinfo.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(interceptors).Build()
	nullLogger := logr.New(&log.NullLogSink{})
	tracker := &defaultPolicyTracker{logger: nullLogger}
	return &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: nullLogger}, tracker
}

func labelSelector(kv ...string) *metav1.LabelSelector {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return &metav1.LabelSelector{MatchLabels: m}
}

func expressionSelector(key string, op metav1.LabelSelectorOperator, values ...string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: key, Operator: op, Values: values},
	}}
}

func namespaceWithLabels(name string, l map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l}}
}

func podWithLabels(namespace, name string, l map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: l},
		Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
	}
}

func policyPeers(peers ...networking.NetworkPolicyPeer) []networking.NetworkPolicyPeer {
	return peers
}

func namespacePeer(nsSelector, podSelector *metav1.LabelSelector) networking.NetworkPolicyPeer {
	return networking.NetworkPolicyPeer{NamespaceSelector: nsSelector, PodSelector: podSelector}
}

// networkPolicyWithPeers returns a policy that selects app=server pods, with one ingress rule from the ingress peers
// and one egress rule to the egress peers, each omitted when there are none.
func networkPolicyWithPeers(namespace, name string,
	ingress, egress []networking.NetworkPolicyPeer) *networking.NetworkPolicy {
	p := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       networking.NetworkPolicySpec{PodSelector: *labelSelector("app", "server")},
	}
	if len(ingress) > 0 {
		p.Spec.Ingress = []networking.NetworkPolicyIngressRule{{From: ingress}}
	}
	if len(egress) > 0 {
		p.Spec.Egress = []networking.NetworkPolicyEgressRule{{To: egress}}
	}
	return p
}

// anpWithPeers is networkPolicyWithPeers for an ApplicationNetworkPolicy.
func anpWithPeers(namespace, name string, ingress []networking.NetworkPolicyPeer,
	egress []policyinfo.ApplicationNetworkPolicyPeer) *policyinfo.ApplicationNetworkPolicy {
	p := &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       policyinfo.ApplicationNetworkPolicySpec{PodSelector: *labelSelector("app", "server")},
	}
	if len(ingress) > 0 {
		p.Spec.Ingress = []networking.NetworkPolicyIngressRule{{From: ingress}}
	}
	if len(egress) > 0 {
		p.Spec.Egress = []policyinfo.ApplicationNetworkPolicyEgressRule{{To: egress}}
	}
	return p
}

func sortedPolicyNames(ps []networking.NetworkPolicy) []string {
	out := []string{}
	for i := range ps {
		out = append(out, k8s.NamespacedName(&ps[i]).String())
	}
	sort.Strings(out)
	return out
}

func sortedANPNames(ps []policyinfo.ApplicationNetworkPolicy) []string {
	out := []string{}
	for i := range ps {
		out = append(out, k8s.NamespacedName(&ps[i]).String())
	}
	sort.Strings(out)
	return out
}

func TestPolicyReferenceResolver_GetReferredPoliciesForPod_namespaceSelectorPeers(t *testing.T) {
	var (
		teamA   = labelSelector("team", "a")
		teamB   = labelSelector("team", "b")
		appWeb  = labelSelector("app", "web")
		appDB   = labelSelector("app", "db")
		all     = &metav1.LabelSelector{}
		invalid = expressionSelector("team", metav1.LabelSelectorOpIn) // In without values does not parse
		web     = map[string]string{"app": "web"}
	)
	ingress := func(peers ...networking.NetworkPolicyPeer) []*networking.NetworkPolicy {
		return []*networking.NetworkPolicy{networkPolicyWithPeers("servers", "p", peers, nil)}
	}
	tests := []struct {
		name     string
		policies []*networking.NetworkPolicy
		pod      *corev1.Pod
		podOld   *corev1.Pod
		want     []string
	}{
		{
			name:     "namespaceSelector and podSelector both match",
			policies: ingress(namespacePeer(teamA, appWeb)),
			pod:      podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "namespaceSelector matches, podSelector does not",
			policies: ingress(namespacePeer(teamA, appDB)),
			pod:      podWithLabels("clients", "c", web),
		},
		{
			name:     "podSelector matches, namespaceSelector does not",
			policies: ingress(namespacePeer(teamB, appWeb)),
			pod:      podWithLabels("clients", "c", web),
		},
		{
			name:     "namespaceSelector without podSelector selects every pod in the namespace",
			policies: ingress(namespacePeer(teamA, nil)),
			pod:      podWithLabels("clients", "c", nil),
			want:     []string{"servers/p"},
		},
		{
			name:     "empty podSelector selects every pod in the namespace",
			policies: ingress(namespacePeer(teamA, all)),
			pod:      podWithLabels("clients", "c", nil),
			want:     []string{"servers/p"},
		},
		{
			name:     "empty namespaceSelector selects a namespace with no labels",
			policies: ingress(namespacePeer(all, nil)),
			pod:      podWithLabels("bare", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "namespaceSelector with labels does not select a namespace with no labels",
			policies: ingress(namespacePeer(teamA, nil)),
			pod:      podWithLabels("bare", "c", web),
		},
		{
			name:     "pod in a namespace missing from the cache matches no namespaceSelector peer",
			policies: ingress(namespacePeer(all, nil)),
			pod:      podWithLabels("missing", "c", web),
		},
		{
			name:     "matchExpressions In",
			policies: ingress(namespacePeer(expressionSelector("team", metav1.LabelSelectorOpIn, "a", "b"), nil)),
			pod:      podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "matchExpressions NotIn excludes the namespace",
			policies: ingress(namespacePeer(expressionSelector("team", metav1.LabelSelectorOpNotIn, "a"), nil)),
			pod:      podWithLabels("clients", "c", web),
		},
		{
			name:     "matchExpressions Exists",
			policies: ingress(namespacePeer(expressionSelector("env", metav1.LabelSelectorOpExists), nil)),
			pod:      podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "matchExpressions DoesNotExist on the pod selector",
			policies: ingress(namespacePeer(teamA, expressionSelector("tier", metav1.LabelSelectorOpDoesNotExist))),
			pod:      podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "invalid namespaceSelector never matches",
			policies: ingress(namespacePeer(invalid, nil)),
			pod:      podWithLabels("clients", "c", web),
		},
		{
			name:     "invalid podSelector never matches",
			policies: ingress(namespacePeer(teamA, invalid)),
			pod:      podWithLabels("clients", "c", web),
		},
		{
			name:     "invalid peer does not hide a valid peer in the same rule",
			policies: ingress(namespacePeer(invalid, nil), namespacePeer(teamA, nil)),
			pod:      podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name: "egress peer",
			policies: []*networking.NetworkPolicy{
				networkPolicyWithPeers("servers", "p", nil, policyPeers(namespacePeer(teamA, appWeb))),
			},
			pod:  podWithLabels("clients", "c", web),
			want: []string{"servers/p"},
		},
		{
			name: "match on a later rule and a later peer",
			policies: []*networking.NetworkPolicy{{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "servers"},
				Spec: networking.NetworkPolicySpec{Ingress: []networking.NetworkPolicyIngressRule{
					{From: []networking.NetworkPolicyPeer{
						namespacePeer(teamB, nil),
						{IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"}},
					}},
					{From: []networking.NetworkPolicyPeer{
						{PodSelector: appWeb},
						namespacePeer(labelSelector("env", "prod"), appWeb),
					}},
				}},
			}},
			pod:  podWithLabels("clients", "c", web),
			want: []string{"servers/p"},
		},
		{
			name:     "only the old labels match, so the pod must be removed from the policy",
			policies: ingress(namespacePeer(teamA, appWeb)),
			pod:      podWithLabels("clients", "c", map[string]string{"app": "other"}),
			podOld:   podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name:     "neither the old nor the new labels match",
			policies: ingress(namespacePeer(teamA, appWeb)),
			pod:      podWithLabels("clients", "c", map[string]string{"app": "other"}),
			podOld:   podWithLabels("clients", "c", map[string]string{"app": "older"}),
		},
		{
			name:     "podSelector-only peer of a policy in another namespace does not match",
			policies: ingress(namespacePeer(teamB, nil), networking.NetworkPolicyPeer{PodSelector: appWeb}),
			pod:      podWithLabels("clients", "c", web),
		},
		{
			name: "policy in the pod's own namespace is returned once",
			policies: []*networking.NetworkPolicy{
				networkPolicyWithPeers("clients", "p", policyPeers(namespacePeer(teamA, nil)), nil),
			},
			pod:  podWithLabels("clients", "c", web),
			want: []string{"clients/p"},
		},
		{
			name: "only the matching policies are returned",
			policies: []*networking.NetworkPolicy{
				networkPolicyWithPeers("servers", "match-ingress", policyPeers(namespacePeer(teamA, nil)), nil),
				networkPolicyWithPeers("servers", "other-team", policyPeers(namespacePeer(teamB, nil)), nil),
				networkPolicyWithPeers("db", "match-egress", nil, policyPeers(namespacePeer(all, appWeb))),
				networkPolicyWithPeers("db", "other-app", nil, policyPeers(namespacePeer(all, appDB))),
			},
			pod:  podWithLabels("clients", "c", web),
			want: []string{"db/match-egress", "servers/match-ingress"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{
				namespaceWithLabels("clients", map[string]string{"team": "a", "env": "prod"}),
				namespaceWithLabels("bare", nil),
			}
			for _, p := range tt.policies {
				objs = append(objs, p)
			}
			r, tracker := newFakePolicyReferenceResolver(t, interceptor.Funcs{}, objs...)
			for _, p := range tt.policies {
				tracker.UpdatePolicy(p)
			}
			want := tt.want
			if want == nil {
				want = []string{}
			}

			before, err := referenceGetReferredPoliciesForPod(context.Background(), r, tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, sortedPolicyNames(before), "per-event scan")

			got, err := r.GetReferredPoliciesForPod(context.Background(), tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, sortedPolicyNames(got))
		})
	}
}

func TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_namespaceSelectorPeers(t *testing.T) {
	var (
		teamA  = labelSelector("team", "a")
		appWeb = labelSelector("app", "web")
		all    = &metav1.LabelSelector{}
		web    = map[string]string{"app": "web"}
	)
	egress := func(peers ...policyinfo.ApplicationNetworkPolicyPeer) []*policyinfo.ApplicationNetworkPolicy {
		return []*policyinfo.ApplicationNetworkPolicy{anpWithPeers("servers", "p", nil, peers)}
	}
	tests := []struct {
		name     string
		policies []*policyinfo.ApplicationNetworkPolicy
		pod      *corev1.Pod
		podOld   *corev1.Pod
		want     []string
	}{
		{
			name: "ingress peer",
			policies: []*policyinfo.ApplicationNetworkPolicy{
				anpWithPeers("servers", "p", policyPeers(namespacePeer(teamA, appWeb)), nil),
			},
			pod:  podWithLabels("clients", "c", web),
			want: []string{"servers/p"},
		},
		{
			name:     "egress peer",
			policies: egress(policyinfo.ApplicationNetworkPolicyPeer{NamespaceSelector: teamA, PodSelector: appWeb}),
			pod:      podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name: "egress peer with a podSelector that does not match",
			policies: egress(policyinfo.ApplicationNetworkPolicyPeer{
				NamespaceSelector: teamA, PodSelector: labelSelector("app", "db"),
			}),
			pod: podWithLabels("clients", "c", web),
		},
		{
			name:     "egress namespaceSelector without podSelector selects every pod in the namespace",
			policies: egress(policyinfo.ApplicationNetworkPolicyPeer{NamespaceSelector: teamA}),
			pod:      podWithLabels("clients", "c", nil),
			want:     []string{"servers/p"},
		},
		{
			name:     "empty namespaceSelector selects a namespace with no labels",
			policies: egress(policyinfo.ApplicationNetworkPolicyPeer{NamespaceSelector: all}),
			pod:      podWithLabels("bare", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name: "pod in a namespace missing from the cache matches no namespaceSelector peer",
			policies: []*policyinfo.ApplicationNetworkPolicy{
				anpWithPeers("servers", "p", policyPeers(namespacePeer(all, nil)), nil),
			},
			pod: podWithLabels("missing", "c", web),
		},
		{
			name: "domainNames peer is not a namespace reference",
			policies: egress(policyinfo.ApplicationNetworkPolicyPeer{
				DomainNames: []policyinfo.DomainName{"example.com"},
			}),
			pod: podWithLabels("clients", "c", web),
		},
		{
			name:     "only the old labels match",
			policies: egress(policyinfo.ApplicationNetworkPolicyPeer{NamespaceSelector: teamA, PodSelector: appWeb}),
			pod:      podWithLabels("clients", "c", map[string]string{"app": "other"}),
			podOld:   podWithLabels("clients", "c", web),
			want:     []string{"servers/p"},
		},
		{
			name: "only the matching policies are returned",
			policies: []*policyinfo.ApplicationNetworkPolicy{
				anpWithPeers("servers", "match", policyPeers(namespacePeer(teamA, nil)), nil),
				anpWithPeers("servers", "other-team", []networking.NetworkPolicyPeer{
					namespacePeer(labelSelector("team", "b"), nil),
				}, nil),
			},
			pod:  podWithLabels("clients", "c", web),
			want: []string{"servers/match"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{
				namespaceWithLabels("clients", map[string]string{"team": "a"}),
				namespaceWithLabels("bare", nil),
			}
			for _, p := range tt.policies {
				objs = append(objs, p)
			}
			r, tracker := newFakePolicyReferenceResolver(t, interceptor.Funcs{}, objs...)
			for _, p := range tt.policies {
				tracker.UpdateGenericPolicy(p)
			}
			want := tt.want
			if want == nil {
				want = []string{}
			}

			before, err := referenceGetReferredANPsForPod(context.Background(), r, tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, sortedANPNames(before), "per-event scan")

			got, err := r.GetReferredApplicationNetworkPoliciesForPod(context.Background(), tt.pod, tt.podOld)
			require.NoError(t, err)
			assert.Equal(t, want, sortedANPNames(got))
		})
	}
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_matchesPerEventScan compares the result with the per-event
// scan over random policy sets: namespaceSelector peers with and without a podSelector, podSelector-only and ipBlock
// peers, empty, matchExpressions and invalid selectors, unlabelled namespaces, a namespace missing from the cache, and
// label changes between the old and the new pod.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_matchesPerEventScan(t *testing.T) {
	rng := rand.New(rand.NewSource(20261113))
	for round := 0; round < 10; round++ {
		r, tracker, namespaces := newRandomPolicyReferenceResolver(t, rng)
		for _, p := range randomNetworkPolicies(rng, namespaces, 30) {
			require.NoError(t, r.k8sClient.Create(context.Background(), p))
			tracker.UpdatePolicy(p)
		}
		for i, ev := range randomPodEvents(rng, namespaces, 30) {
			want, err := referenceGetReferredPoliciesForPod(context.Background(), r, ev.pod, ev.podOld)
			require.NoError(t, err)
			got, err := r.GetReferredPoliciesForPod(context.Background(), ev.pod, ev.podOld)
			require.NoError(t, err)
			assert.Equal(t, sortedPolicyNames(want), sortedPolicyNames(got), "round %d pod %d %s labels=%v old=%v",
				round, i, k8s.NamespacedName(ev.pod), ev.pod.Labels, ev.podOld != nil)
		}
	}
}

// TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_matchesPerEventScan is the ANP version, where
// egress peers are ApplicationNetworkPolicyPeers and may carry domainNames.
func TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_matchesPerEventScan(t *testing.T) {
	rng := rand.New(rand.NewSource(20261114))
	for round := 0; round < 10; round++ {
		r, tracker, namespaces := newRandomPolicyReferenceResolver(t, rng)
		for _, np := range randomNetworkPolicies(rng, namespaces, 30) {
			anp := anpFromNetworkPolicy(rng, np)
			require.NoError(t, r.k8sClient.Create(context.Background(), anp))
			tracker.UpdateGenericPolicy(anp)
		}
		for i, ev := range randomPodEvents(rng, namespaces, 30) {
			want, err := referenceGetReferredANPsForPod(context.Background(), r, ev.pod, ev.podOld)
			require.NoError(t, err)
			got, err := r.GetReferredApplicationNetworkPoliciesForPod(context.Background(), ev.pod, ev.podOld)
			require.NoError(t, err)
			assert.Equal(t, sortedANPNames(want), sortedANPNames(got), "round %d pod %d %s labels=%v old=%v",
				round, i, k8s.NamespacedName(ev.pod), ev.pod.Labels, ev.podOld != nil)
		}
	}
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_readsOnlyMatchingPolicies checks the reads a pod event costs:
// the pod's namespace at most once, and only the policies that select the pod.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_readsOnlyMatchingPolicies(t *testing.T) {
	var gets []string
	count := interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey,
		obj client.Object, opts ...client.GetOption) error {
		gets = append(gets, fmt.Sprintf("%T %s", obj, key))
		return c.Get(ctx, key, obj, opts...)
	}}
	objs := []client.Object{namespaceWithLabels("clients", map[string]string{"team": "a"})}
	var policies []*networking.NetworkPolicy
	for i := 0; i < 50; i++ {
		team := "b"
		if i == 7 {
			team = "a"
		}
		p := networkPolicyWithPeers("servers", fmt.Sprintf("p%d", i), []networking.NetworkPolicyPeer{
			namespacePeer(labelSelector("team", team), nil),
		}, nil)
		policies = append(policies, p)
		objs = append(objs, p)
	}
	r, tracker := newFakePolicyReferenceResolver(t, count, objs...)
	for _, p := range policies {
		tracker.UpdatePolicy(p)
	}

	got, err := r.GetReferredPoliciesForPod(context.Background(), podWithLabels("clients", "c", nil), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/p7"}, sortedPolicyNames(got))
	assert.ElementsMatch(t, []string{"*v1.Namespace /clients", "*v1.NetworkPolicy servers/p7"}, gets)

	gets = nil
	got, err = r.GetReferredPoliciesForPod(context.Background(), podWithLabels("missing", "c", nil), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, []string{"*v1.Namespace /missing"}, gets, "a missing namespace stops the scan")
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_followsPolicyChanges checks that the matching follows spec
// updates and deletions, so that a stale entry can neither hide a new peer nor keep a removed one.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_followsPolicyChanges(t *testing.T) {
	ctx := context.Background()
	target := networkPolicyWithPeers("servers", "p", []networking.NetworkPolicyPeer{
		namespacePeer(labelSelector("team", "a"), labelSelector("app", "job")),
	}, nil)
	r, tracker := newFakePolicyReferenceResolver(t, interceptor.Funcs{},
		namespaceWithLabels("clients", map[string]string{"team": "a"}), target)
	tracker.UpdatePolicy(target)
	other := podWithLabels("clients", "o", map[string]string{"app": "other"})

	got, err := r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "pod not selected by the peer")

	widened := target.DeepCopy()
	widened.Spec.Ingress[0].From[0].PodSelector = nil
	require.NoError(t, r.k8sClient.Update(ctx, widened))
	tracker.UpdatePolicy(widened)
	got, err = r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/p"}, sortedPolicyNames(got), "widened peer")

	sameNamespaceOnly := widened.DeepCopy()
	sameNamespaceOnly.Spec.Ingress[0].From[0] = networking.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	require.NoError(t, r.k8sClient.Update(ctx, sameNamespaceOnly))
	tracker.UpdatePolicy(sameNamespaceOnly)
	got, err = r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "namespaceSelector removed")

	// Deleted from the cache before the policy event handler removes it from the tracker.
	tracker.UpdatePolicy(widened)
	require.NoError(t, r.k8sClient.Delete(ctx, widened))
	got, err = r.GetReferredPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "policy deleted from the cache")
}

// TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_followsPolicyChanges is the ANP version.
func TestPolicyReferenceResolver_GetReferredApplicationNetworkPoliciesForPod_followsPolicyChanges(t *testing.T) {
	ctx := context.Background()
	target := anpWithPeers("servers", "p", nil, []policyinfo.ApplicationNetworkPolicyPeer{
		{NamespaceSelector: labelSelector("team", "a"), PodSelector: labelSelector("app", "db")},
	})
	r, tracker := newFakePolicyReferenceResolver(t, interceptor.Funcs{},
		namespaceWithLabels("clients", map[string]string{"team": "a"}), target)
	tracker.UpdateGenericPolicy(target)
	other := podWithLabels("clients", "o", map[string]string{"app": "other"})

	got, err := r.GetReferredApplicationNetworkPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "pod not selected by the peer")

	widened := target.DeepCopy()
	widened.Spec.Egress[0].To[0].PodSelector = nil
	require.NoError(t, r.k8sClient.Update(ctx, widened))
	tracker.UpdateGenericPolicy(widened)
	got, err = r.GetReferredApplicationNetworkPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"servers/p"}, sortedANPNames(got), "widened peer")

	narrowed := widened.DeepCopy()
	narrowed.Spec.Egress[0].To[0].NamespaceSelector = labelSelector("team", "b")
	require.NoError(t, r.k8sClient.Update(ctx, narrowed))
	tracker.UpdateGenericPolicy(narrowed)
	got, err = r.GetReferredApplicationNetworkPoliciesForPod(ctx, other, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "narrowed peer")
}

// TestPolicyReferenceResolver_GetReferredPoliciesForPod_concurrentPolicyUpdates resolves pods while policies are
// updated and removed, as the policy and pod informers do on their own goroutines. Run with -race.
func TestPolicyReferenceResolver_GetReferredPoliciesForPod_concurrentPolicyUpdates(t *testing.T) {
	ctx := context.Background()
	teamA := policyPeers(namespacePeer(labelSelector("team", "a"), nil))
	matching := networkPolicyWithPeers("servers", "p", teamA, nil)
	notMatching := networkPolicyWithPeers("servers", "p", []networking.NetworkPolicyPeer{
		namespacePeer(labelSelector("team", "b"), nil),
	}, nil)
	anp := anpWithPeers("servers", "p", teamA, nil)
	r, tracker := newFakePolicyReferenceResolver(t, interceptor.Funcs{},
		namespaceWithLabels("clients", map[string]string{"team": "a"}), matching, anp)
	pod := podWithLabels("clients", "c", nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			switch i % 3 {
			case 0:
				tracker.UpdatePolicy(matching)
				tracker.UpdateGenericPolicy(anp)
			case 1:
				tracker.UpdatePolicy(notMatching)
			default:
				tracker.RemovePolicy(matching)
				tracker.RemoveGenericPolicy(anp)
			}
		}
	}()
	for i := 0; i < 2000; i++ {
		got, err := r.GetReferredPoliciesForPod(ctx, pod, nil)
		require.NoError(t, err)
		assert.Subset(t, []string{"servers/p"}, sortedPolicyNames(got))
		gotANP, err := r.GetReferredApplicationNetworkPoliciesForPod(ctx, pod, nil)
		require.NoError(t, err)
		assert.Subset(t, []string{"servers/p"}, sortedANPNames(gotANP))
	}
	wg.Wait()
}

// BenchmarkPolicyReferenceResolver_GetReferredPoliciesForPod resolves one pod event against 5,000 policies in other
// namespaces with 10 namespaceSelector peers each, over 500 namespaces. reference-scan is the per-event scan.
func BenchmarkPolicyReferenceResolver_GetReferredPoliciesForPod(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	var objs []client.Object
	namespaces := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		ns := namespaceWithLabels(fmt.Sprintf("ns-%d", i), map[string]string{"team": fmt.Sprintf("t%d", i%20)})
		namespaces = append(namespaces, ns.Name)
		objs = append(objs, ns)
	}
	var policies []*networking.NetworkPolicy
	for i := 0; i < 5000; i++ {
		var peers []networking.NetworkPolicyPeer
		for j := 0; j < 10; j++ {
			peers = append(peers, namespacePeer(labelSelector("team", fmt.Sprintf("t%d", rng.Intn(20))),
				labelSelector("app", fmt.Sprintf("a%d", rng.Intn(5000)))))
		}
		p := networkPolicyWithPeers(namespaces[rng.Intn(500)], fmt.Sprintf("p%d", i), peers, nil)
		p.Spec.PodSelector = *labelSelector("app", fmt.Sprintf("a%d", i))
		policies = append(policies, p)
		objs = append(objs, p)
	}
	pod := podWithLabels("ns-7", "j", map[string]string{"app": "a42"})

	b.Run("reference-scan", func(b *testing.B) {
		r, tracker := newFakePolicyReferenceResolver(b, interceptor.Funcs{}, objs...)
		for _, p := range policies {
			tracker.UpdatePolicy(p)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := referenceGetReferredPoliciesForPod(context.Background(), r, pod, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("tracked-peers", func(b *testing.B) {
		r, tracker := newFakePolicyReferenceResolver(b, interceptor.Funcs{}, objs...)
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

// newRandomPolicyReferenceResolver returns a resolver over four namespaces with random labels, possibly none.
func newRandomPolicyReferenceResolver(t *testing.T, rng *rand.Rand) (*defaultPolicyReferenceResolver,
	*defaultPolicyTracker, []string) {
	namespaces := []string{"ns-a", "ns-b", "ns-c", "ns-d"}
	var objs []client.Object
	for _, n := range namespaces {
		objs = append(objs, namespaceWithLabels(n, randomLabelSet(rng)))
	}
	r, tracker := newFakePolicyReferenceResolver(t, interceptor.Funcs{}, objs...)
	return r, tracker, namespaces
}

type randomPodEvent struct {
	pod, podOld *corev1.Pod
}

// randomPodEvents returns pod events in the given namespaces and in one missing from the cache; about half are
// updates whose old pod has different labels.
func randomPodEvents(rng *rand.Rand, namespaces []string, n int) []randomPodEvent {
	podNamespaces := append([]string{"ns-missing"}, namespaces...)
	out := make([]randomPodEvent, 0, n)
	for i := 0; i < n; i++ {
		ev := randomPodEvent{pod: podWithLabels(podNamespaces[rng.Intn(len(podNamespaces))], fmt.Sprintf("pod%d", i),
			randomLabelSet(rng))}
		if rng.Intn(2) == 0 {
			ev.podOld = ev.pod.DeepCopy()
			ev.podOld.Labels = randomLabelSet(rng)
		}
		out = append(out, ev)
	}
	return out
}

func randomLabelSelector(rng *rand.Rand) *metav1.LabelSelector {
	keys := []string{"team", "tier", "env"}
	vals := []string{"a", "b", "c"}
	switch rng.Intn(6) {
	case 0:
		return &metav1.LabelSelector{}
	case 1:
		return labelSelector(keys[rng.Intn(3)], vals[rng.Intn(3)])
	case 2:
		return expressionSelector(keys[rng.Intn(3)], metav1.LabelSelectorOpIn, vals[rng.Intn(3)], vals[rng.Intn(3)])
	case 3:
		return expressionSelector(keys[rng.Intn(3)], metav1.LabelSelectorOpNotIn, vals[rng.Intn(3)])
	case 4:
		return expressionSelector(keys[rng.Intn(3)], metav1.LabelSelectorOpDoesNotExist)
	default:
		return labelSelector(keys[rng.Intn(3)], vals[rng.Intn(3)], keys[rng.Intn(3)], vals[rng.Intn(3)])
	}
}

func randomNetworkPolicyPeer(rng *rand.Rand) networking.NetworkPolicyPeer {
	switch rng.Intn(6) {
	case 0:
		return namespacePeer(randomLabelSelector(rng), nil)
	case 1, 2:
		return namespacePeer(randomLabelSelector(rng), randomLabelSelector(rng))
	case 3:
		return networking.NetworkPolicyPeer{PodSelector: randomLabelSelector(rng)}
	case 4:
		return networking.NetworkPolicyPeer{IPBlock: &networking.IPBlock{CIDR: "10.0.0.0/8"}}
	default:
		// In without values does not parse.
		return namespacePeer(expressionSelector("team", metav1.LabelSelectorOpIn), randomLabelSelector(rng))
	}
}

func randomNetworkPolicies(rng *rand.Rand, namespaces []string, n int) []*networking.NetworkPolicy {
	port := intstr.FromInt32(8080)
	out := make([]*networking.NetworkPolicy, 0, n)
	for i := 0; i < n; i++ {
		p := &networking.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("p%d", i),
				Namespace: namespaces[rng.Intn(len(namespaces))],
			},
			Spec: networking.NetworkPolicySpec{PodSelector: *randomLabelSelector(rng)},
		}
		for k := 0; k < 1+rng.Intn(3); k++ {
			rule := networking.NetworkPolicyIngressRule{Ports: []networking.NetworkPolicyPort{{Port: &port}}}
			for j := 0; j < 1+rng.Intn(3); j++ {
				rule.From = append(rule.From, randomNetworkPolicyPeer(rng))
			}
			p.Spec.Ingress = append(p.Spec.Ingress, rule)
		}
		if rng.Intn(2) == 0 {
			rule := networking.NetworkPolicyEgressRule{}
			for j := 0; j < 1+rng.Intn(2); j++ {
				rule.To = append(rule.To, randomNetworkPolicyPeer(rng))
			}
			p.Spec.Egress = append(p.Spec.Egress, rule)
		}
		out = append(out, p)
	}
	return out
}

// anpFromNetworkPolicy converts a NetworkPolicy into an ApplicationNetworkPolicy and turns about a quarter of its
// egress peers into domainNames-only peers.
func anpFromNetworkPolicy(rng *rand.Rand, np *networking.NetworkPolicy) *policyinfo.ApplicationNetworkPolicy {
	anp := &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: *np.ObjectMeta.DeepCopy(),
		Spec:       policyinfo.ApplicationNetworkPolicySpec{PodSelector: np.Spec.PodSelector, Ingress: np.Spec.Ingress},
	}
	for _, rule := range np.Spec.Egress {
		out := policyinfo.ApplicationNetworkPolicyEgressRule{Ports: rule.Ports}
		for _, peer := range rule.To {
			if rng.Intn(4) == 0 {
				out.To = append(out.To, policyinfo.ApplicationNetworkPolicyPeer{
					DomainNames: []policyinfo.DomainName{"example.com"},
				})
				continue
			}
			out.To = append(out.To, policyinfo.ApplicationNetworkPolicyPeer{
				NamespaceSelector: peer.NamespaceSelector, PodSelector: peer.PodSelector, IPBlock: peer.IPBlock,
			})
		}
		anp.Spec.Egress = append(anp.Spec.Egress, out)
	}
	return anp
}

func randomLabelSet(rng *rand.Rand) map[string]string {
	m := map[string]string{}
	for _, k := range []string{"team", "tier", "env"} {
		if rng.Intn(2) == 0 {
			m[k] = []string{"a", "b", "c"}[rng.Intn(3)]
		}
	}
	return m
}
