package eventhandlers

import (
	"context"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/backend"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
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
	nsResolver backend.NamespaceResolver, logger logr.Logger) handler.EventHandler {
	return &enqueueRequestForNamespaceEvent{
		k8sClient:       k8sClient,
		policyEventChan: policyEventChan,
		nsResolver:      nsResolver,
		logger:          logger,
	}
}

var _ handler.EventHandler = (*enqueueRequestForNamespaceEvent)(nil)

type enqueueRequestForNamespaceEvent struct {
	k8sClient       client.Client
	policyEventChan chan<- event.GenericEvent
	logger          logr.Logger
	nsResolver      backend.NamespaceResolver
}

func (h *enqueueRequestForNamespaceEvent) Create(event event.CreateEvent, q workqueue.RateLimitingInterface) {
	ns := event.Object.(*corev1.Namespace)
	h.logger.V(1).Info("handling create event", "namespace", k8s.NamespacedName(ns))
	h.enqueueReferredPolicies(q, ns, nil)
}

func (h *enqueueRequestForNamespaceEvent) Update(event event.UpdateEvent, q workqueue.RateLimitingInterface) {
	nsNew := event.ObjectNew.(*corev1.Namespace)
	nsOld := event.ObjectOld.(*corev1.Namespace)

	h.logger.V(1).Info("handling update event", "namespace", k8s.NamespacedName(nsNew))
	if equality.Semantic.DeepEqual(nsOld.Labels, nsNew.Labels) &&
		equality.Semantic.DeepEqual(nsOld.Annotations, nsNew.Annotations) {
		return
	}
	h.enqueueReferredPolicies(q, nsNew, nsOld)
}

func (h *enqueueRequestForNamespaceEvent) Delete(event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	ns := event.Object.(*corev1.Namespace)
	h.logger.V(1).Info("handling delete event", "namespace", k8s.NamespacedName(ns))
	h.enqueueReferredPolicies(q, ns, nil)
}

func (h *enqueueRequestForNamespaceEvent) Generic(_ event.GenericEvent, _ workqueue.RateLimitingInterface) {
	return
}

func (h *enqueueRequestForNamespaceEvent) enqueueReferredPolicies(_ workqueue.RateLimitingInterface, ns, nsOld *corev1.Namespace) {
	referredPolicies, err := h.nsResolver.GetReferredPolicies(context.Background(), ns, nsOld)
	if err != nil {
		h.logger.Error(err, "unable to get referred policies", "namespace", k8s.NamespacedName(ns))
		return
	}
	for i := range referredPolicies {
		policy := &referredPolicies[i]
		h.logger.Info("enqueue policy for namespace event", "policy", k8s.NamespacedName(policy), "namespace", k8s.NamespacedName(ns))
		h.policyEventChan <- event.GenericEvent{
			Object: policy,
		}
	}
}
