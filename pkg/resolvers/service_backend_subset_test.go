package resolvers

import (
	"context"
	"errors"
	"testing"

	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

const optInLabel = "allow.restate.dev/restate"

func workerService() corev1.Service {
	return corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "intro-dp"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.100.134.183",
			Selector:  map[string]string{"app": "worker"},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8003, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt(80)},
			},
		},
	}
}

func workerPod(name string, optIn bool, phase corev1.PodPhase) corev1.Pod {
	l := map[string]string{"app": "worker"}
	if optIn {
		l[optInLabel] = "true"
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "intro-dp", Labels: l},
		Status:     corev1.PodStatus{PodIP: "192.168.57.42", Phase: phase},
	}
}

func optInPeerSelector() *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{optInLabel: "true"}}
}

func TestIsServiceSelectedByPeer(t *testing.T) {
	svc := workerService()
	optIn, err := metav1.LabelSelectorAsSelector(optInPeerSelector())
	require.NoError(t, err)

	tests := []struct {
		name    string
		svc     corev1.Service
		peer    labels.Selector
		pods    []corev1.Pod
		listErr error
		want    bool
	}{
		{
			name: "svc selector satisfies peer without listing pods",
			svc:  svc,
			peer: labels.SelectorFromSet(map[string]string{"app": "worker"}),
			want: true,
		},
		{
			name: "empty peer selector matches any service",
			svc:  corev1.Service{Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1"}},
			peer: labels.Everything(),
			want: true,
		},
		{
			name: "every backend carries the opt-in label",
			svc:  svc,
			peer: optIn,
			pods: []corev1.Pod{workerPod("w1", true, corev1.PodRunning), workerPod("w2", true, corev1.PodPending)},
			want: true,
		},
		{
			name: "one backend without the opt-in label blocks the ClusterIP",
			svc:  svc,
			peer: optIn,
			pods: []corev1.Pod{workerPod("w1", true, corev1.PodRunning), workerPod("w2", false, corev1.PodRunning)},
			want: false,
		},
		{
			name: "terminal backend without the label is ignored",
			svc:  svc,
			peer: optIn,
			pods: []corev1.Pod{workerPod("w1", true, corev1.PodRunning), workerPod("w2", false, corev1.PodSucceeded)},
			want: true,
		},
		{
			name: "pods outside the service selector are ignored",
			svc:  svc,
			peer: optIn,
			pods: []corev1.Pod{
				workerPod("w1", true, corev1.PodRunning),
				{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "intro-dp", Labels: map[string]string{"app": "other"}}},
			},
			want: true,
		},
		{
			name: "service with no backends is not allowed",
			svc:  svc,
			peer: optIn,
			want: false,
		},
		{
			name: "service without selector is not allowed for a non-empty peer",
			svc:  corev1.Service{Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1"}},
			peer: optIn,
			want: false,
		},
		{
			name:    "pod list error fails closed",
			svc:     svc,
			peer:    optIn,
			listErr: errors.New("cache unavailable"),
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewEndpointsResolver(nil, logr.New(&log.NullLogSink{}))
			getAllPods := func() ([]corev1.Pod, error) { return tt.pods, tt.listErr }
			assert.Equal(t, tt.want, r.isServiceSelectedByPeer(&tt.svc, tt.peer, getAllPods))
		})
	}
}

// Issue aws/aws-network-policy-agent#572: the peer selects backends by an opt-in label the Service
// selector does not use. The ClusterIP must be allowed only while every backend carries the label.
func TestGetMatchingServiceClusterIPs_OptInLabelNotInServiceSelector(t *testing.T) {
	tests := []struct {
		name    string
		pods    []corev1.Pod
		wantIPs []policyinfo.NetworkAddress
	}{
		{
			name:    "all backends opted in",
			pods:    []corev1.Pod{workerPod("w1", true, corev1.PodRunning)},
			wantIPs: []policyinfo.NetworkAddress{"10.100.134.183"},
		},
		{
			name: "unlabeled backend joins the service",
			pods: []corev1.Pod{workerPod("w1", true, corev1.PodRunning), workerPod("w2", false, corev1.PodRunning)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockClient := mock_client.NewMockClient(ctrl)
			r := NewEndpointsResolver(mockClient, logr.New(&log.NullLogSink{}))

			mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.ServiceList{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, list *corev1.ServiceList, opts ...client.ListOption) error {
					list.Items = []corev1.Service{workerService()}
					return nil
				})
			pods := tt.pods
			mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, list *corev1.PodList, opts ...client.ListOption) error {
					list.Items = pods
					return nil
				}).Times(1)

			got := r.getMatchingServiceClusterIPs(context.Background(), optInPeerSelector(), "intro-dp", nil)
			var gotIPs []policyinfo.NetworkAddress
			for _, ep := range got {
				gotIPs = append(gotIPs, ep.CIDR)
			}
			assert.Equal(t, tt.wantIPs, gotIPs)
		})
	}
}

