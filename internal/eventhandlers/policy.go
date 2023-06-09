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
	"time"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/backend"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/go-logr/logr"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// NewEnqueueRequestForPolicyEvent constructs new enqueueRequestsForPolicyEvent
func NewEnqueueRequestForPolicyEvent(policyTracker backend.PolicyTracker, podUpdateBatchPeriodDuration time.Duration,
	logger logr.Logger) handler.EventHandler {
	return &enqueueRequestForPolicyEvent{
		policyTracker:                policyTracker,
		podUpdateBatchPeriodDuration: podUpdateBatchPeriodDuration,
		logger:                       logger,
	}
}

var _ handler.EventHandler = (*enqueueRequestForPolicyEvent)(nil)

type enqueueRequestForPolicyEvent struct {
	policyTracker                backend.PolicyTracker
	podUpdateBatchPeriodDuration time.Duration
	logger                       logr.Logger
}

func (h *enqueueRequestForPolicyEvent) Create(e event.CreateEvent, queue workqueue.RateLimitingInterface) {
	policy := e.Object.(*networking.NetworkPolicy)
	h.logger.V(1).Info("handling create event", "policy", k8s.NamespacedName(policy))
	h.enqueuePolicy(queue, policy, 0)
}

func (h *enqueueRequestForPolicyEvent) Update(e event.UpdateEvent, queue workqueue.RateLimitingInterface) {
	oldPolicy := e.ObjectOld.(*networking.NetworkPolicy)
	newPolicy := e.ObjectNew.(*networking.NetworkPolicy)

	h.logger.V(1).Info("handling update event", "policy", k8s.NamespacedName(newPolicy))
	if equality.Semantic.DeepEqual(oldPolicy.Annotations, newPolicy.Annotations) &&
		equality.Semantic.DeepEqual(oldPolicy.Spec, newPolicy.Spec) &&
		equality.Semantic.DeepEqual(oldPolicy.DeletionTimestamp.IsZero(), newPolicy.DeletionTimestamp.IsZero()) {
		return
	}
	h.enqueuePolicy(queue, newPolicy, 0)
}

func (h *enqueueRequestForPolicyEvent) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	policy := e.Object.(*networking.NetworkPolicy)
	h.logger.V(1).Info("handling delete event", "policy", k8s.NamespacedName(policy))
	h.policyTracker.RemovePolicy(policy)
}

func (h *enqueueRequestForPolicyEvent) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
	policy := e.Object.(*networking.NetworkPolicy)
	h.logger.V(1).Info("handling generic event", "policy", k8s.NamespacedName(policy))
	h.enqueuePolicy(q, policy, h.podUpdateBatchPeriodDuration)
}

func (h *enqueueRequestForPolicyEvent) enqueuePolicy(queue workqueue.RateLimitingInterface, policy *networking.NetworkPolicy, addAfter time.Duration) {
	h.policyTracker.UpdatePolicy(policy)
	queue.AddAfter(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: policy.Namespace,
			Name:      policy.Name,
		},
	}, addAfter)
}
