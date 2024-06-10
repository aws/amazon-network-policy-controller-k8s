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

package controllers

import (
	"context"
	"time"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/aws/amazon-network-policy-controller-k8s/internal/eventhandlers"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/config"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/policyendpoints"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

const (
	controllerName      = "policy"
	policyFinalizerName = "networking.k8s.aws/resources"
)

func NewPolicyReconciler(k8sClient client.Client, policyEndpointsManager policyendpoints.PolicyEndpointsManager,
	controllerConfig config.ControllerConfig, finalizerManager k8s.FinalizerManager, logger logr.Logger) *policyReconciler {
	policyTracker := resolvers.NewPolicyTracker(logger.WithName("policy-tracker"))
	policyResolver := resolvers.NewPolicyReferenceResolver(k8sClient, policyTracker, logger.WithName("policy-resolver"))
	return &policyReconciler{
		k8sClient:                    k8sClient,
		policyResolver:               policyResolver,
		policyTracker:                policyTracker,
		policyEndpointsManager:       policyEndpointsManager,
		podUpdateBatchPeriodDuration: controllerConfig.PodUpdateBatchPeriodDuration,
		finalizerManager:             finalizerManager,
		maxConcurrentReconciles:      controllerConfig.MaxConcurrentReconciles,
		logger:                       logger,
	}
}

var _ reconcile.Reconciler = (*policyReconciler)(nil)

type policyReconciler struct {
	k8sClient                    client.Client
	policyResolver               resolvers.PolicyReferenceResolver
	policyTracker                resolvers.PolicyTracker
	policyEndpointsManager       policyendpoints.PolicyEndpointsManager
	podUpdateBatchPeriodDuration time.Duration
	finalizerManager             k8s.FinalizerManager

	maxConcurrentReconciles int
	logger                  logr.Logger
}

//+kubebuilder:rbac:groups=networking.k8s.aws,resources=policyendpoints,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.aws,resources=policyendpoints/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=networking.k8s.aws,resources=policyendpoints/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups="networking.k8s.io",resources=networkpolicies,verbs=get;list;watch;update;patch

func (r *policyReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.logger.Info("Got reconcile request", "resource", request)
	return ctrl.Result{}, r.reconcile(ctx, request)
}

func (r *policyReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := r.setupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return err
	}
	policyEventChan := make(chan event.GenericEvent)
	policyEventHandler := eventhandlers.NewEnqueueRequestForPolicyEvent(r.policyTracker, r.podUpdateBatchPeriodDuration,
		r.logger.WithName("eventHandler").WithName("policy"))
	podEventHandler := eventhandlers.NewEnqueueRequestForPodEvent(policyEventChan, r.k8sClient, r.policyResolver,
		r.logger.WithName("eventHandler").WithName("pod"))
	nsEventHandler := eventhandlers.NewEnqueueRequestForNamespaceEvent(policyEventChan, r.k8sClient, r.policyResolver,
		r.logger.WithName("eventHandler").WithName("namespace"))
	svcEventHandler := eventhandlers.NewEnqueueRequestForServiceEvent(policyEventChan, r.k8sClient, r.policyResolver,
		r.logger.WithName("eventHandler").WithName("service"))

	if err := mgr.AddHealthzCheck("policy-controller", healthz.Ping); err != nil {
		r.logger.Error(err, "Failed to setup the policy controller healthz check")
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		Watches(&networking.NetworkPolicy{}, policyEventHandler).
		Watches(&corev1.Pod{}, podEventHandler).
		Watches(&corev1.Namespace{}, nsEventHandler).
		Watches(&corev1.Service{}, svcEventHandler).
		WatchesRawSource(source.Channel(policyEventChan, policyEventHandler)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.maxConcurrentReconciles,
		}).Complete(r)
}

func (r *policyReconciler) reconcile(ctx context.Context, request reconcile.Request) error {
	policy := &networking.NetworkPolicy{}
	if err := r.k8sClient.Get(ctx, request.NamespacedName, policy); err != nil {
		r.logger.Info("Unable to get policy", "resource", policy, "err", err)
		return client.IgnoreNotFound(err)
	}
	if !policy.DeletionTimestamp.IsZero() {
		return r.cleanupPolicy(ctx, policy)
	}
	return r.reconcilePolicy(ctx, policy)
}

func (r *policyReconciler) reconcilePolicy(ctx context.Context, policy *networking.NetworkPolicy) error {
	if err := r.finalizerManager.AddFinalizers(ctx, policy, policyFinalizerName); err != nil {
		return err
	}
	return r.policyEndpointsManager.Reconcile(ctx, policy)
}

func (r *policyReconciler) cleanupPolicy(ctx context.Context, policy *networking.NetworkPolicy) error {
	if k8s.HasFinalizer(policy, policyFinalizerName) {
		r.policyTracker.RemovePolicy(policy)
		if err := r.policyEndpointsManager.Cleanup(ctx, policy); err != nil {
			return err
		}
		if err := r.finalizerManager.RemoveFinalizers(ctx, policy, policyFinalizerName); err != nil {
			return err
		}
	}
	return nil
}

func (r *policyReconciler) setupIndexes(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	if err := fieldIndexer.IndexField(ctx, &policyinfo.PolicyEndpoint{}, policyendpoints.IndexKeyPolicyReferenceName,
		policyendpoints.IndexFunctionPolicyReferenceName); err != nil {
		return err
	}
	return nil
}
