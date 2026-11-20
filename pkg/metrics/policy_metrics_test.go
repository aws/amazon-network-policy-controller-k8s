package metrics

import (
	"context"
	"testing"

	"github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSetPolicyObjectCount(t *testing.T) {
	PolicyObjectCount.Reset()

	SetPolicyObjectCount("TestPolicy", 5)

	metric := &dto.Metric{}
	PolicyObjectCount.WithLabelValues("TestPolicy").Write(metric)

	if metric.GetGauge().GetValue() != 5 {
		t.Errorf("Expected count 5, got %f", metric.GetGauge().GetValue())
	}
}

func TestIncDecPolicyObjectCount(t *testing.T) {
	PolicyObjectCount.Reset()

	IncPolicyObjectCount("TestPolicy")
	IncPolicyObjectCount("TestPolicy")

	metric := &dto.Metric{}
	PolicyObjectCount.WithLabelValues("TestPolicy").Write(metric)

	if metric.GetGauge().GetValue() != 2 {
		t.Errorf("Expected count 2 after inc, got %f", metric.GetGauge().GetValue())
	}

	DecPolicyObjectCount("TestPolicy")
	PolicyObjectCount.WithLabelValues("TestPolicy").Write(metric)

	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected count 1 after dec, got %f", metric.GetGauge().GetValue())
	}
}

func TestInitializePolicyObjectCounts(t *testing.T) {
	scheme := runtime.NewScheme()
	networking.AddToScheme(scheme)
	v1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "np1", Namespace: "default"}},
		&v1alpha1.ApplicationNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "anp1", Namespace: "default"}},
		&v1alpha1.ClusterNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "cnp1"}},
	).Build()

	PolicyObjectCount.Reset()
	AdvancedNetworkPolicyEnabled.Set(0)

	InitializePolicyObjectCounts(context.Background(), client)

	metric := &dto.Metric{}
	PolicyObjectCount.WithLabelValues("NetworkPolicy").Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected NetworkPolicy count 1, got %f", metric.GetGauge().GetValue())
	}

	PolicyObjectCount.WithLabelValues("ApplicationNetworkPolicy").Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected ApplicationNetworkPolicy count 1, got %f", metric.GetGauge().GetValue())
	}

	PolicyObjectCount.WithLabelValues("ClusterNetworkPolicy").Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected ClusterNetworkPolicy count 1, got %f", metric.GetGauge().GetValue())
	}

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected advanced_network_policy_enabled to be 1, got %f", metric.GetGauge().GetValue())
	}
}

func TestAdvancedNetworkPolicyEnabled(t *testing.T) {
	// Reset metrics
	PolicyObjectCount.Reset()
	AdvancedNetworkPolicyEnabled.Set(0)

	// Test case 1: No ANP or CNP policies - should be 0
	UpdateAdvancedNetworkPolicyEnabled()

	metric := &dto.Metric{}
	AdvancedNetworkPolicyEnabled.Write(metric)

	if metric.GetGauge().GetValue() != 0 {
		t.Errorf("Expected advanced_network_policy_enabled to be 0, got %f", metric.GetGauge().GetValue())
	}

	// Test case 2: Add ANP policy - should be 1
	SetPolicyObjectCount("ApplicationNetworkPolicy", 1)
	UpdateAdvancedNetworkPolicyEnabled()

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected advanced_network_policy_enabled to be 1 with ANP, got %f", metric.GetGauge().GetValue())
	}

	// Test case 3: Remove ANP, add CNP - should still be 1
	SetPolicyObjectCount("ApplicationNetworkPolicy", 0)
	SetPolicyObjectCount("ClusterNetworkPolicy", 1)
	UpdateAdvancedNetworkPolicyEnabled()

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected advanced_network_policy_enabled to be 1 with CNP, got %f", metric.GetGauge().GetValue())
	}

	// Test case 4: Both ANP and CNP - should be 1
	SetPolicyObjectCount("ApplicationNetworkPolicy", 1)
	SetPolicyObjectCount("ClusterNetworkPolicy", 1)
	UpdateAdvancedNetworkPolicyEnabled()

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected advanced_network_policy_enabled to be 1 with both ANP and CNP, got %f", metric.GetGauge().GetValue())
	}

	// Test case 5: Only NetworkPolicy, no ANP or CNP - should be 0
	SetPolicyObjectCount("ApplicationNetworkPolicy", 0)
	SetPolicyObjectCount("ClusterNetworkPolicy", 0)
	SetPolicyObjectCount("NetworkPolicy", 5) // Only regular NetworkPolicy
	UpdateAdvancedNetworkPolicyEnabled()

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 0 {
		t.Errorf("Expected advanced_network_policy_enabled to be 0 with only NetworkPolicy, got %f", metric.GetGauge().GetValue())
	}

	// Test case 6: Remove all - should be 0
	SetPolicyObjectCount("ApplicationNetworkPolicy", 0)
	SetPolicyObjectCount("ClusterNetworkPolicy", 0)
	SetPolicyObjectCount("NetworkPolicy", 0)
	UpdateAdvancedNetworkPolicyEnabled()

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 0 {
		t.Errorf("Expected advanced_network_policy_enabled to be 0 with no policies, got %f", metric.GetGauge().GetValue())
	}
}

