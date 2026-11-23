package resolvers

import (
	"context"
	"fmt"
	"testing"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func subjectNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"role": "subject"}}}
}

func peerNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"role": "peer"}}}
}

// namedCNPPort is a CNP ingress port given by name, which resolves against each subject pod.
func namedCNPPort(name string) []policyinfo.ClusterNetworkPolicyPort {
	return []policyinfo.ClusterNetworkPolicyPort{{NamedPort: &name}}
}

func numericCNPPort(port int32) []policyinfo.ClusterNetworkPolicyPort {
	return []policyinfo.ClusterNetworkPolicyPort{{
		PortNumber: &policyinfo.CNPPort{Protocol: corev1.ProtocolTCP, Port: port},
	}}
}

// cnpWithRules is a CNP whose subject is every pod of the namespaces labelled role=subject.
func cnpWithRules(rules ...policyinfo.ClusterNetworkPolicyIngressRule) *policyinfo.ClusterNetworkPolicy {
	return &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "cnp"},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Tier:     policyinfo.AdminTier,
			Priority: 10,
			Subject: policyinfo.ClusterNetworkPolicySubject{
				Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "subject"}},
			},
			Ingress: rules,
		},
	}
}

// peerRule allows or denies the pods of the namespaces labelled role=peer on the given ports.
func peerRule(name string, action policyinfo.ClusterNetworkPolicyRuleAction, ports []policyinfo.ClusterNetworkPolicyPort) policyinfo.ClusterNetworkPolicyIngressRule {
	rule := policyinfo.ClusterNetworkPolicyIngressRule{
		Name:   name,
		Action: action,
		From: []policyinfo.ClusterNetworkPolicyIngressPeer{{
			Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "peer"}},
		}},
	}
	if ports != nil {
		rule.Ports = &ports
	}
	return rule
}

// actionPortsOf reports every (action, port) pair a group resolved, which is what tells a rule's
// action apart from a neighbour's after grouping.
func actionPortsOf(t *testing.T, eps []policyinfo.ClusterEndpointInfo) []string {
	t.Helper()
	var out []string
	for _, ep := range eps {
		for _, p := range ep.Ports {
			require.NotNil(t, p.Port)
			out = append(out, fmt.Sprintf("%s:%d", ep.Action, *p.Port))
		}
	}
	return out
}

// cidrPortsOf reports the peer each port was allowed from.
func cidrPortsOf(t *testing.T, eps []policyinfo.ClusterEndpointInfo) []string {
	t.Helper()
	var out []string
	for _, ep := range eps {
		for _, p := range ep.Ports {
			require.NotNil(t, p.Port)
			out = append(out, fmt.Sprintf("%s:%d", ep.CIDR, *p.Port))
		}
	}
	return out
}

func clusterPortsOf(t *testing.T, eps []policyinfo.ClusterEndpointInfo) []int32 {
	t.Helper()
	var out []int32
	for _, ep := range eps {
		for _, p := range ep.Ports {
			require.NotNil(t, p.Port)
			out = append(out, *p.Port)
		}
	}
	return out
}

// podsOf names a group's pods as namespace/name, so a group spanning namespaces is visible.
func podsOf(g ClusterIngressGroup) []string {
	var out []string
	for _, pe := range g.PodEndpoints {
		out = append(out, pe.Namespace+"/"+pe.Name)
	}
	return out
}

func TestResolveClusterNetworkPolicyGroups_DivergentNamedPorts(t *testing.T) {
	podA := readyPod("a", "sub", nil, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub", nil, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub"), peerNamespace("peer"), podA, podB, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")))
	groups, shared, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 2, "the subject pods resolve api differently, so they cannot share a rule set")

	byPod := map[string][]int32{}
	keys := map[string]bool{}
	for _, g := range groups {
		assert.NotEmpty(t, g.Key, "with more than one group every object must be stamped")
		keys[g.Key] = true
		require.Len(t, g.PodEndpoints, 1)
		byPod[g.PodEndpoints[0].Name] = clusterPortsOf(t, g.Ingress)
	}
	assert.Len(t, keys, 2, "the two groups must have distinct keys")
	assert.Equal(t, map[string][]int32{"a": {8080}, "b": {9090}}, byPod,
		"each pod must be allowed only the port it resolved")
	assert.Empty(t, shared, "the only rule names a port, so every rule belongs to a group")
}

