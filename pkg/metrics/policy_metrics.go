package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	PolicyReconciliations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "network_policy_reconciliations_total",
			Help: "Total number of policy reconciliations by type and result",
		},
		[]string{"policy_type", "result"},
	)

	PolicyReconcileLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "network_policy_reconcile_duration_seconds",
			Help: "How long in seconds reconciling a policy takes",
		},
		[]string{"policy_type"},
	)

	PolicyObjectCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "network_policy_objects_total",
			Help: "Total number of policy objects in the cluster by type",
		},
		[]string{"policy_type"},
	)

	AdvancedNetworkPolicyEnabled = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "advanced_network_policy_enabled",
			Help: "Indicates if advanced network policies (ANP or CNP) are in use (1 if enabled, 0 if disabled)",
		},
	)

	SplitPolicyCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "network_policy_split_policies_total",
			Help: "Number of NetworkPolicies whose selected pods resolve a named port differently, so they are split into more than one PolicyEndpoint target group",
		},
	)

	MaxPolicyTargetGroups = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "network_policy_target_groups_max",
			Help: "Largest number of PolicyEndpoint target groups any single NetworkPolicy is split into, 0 when none are split",
		},
	)
)

// splitPolicies is the group count of every policy currently split, keyed by policy. The two
// gauges above are a population, not a rate, so they are recomputed from this rather than
// incremented: a reconcile reports a policy's whole count each time, including when it drops
// back to one group.
var splitPolicies = struct {
	sync.Mutex
	counts map[types.NamespacedName]int
}{counts: map[types.NamespacedName]int{}}

func init() {
	metrics.Registry.MustRegister(PolicyReconciliations, PolicyReconcileLatency, PolicyObjectCount,
		AdvancedNetworkPolicyEnabled, SplitPolicyCount, MaxPolicyTargetGroups)
}

// SetPolicyTargetGroups records how many target groups a policy resolved into. A count of one or
// zero means the policy is not split, which is the common case and the only case before named
// ports resolve differently across a policy's pods.
func SetPolicyTargetGroups(policy types.NamespacedName, groups int) {
	splitPolicies.Lock()
	defer splitPolicies.Unlock()

	if groups > 1 {
		splitPolicies.counts[policy] = groups
	} else {
		delete(splitPolicies.counts, policy)
	}
	refreshSplitPolicyGaugesLocked()
}

// ForgetPolicyTargetGroups drops a deleted policy, so its groups stop counting toward the gauges.
func ForgetPolicyTargetGroups(policy types.NamespacedName) {
	splitPolicies.Lock()
	defer splitPolicies.Unlock()

	delete(splitPolicies.counts, policy)
	refreshSplitPolicyGaugesLocked()
}

func refreshSplitPolicyGaugesLocked() {
	max := 0
	for _, groups := range splitPolicies.counts {
		if groups > max {
			max = groups
		}
	}
	SplitPolicyCount.Set(float64(len(splitPolicies.counts)))
	MaxPolicyTargetGroups.Set(float64(max))
}

func RecordPolicyReconcileLatency(policyType string, duration time.Duration) {
	PolicyReconcileLatency.WithLabelValues(policyType).Observe(duration.Seconds())
}

func SetPolicyObjectCount(policyType string, count float64) {
	PolicyObjectCount.WithLabelValues(policyType).Set(count)
}

func IncPolicyObjectCount(policyType string) {
	PolicyObjectCount.WithLabelValues(policyType).Inc()
}

func DecPolicyObjectCount(policyType string) {
	PolicyObjectCount.WithLabelValues(policyType).Dec()
}

// InitializePolicyObjectCounts initializes counters by listing existing policies (called once at startup)
func InitializePolicyObjectCounts(ctx context.Context, k8sClient client.Client) error {
	// Initialize NetworkPolicy count
	var netpols networking.NetworkPolicyList
	if err := k8sClient.List(ctx, &netpols, &client.ListOptions{}); err != nil {
		return err
	}
	SetPolicyObjectCount("NetworkPolicy", float64(len(netpols.Items)))

	// Initialize ApplicationNetworkPolicy count
	var anps v1alpha1.ApplicationNetworkPolicyList
	if err := k8sClient.List(ctx, &anps, &client.ListOptions{}); err != nil {
		return err
	}
	SetPolicyObjectCount("ApplicationNetworkPolicy", float64(len(anps.Items)))

	// Initialize ClusterNetworkPolicy count
	var cnps v1alpha1.ClusterNetworkPolicyList
	if err := k8sClient.List(ctx, &cnps, &client.ListOptions{}); err != nil {
		return err
	}
	SetPolicyObjectCount("ClusterNetworkPolicy", float64(len(cnps.Items)))

	// Update advanced network policy enabled metric
	UpdateAdvancedNetworkPolicyEnabled()
	return nil
}

// Event-driven counter updates (no API calls)
func OnPolicyCreated(policyType string) {
	IncPolicyObjectCount(policyType)
	UpdateAdvancedNetworkPolicyEnabled()
}

func OnPolicyDeleted(policyType string) {
	DecPolicyObjectCount(policyType)
	UpdateAdvancedNetworkPolicyEnabled()
}

// UpdateAdvancedNetworkPolicyEnabled sets the metric to 1 if ANP or CNP policies exist, 0 otherwise
func UpdateAdvancedNetworkPolicyEnabled() {
	anpCount := PolicyObjectCount.WithLabelValues("ApplicationNetworkPolicy")
	cnpCount := PolicyObjectCount.WithLabelValues("ClusterNetworkPolicy")

	anpMetric := &dto.Metric{}
	cnpMetric := &dto.Metric{}

	anpCount.Write(anpMetric)
	cnpCount.Write(cnpMetric)

	if anpMetric.GetGauge().GetValue() > 0 || cnpMetric.GetGauge().GetValue() > 0 {
		AdvancedNetworkPolicyEnabled.Set(1)
	} else {
		AdvancedNetworkPolicyEnabled.Set(0)
	}
}
