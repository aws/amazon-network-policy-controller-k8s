package resolvers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"strings"
	"sync"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IngressGroup is a set of target pods that resolved the policy's ingress ports the same way.
type IngressGroup struct {
	// Key is empty when every selected pod agreed, leaving spec.targetGroup unset.
	Key          string
	Ingress      []policyinfo.EndpointInfo
	PodEndpoints []policyinfo.PodEndpoint
}

// ungroupedKey marks a rule whose ports do not depend on the pod.
const ungroupedKey = ""

// ResolveGroups partitions a policy's ingress rules and target pods by the ports each pod
// resolved. Egress is returned once: it resolves against peer pods, so it does not vary by group.
func (r *defaultEndpointsResolver) ResolveGroups(ctx context.Context, policy *networking.NetworkPolicy) ([]IngressGroup,
	[]policyinfo.EndpointInfo, error) {
	var (
		egressEndpoints []policyinfo.EndpointInfo
		podEndpoints    []policyinfo.PodEndpoint
		plan            *ingressPortPlan
		egressErr       error
		podErr          error
		planErr         error
		wg              sync.WaitGroup
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		egressEndpoints, egressErr = r.computeEgressEndpoints(ctx, policy)
	}()
	go func() {
		defer wg.Done()
		podEndpoints, podErr = r.computePodSelectorEndpoints(ctx, policy)
	}()
	go func() {
		defer wg.Done()
		plan, planErr = r.planIngressPorts(ctx, policy)
	}()
	wg.Wait()

	if err := stderrors.Join(egressErr, podErr, planErr); err != nil {
		return nil, nil, err
	}

	if len(plan.groupKeys) > 1 {
		r.logger.Info("named ingress ports resolve to more than one port set across the pods this policy selects, emitting one PolicyEndpoint group per set",
			"policy", k8s.NamespacedName(policy), "portSets", len(plan.groupKeys))
	}

	groups := make([]IngressGroup, 0, len(plan.groupKeys))
	for _, key := range plan.groupKeys {
		perRule := plan.ruleKeysByGroup[key]
		ingress, err := r.buildIngressEndpoints(ctx, policy, func(i int, _ networking.NetworkPolicyIngressRule) []policyinfo.Port {
			return plan.portsByRule[i][perRule[i]]
		})
		if err != nil {
			return nil, nil, err
		}
		groups = append(groups, IngressGroup{
			Key:          plan.publicKey(key),
			Ingress:      ingress,
			PodEndpoints: plan.podEndpointsFor(key, podEndpoints),
		})
	}
	return groups, egressEndpoints, nil
}

// ingressPortPlan holds the distinct port sets per ingress rule and each pod's place in them.
type ingressPortPlan struct {
	// portsByRule[i][k] is rule i's ports for the pods whose rule-i key is k.
	portsByRule []map[string][]policyinfo.Port
	// ruleKeysByGroup maps a group to its per-rule keys, in rule order.
	ruleKeysByGroup map[string][]string
	// groupKeys is every group, in first-seen pod order.
	groupKeys []string
	// podGroup maps a target pod to its group.
	podGroup map[types.NamespacedName]string
}

// publicKey is what lands in spec.targetGroup. A single group stays unset, so a policy whose pods
// agree produces the same output it did before grouping existed.
func (p *ingressPortPlan) publicKey(group string) string {
	if len(p.groupKeys) <= 1 {
		return ""
	}
	return group
}

// podEndpointsFor keeps one group's pod endpoints. A single group holds every pod.
func (p *ingressPortPlan) podEndpointsFor(group string, all []policyinfo.PodEndpoint) []policyinfo.PodEndpoint {
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

// planIngressPorts groups the target pods by the port sets they resolved across all ingress rules.
// A pod's group spans the whole policy, not one rule, because one PolicyEndpoint carries them all.
func (r *defaultEndpointsResolver) planIngressPorts(ctx context.Context, policy *networking.NetworkPolicy) (*ingressPortPlan, error) {
	rules := policy.Spec.Ingress
	plan := &ingressPortPlan{
		portsByRule:     make([]map[string][]policyinfo.Port, len(rules)),
		ruleKeysByGroup: map[string][]string{},
		podGroup:        map[types.NamespacedName]string{},
	}

	// Pods are only needed for rules that name a port.
	var pods []corev1.Pod
	needPods := false
	for _, rule := range rules {
		if rulePortsAreNamed(rule.Ports) {
			needPods = true
			break
		}
	}
	if needPods {
		listed, err := r.listPolicyTargetPods(ctx, policy)
		if err != nil {
			return nil, err
		}
		pods = listed
	}

	// A rule without named ports is pod independent.
	perRulePodKeys := make([]map[types.NamespacedName]string, len(rules))
	for i, rule := range rules {
		plan.portsByRule[i] = map[string][]policyinfo.Port{}
		if !rulePortsAreNamed(rule.Ports) {
			ports := r.resolveIngressPorts(ctx, policy, rule.Ports)
			plan.portsByRule[i][ungroupedKey] = ports
			continue
		}
		perRulePodKeys[i] = map[types.NamespacedName]string{}
		for j := range pods {
			pod := &pods[j]
			ports := dedupPorts(r.getPortList(*pod, rule.Ports))
			key := portSetKey(ports)
			plan.portsByRule[i][key] = ports
			perRulePodKeys[i][types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}] = key
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

// listPolicyTargetPods lists the ready pods the policy applies to.
func (r *defaultEndpointsResolver) listPolicyTargetPods(ctx context.Context, policy *networking.NetworkPolicy) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
		LabelSelector: r.createPodLabelSelector(&policy.Spec.PodSelector),
		Namespace:     policy.Namespace,
	}); err != nil {
		return nil, err
	}
	ready := make([]corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		if k8s.IsPodNetworkReady(&podList.Items[i]) {
			ready = append(ready, podList.Items[i])
		}
	}
	return ready, nil
}

func rulePortsAreNamed(ports []networking.NetworkPolicyPort) bool {
	for _, p := range ports {
		if p.Port != nil && p.Port.Type == intstr.String {
			return true
		}
	}
	return false
}

// groupKey identifies a pod's resolution across every rule. Rule order is part of the identity,
// so the keys are joined rather than sorted.
func groupKey(ruleKeys []string) string {
	joined := strings.Join(ruleKeys, "|")
	sum := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(sum[:])[:10]
}