// A ClusterNetworkPolicy is cluster-scoped and one ClusterPolicyEndpoint carries pods from every
// subject namespace, so a group is keyed by the ports it resolved and not by namespace: pods that
// resolved the same ports share a group wherever they live.
func TestResolveClusterNetworkPolicyGroups_GroupSpansNamespaces(t *testing.T) {
	podA := readyPod("a", "sub-1", nil, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub-2", nil, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podC := readyPod("c", "sub-2", nil, "192.168.0.3", corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub-1"), subjectNamespace("sub-2"), peerNamespace("peer"),
			podA, podB, podC, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")))
	groups, _, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 2, "two distinct port sets, not three namespaces' worth of groups")

	byPorts := map[int32][]string{}
	for _, g := range groups {
		ports := clusterPortsOf(t, g.Ingress)
		require.Len(t, ports, 1)
		byPorts[ports[0]] = podsOf(g)
	}
	assert.ElementsMatch(t, []string{"sub-1/a", "sub-2/b"}, byPorts[8080],
		"pods in different namespaces that resolved the same port share one group")
	assert.Equal(t, []string{"sub-2/c"}, byPorts[9090])
}

// Before grouping, named ports resolved against the pods of one subject namespace only and the
// result was applied to every subject pod, so a pod in another namespace was allowed a port it
// does not serve.
func TestResolveClusterNetworkPolicyGroups_NamedPortsResolvePerNamespace(t *testing.T) {
	podA := readyPod("a", "sub-1", nil, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub-2", nil, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub-1"), subjectNamespace("sub-2"), peerNamespace("peer"),
			podA, podB, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")))
	groups, _, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 2)

	byPod := map[string][]int32{}
	for _, g := range groups {
		require.Len(t, g.PodEndpoints, 1)
		byPod[g.PodEndpoints[0].Namespace] = clusterPortsOf(t, g.Ingress)
	}
	assert.Equal(t, map[string][]int32{"sub-1": {8080}, "sub-2": {9090}}, byPod,
		"a pod must be allowed only the port its own container serves")
}

func TestResolveClusterNetworkPolicyGroups_UniformNamedPortsStayUngrouped(t *testing.T) {
	podA := readyPod("a", "sub", nil, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub", nil, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub"), peerNamespace("peer"), podA, podB, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")))
	groups, shared, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	// Unset, so a cluster with no divergence produces the same objects it did before grouping and
	// no agent sees the new field.
	assert.Empty(t, groups[0].Key)
	assert.Len(t, groups[0].PodEndpoints, 2)
	assert.Equal(t, []int32{8080}, clusterPortsOf(t, groups[0].Ingress))
	assert.Empty(t, shared, "one group carries every rule, so nothing is shared beside it")
}

func TestResolveClusterNetworkPolicyGroups_NumericPortsNeverGroup(t *testing.T) {
	podA := readyPod("a", "sub", nil, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub", nil, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub"), peerNamespace("peer"), podA, podB, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, numericCNPPort(80)))
	groups, shared, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 1, "a numeric port means the same thing on every pod, so it never splits")
	assert.Empty(t, groups[0].Key)
	assert.Equal(t, []int32{80}, clusterPortsOf(t, groups[0].Ingress))
	assert.Empty(t, shared, "without a split the rule stays in the single group, as it was before grouping")
}

