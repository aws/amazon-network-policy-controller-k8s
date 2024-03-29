package config

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/spf13/pflag"
)

const (
	flagKubeconfig              = "kubeconfig"
	flagMetricsBindAddr         = "metrics-bind-addr"
	flagHealthProbeBindAddr     = "health-probe-bind-addr"
	flagEnableLeaderElection    = "enable-leader-election"
	flagLeaderElectionID        = "leader-election-id"
	flagLeaderElectionNamespace = "leader-election-namespace"
	flagWatchNamespace          = "watch-namespace"

	defaultKubeconfig              = ""
	defaultLeaderElectionID        = "amazon-network-policy-controller-k8s"
	defaultLeaderElectionNamespace = ""
	defaultWatchNamespace          = corev1.NamespaceAll
	defaultMetricsAddr             = ":8080"
	defaultHealthProbeBindAddress  = ":8081"
	defaultQPS                     = 20
	defaultBurst                   = 100
)

// RuntimeConfig stores the configuration for the controller-runtime
type RuntimeConfig struct {
	APIServer               string
	KubeConfig              string
	MetricsBindAddress      string
	HealthProbeBindAddress  string
	EnableLeaderElection    bool
	LeaderElectionID        string
	LeaderElectionNamespace string
	WatchNamespace          string
	SyncPeriod              time.Duration
}

func (c *RuntimeConfig) BindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.KubeConfig, flagKubeconfig, defaultKubeconfig,
		"Path to the kubeconfig file containing authorization and API server information.")
	fs.StringVar(&c.MetricsBindAddress, flagMetricsBindAddr, defaultMetricsAddr,
		"The address the metric endpoint binds to.")
	fs.StringVar(&c.HealthProbeBindAddress, flagHealthProbeBindAddr, defaultHealthProbeBindAddress,
		"The address the health probes binds to.")
	fs.BoolVar(&c.EnableLeaderElection, flagEnableLeaderElection, true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	fs.StringVar(&c.LeaderElectionID, flagLeaderElectionID, defaultLeaderElectionID,
		"Name of the leader election ID to use for this controller")
	fs.StringVar(&c.LeaderElectionNamespace, flagLeaderElectionNamespace, defaultLeaderElectionNamespace,
		"Name of the leader election ID to use for this controller")
	fs.StringVar(&c.WatchNamespace, flagWatchNamespace, defaultWatchNamespace,
		"Namespace the controller watches for updates to Kubernetes objects, If empty, all namespaces are watched.")
}

// BuildRestConfig builds the REST config for the controller runtime
func BuildRestConfig(rtCfg RuntimeConfig) (*rest.Config, error) {
	var restCFG *rest.Config
	var err error
	if rtCfg.KubeConfig == "" {
		restCFG, err = rest.InClusterConfig()
	} else {
		restCFG, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: rtCfg.KubeConfig}, &clientcmd.ConfigOverrides{}).ClientConfig()
	}
	if err != nil {
		return nil, err
	}
	restCFG.QPS = defaultQPS
	restCFG.Burst = defaultBurst
	return restCFG, nil
}

// BuildRuntimeOptions builds the options for the controller runtime based on config
func BuildRuntimeOptions(rtCfg RuntimeConfig, scheme *runtime.Scheme) ctrl.Options {
	// if DefaultNamespaces in Options is not set, cache will watch for all namespaces
	cacheOptions := cache.Options{}
	if rtCfg.WatchNamespace != corev1.NamespaceAll {
		cacheOptions = cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				rtCfg.WatchNamespace: {},
			},
		}
	}
	return ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: rtCfg.MetricsBindAddress},
		HealthProbeBindAddress:  rtCfg.HealthProbeBindAddress,
		LeaderElection:          rtCfg.EnableLeaderElection,
		LeaderElectionID:        rtCfg.LeaderElectionID,
		LeaderElectionNamespace: rtCfg.LeaderElectionNamespace,
		Cache:                   cacheOptions,
	}
}
