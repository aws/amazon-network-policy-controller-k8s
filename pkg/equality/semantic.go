package equality

import (
	"fmt"
	"sort"

	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// EqualPolicyEndpointSpec returns true when two PolicyEndpointSpec values
// are semantically equal, treating Ingress, Egress, and
// PodSelectorEndpoints as unordered sets. Ports and Except sub-slices
// inside each EndpointInfo are also compared order-insensitively.
func EqualPolicyEndpointSpec(a, b policyinfo.PolicyEndpointSpec) bool {
	// Ordered / scalar fields — use the standard comparator.
	if !equality.Semantic.DeepEqual(a.PodSelector, b.PodSelector) {
		return false
	}
	if !equality.Semantic.DeepEqual(a.PolicyRef, b.PolicyRef) {
		return false
	}
	if !equalPodIsolation(a.PodIsolation, b.PodIsolation) {
		return false
	}
	// Regrouping can leave the rules and the pods identical and change only this, so comparing
	// it is what makes the PolicyEndpoint get updated.
	if a.TargetGroup != b.TargetGroup {
		return false
	}

	// Unordered slice fields.
	if !equalEndpointInfoSlice(a.Ingress, b.Ingress) {
		return false
	}
	if !equalEndpointInfoSlice(a.Egress, b.Egress) {
		return false
	}
	if !equalPodEndpointSlice(a.PodSelectorEndpoints, b.PodSelectorEndpoints) {
		return false
	}
	return true
}

// EqualClusterPolicyEndpointSpec returns true when two
// ClusterPolicyEndpointSpec values are semantically equal, treating
// Ingress, Egress, and PodSelectorEndpoints as unordered sets.
func EqualClusterPolicyEndpointSpec(a, b policyinfo.ClusterPolicyEndpointSpec) bool {
	if !equality.Semantic.DeepEqual(a.PolicyRef, b.PolicyRef) {
		return false
	}
	if a.Tier != b.Tier {
		return false
	}
	if a.Priority != b.Priority {
		return false
	}
	if !equality.Semantic.DeepEqual(a.Subject, b.Subject) {
		return false
	}
	if !equalClusterEndpointInfoSlice(a.Ingress, b.Ingress) {
		return false
	}
	if !equalClusterEndpointInfoSlice(a.Egress, b.Egress) {
		return false
	}
	if !equalPodEndpointSlice(a.PodSelectorEndpoints, b.PodSelectorEndpoints) {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// EndpointInfo helpers
// ---------------------------------------------------------------------------

// endpointInfoKey produces a canonical string for an EndpointInfo that is
// independent of the order of its Ports and Except sub-slices.
func endpointInfoKey(e policyinfo.EndpointInfo) string {
	return fmt.Sprintf("cidr=%s,domain=%s,ports=%s,except=%s",
		e.CIDR, e.DomainName, canonicalPorts(e.Ports), canonicalExcept(e.Except))
}

func equalEndpointInfoSlice(a, b []policyinfo.EndpointInfo) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[string]int, len(a))
	for _, e := range a {
		counts[endpointInfoKey(e)]++
	}
	for _, e := range b {
		k := endpointInfoKey(e)
		counts[k]--
		if counts[k] < 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// ClusterEndpointInfo helpers
// ---------------------------------------------------------------------------

func clusterEndpointInfoKey(e policyinfo.ClusterEndpointInfo) string {
	return fmt.Sprintf("cidr=%s,domain=%s,action=%s,ports=%s",
		e.CIDR, e.DomainName, e.Action, canonicalPorts(e.Ports))
}

func equalClusterEndpointInfoSlice(a, b []policyinfo.ClusterEndpointInfo) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[string]int, len(a))
	for _, e := range a {
		counts[clusterEndpointInfoKey(e)]++
	}
	for _, e := range b {
		k := clusterEndpointInfoKey(e)
		counts[k]--
		if counts[k] < 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// PodEndpoint helpers
// ---------------------------------------------------------------------------

func podEndpointKey(p policyinfo.PodEndpoint) string {
	return fmt.Sprintf("%s/%s/%s/%s", p.Namespace, p.Name, p.PodIP, p.HostIP)
}

func equalPodEndpointSlice(a, b []policyinfo.PodEndpoint) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[string]int, len(a))
	for _, p := range a {
		counts[podEndpointKey(p)]++
	}
	for _, p := range b {
		k := podEndpointKey(p)
		counts[k]--
		if counts[k] < 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// PodIsolation ([]networking.PolicyType) — small slice, order-insensitive
// ---------------------------------------------------------------------------

func equalPodIsolation(a, b []networking.PolicyType) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[networking.PolicyType]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Canonical stringifiers for sub-slices (sorted for determinism)
// ---------------------------------------------------------------------------

func canonicalPorts(ports []policyinfo.Port) string {
	if len(ports) == 0 {
		return ""
	}
	keys := make([]string, 0, len(ports))
	for _, p := range ports {
		keys = append(keys, portKey(p))
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}

func portKey(p policyinfo.Port) string {
	proto, port, endPort := "", "", ""
	if p.Protocol != nil {
		proto = string(*p.Protocol)
	}
	if p.Port != nil {
		port = fmt.Sprintf("%d", *p.Port)
	}
	if p.EndPort != nil {
		endPort = fmt.Sprintf("%d", *p.EndPort)
	}
	return fmt.Sprintf("%s/%s/%s", proto, port, endPort)
}

func canonicalExcept(except []policyinfo.NetworkAddress) string {
	if len(except) == 0 {
		return ""
	}
	sorted := make([]string, len(except))
	for i, e := range except {
		sorted[i] = string(e)
	}
	sort.Strings(sorted)
	return fmt.Sprint(sorted)
}
