package resolvers

// import (
// 	"context"
// 	"sort"
// 	"testing"

// 	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
// 	"github.com/go-logr/logr"
// 	"github.com/golang/mock/gomock"
// 	"github.com/pkg/errors"
// 	"github.com/stretchr/testify/assert"
// 	"github.com/stretchr/testify/require"
// 	networking "k8s.io/api/networking/v1"
// 	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
// 	"sigs.k8s.io/controller-runtime/pkg/client"
// 	"sigs.k8s.io/controller-runtime/pkg/log"

// 	corev1 "k8s.io/api/core/v1"
// 	"k8s.io/apimachinery/pkg/types"
// )

// func TestPolicyReferenceResolver_GetReferredPoliciesForService(t *testing.T) {
// 	type policyGetCall struct {
// 		policyRef types.NamespacedName
// 		policy    *networking.NetworkPolicy
// 		err       error
// 	}
// 	type namespaceGetCall struct {
// 		nsRef types.NamespacedName
// 		ns    *corev1.Namespace
// 		times int
// 		err   error
// 	}
// 	policyIng := &networking.NetworkPolicy{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      "app-allow",
// 			Namespace: "ns",
// 		},
// 		Spec: networking.NetworkPolicySpec{
// 			Ingress: []networking.NetworkPolicyIngressRule{
// 				{
// 					From: []networking.NetworkPolicyPeer{
// 						{
// 							PodSelector: &metav1.LabelSelector{
// 								MatchLabels: map[string]string{
// 									"key": "value",
// 								},
// 							},
// 							NamespaceSelector: &metav1.LabelSelector{
// 								MatchLabels: map[string]string{
// 									"ns": "select",
// 								},
// 							},
// 						},
// 					},
// 				},
// 			},
// 		},
// 	}
// 	policyEgr := &networking.NetworkPolicy{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      "policy",
// 			Namespace: "egr",
// 		},
// 		Spec: networking.NetworkPolicySpec{
// 			Egress: []networking.NetworkPolicyEgressRule{
// 				{
// 					To: []networking.NetworkPolicyPeer{
// 						{
// 							NamespaceSelector: &metav1.LabelSelector{
// 								MatchLabels: map[string]string{
// 									"ns": "select",
// 								},
// 							},
// 						},
// 					},
// 				},
// 			},
// 		},
// 	}
// 	noSampleNetpol := &networking.NetworkPolicy{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      "no-sample",
// 			Namespace: "ns",
// 		},
// 		Spec: networking.NetworkPolicySpec{
// 			Egress: []networking.NetworkPolicyEgressRule{
// 				{
// 					To: []networking.NetworkPolicyPeer{
// 						{
// 							NamespaceSelector: &metav1.LabelSelector{
// 								MatchLabels: map[string]string{
// 									"ns": "select",
// 								},
// 							},
// 						},
// 					},
// 				},
// 			},
// 		},
// 	}
// 	sampleSvc := &corev1.Service{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      "svc",
// 			Namespace: "ns",
// 			Labels: map[string]string{
// 				"svc": "sample",
// 			},
// 		},
// 		Spec: corev1.ServiceSpec{
// 			ClusterIP: "10.100.187.163",
// 			Selector: map[string]string{
// 				"app": "sample",
// 			},
// 		},
// 	}

// 	tests := []struct {
// 		name                      string
// 		trackedNamespacedPolicies []types.NamespacedName
// 		trackedEgressPolicies     []types.NamespacedName
// 		policyGetCalls            []policyGetCall
// 		nsGetCalls                []namespaceGetCall
// 		service                   *corev1.Service
// 		serviceOld                *corev1.Service
// 		want                      []networking.NetworkPolicy
// 		wantErr                   string
// 	}{
// 		{
// 			name:    "no tracked policies",
// 			service: sampleSvc,
// 		},
// 		{
// 			name: "tracked policies match service selector",
// 			service: &corev1.Service{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      "svc",
// 					Namespace: "egr",
// 					Labels: map[string]string{
// 						"svc": "egr",
// 					},
// 				},
// 				Spec: corev1.ServiceSpec{
// 					ClusterIP: "10.100.187.163",
// 					Selector: map[string]string{
// 						"ns": "select",
// 					},
// 				},
// 			},
// 			trackedEgressPolicies: []types.NamespacedName{
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 				{
// 					Name:      "no-sample",
// 					Namespace: "ns",
// 				},
// 			},
// 			trackedNamespacedPolicies: []types.NamespacedName{
// 				{
// 					Name:      "app-allow",
// 					Namespace: "x-ns",
// 				},
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 			},
// 			policyGetCalls: []policyGetCall{

