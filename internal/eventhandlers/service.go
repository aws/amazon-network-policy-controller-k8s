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

// NewEnqueueRequestForServiceEvent constructs a new enqueueRequestForServiceEvent
func NewEnqueueRequestForServiceEvent(policyEventChan chan<- event.GenericEvent, k8sClient client.Client,
	svcUtils backend.ServiceResolver, logger logr.Logger) handler.EventHandler {
	return &enqueueRequestForServiceEvent{
		k8sClient:       k8sClient,
		policyEventChan: policyEventChan,
		svcUtils:        svcUtils,
		logger:          logger,
	}
}

var _ handler.EventHandler = (*enqueueRequestForServiceEvent)(nil)

type enqueueRequestForServiceEvent struct {
	k8sClient       client.Client
	policyEventChan chan<- event.GenericEvent
	svcUtils        backend.ServiceResolver
	logger          logr.Logger
}

func (h *enqueueRequestForServiceEvent) Create(createEvent event.CreateEvent, q workqueue.RateLimitingInterface) {
	serviceNew := createEvent.Object.(*corev1.Service)
	h.logger.V(2).Info("handling service create event", "service", k8s.NamespacedName(serviceNew))
	h.enqueueReferredPolicies(q, serviceNew, nil)
}

func (h *enqueueRequestForServiceEvent) Update(updateEvent event.UpdateEvent, q workqueue.RateLimitingInterface) {
	serviceOld := updateEvent.ObjectOld.(*corev1.Service)
	serviceNew := updateEvent.ObjectNew.(*corev1.Service)

	h.logger.V(2).Info("handling service update event", "service", k8s.NamespacedName(serviceNew))
	if equality.Semantic.DeepEqual(serviceOld.Annotations, serviceOld.Annotations) &&
		equality.Semantic.DeepEqual(serviceOld.Labels, serviceNew.Labels) &&
		equality.Semantic.DeepEqual(serviceOld.Spec, serviceNew.Spec) &&
		equality.Semantic.DeepEqual(serviceOld.DeletionTimestamp.IsZero(), serviceNew.DeletionTimestamp.IsZero()) {
		return
	}
	h.enqueueReferredPolicies(q, serviceNew, serviceOld)
}

func (h *enqueueRequestForServiceEvent) Delete(deleteEvent event.DeleteEvent, q workqueue.RateLimitingInterface) {
	serviceNew := deleteEvent.Object.(*corev1.Service)
	h.logger.V(2).Info("handling service delete event", "service", k8s.NamespacedName(serviceNew))
	h.enqueueReferredPolicies(q, serviceNew, nil)
}

func (h *enqueueRequestForServiceEvent) Generic(_ event.GenericEvent, _ workqueue.RateLimitingInterface) {
	return
}

func (h *enqueueRequestForServiceEvent) enqueueReferredPolicies(_ workqueue.RateLimitingInterface, svc *corev1.Service, svcOld *corev1.Service) {
	referredPolicies, err := h.svcUtils.GetReferredPolicies(context.Background(), svc, svcOld)
	if err != nil {
		h.logger.Error(err, "unable to get referred policies", "service", k8s.NamespacedName(svc))
	}
	for i := range referredPolicies {
		policy := &referredPolicies[i]
		h.logger.Info("enqueue from service reference", "policy", k8s.NamespacedName(policy), "svc", k8s.NamespacedName(svc))
		h.policyEventChan <- event.GenericEvent{
			Object: policy,
		}
	}
}
