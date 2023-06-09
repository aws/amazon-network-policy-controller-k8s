/*
Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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

// NewEnqueueRequestForPodEvent constructs new enqueueRequestsForPodEvent
func NewEnqueueRequestForPodEvent(policyEventChan chan<- event.GenericEvent, k8sClient client.Client,
	podResolver backend.PodResolver, logger logr.Logger) handler.EventHandler {
	return &enqueueRequestForPodEvent{
		k8sClient:       k8sClient,
		policyEventChan: policyEventChan,
		logger:          logger,
		podResolver:     podResolver,
	}
}

var _ handler.EventHandler = (*enqueueRequestForPodEvent)(nil)

type enqueueRequestForPodEvent struct {
	k8sClient       client.Client
	policyEventChan chan<- event.GenericEvent
	logger          logr.Logger
	podResolver     backend.PodResolver
}

func (h *enqueueRequestForPodEvent) Create(event event.CreateEvent, q workqueue.RateLimitingInterface) {
	podNew := event.Object.(*corev1.Pod)
	h.logger.V(1).Info("handling pod create event", "pod", k8s.NamespacedName(podNew))
	h.enqueueReferredPolicies(q, podNew, nil)
}

func (h *enqueueRequestForPodEvent) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	podOld := e.ObjectOld.(*corev1.Pod)
	podNew := e.ObjectNew.(*corev1.Pod)

	h.logger.V(1).Info("handling pod update event", "pod", k8s.NamespacedName(podNew))
	if equality.Semantic.DeepEqual(podOld.Annotations, podNew.Annotations) &&
		equality.Semantic.DeepEqual(podOld.Labels, podNew.Labels) &&
		equality.Semantic.DeepEqual(podOld.DeletionTimestamp.IsZero(), podNew.DeletionTimestamp.IsZero()) &&
		equality.Semantic.DeepEqual(podOld.Status.PodIP, podNew.Status.PodIP) {
		return
	}
	h.enqueueReferredPolicies(q, podNew, podOld)
}

func (h *enqueueRequestForPodEvent) Delete(e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	pod := e.Object.(*corev1.Pod)
	h.logger.V(1).Info("handling delete event", "pod", k8s.NamespacedName(pod))
	h.enqueueReferredPolicies(q, pod, nil)
}

func (h *enqueueRequestForPodEvent) Generic(_ event.GenericEvent, _ workqueue.RateLimitingInterface) {
	return
}

func (h *enqueueRequestForPodEvent) enqueueReferredPolicies(_ workqueue.RateLimitingInterface, pod *corev1.Pod, podOld *corev1.Pod) {
	referredPolicies, err := h.podResolver.GetReferredPolicies(context.Background(), pod, podOld)
	if err != nil {
		h.logger.Error(err, "unable to get referred policies", "pod", k8s.NamespacedName(pod))
		return
	}
	for i := range referredPolicies {
		policy := &referredPolicies[i]
		h.logger.Info("enqueue from pod reference", "policy", k8s.NamespacedName(policy), "pod", k8s.NamespacedName(pod))
		h.policyEventChan <- event.GenericEvent{
			Object: policy,
		}
	}
}
