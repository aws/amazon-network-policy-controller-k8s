package eventhandlers

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

// countingResolver refers every object to one policy of each kind and counts resolver calls.
type countingResolver struct {
	calls int
}

var _ resolvers.PolicyReferenceResolver = (*countingResolver)(nil)

func (r *countingResolver) policies() []networking.NetworkPolicy {
	r.calls++
	return []networking.NetworkPolicy{{ObjectMeta: metav1.ObjectMeta{Name: "np", Namespace: "ns"}}}
}

func (r *countingResolver) anps() []policyinfo.ApplicationNetworkPolicy {
	r.calls++
	return []policyinfo.ApplicationNetworkPolicy{{ObjectMeta: metav1.ObjectMeta{Name: "anp", Namespace: "ns"}}}
}

func (r *countingResolver) cnps() []policyinfo.ClusterNetworkPolicy {
	r.calls++
	return []policyinfo.ClusterNetworkPolicy{{ObjectMeta: metav1.ObjectMeta{Name: "cnp"}}}
}

func (r *countingResolver) GetReferredPoliciesForPod(_ context.Context, _, _ *corev1.Pod) ([]networking.NetworkPolicy, error) {
	return r.policies(), nil
}

func (r *countingResolver) GetReferredPoliciesForNamespace(_ context.Context, _, _ *corev1.Namespace) ([]networking.NetworkPolicy, error) {
	return r.policies(), nil
}

func (r *countingResolver) GetReferredPoliciesForService(_ context.Context, _, _ *corev1.Service) ([]networking.NetworkPolicy, error) {
	return r.policies(), nil
}

func (r *countingResolver) GetReferredApplicationNetworkPoliciesForPod(_ context.Context, _, _ *corev1.Pod) ([]policyinfo.ApplicationNetworkPolicy, error) {
	return r.anps(), nil
}

func (r *countingResolver) GetReferredApplicationNetworkPoliciesForNamespace(_ context.Context, _, _ *corev1.Namespace) ([]policyinfo.ApplicationNetworkPolicy, error) {
	return r.anps(), nil
}

func (r *countingResolver) GetReferredApplicationNetworkPoliciesForService(_ context.Context, _, _ *corev1.Service) ([]policyinfo.ApplicationNetworkPolicy, error) {
	return r.anps(), nil
}

func (r *countingResolver) GetReferredClusterPoliciesForPod(_ context.Context, _, _ *corev1.Pod) ([]policyinfo.ClusterNetworkPolicy, error) {
	return r.cnps(), nil
}

func (r *countingResolver) GetReferredClusterPoliciesForNamespace(_ context.Context, _, _ *corev1.Namespace) ([]policyinfo.ClusterNetworkPolicy, error) {
	return r.cnps(), nil
}

func (r *countingResolver) GetReferredClusterPoliciesForService(_ context.Context, _, _ *corev1.Service) ([]policyinfo.ClusterNetworkPolicy, error) {
	return r.cnps(), nil
}

func TestCreateHandlers_InitialList(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
		Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"}}

	newHandlers := map[string]func(chan<- event.GenericEvent, resolvers.PolicyReferenceResolver) (handler.EventHandler, client.Object){
		"pod": func(ch chan<- event.GenericEvent, r resolvers.PolicyReferenceResolver) (handler.EventHandler, client.Object) {
			return NewEnqueueRequestForPodEvent(ch, nil, r, logr.Discard()), pod
		},
		"namespace": func(ch chan<- event.GenericEvent, r resolvers.PolicyReferenceResolver) (handler.EventHandler, client.Object) {
			return NewEnqueueRequestForNamespaceEvent(ch, nil, r, logr.Discard()), ns
		},
		"service": func(ch chan<- event.GenericEvent, r resolvers.PolicyReferenceResolver) (handler.EventHandler, client.Object) {
			return NewEnqueueRequestForServiceEvent(ch, nil, r, logr.Discard()), svc
		},
	}

	tests := []struct {
		name            string
		isInInitialList bool
		wantCalls       int
		wantEvents      int
	}{
		{
			name:            "initial list create is skipped",
			isInInitialList: true,
			wantCalls:       0,
			wantEvents:      0,
		},
		{
			name:            "create after initial list enqueues referred policies",
			isInInitialList: false,
			wantCalls:       3,
			wantEvents:      3,
		},
	}
	for kind, newHandler := range newHandlers {
		for _, tt := range tests {
			t.Run(kind+"/"+tt.name, func(t *testing.T) {
				resolver := &countingResolver{}
				// Buffered so the handler does not block; it sends at most one event per policy kind.
				ch := make(chan event.GenericEvent, 3)
				h, obj := newHandler(ch, resolver)

				h.Create(context.Background(), event.CreateEvent{Object: obj, IsInInitialList: tt.isInInitialList}, nil)

				assert.Equal(t, tt.wantCalls, resolver.calls)
				assert.Len(t, ch, tt.wantEvents)
			})
		}
	}
}
