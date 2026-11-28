package policyendpoints

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// A no-op reconcile runs all three packing passes with nothing to place, and a pod-only change
// runs the ingress and egress passes empty. Such a pass must not order the targets, which costs
// a sort and a scan per pass. Fill order sorts by name, so an untouched list proves it was skipped.
func TestPack_PassWithNothingToPlaceLeavesTheTargetsAlone(t *testing.T) {
	m := &policyEndpointsManager{endpointChunkSize: 200, logger: logr.Discard()}
	pe := func(name string) policyinfo.PolicyEndpoint {
		return policyinfo.PolicyEndpoint{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	targets := newPolicyEndpointTargets()
	targets.modified = []policyinfo.PolicyEndpoint{pe("z"), pe("a")}
	m.packingIngressRules(PolicyMetadata{}, nil, targets)
	m.packingEgressRules(PolicyMetadata{}, map[string]policyinfo.EndpointInfo{}, targets)
	m.packingPodSelectorEndpoints(PolicyMetadata{}, nil, targets)
	assert.Equal(t, []string{"z", "a"}, []string{targets.modified[0].Name, targets.modified[1].Name})
	assert.Empty(t, targets.created)

	cpe := func(name string) policyinfo.ClusterPolicyEndpoint {
		return policyinfo.ClusterPolicyEndpoint{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	clusterTargets := newClusterPolicyEndpointTargets()
	clusterTargets.modified = []policyinfo.ClusterPolicyEndpoint{cpe("z"), cpe("a")}
	m.packingClusterIngressRules(ClusterPolicyMetadata{}, nil, clusterTargets)
	m.packingClusterEgressRules(ClusterPolicyMetadata{}, map[string]policyinfo.ClusterEndpointInfo{}, clusterTargets)
	m.packingClusterPodSelectorEndpoints(ClusterPolicyMetadata{}, nil, clusterTargets)
	assert.Equal(t, []string{"z", "a"}, []string{clusterTargets.modified[0].Name, clusterTargets.modified[1].Name})
	assert.Empty(t, clusterTargets.created)
}
