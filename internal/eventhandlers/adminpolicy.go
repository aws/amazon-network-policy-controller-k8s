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
	"time"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	adminnetworking "sigs.k8s.io/network-policy-api/apis/v1alpha1"
)

// NewEnqueueRequestForAdminPolicyEvent constructs new enqueueRequestsForAdminPolicyEvent
func NewEnqueueRequestForAdminPolicyEvent(policyTracker resolvers.PolicyTracker, podUpdateBatchPeriodDuration time.Duration,
	logger logr.Logger) handler.EventHandler {
	return &enqueueRequestForAdminPolicyEvent{
		policyTracker:                policyTracker,
		podUpdateBatchPeriodDuration: podUpdateBatchPeriodDuration,
		logger:                       logger,
	}
}

var _ handler.EventHandler = (*enqueueRequestForAdminPolicyEvent)(nil)

type enqueueRequestForAdminPolicyEvent struct {
	policyTracker                resolvers.PolicyTracker
	podUpdateBatchPeriodDuration time.Duration
	logger                       logr.Logger
}

func (h *enqueueRequestForAdminPolicyEvent) Create(_ context.Context, e event.CreateEvent, queue workqueue.RateLimitingInterface) {
	policy := e.Object.(*adminnetworking.AdminNetworkPolicy)
	h.logger.V(1).Info("Handling create event", "admin policy", k8s.NamespacedName(policy))
	h.enqueuePolicy(queue, policy, 0)
}

func (h *enqueueRequestForAdminPolicyEvent) Update(_ context.Context, e event.UpdateEvent, queue workqueue.RateLimitingInterface) {
	oldPolicy := e.ObjectOld.(*adminnetworking.AdminNetworkPolicy)
	newPolicy := e.ObjectNew.(*adminnetworking.AdminNetworkPolicy)

	h.logger.V(1).Info("Handling update event", "admin policy", k8s.NamespacedName(newPolicy))
	if !equality.Semantic.DeepEqual(newPolicy.ResourceVersion, oldPolicy.ResourceVersion) && equality.Semantic.DeepEqual(oldPolicy.Spec, newPolicy.Spec) &&
		equality.Semantic.DeepEqual(oldPolicy.DeletionTimestamp.IsZero(), newPolicy.DeletionTimestamp.IsZero()) {
		return
	}
	h.enqueuePolicy(queue, newPolicy, 0)
}

func (h *enqueueRequestForAdminPolicyEvent) Delete(_ context.Context, e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	policy := e.Object.(*adminnetworking.AdminNetworkPolicy)
	h.logger.V(1).Info("Handling delete event", "admin policy", k8s.NamespacedName(policy))
	h.policyTracker.RemovePolicy(nil, policy, true)
}

func (h *enqueueRequestForAdminPolicyEvent) Generic(_ context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
	val := e.Object.GetObjectKind()
	// This is a hacky solution
	if val.GroupVersionKind().Kind != "AdminNetworkPolicy" {
		return
	}
	policy := e.Object.(*adminnetworking.AdminNetworkPolicy)
	h.logger.Info("Handling generic event", "admin policy", k8s.NamespacedName(policy))
	h.enqueuePolicy(q, policy, h.podUpdateBatchPeriodDuration)
}

func (h *enqueueRequestForAdminPolicyEvent) enqueuePolicy(queue workqueue.RateLimitingInterface, policy *adminnetworking.AdminNetworkPolicy, addAfter time.Duration) {
	h.policyTracker.UpdatePolicy(nil, policy, true)
	queue.AddAfter(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: "",
			Name:      policy.Name,
		},
	}, addAfter)
}
