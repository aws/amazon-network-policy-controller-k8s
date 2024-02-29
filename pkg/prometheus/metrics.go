package prometheus

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	CleanupNetworkPolicyEndpointsErrCnt = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "cleanup_network_policy_endpoints_err_count",
			Help: "The number of errors encountered while cleaning up network policy endpoints",
		},
	)

	ResolveNetworkPolicyEndpointsErrCnt = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "resolve_network_policy_endpoints_err_count",
			Help: "The number of errors encountered while resolving network policy endpoints",
		},
	)

	ComputeIngressEndpointsErrCnt = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "compute_ingress_endpoints_err_count",
			Help: "The number of errors encountered while computing ingress endpoints",
		},
	)

	ComputeEgressEndpointsErrCnt = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "compute_egress_endpoints_err_count",
			Help: "The number of errors encountered while computing egress endpoints",
		},
	)

	ComputePodEndpointsErrCnt = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "compute_pod_endpoints_err_count",
			Help: "The number of errors encountered while computing pod endpoints",
		},
	)
)

func RegisterPrometheus() {
	metrics.Registry.MustRegister(
		CleanupNetworkPolicyEndpointsErrCnt,
		ResolveNetworkPolicyEndpointsErrCnt,
		ComputeIngressEndpointsErrCnt,
		ComputeEgressEndpointsErrCnt,
		ComputePodEndpointsErrCnt,
	)
}
