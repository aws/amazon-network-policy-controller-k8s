package config

import (
	"time"

	"github.com/spf13/pflag"
)

const (
	flagLogLevel                     = "log-level"
	flagMaxConcurrentReconciles      = "max-concurrent-reconciles"
	flagEnableConfigMapCheck         = "enable-configmap-check"
	flagEndpointChunkSize            = "endpoint-chunk-size"
	flagEnableGoProfiling            = "enable-goprofiling"
	flagListPageSize                 = "list-page-size"
	defaultLogLevel                  = "info"
	defaultMaxConcurrentReconciles   = 3
	defaultEndpointsChunkSize        = 200
	defaultEnableConfigMapCheck      = true
	defaultListPageSize              = 1000
	flagPodUpdateBatchPeriodDuration = "pod-update-batch-period-duration"
	defaultBatchPeriodDuration       = 1 * time.Second
	defaultEnableGoProfiling         = false
)

// ControllerConfig contains the controller configuration
type ControllerConfig struct {
	// Log level for the controller logs
	LogLevel string
	// EndpointChunkSize specifies the number of endpoints to include in a single chunk
	EndpointChunkSize int
	// EnableConfigMapCheck enables checking the configmap for starting the NP controller
	EnableConfigMapCheck bool
	// MaxConcurrentReconciles specifies the max number of reconcile loops
	MaxConcurrentReconciles int
	// PodUpdateBatchPeriodDuration specifies the duration between batch updates of pods
	PodUpdateBatchPeriodDuration time.Duration
	// Configurations for the Controller Runtime
	RuntimeConfig RuntimeConfig
	// EnableGoProfiling enables the goprofiling for dev purpose
	EnableGoProfiling bool
	// ListPageSize specifies the page size for k8s list calls
	ListPageSize int
}

func (cfg *ControllerConfig) BindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&cfg.LogLevel, flagLogLevel, defaultLogLevel,
		"Set the controller log level - info, debug")
	fs.IntVar(&cfg.MaxConcurrentReconciles, flagMaxConcurrentReconciles, defaultMaxConcurrentReconciles, ""+
		"Maximum number of concurrent reconcile loops")
	fs.IntVar(&cfg.EndpointChunkSize, flagEndpointChunkSize, defaultEndpointsChunkSize, ""+
		"Number of endpoints to include in a single policy endpoints resource")
	fs.BoolVar(&cfg.EnableConfigMapCheck, flagEnableConfigMapCheck, defaultEnableConfigMapCheck,
		"Enable checking the configmap for starting the network policy controller")
	fs.DurationVar(&cfg.PodUpdateBatchPeriodDuration, flagPodUpdateBatchPeriodDuration, defaultBatchPeriodDuration, ""+
		"Duration between batch updates of pods")
	fs.BoolVar(&cfg.EnableGoProfiling, flagEnableGoProfiling, defaultEnableGoProfiling,
		"Enable goprofiling for develop purpose")
	fs.IntVar(&cfg.ListPageSize, flagListPageSize, defaultListPageSize,
		"Page size for k8s list calls")
	cfg.RuntimeConfig.BindFlags(fs)
}
