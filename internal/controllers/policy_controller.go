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
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/aws/amazon-network-policy-controller-k8s/internal/eventhandlers"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/backend"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/config"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/policyendpoints"
)

const (
	controllerName      = "policy"
	policyFinalizerName = "networking.k8s.aws/resources"
)

func NewPolicyReconciler(k8sClient client.Client, policyEndpointsManager policyendpoints.PolicyEndpointsManager,
	controllerConfig config.ControllerConfig, finalizerManager k8s.FinalizerManager, logger logr.Logger) *policyReconciler {
	policyTracker := backend.NewPolicyTracker(logger.WithName("policy-tracker"))
	podUtils := backend.NewPodResolver(k8sClient, policyTracker, logger.WithName("pod-utils"))
	svcUtils := backend.NewServiceResolver(k8sClient, policyTracker, logger.WithName("service-utils"))
	nsUtils := backend.NewNamespaceResolver(k8sClient, policyTracker, logger.WithName("namespace-utils"))
	return &policyReconciler{
		k8sClient:                    k8sClient,
		podUtils:                     podUtils,
		serviceUtils:                 svcUtils,
		nsUtils:                      nsUtils,
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
	podUtils                     backend.PodResolver
	serviceUtils                 backend.ServiceResolver
	nsUtils                      backend.NamespaceResolver
	policyTracker                backend.PolicyTracker
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
	r.logger.Info("Got reconcile request", "req", request)
	policy := &networking.NetworkPolicy{}
	if err := r.k8sClient.Get(ctx, request.NamespacedName, policy); err != nil {
		r.logger.Info("unable to get policy", "resource", policy, "err", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if policy.DeletionTimestamp.IsZero() {
		if err := r.reconcilePolicy(ctx, policy); err != nil {
			if apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
				return ctrl.Result{}, nil
			}
			r.logger.Error(err, "Error during policy reconcile, re-queueing")
			return ctrl.Result{Requeue: true}, nil
		}
	} else {
		if err := r.cleanupPolicy(ctx, policy); err != nil {
			r.logger.Error(err, "Error during policy cleanup, re-queueing")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *policyReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	c, err := controller.New(controllerName, mgr, controller.Options{
		MaxConcurrentReconciles: r.maxConcurrentReconciles,
		Reconciler:              r,
	})
	if err != nil {
		return err
	}
	if err := r.setupWatches(ctx, c); err != nil {
		return err
	}
	return nil
}

func (r *policyReconciler) setupWatches(_ context.Context, c controller.Controller) error {
	policyEventChan := make(chan event.GenericEvent)
	policyEventHandler := eventhandlers.NewEnqueueRequestForPolicyEvent(r.policyTracker, r.podUpdateBatchPeriodDuration,
		r.logger.WithName("eventHandler").WithName("policy"))
	podEventHandler := eventhandlers.NewEnqueueRequestForPodEvent(policyEventChan, r.k8sClient, r.podUtils,
		r.logger.WithName("eventHandler").WithName("pod"))
	nsEventHandler := eventhandlers.NewEnqueueRequestForNamespaceEvent(policyEventChan, r.k8sClient, r.nsUtils,
		r.logger.WithName("eventHandler").WithName("namespace"))
	svcEventHandler := eventhandlers.NewEnqueueRequestForServiceEvent(policyEventChan, r.k8sClient, r.serviceUtils,
		r.logger.WithName("eventHandler").WithName("service"))

	if err := c.Watch(&source.Channel{Source: policyEventChan}, policyEventHandler); err != nil {
		return err
	}
	if err := c.Watch(&source.Kind{Type: &networking.NetworkPolicy{}}, policyEventHandler); err != nil {
		return err
	}
	if err := c.Watch(&source.Kind{Type: &corev1.Pod{}}, podEventHandler); err != nil {
		return err
	}
	if err := c.Watch(&source.Kind{Type: &corev1.Namespace{}}, nsEventHandler); err != nil {
		return err
	}
	if err := c.Watch(&source.Kind{Type: &corev1.Service{}}, svcEventHandler); err != nil {
		return err
	}
	return nil
}

func (r *policyReconciler) reconcilePolicy(ctx context.Context, policy *networking.NetworkPolicy) error {
	if err := r.finalizerManager.AddFinalizers(ctx, policy, policyFinalizerName); err != nil {
		return err
	}
	return r.policyEndpointsManager.Reconcile(ctx, policy)
}

func (r *policyReconciler) cleanupPolicy(ctx context.Context, policy *networking.NetworkPolicy) error {
	if k8s.HasFinalizer(policy, policyFinalizerName) {
		if err := r.policyEndpointsManager.Cleanup(ctx, policy); err != nil {
			return err
		}
		if err := r.finalizerManager.RemoveFinalizers(ctx, policy, policyFinalizerName); err != nil {
			return err
		}
	}
	return nil
}
