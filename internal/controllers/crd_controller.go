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
	_ "embed"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion/scheme"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const policyendpointsCrdName = "policyendpoints.networking.k8s.aws"

//go:embed crds.yaml
var policyEndpointsCrd string

func decodePolicyEndpointsCrd() (*apiextensionsv1.CustomResourceDefinition, error) {
	decoder := scheme.Codecs.UniversalDeserializer()
	obj := &apiextensionsv1.CustomResourceDefinition{}

	_, _, err := decoder.Decode([]byte(policyEndpointsCrd), nil, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to decode CRD from embedded YAML: %w", err)
	}
	return obj, nil
}

func NewCRDReconciler(k8sClient client.Client, logger logr.Logger) *CRDReconciler {
	return &CRDReconciler{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

// CRDReconciler reconciles a CRD object
type CRDReconciler struct {
	k8sClient client.Client
	logger    logr.Logger
}

// +kubebuilder:rbac:groups=extensions,resources=crds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=extensions,resources=crds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=extensions,resources=crds/finalizers,verbs=update
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update

func (r *CRDReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)
	r.logger.Info("Got reconcile request", "resource", req)
	if req.Name != policyendpointsCrdName {
		r.logger.Info("Ignoring reconcile request for non-policyendpoints CRD", "name", req.Name)
		return ctrl.Result{}, nil
	}
	desiredCRD, err := decodePolicyEndpointsCrd()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to load CRD from file: %w", err)
	}
	existing := &apiextensionsv1.CustomResourceDefinition{}
	err = r.k8sClient.Get(ctx, types.NamespacedName{Name: desiredCRD.Name}, existing)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			r.logger.Info("CRD not found, creating...")
			return ctrl.Result{}, r.k8sClient.Create(ctx, desiredCRD)
		}
		return ctrl.Result{}, err
	}
	// Optionally update spec here if needed
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CRDReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiextensionsv1.CustomResourceDefinition{}).
		Named("crd").
		Complete(r)
}
