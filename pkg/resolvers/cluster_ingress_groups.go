package resolvers

import (
	"context"
	"fmt"
	"sort"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClusterIngressGroup is a set of subject pods that resolved the policy's ingress ports the same
// way. A group spans namespaces, since the policy is cluster-scoped and one ClusterPolicyEndpoint
// already carries pods from every subject namespace.
type ClusterIngressGroup struct {
	// Key is empty when every subject pod agreed, leaving spec.targetGroup unset.
	Key          string
	Ingress      []policyinfo.ClusterEndpointInfo
	PodEndpoints []policyinfo.PodEndpoint
}

// ResolveClusterNetworkPolicyGroups is ResolveClusterNetworkPolicy with the ingress rules and
// subject pods partitioned by the ports each pod resolved. A rule naming no container port goes
// to the shared return instead of a copy per group, as does egress.
func (r *clusterNetworkPolicyEndpointsResolver) ResolveClusterNetworkPolicyGroups(ctx context.Context,
	cnp *policyinfo.ClusterNetworkPolicy) ([]ClusterIngressGroup, []policyinfo.ClusterEndpointInfo,
	[]policyinfo.ClusterEndpointInfo, error) {

	podEndpoints, err := r.resolvePodEndpointsFromSubject(ctx, cnp.Spec.Subject)
	if err != nil {
		return nil, nil, nil, err
	}

	var egressRules []policyinfo.ClusterEndpointInfo
	if len(cnp.Spec.Egress) > 0 {
		egressRules, err = r.resolveCNPEgressRules(ctx, cnp)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	groups, sharedIngress, err := r.resolveCNPIngressGroups(ctx, cnp, podEndpoints)
	if err != nil {
		return nil, nil, nil, err
	}

	r.logger.Info("Resolved ClusterNetworkPolicy endpoints", "policy", cnp.Name, "ingressGroups", len(groups),
		"sharedIngress", len(sharedIngress), "egress", len(egressRules), "pod selector endpoints", len(podEndpoints))

	return groups, sharedIngress, egressRules, nil
}

// resolveCNPIngressGroups builds one group's ingress per distinct port set the subject pods
// resolved, plus the ingress that does not depend on the pod.
func (r *clusterNetworkPolicyEndpointsResolver) resolveCNPIngressGroups(ctx context.Context,
	cnp *policyinfo.ClusterNetworkPolicy, podEndpoints []policyinfo.PodEndpoint) ([]ClusterIngressGroup,
	[]policyinfo.ClusterEndpointInfo, error) {

	subjectNamespaces, err := r.getSubjectNamespaces(ctx, cnp)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving CNP ingress rules: %w", err)
	}

	plan, err := r.planCNPIngressPorts(ctx, cnp, subjectNamespaces)
	if err != nil {
		return nil, nil, err
	}

	// Only the temp NP's metadata needs a namespace; peers and ports are already resolved.
	namespace := ""
	if len(subjectNamespaces) > 0 {
		namespace = subjectNamespaces[0]
	}

	// Only a policy that splits has anything to share; with one group the rules all go in it.
	split := len(plan.groupKeys) > 1
	if split {
		r.logger.Info("named ingress ports resolve to more than one port set across the pods this policy selects, emitting one ClusterPolicyEndpoint group per set",
			"policy", cnp.Name, "portSets", len(plan.groupKeys))
	}

	groups := make([]ClusterIngressGroup, 0, len(plan.groupKeys))
	for _, key := range plan.groupKeys {
		perRule := plan.ruleKeysByGroup[key]
		ingress, err := r.buildCNPIngress(ctx, cnp, namespace, func(i int) ([]policyinfo.Port, bool) {
			if plan.skipped[i] {
				return nil, false
			}
			if !plan.podDependent[i] {
				// A split policy carries these once as shared ingress instead.
				return nil, !split
			}
			ports := plan.portsByRule[i][perRule[i]]
			if len(ports) == 0 {
				// Resolved none of the rule's named ports, so the rule allows this group nothing.
				return nil, false
			}
			return ports, true
		})
		if err != nil {
			return nil, nil, err
		}
		groups = append(groups, ClusterIngressGroup{
			Key:          plan.publicKey(key),
			Ingress:      ingress,
			PodEndpoints: plan.podEndpointsFor(key, podEndpoints),
		})
	}

	var sharedIngress []policyinfo.ClusterEndpointInfo
	if split {
		sharedIngress, err = r.buildCNPIngress(ctx, cnp, namespace, func(i int) ([]policyinfo.Port, bool) {
			if plan.skipped[i] || plan.podDependent[i] {
				return nil, false
			}
			return nil, true
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return groups, sharedIngress, nil
}

// buildCNPIngress resolves the peers of each ingress rule portsFor accepts, tagging each endpoint
// with that rule's own action so rules of differing actions never merge. Nil ports keeps the
// rule's own; non-nil substitutes a group's, so peers resolve without listing pods again.
func (r *clusterNetworkPolicyEndpointsResolver) buildCNPIngress(ctx context.Context,
	cnp *policyinfo.ClusterNetworkPolicy, namespace string,
	portsFor func(int) ([]policyinfo.Port, bool)) ([]policyinfo.ClusterEndpointInfo, error) {

	var endpointInfos []policyinfo.ClusterEndpointInfo
	for i, rule := range cnp.Spec.Ingress {
		ports, include := portsFor(i)
		if !include {
			continue
		}
		tempNP := r.convertSingleCNPIngressRuleToNP(cnp, rule, namespace)
		if ports != nil {
			tempNP.Spec.Ingress[0].Ports = resolvedPortsToNPPorts(ports)
		}
		ingressRules, _, _, err := r.baseResolver.Resolve(ctx, tempNP)
		if err != nil {
			return nil, err
		}
		for _, endpoint := range ingressRules {
			endpointInfos = append(endpointInfos, policyinfo.ClusterEndpointInfo{
				CIDR:   endpoint.CIDR,
				Ports:  endpoint.Ports,
				Action: rule.Action,
			})
		}
	}
	return r.mergeClusterEndpointInfo(endpointInfos), nil
}

// resolvedPortsToNPPorts turns resolved ports back into NetworkPolicy ports. Every port is a
// number, so the base resolver takes its numeric path and does not list pods.
func resolvedPortsToNPPorts(ports []policyinfo.Port) []networking.NetworkPolicyPort {
	npPorts := make([]networking.NetworkPolicyPort, 0, len(ports))
	for _, port := range ports {
		npPort := networking.NetworkPolicyPort{Protocol: port.Protocol, EndPort: port.EndPort}
		if port.Port != nil {
			npPort.Port = &intstr.IntOrString{Type: intstr.Int, IntVal: *port.Port}
		}
		npPorts = append(npPorts, npPort)
	}
	return npPorts
}

// clusterIngressPortPlan holds the distinct port sets per CNP ingress rule and each subject pod's
// place in them.
type clusterIngressPortPlan struct {
	// portsByRule[i][k] is rule i's ports for the pods whose rule-i key is k.
	portsByRule []map[string][]policyinfo.Port
	// podDependent[i] is true when rule i names a port, so its ports vary by pod.
	podDependent []bool
	// skipped[i] is true when rule i names a port but the subject selector matched no namespace.
	skipped []bool
	// ruleKeysByGroup maps a group to its per-rule keys, in rule order.
	ruleKeysByGroup map[string][]string
	// groupKeys is every group, in subject pod order.
	groupKeys []string
	// podGroup maps a subject pod to its group.
	podGroup map[types.NamespacedName]string
}

// publicKey is what lands in spec.targetGroup. A single group stays unset.
func (p *clusterIngressPortPlan) publicKey(group string) string {
	if len(p.groupKeys) <= 1 {
		return ""
	}
	return group
}

// podEndpointsFor keeps one group's pod endpoints. A single group holds every pod.
func (p *clusterIngressPortPlan) podEndpointsFor(group string, all []policyinfo.PodEndpoint) []policyinfo.PodEndpoint {
	if len(p.groupKeys) <= 1 {
		return all
	}
	var out []policyinfo.PodEndpoint
	for _, pe := range all {
		if p.podGroup[types.NamespacedName{Namespace: pe.Namespace, Name: pe.Name}] == group {
			out = append(out, pe)
		}
	}
	return out
}

// planCNPIngressPorts groups the subject pods by the port sets they resolved across all ingress
// rules. A pod's group spans the policy, not one rule, since one object carries them all.
func (r *clusterNetworkPolicyEndpointsResolver) planCNPIngressPorts(ctx context.Context,
	cnp *policyinfo.ClusterNetworkPolicy, subjectNamespaces []string) (*clusterIngressPortPlan, error) {

	rules := cnp.Spec.Ingress
	plan := &clusterIngressPortPlan{
		portsByRule:     make([]map[string][]policyinfo.Port, len(rules)),
		podDependent:    make([]bool, len(rules)),
		skipped:         make([]bool, len(rules)),
		ruleKeysByGroup: map[string][]string{},
		podGroup:        map[types.NamespacedName]string{},
	}

	// Pods are only needed for rules that name a port.
	needPods := false
	for i, rule := range rules {
		if !hasNamedCNPPorts(rule.Ports) {
			continue
		}
		if len(subjectNamespaces) == 0 {
			r.logger.Info("Skipping CNP ingress rule with named ports because subject selector matched no namespaces",
				"policy", cnp.Name, "rule", rule.Name)
			plan.skipped[i] = true
			continue
		}
		plan.podDependent[i] = true
		needPods = true
	}

	var pods []corev1.Pod
	if needPods {
		listed, err := r.listSubjectPods(ctx, cnp, subjectNamespaces)
		if err != nil {
			return nil, err
		}
		pods = listed
	}

	perRulePodKeys := make([]map[types.NamespacedName]string, len(rules))
	for i, rule := range rules {
		plan.portsByRule[i] = map[string][]policyinfo.Port{}
		if !plan.podDependent[i] {
			continue
		}
		npPorts := r.convertCNPPortsToNPPorts(*rule.Ports)
		perRulePodKeys[i] = map[types.NamespacedName]string{}
		for j := range pods {
			ports := r.baseResolver.ResolvePodPorts(pods[j], npPorts)
			key := portSetKey(ports)
			plan.portsByRule[i][key] = ports
			perRulePodKeys[i][types.NamespacedName{Namespace: pods[j].Namespace, Name: pods[j].Name}] = key
		}
	}

	// Group the pods by their tuple of per-rule keys.
	for j := range pods {
		pod := types.NamespacedName{Namespace: pods[j].Namespace, Name: pods[j].Name}
		ruleKeys := make([]string, len(rules))
		for i := range rules {
			if perRulePodKeys[i] == nil {
				ruleKeys[i] = ungroupedKey
				continue
			}
			ruleKeys[i] = perRulePodKeys[i][pod]
		}
		group := groupKey(ruleKeys)
		if _, seen := plan.ruleKeysByGroup[group]; !seen {
			plan.ruleKeysByGroup[group] = ruleKeys
			plan.groupKeys = append(plan.groupKeys, group)
		}
		plan.podGroup[pod] = group
	}

	// No pods, so a single pod-independent group.
	if len(plan.groupKeys) == 0 {
		ruleKeys := make([]string, len(rules))
		group := groupKey(ruleKeys)
		plan.ruleKeysByGroup[group] = ruleKeys
		plan.groupKeys = []string{group}
	}
	return plan, nil
}

// listSubjectPods lists the ready pods the CNP's subject selects, across every subject namespace.
// Sorted, so which group is packed into which existing object does not depend on List order.
func (r *clusterNetworkPolicyEndpointsResolver) listSubjectPods(ctx context.Context,
	cnp *policyinfo.ClusterNetworkPolicy, subjectNamespaces []string) ([]corev1.Pod, error) {

	// Subject.Namespaces selects every pod in the namespace, so only Subject.Pods narrows it.
	var podSelector metav1.LabelSelector
	if cnp.Spec.Subject.Pods != nil {
		podSelector = cnp.Spec.Subject.Pods.PodSelector
	}
	selector, err := metav1.LabelSelectorAsSelector(&podSelector)
	if err != nil {
		return nil, fmt.Errorf("listing CNP subject pods: %w", err)
	}

	var pods []corev1.Pod
	for _, ns := range subjectNamespaces {
		podList := &corev1.PodList{}
		if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
			LabelSelector: selector,
			Namespace:     ns,
		}); err != nil {
			return nil, err
		}
		for i := range podList.Items {
			if k8s.IsPodNetworkReady(&podList.Items[i]) {
				pods = append(pods, podList.Items[i])
			}
		}
	}
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})
	return pods, nil
}
