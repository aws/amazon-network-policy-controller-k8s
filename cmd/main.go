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

package main

import (
	"context"
	"os"

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	"go.uber.org/zap/zapcore"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/internal/controllers"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/config"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/policyendpoints"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/utils/configmap"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/version"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(policyinfo.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	infoLogger := getLoggerWithLogLevel("info")
	infoLogger.Info("version",
		"GitVersion", version.GitVersion,
		"GitCommit", version.GitCommit,
		"BuildDate", version.BuildDate,
	)
	controllerCFG, err := loadControllerConfig()
	if err != nil {
		infoLogger.Error(err, "unable to load controller config")
		os.Exit(1)
	}
	ctrlLogger := getLoggerWithLogLevel(controllerCFG.LogLevel)
	ctrl.SetLogger(ctrlLogger)

	restCFG, err := config.BuildRestConfig(controllerCFG.RuntimeConfig)
	if err != nil {
		setupLog.Error(err, "unable to build REST config")
		os.Exit(1)
	}
	clientSet, err := kubernetes.NewForConfig(restCFG)
	if err != nil {
		setupLog.Error(err, "unable to obtain clientSet")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	enableNetworkPolicyController := true
	setupLog.Info("Checking arg for enabling CM", "ConfigMapEnabled", controllerCFG.EnableConfigMapCheck)
	setupLog.Info("Checking arg for PE chunk size", "PEChunkSize", controllerCFG.EndpointChunkSize)
	if controllerCFG.EnableConfigMapCheck {
		var cancelFn context.CancelFunc
		ctx, cancelFn = context.WithCancel(ctx)
		setupLog.Info("Enable network policy controller based on configuration", "configmap", configmap.GetControllerConfigMapId())
		configMapManager := config.NewConfigmapManager(configmap.GetControllerConfigMapId(),
			clientSet, cancelFn, configmap.GetConfigmapCheckFn(), ctrl.Log.WithName("configmap-manager"))
		if err := configMapManager.MonitorConfigMap(ctx); err != nil {
			setupLog.Error(err, "Unable to monitor configmap for checking if controller is enabled")
			os.Exit(1)
		}
		enableNetworkPolicyController = configMapManager.IsControllerEnabled()
		if !enableNetworkPolicyController {
			setupLog.Info("Disabling leader election since network policy controller is not enabled")
			controllerCFG.RuntimeConfig.EnableLeaderElection = false
		}
	}

	rtOpts := config.BuildRuntimeOptions(controllerCFG.RuntimeConfig, scheme)

	mgr, err := ctrl.NewManager(restCFG, rtOpts)
	if err != nil {
		setupLog.Error(err, "unable to create controller manager")
		os.Exit(1)
	}

	policyEndpointsManager := policyendpoints.NewPolicyEndpointsManager(mgr.GetClient(),
		controllerCFG.EndpointChunkSize, ctrl.Log.WithName("endpoints-manager"))
	finalizerManager := k8s.NewDefaultFinalizerManager(mgr.GetClient(), ctrl.Log.WithName("finalizer-manager"))
	policyController := controllers.NewPolicyReconciler(mgr.GetClient(), policyEndpointsManager,
		controllerCFG, finalizerManager, ctrl.Log.WithName("controllers").WithName("policy"))
	if enableNetworkPolicyController {
		setupLog.Info("Network Policy controller is enabled, starting watches")
		if err := policyController.SetupWithManager(ctx, mgr); err != nil {
			setupLog.Error(err, "Unable to setup network policy controller")
			os.Exit(1)
		}
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
	setupLog.Info("starting controller manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running controller manager")
		os.Exit(1)
	}
	setupLog.Info("controller manager stopped")

}

// loadControllerConfig loads the controller configuration
func loadControllerConfig() (config.ControllerConfig, error) {
	controllerConfig := config.ControllerConfig{}
	fs := pflag.NewFlagSet("", pflag.ExitOnError)
	controllerConfig.BindFlags(fs)

	if err := fs.Parse(os.Args); err != nil {
		return controllerConfig, err
	}

	return controllerConfig, nil
}

// getLoggerWithLogLevel returns logger with specific log level.
func getLoggerWithLogLevel(logLevel string) logr.Logger {
	var zapLevel zapcore.Level
	switch logLevel {
	case "info":
		zapLevel = zapcore.InfoLevel
	case "debug":
		zapLevel = zapcore.DebugLevel
	default:
		zapLevel = zapcore.InfoLevel
	}
	return zap.New(zap.UseDevMode(false),
		zap.Level(zapLevel),
		zap.StacktraceLevel(zapcore.FatalLevel),
	)
}