// 				{
// 					policyRef: types.NamespacedName{
// 						Name:      "policy",
// 						Namespace: "egr",
// 					},
// 					policy: policyEgr,
// 				},
// 				{
// 					policyRef: types.NamespacedName{
// 						Name:      "no-sample",
// 						Namespace: "ns",
// 					},
// 					policy: noSampleNetpol,
// 				},
// 			},
// 			nsGetCalls: []namespaceGetCall{
// 				{
// 					nsRef: types.NamespacedName{
// 						Name: "egr",
// 					},
// 					ns: &corev1.Namespace{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name: "egr",
// 							Labels: map[string]string{
// 								"ns": "select",
// 							},
// 						},
// 					},
// 				},
// 			},
// 			want: []networking.NetworkPolicy{
// 				*policyEgr,
// 			},
// 		},
// 		{
// 			name: "tracked policies match old service selector",
// 			service: &corev1.Service{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      "svc",
// 					Namespace: "egr",
// 					Labels: map[string]string{
// 						"svc": "egr",
// 					},
// 				},
// 				Spec: corev1.ServiceSpec{
// 					ClusterIP: "10.100.187.163",
// 					Selector: map[string]string{
// 						"ns": "select-new",
// 					},
// 				},
// 			},
// 			serviceOld: &corev1.Service{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      "svc",
// 					Namespace: "egr",
// 					Labels: map[string]string{
// 						"svc": "egr",
// 					},
// 				},
// 				Spec: corev1.ServiceSpec{
// 					ClusterIP: "10.100.187.163",
// 					Selector: map[string]string{
// 						"ns": "select",
// 					},
// 				},
// 			},
// 			trackedEgressPolicies: []types.NamespacedName{
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 				{
// 					Name:      "no-sample",
// 					Namespace: "ns",
// 				},
// 			},
// 			trackedNamespacedPolicies: []types.NamespacedName{
// 				{
// 					Name:      "app-allow",
// 					Namespace: "x-ns",
// 				},
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 			},
// 			policyGetCalls: []policyGetCall{

// 				{
// 					policyRef: types.NamespacedName{
// 						Name:      "policy",
// 						Namespace: "egr",
// 					},
// 					policy: policyEgr,
// 				},
// 				{
// 					policyRef: types.NamespacedName{
// 						Name:      "no-sample",
// 						Namespace: "ns",
// 					},
// 					policy: noSampleNetpol,
// 				},
// 			},
// 			nsGetCalls: []namespaceGetCall{
// 				{
// 					nsRef: types.NamespacedName{
// 						Name: "egr",
// 					},
// 					ns: &corev1.Namespace{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name: "egr",
// 							Labels: map[string]string{
// 								"ns": "select",
// 							},
// 						},
// 					},
// 				},
// 			},
// 			want: []networking.NetworkPolicy{
// 				*policyEgr,
// 			},
// 		},
// 		{
// 			name: "policy get error",
// 			service: &corev1.Service{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      "svc",
// 					Namespace: "egr",
// 					Labels: map[string]string{
// 						"svc": "egr",
// 					},
// 				},
// 				Spec: corev1.ServiceSpec{
// 					ClusterIP: "10.100.187.163",
// 					Selector: map[string]string{
// 						"ns": "select",
// 					},
// 				},
// 			},
// 			trackedEgressPolicies: []types.NamespacedName{
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 				{
// 					Name:      "no-sample",
// 					Namespace: "ns",
// 				},
// 			},
// 			trackedNamespacedPolicies: []types.NamespacedName{
// 				{
// 					Name:      "app-allow",
// 					Namespace: "x-ns",
// 				},
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 			},
// 			policyGetCalls: []policyGetCall{

// 				{
// 					policyRef: types.NamespacedName{
// 						Name:      "policy",
// 						Namespace: "egr",
// 					},
// 					err: errors.New("some error"),
// 				},
// 			},
// 			nsGetCalls: []namespaceGetCall{
// 				{
// 					nsRef: types.NamespacedName{
// 						Name: "egr",
// 					},
// 					ns: &corev1.Namespace{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name: "egr",
// 							Labels: map[string]string{
// 								"ns": "select",
// 							},
// 						},
// 					},
// 				},
// 			},
// 			wantErr: "failed to get policy: some error",
// 		},
// 		{
// 			name:    "tracked policies do not match",
// 			service: sampleSvc,
// 			trackedEgressPolicies: []types.NamespacedName{
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 				{
// 					Name:      "no-sample",
// 					Namespace: "ns",
// 				},
// 			},
// 			trackedNamespacedPolicies: []types.NamespacedName{
// 				{
// 					Name:      "app-allow",
// 					Namespace: "x-ns",
// 				},
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 			},
// 			policyGetCalls: []policyGetCall{

