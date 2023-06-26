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
	defaultLogLevel                  = "info"
	defaultMaxConcurrentReconciles   = 3
	defaultEndpointsChunkSize        = 200
	defaultEnableConfigMapCheck      = true
	flagPodUpdateBatchPeriodDuration = "pod-update-batch-period-duration"
	defaultBatchPeriodDuration       = 1 * time.Second
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
	cfg.RuntimeConfig.BindFlags(fs)
}