// Each CNP ingress rule carries its own action, so grouping must keep a rule's resolved ports with
// that rule's action rather than merging rules that resolved the same ports.
func TestResolveClusterNetworkPolicyGroups_PreservesRuleActions(t *testing.T) {
	podA := readyPod("a", "sub", nil, "192.168.0.1",
		corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
		corev1.ContainerPort{Name: "admin", ContainerPort: 8081, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub", nil, "192.168.0.2",
		corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
		corev1.ContainerPort{Name: "admin", ContainerPort: 9091, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub"), peerNamespace("peer"), podA, podB, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(
		peerRule("allow-api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")),
		peerRule("deny-admin", policyinfo.ClusterNetworkPolicyRuleActionDeny, namedCNPPort("admin")),
	)
	groups, shared, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 2)
	assert.Empty(t, shared, "both rules name a port, so both belong to a group")

	byPod := map[string][]string{}
	for _, g := range groups {
		require.Len(t, g.PodEndpoints, 1)
		byPod[g.PodEndpoints[0].Name] = actionPortsOf(t, g.Ingress)
	}
	assert.ElementsMatch(t, []string{"Accept:8080", "Deny:8081"}, byPod["a"],
		"each rule's resolved port must keep that rule's action")
	assert.ElementsMatch(t, []string{"Accept:9090", "Deny:9091"}, byPod["b"])
}

// A split policy's pod-independent rules are carried once, beside the groups, rather than copied
// into each of them: an ungrouped ClusterPolicyEndpoint applies to every subject pod.
func TestResolveClusterNetworkPolicyGroups_PodIndependentRulesAreShared(t *testing.T) {
	podA := readyPod("a", "sub", nil, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub", nil, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub"), peerNamespace("peer"), podA, podB, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(
		peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")),
		peerRule("metrics", policyinfo.ClusterNetworkPolicyRuleActionDeny, numericCNPPort(9100)),
	)
	groups, shared, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 2, "the named port still splits the pods")

	byPod := map[string][]string{}
	for _, g := range groups {
		require.Len(t, g.PodEndpoints, 1)
		byPod[g.PodEndpoints[0].Name] = actionPortsOf(t, g.Ingress)
	}
	assert.Equal(t, map[string][]string{"a": {"Accept:8080"}, "b": {"Accept:9090"}}, byPod,
		"a group carries only the rules whose ports it resolved, not the numeric rule")
	assert.Equal(t, []string{"Deny:9100"}, actionPortsOf(t, shared),
		"the numeric rule is carried once, with its own action, for every pod")
}

// Preserved from before grouping: with no subject namespace there are no destination pods to
// resolve a named port against, so the rule carries nothing rather than everything.
func TestResolveClusterNetworkPolicyGroups_NamedPortsWithoutSubjectNamespaceSkipped(t *testing.T) {
	peer := readyPod("p", "peer", nil, "192.168.1.1")
	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(peerNamespace("peer"), peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(
		peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")),
		peerRule("metrics", policyinfo.ClusterNetworkPolicyRuleActionDeny, numericCNPPort(9100)),
	)
	groups, shared, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 1, "no subject pods, so nothing splits")
	assert.Empty(t, groups[0].Key)
	assert.Empty(t, shared)
	assert.Equal(t, []string{"Deny:9100"}, actionPortsOf(t, groups[0].Ingress),
		"the named-port rule is skipped, the numeric one still resolves")
}

// A named port no subject pod declares resolves to nothing, so the rule allows that group nothing
// rather than every port.
func TestResolveClusterNetworkPolicyGroups_UnresolvableNamedPortAllowsNothing(t *testing.T) {
	podA := readyPod("a", "sub", nil, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "sub", nil, "192.168.0.2", corev1.ContainerPort{Name: "other", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	peer := readyPod("p", "peer", nil, "192.168.1.1")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(subjectNamespace("sub"), peerNamespace("peer"), podA, podB, peer).Build()
	r := NewClusterNetworkPolicyEndpointsResolver(fakeClient, logr.Discard())

	cnp := cnpWithRules(peerRule("api", policyinfo.ClusterNetworkPolicyRuleActionAccept, namedCNPPort("api")))
	groups, _, _, err := r.ResolveClusterNetworkPolicyGroups(context.TODO(), cnp)
	require.NoError(t, err)
	require.Len(t, groups, 2)

	byPod := map[string][]string{}
	for _, g := range groups {
		require.Len(t, g.PodEndpoints, 1)
		byPod[g.PodEndpoints[0].Name] = cidrPortsOf(t, g.Ingress)
	}
	assert.Equal(t, []string{"192.168.1.1:8080"}, byPod["a"])
	assert.Empty(t, byPod["b"], "podB declares no api port, so the rule must not open every port on it")
}