// 				{
// 					policyRef: types.NamespacedName{
// 						Name:      "policy",
// 						Namespace: "egr",
// 					},
// 					policy: policyEgr,
// 				},
// 				{
// 					policyRef: types.NamespacedName{
// 						Name:      "no-sample",
// 						Namespace: "ns",
// 					},
// 					policy: noSampleNetpol,
// 				},
// 			},
// 			nsGetCalls: []namespaceGetCall{
// 				{
// 					nsRef: types.NamespacedName{
// 						Name: "ns",
// 					},
// 					ns: &corev1.Namespace{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name: "ns",
// 							Labels: map[string]string{
// 								"scope": "ns",
// 							},
// 						},
// 					},
// 				},
// 			},
// 		},
// 		{
// 			name: "headless svc",
// 			service: &corev1.Service{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      "headless-svc",
// 					Namespace: "egr",
// 					Labels: map[string]string{
// 						"svc": "headless",
// 					},
// 				},
// 				Spec: corev1.ServiceSpec{
// 					ClusterIP: "None",
// 					Selector: map[string]string{
// 						"ns": "select",
// 					},
// 				},
// 			},
// 			trackedNamespacedPolicies: []types.NamespacedName{
// 				{
// 					Name:      "app-allow",
// 					Namespace: "x-ns",
// 				},
// 				{
// 					Name:      "policy",
// 					Namespace: "egr",
// 				},
// 			},
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			ctrl := gomock.NewController(t)
// 			defer ctrl.Finish()
// 			_ = policyIng
// 			_ = policyEgr
// 			mockClient := mock_client.NewMockClient(ctrl)
// 			nullLogger := logr.New(&log.NullLogSink{})
// 			policyTracker := &defaultPolicyTracker{
// 				logger: nullLogger,
// 			}
// 			policyResolver := &defaultPolicyReferenceResolver{
// 				k8sClient:     mockClient,
// 				policyTracker: policyTracker,
// 				logger:        nullLogger,
// 			}
// 			for _, ref := range tt.trackedNamespacedPolicies {
// 				policyTracker.namespacedPolicies.Store(ref, true)
// 			}
// 			for _, ref := range tt.trackedEgressPolicies {
// 				policyTracker.egressRulesPolicies.Store(ref, true)
// 			}
// 			for _, item := range tt.policyGetCalls {
// 				call := item
// 				mockClient.EXPECT().Get(gomock.Any(), call.policyRef, gomock.Any()).DoAndReturn(
// 					func(ctx context.Context, key types.NamespacedName, policy *networking.NetworkPolicy, opts ...client.GetOption) error {
// 						if call.policy != nil {
// 							*policy = *call.policy
// 						}
// 						return call.err
// 					},
// 				).AnyTimes()
// 			}
// 			for _, item := range tt.nsGetCalls {
// 				call := item
// 				mockClient.EXPECT().Get(gomock.Any(), call.nsRef, gomock.Any()).DoAndReturn(
// 					func(ctx context.Context, key types.NamespacedName, ns *corev1.Namespace, opts ...client.GetOption) error {
// 						if call.ns != nil {
// 							*ns = *call.ns
// 						}
// 						return call.err
// 					},
// 				).AnyTimes()
// 			}
// 			got, _, err := policyResolver.GetReferredPoliciesForService(context.Background(), tt.service, tt.serviceOld)
// 			if len(tt.wantErr) > 0 {
// 				assert.EqualError(t, err, tt.wantErr)
// 			} else {
// 				require.NoError(t, err)
// 				sort.Slice(tt.want, func(i, j int) bool {
// 					return tt.want[i].String() < tt.want[j].String()
// 				})
// 				sort.Slice(got, func(i, j int) bool {
// 					return got[i].String() < got[j].String()
// 				})
// 				assert.Equal(t, tt.want, got)
// 			}
// 		})
// 	}
// }

