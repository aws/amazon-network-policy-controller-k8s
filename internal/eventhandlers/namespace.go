package eventhandlers

import (
	"context"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// NewEnqueueRequestForNamespaceEvent construct enqueueRequestsForNamespaceEvent
func NewEnqueueRequestForNamespaceEvent(policyEventChan chan<- event.GenericEvent, k8sClient client.Client,
	policyResolver resolvers.PolicyReferenceResolver, logger logr.Logger) handler.EventHandler {
	return &enqueueRequestForNamespaceEvent{
		k8sClient:       k8sClient,
		policyEventChan: policyEventChan,
		policyResolver:  policyResolver,
		logger:          logger,
	}
}

var _ handler.EventHandler = (*enqueueRequestForNamespaceEvent)(nil)

type enqueueRequestForNamespaceEvent struct {
	k8sClient       client.Client
	policyEventChan chan<- event.GenericEvent
	logger          logr.Logger
	policyResolver  resolvers.PolicyReferenceResolver
}

func (h *enqueueRequestForNamespaceEvent) Create(ctx context.Context, event event.CreateEvent, q workqueue.RateLimitingInterface) {
	ns := event.Object.(*corev1.Namespace)
	h.logger.V(1).Info("Handling create event", "namespace", k8s.NamespacedName(ns))
	h.enqueueReferredPolicies(ctx, q, ns, nil)
}

func (h *enqueueRequestForNamespaceEvent) Update(ctx context.Context, event event.UpdateEvent, q workqueue.RateLimitingInterface) {
	nsNew := event.ObjectNew.(*corev1.Namespace)
	nsOld := event.ObjectOld.(*corev1.Namespace)

	h.logger.V(1).Info("Handling update event", "namespace", k8s.NamespacedName(nsNew))
	if equality.Semantic.DeepEqual(nsOld.Labels, nsNew.Labels) &&
		equality.Semantic.DeepEqual(nsOld.DeletionTimestamp.IsZero(), nsNew.DeletionTimestamp.IsZero()) {
		return
	}
	h.enqueueReferredPolicies(ctx, q, nsNew, nsOld)
}

func (h *enqueueRequestForNamespaceEvent) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	ns := event.Object.(*corev1.Namespace)
	h.logger.V(1).Info("Handling delete event", "namespace", k8s.NamespacedName(ns))
	h.enqueueReferredPolicies(ctx, q, ns, nil)
}

func (h *enqueueRequestForNamespaceEvent) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.RateLimitingInterface) {
	return
}

func (h *enqueueRequestForNamespaceEvent) enqueueReferredPolicies(ctx context.Context, _ workqueue.RateLimitingInterface, ns, nsOld *corev1.Namespace) {
	referredPolicies, err := h.policyResolver.GetReferredPoliciesForNamespace(ctx, ns, nsOld)
	if err != nil {
		h.logger.Error(err, "Unable to get referred policies", "namespace", k8s.NamespacedName(ns))
		return
	}
	for i := range referredPolicies {
		policy := &referredPolicies[i]
		h.logger.V(1).Info("Enqueue from namespace reference", "policy", k8s.NamespacedName(policy), "namespace", k8s.NamespacedName(ns))
		h.policyEventChan <- event.GenericEvent{
			Object: policy,
		}
	}
}
