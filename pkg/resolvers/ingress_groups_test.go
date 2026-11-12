package resolvers

import (
	"context"
	"testing"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func groupScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, policyinfo.AddToScheme(s))
	return s
}

func readyPod(name, ns string, labels map[string]string, ip string, ports ...corev1.ContainerPort) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: ports}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			PodIP:      ip,
			HostIP:     "10.0.0.1",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// namedPortPolicy allows ingress from app=client on the named port "api".
func namedPortPolicy() *networking.NetworkPolicy {
	api := intstr.FromString("api")
	tcp := corev1.ProtocolTCP
	return &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "np", Namespace: "dst"},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress},
			Ingress: []networking.NetworkPolicyIngressRule{{
				Ports: []networking.NetworkPolicyPort{{Protocol: &tcp, Port: &api}},
				From: []networking.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "client"}},
				}},
			}},
		},
	}
}

func portsOf(t *testing.T, eps []policyinfo.EndpointInfo) []int32 {
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

func TestResolveGroups_DivergentNamedPorts(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dst", Labels: map[string]string{"kubernetes.io/metadata.name": "dst"}}}
	podA := readyPod("a", "dst", map[string]string{"app": "api"}, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "dst", map[string]string{"app": "api"}, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	client := readyPod("c", "dst", map[string]string{"app": "client"}, "192.168.0.3")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(ns, podA, podB, client).Build()
	r := NewEndpointsResolver(fakeClient, logr.Discard())

	groups, _, err := r.ResolveGroups(context.TODO(), namedPortPolicy())
	require.NoError(t, err)
	require.Len(t, groups, 2, "the pods resolve api differently, so they cannot share a rule set")

	byPod := map[string][]int32{}
	keys := map[string]bool{}
	for _, g := range groups {
		assert.NotEmpty(t, g.Key, "with more than one group every object must be stamped")
		keys[g.Key] = true
		require.Len(t, g.PodEndpoints, 1)
		byPod[g.PodEndpoints[0].Name] = portsOf(t, g.Ingress)
	}
	assert.Len(t, keys, 2, "the two groups must have distinct keys")
	assert.Equal(t, map[string][]int32{"a": {8080}, "b": {9090}}, byPod,
		"each pod must be allowed only the port it resolved")
}

func TestResolveGroups_UniformNamedPortsStayUngrouped(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dst", Labels: map[string]string{"kubernetes.io/metadata.name": "dst"}}}
	podA := readyPod("a", "dst", map[string]string{"app": "api"}, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "dst", map[string]string{"app": "api"}, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	client := readyPod("c", "dst", map[string]string{"app": "client"}, "192.168.0.3")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(ns, podA, podB, client).Build()
	r := NewEndpointsResolver(fakeClient, logr.Discard())

	groups, _, err := r.ResolveGroups(context.TODO(), namedPortPolicy())
	require.NoError(t, err)
	require.Len(t, groups, 1)
	// Unset, so a cluster with no divergence produces the same objects it did before grouping
	// and no agent sees the new field.
	assert.Empty(t, groups[0].Key)
	assert.Len(t, groups[0].PodEndpoints, 2)
	assert.Equal(t, []int32{8080}, portsOf(t, groups[0].Ingress))
}

func TestResolveGroups_NumericPortsNeverGroup(t *testing.T) {
	tcp := corev1.ProtocolTCP
	numeric := intstr.FromInt(80)
	policy := namedPortPolicy()
	policy.Spec.Ingress[0].Ports = []networking.NetworkPolicyPort{{Protocol: &tcp, Port: &numeric}}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dst", Labels: map[string]string{"kubernetes.io/metadata.name": "dst"}}}
	podA := readyPod("a", "dst", map[string]string{"app": "api"}, "192.168.0.1", corev1.ContainerPort{Name: "api", ContainerPort: 8080, Protocol: corev1.ProtocolTCP})
	podB := readyPod("b", "dst", map[string]string{"app": "api"}, "192.168.0.2", corev1.ContainerPort{Name: "api", ContainerPort: 9090, Protocol: corev1.ProtocolTCP})
	client := readyPod("c", "dst", map[string]string{"app": "client"}, "192.168.0.3")

	fakeClient := fake.NewClientBuilder().WithScheme(groupScheme(t)).
		WithRuntimeObjects(ns, podA, podB, client).Build()
	r := NewEndpointsResolver(fakeClient, logr.Discard())

	groups, _, err := r.ResolveGroups(context.TODO(), policy)
	require.NoError(t, err)
	require.Len(t, groups, 1, "a numeric port means the same thing on every pod, so it never splits")
	assert.Empty(t, groups[0].Key)
	assert.Equal(t, []int32{80}, portsOf(t, groups[0].Ingress))
}

func TestPortSetKey(t *testing.T) {
	tcp := func(n int32) policyinfo.Port {
		p := corev1.ProtocolTCP
		v := n
		return policyinfo.Port{Protocol: &p, Port: &v}
	}
	// dedupPorts returns map values, so the key must not depend on order.
	assert.Equal(t, portSetKey([]policyinfo.Port{tcp(8080), tcp(9090)}), portSetKey([]policyinfo.Port{tcp(9090), tcp(8080)}))
	assert.NotEqual(t, portSetKey([]policyinfo.Port{tcp(8080)}), portSetKey([]policyinfo.Port{tcp(9090)}))
	assert.NotEqual(t, portSetKey([]policyinfo.Port{tcp(8080)}), portSetKey([]policyinfo.Port{tcp(8080), tcp(9090)}))
	// Resolved nothing is its own group, distinct from any real set.
	assert.Equal(t, "none", portSetKey(nil))
	assert.NotEqual(t, "none", portSetKey([]policyinfo.Port{tcp(8080)}))
}