// func TestPolicyReferenceResolver_isServiceMatchLabelSelector(t *testing.T) {
// 	type namespaceGetCall struct {
// 		nsRef types.NamespacedName
// 		ns    *corev1.Namespace
// 		err   error
// 	}
// 	nsGetCall := namespaceGetCall{
// 		nsRef: types.NamespacedName{
// 			Name: "application",
// 		},
// 		ns: &corev1.Namespace{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name: "application",
// 				Labels: map[string]string{
// 					"select": "namespace",
// 				},
// 			},
// 		},
// 	}
// 	applicationSvc := &corev1.Service{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      "svc",
// 			Namespace: "application",
// 		},
// 		Spec: corev1.ServiceSpec{
// 			Selector: map[string]string{
// 				"app": "select",
// 			},
// 		},
// 	}
// 	tests := []struct {
// 		name       string
// 		nsGetCalls []namespaceGetCall
// 		svc        *corev1.Service
// 		peer       *networking.NetworkPolicyPeer
// 		namespace  string
// 		want       bool
// 	}{
// 		{
// 			name: "no match different ns",
// 			svc:  applicationSvc,
// 			peer: &networking.NetworkPolicyPeer{},
// 		},
// 		{
// 			name: "same ns match",
// 			svc:  applicationSvc,
// 			peer: &networking.NetworkPolicyPeer{
// 				PodSelector: &metav1.LabelSelector{},
// 			},
// 			namespace: "application",
// 			want:      true,
// 		},
// 		{
// 			name: "svc without pod selector",
// 			svc: &corev1.Service{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      "svc",
// 					Namespace: "ns",
// 				},
// 			},
// 			namespace: "ns",
// 			peer: &networking.NetworkPolicyPeer{
// 				PodSelector: &metav1.LabelSelector{},
// 			},
// 		},
// 		{
// 			name: "pod selector matches svc selector on same ns",
// 			svc: &corev1.Service{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      "svc",
// 					Namespace: "ns",
// 				},
// 				Spec: corev1.ServiceSpec{
// 					Selector: map[string]string{
// 						"app": "select",
// 					},
// 				},
// 			},
// 			peer: &networking.NetworkPolicyPeer{
// 				PodSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"app": "select",
// 					},
// 				},
// 			},
// 			namespace: "ns",
// 			want:      true,
// 		},
// 		{
// 			name:       "svc selector matches peer with ns selector",
// 			svc:        applicationSvc,
// 			nsGetCalls: []namespaceGetCall{nsGetCall},
// 			peer: &networking.NetworkPolicyPeer{
// 				NamespaceSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"select": "namespace",
// 					},
// 				},
// 			},
// 			want: true,
// 		},
// 		{
// 			name:       "svc selector matches peer with ns selector and pod selector",
// 			svc:        applicationSvc,
// 			nsGetCalls: []namespaceGetCall{nsGetCall},
// 			peer: &networking.NetworkPolicyPeer{
// 				NamespaceSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"select": "namespace",
// 					},
// 				},
// 				PodSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"app": "select",
// 					},
// 				},
// 			},
// 			want: true,
// 		},
// 		{
// 			name:       "ns selector matches but pod selector does not",
// 			svc:        applicationSvc,
// 			nsGetCalls: []namespaceGetCall{nsGetCall},
// 			peer: &networking.NetworkPolicyPeer{
// 				NamespaceSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"select": "namespace",
// 					},
// 				},
// 				PodSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"app": "new",
// 					},
// 				},
// 			},
// 		},
// 		{
// 			name:       "pod label selector error",
// 			svc:        applicationSvc,
// 			nsGetCalls: []namespaceGetCall{nsGetCall},
// 			peer: &networking.NetworkPolicyPeer{
// 				NamespaceSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"select": "namespace",
// 					},
// 				},
// 				PodSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"invalid label": "select",
// 					},
// 				},
// 			},
// 		},
// 		{
// 			name:       "invalid ns selector",
// 			svc:        applicationSvc,
// 			nsGetCalls: []namespaceGetCall{nsGetCall},
// 			peer: &networking.NetworkPolicyPeer{
// 				NamespaceSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"select invalid": "namespace",
// 					},
// 				},
// 				PodSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"label": "select",
// 					},
// 				},
// 			},
// 		},
// 		{
// 			name: "ns get error",
// 			svc:  applicationSvc,
// 			nsGetCalls: []namespaceGetCall{
// 				{
// 					nsRef: types.NamespacedName{
// 						Name: "application",
// 					},
// 					err: errors.New("unable to get ns"),
// 				},
// 			},
// 			peer: &networking.NetworkPolicyPeer{
// 				NamespaceSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"select": "namespace",
// 					},
// 				},
// 				PodSelector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"label": "select",
// 					},
// 				},
// 			},
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			ctrl := gomock.NewController(t)
// 			defer ctrl.Finish()

// 			mockClient := mock_client.NewMockClient(ctrl)
// 			nullLogger := logr.New(&log.NullLogSink{})
// 			policyTracker := &defaultPolicyTracker{
// 				logger: nullLogger,
// 			}

// 			policyReferenceResolver := &defaultPolicyReferenceResolver{
// 				k8sClient:     mockClient,
// 				policyTracker: policyTracker,
// 				logger:        nullLogger,
// 			}
// 			for _, item := range tt.nsGetCalls {
// 				call := item
// 				mockClient.EXPECT().Get(gomock.Any(), call.nsRef, gomock.Any()).DoAndReturn(
// 					func(ctx context.Context, key types.NamespacedName, ns *corev1.Namespace, opts ...client.GetOption) error {
// 						if call.ns != nil {
// 							*ns = *call.ns
// 						}
// 						return call.err
// 					},
// 				)
// 			}
// 			got := policyReferenceResolver.isServiceMatchLabelSelector(context.Background(), tt.svc, tt.peer, nil, tt.namespace, false)
// 			assert.Equal(t, tt.want, got)
// 		})
// 	}
// }