func TestOnPolicyCreatedAndDeleted(t *testing.T) {
	// Reset metrics
	PolicyObjectCount.Reset()
	AdvancedNetworkPolicyEnabled.Set(0)

	// Create ANP policy
	OnPolicyCreated("ApplicationNetworkPolicy")

	metric := &dto.Metric{}
	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 1 {
		t.Errorf("Expected advanced_network_policy_enabled to be 1 after creating ANP, got %f", metric.GetGauge().GetValue())
	}

	// Delete ANP policy
	OnPolicyDeleted("ApplicationNetworkPolicy")

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 0 {
		t.Errorf("Expected advanced_network_policy_enabled to be 0 after deleting ANP, got %f", metric.GetGauge().GetValue())
	}

	// Create NetworkPolicy - should remain 0
	OnPolicyCreated("NetworkPolicy")

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 0 {
		t.Errorf("Expected advanced_network_policy_enabled to be 0 after creating NetworkPolicy, got %f", metric.GetGauge().GetValue())
	}

	// Delete NetworkPolicy - should still be 0
	OnPolicyDeleted("NetworkPolicy")

	AdvancedNetworkPolicyEnabled.Write(metric)
	if metric.GetGauge().GetValue() != 0 {
		t.Errorf("Expected advanced_network_policy_enabled to be 0 after deleting NetworkPolicy, got %f", metric.GetGauge().GetValue())
	}
}

func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := g.Write(metric); err != nil {
		t.Fatalf("reading gauge: %v", err)
	}
	return metric.GetGauge().GetValue()
}

func TestSetPolicyTargetGroups(t *testing.T) {
	a := types.NamespacedName{Namespace: "ns", Name: "a"}
	b := types.NamespacedName{Namespace: "ns", Name: "b"}
	t.Cleanup(func() {
		ForgetPolicyTargetGroups(a)
		ForgetPolicyTargetGroups(b)
	})

	// One group is the unsplit case and must not count, so the gauges stay at zero on a
	// cluster where no policy's pods disagree.
	SetPolicyTargetGroups(a, 1)
	if got := gaugeValue(t, SplitPolicyCount); got != 0 {
		t.Errorf("one group must not count as split, got %f", got)
	}

	SetPolicyTargetGroups(a, 3)
	SetPolicyTargetGroups(b, 2)
	if got := gaugeValue(t, SplitPolicyCount); got != 2 {
		t.Errorf("expected 2 split policies, got %f", got)
	}
	if got := gaugeValue(t, MaxPolicyTargetGroups); got != 3 {
		t.Errorf("expected max 3 groups, got %f", got)
	}

	// Recomputed, not decremented, so a policy whose pods come back into agreement drops out
	// and the max falls with it.
	SetPolicyTargetGroups(a, 1)
	if got := gaugeValue(t, SplitPolicyCount); got != 1 {
		t.Errorf("expected 1 split policy after a merged back, got %f", got)
	}
	if got := gaugeValue(t, MaxPolicyTargetGroups); got != 2 {
		t.Errorf("expected max 2 groups after a merged back, got %f", got)
	}

	ForgetPolicyTargetGroups(b)
	if got := gaugeValue(t, SplitPolicyCount); got != 0 {
		t.Errorf("a deleted policy must stop counting, got %f", got)
	}
	if got := gaugeValue(t, MaxPolicyTargetGroups); got != 0 {
		t.Errorf("expected max 0 with nothing split, got %f", got)
	}
}