// A backend that does not match the peer joining the service must re-resolve the policy, otherwise
// the previously allowed ClusterIP would keep reaching the new pod.
func TestGetReferredPoliciesForPodServices(t *testing.T) {
	policyRef := types.NamespacedName{Namespace: "restate", Name: "allow-restate-egress"}
	policy := networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: policyRef.Name, Namespace: policyRef.Namespace},
		Spec: networking.NetworkPolicySpec{
			PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
			Egress: []networking.NetworkPolicyEgressRule{{
				To: []networking.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{},
					PodSelector:       optInPeerSelector(),
				}},
			}},
		},
	}
	unlabeled := workerPod("w2", false, corev1.PodRunning)
	notBackend := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "intro-dp", Labels: map[string]string{"app": "other"}}}

	tests := []struct {
		name         string
		pod          corev1.Pod
		backends     []corev1.Pod
		trackPolicy  bool
		wantPolicies []types.NamespacedName
	}{
		{
			name:         "unlabeled backend joins a service with opted-in backends",
			pod:          unlabeled,
			backends:     []corev1.Pod{workerPod("w1", true, corev1.PodRunning), unlabeled},
			trackPolicy:  true,
			wantPolicies: []types.NamespacedName{policyRef},
		},
		{
			name:        "no backend matches the peer so the ClusterIP was never allowed",
			pod:         unlabeled,
			backends:    []corev1.Pod{unlabeled},
			trackPolicy: true,
		},
		{
			name:        "pod is not a backend of any service",
			pod:         notBackend,
			trackPolicy: true,
		},
		{
			name: "no egress policies tracked skips the service scan",
			pod:  unlabeled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockClient := mock_client.NewMockClient(ctrl)
			nullLogger := logr.New(&log.NullLogSink{})
			tracker := &defaultPolicyTracker{logger: nullLogger}
			if tt.trackPolicy {
				tracker.namespacedPolicies.Store(policyRef, true)
				tracker.egressRulesPolicies.Store(policyRef, true)
			}
			r := &defaultPolicyReferenceResolver{k8sClient: mockClient, policyTracker: tracker, logger: nullLogger}

			mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.ServiceList{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, list *corev1.ServiceList, opts ...client.ListOption) error {
					list.Items = []corev1.Service{workerService()}
					return nil
				}).AnyTimes()
			mockClient.EXPECT().Get(gomock.Any(), policyRef, gomock.AssignableToTypeOf(&networking.NetworkPolicy{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj *networking.NetworkPolicy, opts ...client.GetOption) error {
					*obj = policy
					return nil
				}).AnyTimes()
			mockClient.EXPECT().Get(gomock.Any(), types.NamespacedName{Name: "intro-dp"}, gomock.AssignableToTypeOf(&corev1.Namespace{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj *corev1.Namespace, opts ...client.GetOption) error {
					*obj = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "intro-dp"}}
					return nil
				}).AnyTimes()
			backends := tt.backends
			mockClient.EXPECT().List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any()).
				DoAndReturn(func(ctx context.Context, list *corev1.PodList, opts ...client.ListOption) error {
					list.Items = backends
					return nil
				}).AnyTimes()

			got, err := r.getReferredPoliciesForPodServices(context.Background(), &tt.pod, nil, sets.Set[types.NamespacedName]{})
			require.NoError(t, err)
			var gotRefs []types.NamespacedName
			for i := range got {
				gotRefs = append(gotRefs, types.NamespacedName{Namespace: got[i].Namespace, Name: got[i].Name})
			}
			assert.Equal(t, tt.wantPolicies, gotRefs)
		})
	}
}
