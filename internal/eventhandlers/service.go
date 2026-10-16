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

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// NewEnqueueRequestForServiceEvent constructs a new enqueueRequestForServiceEvent
func NewEnqueueRequestForServiceEvent(policyEventChan chan<- event.GenericEvent, k8sClient client.Client,
	policyResolver resolvers.PolicyReferenceResolver, logger logr.Logger) handler.EventHandler {
	return &enqueueRequestForServiceEvent{
		k8sClient:       k8sClient,
		policyEventChan: policyEventChan,
		policyResolver:  policyResolver,
		logger:          logger,
	}
}

var _ handler.EventHandler = (*enqueueRequestForServiceEvent)(nil)

type enqueueRequestForServiceEvent struct {
	k8sClient       client.Client
	policyEventChan chan<- event.GenericEvent
	policyResolver  resolvers.PolicyReferenceResolver
	logger          logr.Logger
}

func (h *enqueueRequestForServiceEvent) Create(ctx context.Context, createEvent event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// Every policy gets its own initial-list Create event and is reconciled against the synced cache, so
	// fanning out from the initial list only duplicates that work, and the resolver cost is O(objects x policies).
	// Relists after the informer has synced are not flagged, so changes missed during a watch gap still go through.
	if createEvent.IsInInitialList {
		return
	}
	serviceNew := createEvent.Object.(*corev1.Service)
	h.logger.V(1).Info("handling service create event", "service", k8s.NamespacedName(serviceNew))
	h.enqueueReferredPolicies(ctx, q, serviceNew, nil)
}

func (h *enqueueRequestForServiceEvent) Update(ctx context.Context, updateEvent event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	serviceOld := updateEvent.ObjectOld.(*corev1.Service)
	serviceNew := updateEvent.ObjectNew.(*corev1.Service)

	h.logger.V(1).Info("handling service update event", "service", k8s.NamespacedName(serviceNew))
	if equality.Semantic.DeepEqual(serviceOld.Spec, serviceNew.Spec) &&
		equality.Semantic.DeepEqual(serviceOld.DeletionTimestamp.IsZero(), serviceNew.DeletionTimestamp.IsZero()) {
		return
	}
	h.enqueueReferredPolicies(ctx, q, serviceNew, serviceOld)
}

func (h *enqueueRequestForServiceEvent) Delete(ctx context.Context, deleteEvent event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	serviceNew := deleteEvent.Object.(*corev1.Service)
	h.logger.V(1).Info("handling service delete event", "service", k8s.NamespacedName(serviceNew))
	h.enqueueReferredPolicies(ctx, q, serviceNew, nil)
}

func (h *enqueueRequestForServiceEvent) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	return
}

func (h *enqueueRequestForServiceEvent) enqueueReferredPolicies(ctx context.Context, _ workqueue.TypedRateLimitingInterface[reconcile.Request], svc *corev1.Service, svcOld *corev1.Service) {
	// Handle Network Policy
	referredPolicies, err := h.policyResolver.GetReferredPoliciesForService(ctx, svc, svcOld)
	if err != nil {
		h.logger.Error(err, "Unable to get referred policies", "service", k8s.NamespacedName(svc))
		return
	}
	for i := range referredPolicies {
		policy := &referredPolicies[i]
		h.logger.V(1).Info("Enqueue policies from service reference", "policy", k8s.NamespacedName(policy), "svc", k8s.NamespacedName(svc))
		h.policyEventChan <- event.GenericEvent{
			Object: policy,
		}
	}

	// Handle ApplicationNetworkPolicy
	referredANPs, err := h.policyResolver.GetReferredApplicationNetworkPoliciesForService(ctx, svc, svcOld)
	if err != nil {
		h.logger.Error(err, "Unable to get referred ANPs", "service", k8s.NamespacedName(svc))
	}
	for i := range referredANPs {
		anp := &referredANPs[i]
		h.logger.V(1).Info("Enqueue ANPs from service reference", "anp", k8s.NamespacedName(anp), "svc", k8s.NamespacedName(svc))
		h.policyEventChan <- event.GenericEvent{
			Object: anp,
		}
	}

	// Handle Cluster Network Policy
	referredClusterPolicies, err := h.policyResolver.GetReferredClusterPoliciesForService(ctx, svc, svcOld)
	if err != nil {
		h.logger.Error(err, "Unable to get referred cluster policies", "service", k8s.NamespacedName(svc))
		return
	}
	for i := range referredClusterPolicies {
		cnp := &referredClusterPolicies[i]
		h.logger.V(1).Info("Enqueue CNP from service reference", "cnp", cnp.Name, "svc", k8s.NamespacedName(svc))
		h.policyEventChan <- event.GenericEvent{
			Object: cnp,
		}
	}
}
