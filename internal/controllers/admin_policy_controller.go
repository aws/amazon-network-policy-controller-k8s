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

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	adminnetworking "sigs.k8s.io/network-policy-api/apis/v1alpha1"

	"github.com/aws/amazon-network-policy-controller-k8s/internal/eventhandlers"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/config"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/policyendpoints"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

const (
	adminControllerName = "adminpolicy"
)

func NewAdminPolicyReconciler(k8sClient client.Client, policyEndpointsManager policyendpoints.PolicyEndpointsManager,
	controllerConfig config.ControllerConfig, finalizerManager k8s.FinalizerManager, logger logr.Logger) *adminPolicyReconciler {
	adminPolicyTracker := resolvers.NewPolicyTracker(logger.WithName("admin-policy-tracker"))
	adminPolicyResolver := resolvers.NewPolicyReferenceResolver(k8sClient, adminPolicyTracker, logger.WithName("admin-policy-resolver"))
	return &adminPolicyReconciler{
		k8sClient:                    k8sClient,
		policyResolver:               adminPolicyResolver,
		policyTracker:                adminPolicyTracker,
		policyEndpointsManager:       policyEndpointsManager,
		podUpdateBatchPeriodDuration: controllerConfig.PodUpdateBatchPeriodDuration,
		finalizerManager:             finalizerManager,
		maxConcurrentReconciles:      controllerConfig.MaxConcurrentReconciles,
		logger:                       logger,
	}
}

var _ reconcile.Reconciler = (*adminPolicyReconciler)(nil)

type adminPolicyReconciler struct {
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
//+kubebuilder:rbac:groups="policy.networking.k8s.io",resources=adminnetworkpolicies,verbs=get;list;watch;update;patch

func (r *adminPolicyReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.logger.Info("Got admin reconcile request", "resource", request)
	return ctrl.Result{}, r.reconcile(ctx, request)
}

func (r *adminPolicyReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	policyEventChan := make(chan event.GenericEvent)
	podEventHandler := eventhandlers.NewEnqueueRequestForPodEvent(policyEventChan, r.k8sClient, r.policyResolver,
		r.logger.WithName("eventHandler").WithName("pod"))
	nsEventHandler := eventhandlers.NewEnqueueRequestForNamespaceEvent(policyEventChan, r.k8sClient, r.policyResolver,
		r.logger.WithName("eventHandler").WithName("namespace"))
	svcEventHandler := eventhandlers.NewEnqueueRequestForServiceEvent(policyEventChan, r.k8sClient, r.policyResolver,
		r.logger.WithName("eventHandler").WithName("service"))
	adminPolicyEventHandler := eventhandlers.NewEnqueueRequestForAdminPolicyEvent(r.policyTracker, r.podUpdateBatchPeriodDuration,
		r.logger.WithName("eventHandler").WithName("adminpolicy"))

	if err := mgr.AddHealthzCheck("admin-policy-controller", healthz.Ping); err != nil {
		r.logger.Error(err, "Failed to setup the admin policy controller healthz check")
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(adminControllerName).
		Watches(&adminnetworking.AdminNetworkPolicy{}, adminPolicyEventHandler).
		Watches(&corev1.Pod{}, podEventHandler).
		Watches(&corev1.Namespace{}, nsEventHandler).
		Watches(&corev1.Service{}, svcEventHandler).
		WatchesRawSource(source.Channel(policyEventChan, adminPolicyEventHandler)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.maxConcurrentReconciles,
		}).Complete(r)
}

func (r *adminPolicyReconciler) reconcile(ctx context.Context, request reconcile.Request) error {
	adminpolicy := &adminnetworking.AdminNetworkPolicy{}
	if err := r.k8sClient.Get(ctx, request.NamespacedName, adminpolicy); err != nil {
		r.logger.Info("Unable to get admin policy", "resource", adminpolicy, "err", err)
		return client.IgnoreNotFound(err)
	}
	namespaces, err := r.podSelectorNamespaces(ctx, adminpolicy)
	if err != nil {
		r.logger.Info("Unable to list namespaces")
	}
	if !adminpolicy.DeletionTimestamp.IsZero() {
		return r.cleanupPolicy(ctx, adminpolicy, namespaces)
	}
	r.logger.Info("Reconcile admin policy")
	return r.reconcilePolicy(ctx, adminpolicy, namespaces)
}

func (r *adminPolicyReconciler) reconcilePolicy(ctx context.Context, adminpolicy *adminnetworking.AdminNetworkPolicy, namespaces []corev1.Namespace) error {
	if err := r.finalizerManager.AddFinalizers(ctx, adminpolicy, policyFinalizerName); err != nil {
		return err
	}
	return r.policyEndpointsManager.ReconcileAdmin(ctx, adminpolicy, true, namespaces)
}

func (r *adminPolicyReconciler) cleanupPolicy(ctx context.Context, adminpolicy *adminnetworking.AdminNetworkPolicy, namespaces []corev1.Namespace) error {
	if k8s.HasFinalizer(adminpolicy, policyFinalizerName) {
		r.policyTracker.RemovePolicy(nil, adminpolicy, true)
		if err := r.policyEndpointsManager.Cleanup(ctx, nil, adminpolicy, true, namespaces); err != nil {
			return err
		}
		if err := r.finalizerManager.RemoveFinalizers(ctx, adminpolicy, policyFinalizerName); err != nil {
			return err
		}
	}
	return nil
}

func (r *adminPolicyReconciler) podSelectorNamespaces(ctx context.Context, adminpolicy *adminnetworking.AdminNetworkPolicy) ([]corev1.Namespace, error) {
	var nsSelector labels.Selector
	var err error
	if adminpolicy.Spec.Subject.Namespaces != nil {
		nsSelector, err = metav1.LabelSelectorAsSelector(adminpolicy.Spec.Subject.Namespaces)
		if err != nil {
			return nil, errors.Wrap(err, "unable to get admin namespace selector")
		}
	} else {
		nsSelector, err = metav1.LabelSelectorAsSelector(&adminpolicy.Spec.Subject.Pods.NamespaceSelector)
		if err != nil {
			return nil, errors.Wrap(err, "unable to get admin namespace selector")
		}
	}
	// All namespaces
	if nsSelector.String() == "" {
		return nil, nil
	}
	nsList := &corev1.NamespaceList{}
	if err := r.k8sClient.List(ctx, nsList, &client.ListOptions{
		LabelSelector: nsSelector,
	}); err != nil {
		r.logger.Info("Unable to List admin NS", "err", err)
		return nil, err
	}
	return nsList.Items, nil
}
