//go:build e2e

/*
Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package startup

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

const (
	namespaceGroupLabel = "networking.k8s.aws/npc-scale-group"
	// servicePort is used by every rule and service when services are generated, so that egress rules resolve
	// service ClusterIPs.
	servicePort = 8080
	// cnpNamePrefix prefixes the names of generated ClusterNetworkPolicies.
	cnpNamePrefix = "scale-cnp-"
)

// workload describes the shape of the generated cluster. Each field exercises a different controller code path:
//
//   - namespaces spreads pods and policies, which bounds the namespace-scoped work done per pod event.
//   - nsSelectorPercent policies use a namespaceSelector peer. The policy tracker evaluates those policies for every
//     pod event in every namespace, and resolving them lists namespaces and then pods in each matching namespace.
//   - broadPolicies select every pod in their namespace and allow egress to every pod in every test namespace,
//     which produces PolicyEndpoints with every generated pod IP.
//   - servicesPerNamespace services back the app labels that egress rules target, which makes egress resolution
//     list services and add their ClusterIPs.
//   - anpPercent of the policies are ApplicationNetworkPolicies, which resolve their rules twice.
//   - cnps ClusterNetworkPolicies are evaluated for every pod, namespace, and service event in the cluster, and
//     are resolved once per subject namespace.
//   - ipBlocksPerPolicy adds ipBlock egress peers, which make policy objects larger without adding pod lookups.
type workload struct {
	namespaces           int
	namespaceGroups      int
	nsSelectorPercent    int
	broadPolicies        int
	servicesPerNamespace int
	anpPercent           int
	cnps                 int
	ipBlocksPerPolicy    int
}

// scenarios are named presets for workload. Individual NPC_SCALE_* variables override preset values.
var scenarios = map[string]workload{
	// baseline is the original shape: one namespace, policies selecting pods by app label.
	"baseline": {namespaces: 1, namespaceGroups: 1, nsSelectorPercent: 10},
	// multi-namespace spreads the same policies across many namespaces.
	"multi-namespace": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 10},
	// namespace-selector makes every policy reference other namespaces.
	"namespace-selector": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 100},
	// broad adds allow-all-cluster egress policies on top of multi-namespace.
	"broad": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 10, broadPolicies: 50},
	// services adds services behind every egress target.
	"services": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 10, servicesPerNamespace: 10},
	// anp creates every policy as an ApplicationNetworkPolicy.
	"anp": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 10, anpPercent: 100},
	// cnp adds ClusterNetworkPolicies on top of multi-namespace.
	"cnp": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 10, cnps: 50},
	// ipblock gives every policy many ipBlock egress peers.
	"ipblock": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 10, ipBlocksPerPolicy: 50},
	// mixed combines every shape.
	"mixed": {namespaces: 50, namespaceGroups: 5, nsSelectorPercent: 30, broadPolicies: 10, servicesPerNamespace: 5,
		anpPercent: 20, cnps: 20, ipBlocksPerPolicy: 10},
}

func scenarioNames() []string {
	names := make([]string, 0, len(scenarios))
	for name := range scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (w workload) String() string {
	return fmt.Sprintf("namespaces=%d namespaceGroups=%d nsSelectorPercent=%d broadPolicies=%d servicesPerNamespace=%d "+
		"anpPercent=%d cnps=%d ipBlocksPerPolicy=%d", w.namespaces, w.namespaceGroups, w.nsSelectorPercent,
		w.broadPolicies, w.servicesPerNamespace, w.anpPercent, w.cnps, w.ipBlocksPerPolicy)
}

// expectedPolicies is the number of policies of any kind the controller has to reconcile.
func (w workload) expectedPolicies() int {
	return cfg.policies + w.broadPolicies + w.cnps
}

func testNamespace(i int) string {
	if cfg.workload.namespaces == 1 {
		return cfg.namespace
	}
	return fmt.Sprintf("%s-%d", cfg.namespace, i%cfg.workload.namespaces)
}

func namespaceGroup(i int) string {
	return fmt.Sprintf("group-%d", i%cfg.workload.namespaceGroups)
}

func newTestNamespace(i int) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: testNamespace(i),
		Labels: map[string]string{
			scaleTestLabel:      "true",
			namespaceGroupLabel: namespaceGroup(i),
		},
	}}
}

// appLabel returns the app label of the i-th object within its namespace. Objects are assigned to namespaces
// round robin, so the index within the namespace is i / namespaces.
func appLabel(i int) string {
	return fmt.Sprintf("app-%d", (i/cfg.workload.namespaces)%cfg.appLabels)
}

func appSelector(app string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{appLabelKey: app}}
}

func nextApp(i, offset int) string {
	return fmt.Sprintf("app-%d", ((i/cfg.workload.namespaces)+offset)%cfg.appLabels)
}

func newKwokPod(i int) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("scale-pod-%d", i),
			Namespace: testNamespace(i),
			Labels: map[string]string{
				appLabelKey:    appLabel(i),
				scaleTestLabel: "true",
			},
		},
		Spec: corev1.PodSpec{
			// Binding directly to the kwok node skips the scheduler, which keeps pod creation fast.
			NodeName: kwokNodeName(i % cfg.kwokNodes),
			// The unreachable and not-ready tolerations keep the node controller from evicting the pods when the kwok
			// controller restarts and its nodes miss heartbeats.
			Tolerations: []corev1.Toleration{
				{Key: kwokTaintKey, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
				{Key: corev1.TaintNodeUnreachable, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
				{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
			},
			Containers: []corev1.Container{{
				Name:  "app",
				Image: "registry.k8s.io/pause:3.10",
				Ports: []corev1.ContainerPort{{ContainerPort: servicePort, Protocol: corev1.ProtocolTCP}},
			}},
		},
	}
}

// newService builds service i of namespace ns, which backs pods with the i-th app label.
func newService(ns, i int) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("scale-svc-%d", i),
			Namespace: testNamespace(ns),
			Labels:    map[string]string{scaleTestLabel: "true"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{appLabelKey: fmt.Sprintf("app-%d", i%cfg.appLabels)},
			Ports: []corev1.ServicePort{{
				Port:       servicePort,
				TargetPort: intstr.FromInt32(servicePort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// policyPort returns the port used by the rules of policy i. Ports vary unless services are generated, in which
// case every rule uses the service port so egress resolution matches the services.
func policyPort(i int) intstr.IntOrString {
	if cfg.workload.servicesPerNamespace > 0 {
		return intstr.FromInt32(servicePort)
	}
	return intstr.FromInt32(int32(8000 + i%1000))
}

func usesNamespaceSelector(i int) bool {
	return i%100 < cfg.workload.nsSelectorPercent
}

func isANP(i int) bool {
	// Offset from usesNamespaceSelector so the two percentages select independent policies.
	return (i+50)%100 < cfg.workload.anpPercent
}

// ingressPeer allows traffic from the next app label in the policy's namespace, or, for namespace-selector
// policies, from the next app label in every namespace of the next namespace group.
func ingressPeer(i int) networking.NetworkPolicyPeer {
	peer := networking.NetworkPolicyPeer{PodSelector: appSelector(nextApp(i, 1))}
	if usesNamespaceSelector(i) {
		peer.NamespaceSelector = &metav1.LabelSelector{
			MatchLabels: map[string]string{namespaceGroupLabel: namespaceGroup(i + 1)},
		}
	}
	return peer
}

func ipBlocks(i int) []networking.IPBlock {
	blocks := make([]networking.IPBlock, 0, cfg.workload.ipBlocksPerPolicy)
	for b := 0; b < cfg.workload.ipBlocksPerPolicy; b++ {
		blocks = append(blocks, networking.IPBlock{
			CIDR:   fmt.Sprintf("172.%d.%d.0/24", 16+(i%16), b%256),
			Except: []string{fmt.Sprintf("172.%d.%d.128/25", 16+(i%16), b%256)},
		})
	}
	return blocks
}

// newPolicy builds policy i as a NetworkPolicy or, for anpPercent of the indexes, an ApplicationNetworkPolicy.
// NetworkPolicies and ApplicationNetworkPolicies get different name prefixes because their PolicyEndpoints are
// looked up by namespace and name only.
func newPolicy(i int) client.Object {
	port := policyPort(i)
	ports := []networking.NetworkPolicyPort{{Protocol: ptr.To(corev1.ProtocolTCP), Port: &port}}
	meta := metav1.ObjectMeta{
		Name:      fmt.Sprintf("scale-policy-%d", i),
		Namespace: testNamespace(i),
		Labels:    map[string]string{scaleTestLabel: "true"},
	}
	ingress := []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{ingressPeer(i)}, Ports: ports}}
	podSelector := metav1.LabelSelector{MatchLabels: map[string]string{appLabelKey: appLabel(i)}}
	policyTypes := []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress}

	if isANP(i) {
		meta.Name = fmt.Sprintf("scale-anp-%d", i)
		egressPeers := []policyinfo.ApplicationNetworkPolicyPeer{{PodSelector: appSelector(nextApp(i, 2))}}
		for _, block := range ipBlocks(i) {
			egressPeers = append(egressPeers, policyinfo.ApplicationNetworkPolicyPeer{IPBlock: &block})
		}
		return &policyinfo.ApplicationNetworkPolicy{
			ObjectMeta: meta,
			Spec: policyinfo.ApplicationNetworkPolicySpec{
				PodSelector: podSelector,
				PolicyTypes: policyTypes,
				Ingress:     ingress,
				Egress:      []policyinfo.ApplicationNetworkPolicyEgressRule{{To: egressPeers, Ports: ports}},
			},
		}
	}

	egressPeers := []networking.NetworkPolicyPeer{{PodSelector: appSelector(nextApp(i, 2))}}
	for _, block := range ipBlocks(i) {
		egressPeers = append(egressPeers, networking.NetworkPolicyPeer{IPBlock: &block})
	}
	return &networking.NetworkPolicy{
		ObjectMeta: meta,
		Spec: networking.NetworkPolicySpec{
			PodSelector: podSelector,
			PolicyTypes: policyTypes,
			Ingress:     ingress,
			Egress:      []networking.NetworkPolicyEgressRule{{To: egressPeers, Ports: ports}},
		},
	}
}

// newBroadPolicy builds a policy that selects every pod in its namespace and allows egress to every pod in every
// test namespace.
func newBroadPolicy(i int) *networking.NetworkPolicy {
	return &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("scale-broad-%d", i),
			Namespace: testNamespace(i),
			Labels:    map[string]string{scaleTestLabel: "true"},
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
			Egress: []networking.NetworkPolicyEgressRule{{
				To: []networking.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{scaleTestLabel: "true"}},
					PodSelector:       &metav1.LabelSelector{},
				}},
			}},
		},
	}
}

// newClusterNetworkPolicy builds CNP i. It applies to one namespace group, accepts ingress from the next group,
// denies egress to the instance metadata endpoint, and accepts egress to one app label in the group after that.
func newClusterNetworkPolicy(i int) *policyinfo.ClusterNetworkPolicy {
	tier := policyinfo.AdminTier
	if i%2 == 1 {
		tier = policyinfo.BaselineTier
	}
	groupSelector := func(offset int) metav1.LabelSelector {
		return metav1.LabelSelector{MatchLabels: map[string]string{namespaceGroupLabel: namespaceGroup(i + offset)}}
	}
	ports := &[]policyinfo.ClusterNetworkPolicyPort{{
		PortNumber: &policyinfo.CNPPort{Protocol: corev1.ProtocolTCP, Port: servicePort},
	}}
	return &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s%d", cnpNamePrefix, i),
			Labels: map[string]string{scaleTestLabel: "true"},
		},
		Spec: policyinfo.ClusterNetworkPolicySpec{
			Tier:     tier,
			Priority: int32(i % 1000),
			Subject:  policyinfo.ClusterNetworkPolicySubject{Namespaces: ptr.To(groupSelector(0))},
			Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{{
				Name:   "from-next-group",
				Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
				From:   []policyinfo.ClusterNetworkPolicyIngressPeer{{Namespaces: ptr.To(groupSelector(1))}},
				Ports:  ports,
			}},
			Egress: []policyinfo.ClusterNetworkPolicyEgressRule{
				{
					Name:   "deny-instance-metadata",
					Action: policyinfo.ClusterNetworkPolicyRuleActionDeny,
					To:     []policyinfo.ClusterNetworkPolicyEgressPeer{{Networks: []policyinfo.CIDR{"169.254.169.254/32"}}},
				},
				{
					Name:   "to-app",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					To: []policyinfo.ClusterNetworkPolicyEgressPeer{{Pods: &policyinfo.NamespacedPod{
						NamespaceSelector: groupSelector(2),
						PodSelector:       *appSelector(fmt.Sprintf("app-%d", i%cfg.appLabels)),
					}}},
					Ports: ports,
				},
			},
		},
	}
}

// testNamespaceNames returns the names of all generated namespaces.
func testNamespaceNames() []string {
	names := make([]string, 0, cfg.workload.namespaces)
	for i := 0; i < cfg.workload.namespaces; i++ {
		names = append(names, testNamespace(i))
	}
	return names
}

func isTestNamespace(ns string) bool {
	return ns == cfg.namespace || strings.HasPrefix(ns, cfg.namespace+"-")
}
