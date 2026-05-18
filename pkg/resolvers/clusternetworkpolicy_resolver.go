package resolvers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClusterNetworkPolicyEndpointsResolver interface {
	// ResolveClusterNetworkPolicy returns the resolved endpoints for the given ClusterNetworkPolicy
	ResolveClusterNetworkPolicy(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) ([]policyinfo.ClusterEndpointInfo, []policyinfo.ClusterEndpointInfo, []policyinfo.PodEndpoint, error)
}

// NewClusterNetworkPolicyEndpointsResolver constructs a new clusterNetworkPolicyEndpointsResolver
func NewClusterNetworkPolicyEndpointsResolver(k8sClient client.Client, logger logr.Logger) *clusterNetworkPolicyEndpointsResolver {
	baseResolver := NewEndpointsResolver(k8sClient, logger.WithName("base-resolver"))
	return &clusterNetworkPolicyEndpointsResolver{
		k8sClient:    k8sClient,
		baseResolver: baseResolver,
		logger:       logger,
	}
}

var _ ClusterNetworkPolicyEndpointsResolver = (*clusterNetworkPolicyEndpointsResolver)(nil)

type clusterNetworkPolicyEndpointsResolver struct {
	k8sClient    client.Client
	baseResolver EndpointsResolver
	logger       logr.Logger
}

func (r *clusterNetworkPolicyEndpointsResolver) ResolveClusterNetworkPolicy(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) ([]policyinfo.ClusterEndpointInfo, []policyinfo.ClusterEndpointInfo, []policyinfo.PodEndpoint, error) {
	var allPodEndpoints []policyinfo.PodEndpoint
	var allIngressRules []policyinfo.ClusterEndpointInfo
	var allEgressRules []policyinfo.ClusterEndpointInfo

	// 1. resolve pod endpoints from Subject
	podEndpoints, err := r.resolvePodEndpointsFromSubject(ctx, cnp.Spec.Subject)
	if err != nil {
		return nil, nil, nil, err
	}
	allPodEndpoints = podEndpoints

	// 2. Process ingress rules - resolve its own target namespaces from peers
	if len(cnp.Spec.Ingress) > 0 {
		allIngressRules, err = r.resolveCNPIngressRules(ctx, cnp)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	// 3. Process egress rules - resolve its own target namespaces from peers
	if len(cnp.Spec.Egress) > 0 {
		allEgressRules, err = r.resolveCNPEgressRules(ctx, cnp)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	r.logger.Info("Resolved ClusterNetworkPolicy endpoints", "policy", cnp.Name, "ingress", len(allIngressRules), "egress", len(allEgressRules), "pod selector endpoints", len(allPodEndpoints))

	return allIngressRules, allEgressRules, allPodEndpoints, nil
}

func (r *clusterNetworkPolicyEndpointsResolver) mergeClusterEndpointInfo(rules []policyinfo.ClusterEndpointInfo) []policyinfo.ClusterEndpointInfo {
	seen := make(map[string]bool)
	var result []policyinfo.ClusterEndpointInfo

	for _, rule := range rules {
		var key string
		if rule.CIDR != "" {
			key = fmt.Sprintf("cidr:%s:%s:%s", rule.CIDR, rule.Action, r.portsToString(rule.Ports))
		} else if rule.DomainName != "" {
			key = fmt.Sprintf("domain:%s:%s:%s", rule.DomainName, rule.Action, r.portsToString(rule.Ports))
		} else {
			// Skip invalid entries
			continue
		}

		if !seen[key] {
			seen[key] = true
			result = append(result, rule)
		}
	}
	return result
}

func (r *clusterNetworkPolicyEndpointsResolver) portsToString(ports []policyinfo.Port) string {
	if len(ports) == 0 {
		return "all"
	}
	var portStrs []string
	for _, port := range ports {
		protocol := "TCP"
		if port.Protocol != nil {
			protocol = string(*port.Protocol)
		}
		portNum := "any"
		if port.Port != nil {
			portNum = fmt.Sprintf("%d", *port.Port)
		}
		portStrs = append(portStrs, fmt.Sprintf("%s:%s", protocol, portNum))
	}
	return strings.Join(portStrs, ",")
}

func (r *clusterNetworkPolicyEndpointsResolver) resolvePodEndpointsFromSubject(ctx context.Context, subject policyinfo.ClusterNetworkPolicySubject) ([]policyinfo.PodEndpoint, error) {
	if subject.Namespaces != nil {
		// Subject.Namespaces - select all pods in matching namespaces
		targetNamespaces, err := r.resolveNamespacesBySelector(ctx, *subject.Namespaces)
		if err != nil {
			return nil, err
		}

		var allPodEndpoints []policyinfo.PodEndpoint
		for _, ns := range targetNamespaces {
			// Empty selector {} = all pods in namespace
			tempNP := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "temp", Namespace: ns},
				Spec: networking.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{}, // Empty = all pods
				},
			}
			_, _, podEndpoints, err := r.baseResolver.Resolve(ctx, tempNP)
			if err != nil {
				return nil, err
			}
			allPodEndpoints = append(allPodEndpoints, podEndpoints...)
		}
		return allPodEndpoints, nil
	} else if subject.Pods != nil {
		// Subject.Pods - namespaceSelector:{} = all namespaces, podSelector:{} = all pods
		targetNamespaces, err := r.resolveNamespacesBySelector(ctx, subject.Pods.NamespaceSelector)
		if err != nil {
			return nil, err
		}

		var allPodEndpoints []policyinfo.PodEndpoint
		for _, ns := range targetNamespaces {
			tempNP := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "temp", Namespace: ns},
				Spec: networking.NetworkPolicySpec{
					PodSelector: subject.Pods.PodSelector, // Could be {} = all pods
				},
			}
			_, _, podEndpoints, err := r.baseResolver.Resolve(ctx, tempNP)
			if err != nil {
				return nil, err
			}
			allPodEndpoints = append(allPodEndpoints, podEndpoints...)
		}
		return allPodEndpoints, nil
	}
	return []policyinfo.PodEndpoint{}, nil
}

func (r *clusterNetworkPolicyEndpointsResolver) resolveNamespacesBySelector(ctx context.Context, nsSelector metav1.LabelSelector) ([]string, error) {
	// Empty selector {} means all namespaces
	if len(nsSelector.MatchLabels) == 0 && len(nsSelector.MatchExpressions) == 0 {
		namespaceList := &corev1.NamespaceList{}
		if err := r.k8sClient.List(ctx, namespaceList); err != nil {
			return nil, err
		}

		var namespaces []string
		for _, ns := range namespaceList.Items {
			namespaces = append(namespaces, ns.Name)
		}
		return namespaces, nil
	}

	// Non-empty selector - use label matching
	namespaceList := &corev1.NamespaceList{}
	selector, err := metav1.LabelSelectorAsSelector(&nsSelector)
	if err != nil {
		return nil, err
	}

	if err := r.k8sClient.List(ctx, namespaceList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}

	var namespaces []string
	for _, ns := range namespaceList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	return namespaces, nil
}

func (r *clusterNetworkPolicyEndpointsResolver) convertCNPPortsToNPPorts(cnpPorts []policyinfo.ClusterNetworkPolicyPort) []networking.NetworkPolicyPort {
	var npPorts []networking.NetworkPolicyPort
	for _, cnpPort := range cnpPorts {
		if cnpPort.PortNumber != nil {
			npPorts = append(npPorts, networking.NetworkPolicyPort{
				Protocol: &cnpPort.PortNumber.Protocol,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: cnpPort.PortNumber.Port},
			})
		}
		if cnpPort.PortRange != nil {
			// Handle port range - create multiple ports or use endPort if supported
			npPorts = append(npPorts, networking.NetworkPolicyPort{
				Protocol: &cnpPort.PortRange.Protocol,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: cnpPort.PortRange.Start},
				EndPort:  &cnpPort.PortRange.End,
			})
		}
		if cnpPort.NamedPort != nil {
			// CNP NamedPort has no Protocol field; default to TCP to match
			// the kubebuilder default on PortNumber/PortRange and prevent
			// nil-protocol panics in baseResolver.getPortList. This is coming because of CNP to NP API translation.
			tcp := corev1.ProtocolTCP
			npPorts = append(npPorts, networking.NetworkPolicyPort{
				Protocol: &tcp,
				Port:     &intstr.IntOrString{Type: intstr.String, StrVal: *cnpPort.NamedPort},
			})
		}
	}
	return npPorts
}

func (r *clusterNetworkPolicyEndpointsResolver) convertCNPPortsToEndpointPorts(cnpPorts *[]policyinfo.ClusterNetworkPolicyPort) []policyinfo.Port {
	if cnpPorts == nil {
		return nil
	}

	var ports []policyinfo.Port
	for _, cnpPort := range *cnpPorts {
		if cnpPort.PortNumber != nil {
			ports = append(ports, policyinfo.Port{
				Protocol: &cnpPort.PortNumber.Protocol,
				Port:     &cnpPort.PortNumber.Port,
			})
		}
		if cnpPort.PortRange != nil {
			ports = append(ports, policyinfo.Port{
				Protocol: &cnpPort.PortRange.Protocol,
				Port:     &cnpPort.PortRange.Start,
				EndPort:  &cnpPort.PortRange.End,
			})
		}
	}
	return ports
}

func hasNamedCNPPorts(cnpPorts *[]policyinfo.ClusterNetworkPolicyPort) bool {
	if cnpPorts == nil {
		return false
	}

	for _, cnpPort := range *cnpPorts {
		if cnpPort.NamedPort != nil {
			return true
		}
	}
	return false
}

func (r *clusterNetworkPolicyEndpointsResolver) resolveCNPEgressRules(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) ([]policyinfo.ClusterEndpointInfo, error) {
	var endpointInfos []policyinfo.ClusterEndpointInfo

	subjectNamespace, err := r.getSubjectNamespace(ctx, cnp)
	if err != nil {
		return nil, fmt.Errorf("resolving CNP egress rules: %w", err)
	}

	for _, rule := range cnp.Spec.Egress {
		for _, peer := range rule.To {
			// Handle each peer type exclusively
			if len(peer.Networks) > 0 {
				// CIDR peer - namespace is irrelevant for CIDR resolution
				tempNP := r.convertSingleCNPEgressRuleToNP(cnp, rule, subjectNamespace)
				_, cidrEgressEndpoints, _, err := r.baseResolver.Resolve(ctx, tempNP)
				if err != nil {
					return nil, err
				}

				for _, endpoint := range cidrEgressEndpoints {
					endpointInfos = append(endpointInfos, policyinfo.ClusterEndpointInfo{
						CIDR:   endpoint.CIDR,
						Ports:  endpoint.Ports,
						Action: rule.Action,
					})
				}
			} else if peer.Namespaces != nil || peer.Pods != nil {
				tempNP := r.convertSingleCNPEgressRuleToNP(cnp, rule, subjectNamespace)
				_, egressEndpoints, _, err := r.baseResolver.Resolve(ctx, tempNP)
				if err != nil {
					return nil, err
				}

				for _, endpoint := range egressEndpoints {
					endpointInfos = append(endpointInfos, policyinfo.ClusterEndpointInfo{
						CIDR:   endpoint.CIDR,
						Ports:  endpoint.Ports,
						Action: rule.Action,
					})
				}
			} else if len(peer.DomainNames) > 0 {
				// Domain name peer - handle directly
				for _, domain := range peer.DomainNames {
					if rule.Action == policyinfo.ClusterNetworkPolicyRuleActionAccept ||
						rule.Action == policyinfo.ClusterNetworkPolicyRuleActionPass {
						endpointInfos = append(endpointInfos, policyinfo.ClusterEndpointInfo{
							DomainName: domain,
							Action:     rule.Action,
							Ports:      r.convertCNPPortsToEndpointPorts(rule.Ports),
						})
					}
					// Ignore Deny action for domainNames as it's not supported
				}
			}
		}
	}

	return r.mergeClusterEndpointInfo(endpointInfos), nil
}

// convertSingleCNPIngressRuleToNP builds a temporary NetworkPolicy that wraps a
// single CNP ingress rule so it can be fed to baseResolver.Resolve(), which
// only understands the upstream NetworkPolicy schema.
//
// Each CNP peer is mapped to an NP peer with an explicit NamespaceSelector
// (Namespaces.* or Pods.NamespaceSelector). Because every NP peer carries an
// explicit NamespaceSelector, baseResolver.resolvePeerNamespaces resolves peer
// namespaces from the selector itself and ignores the temp NP's namespace —
// so a single Resolve() call returns endpoints across all matching namespaces.
//
// The `namespace` argument is used only for named-port resolution against the
// policy's destination pods (subject pods); callers pass the CNP's subject
// namespace.
func (r *clusterNetworkPolicyEndpointsResolver) convertSingleCNPIngressRuleToNP(cnp *policyinfo.ClusterNetworkPolicy, rule policyinfo.ClusterNetworkPolicyIngressRule, namespace string) *networking.NetworkPolicy {
	// Convert CNP ingress peers to NP peers
	var npPeers []networking.NetworkPolicyPeer
	for _, peer := range rule.From {
		npPeer := networking.NetworkPolicyPeer{}
		if peer.Namespaces != nil {
			npPeer.NamespaceSelector = peer.Namespaces
		}
		if peer.Pods != nil {
			npPeer.NamespaceSelector = &peer.Pods.NamespaceSelector
			npPeer.PodSelector = &peer.Pods.PodSelector
		}
		npPeers = append(npPeers, npPeer)
	}

	ingressRule := networking.NetworkPolicyIngressRule{
		From: npPeers,
	}
	if rule.Ports != nil {
		ingressRule.Ports = r.convertCNPPortsToNPPorts(*rule.Ports)
	}

	// Set the temp NP's PodSelector to the CNP's subject pod selector so that
	// named-port resolution (baseResolver.getIngressRulesPorts) only inspects
	// destination (subject) pods.
	var subjectPodSelector metav1.LabelSelector
	if cnp.Spec.Subject.Pods != nil {
		subjectPodSelector = cnp.Spec.Subject.Pods.PodSelector
	}

	return &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cnp.Name,
			Namespace: namespace,
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: subjectPodSelector,
			Ingress:     []networking.NetworkPolicyIngressRule{ingressRule},
		},
	}
}

// convertSingleCNPEgressRuleToNP builds a temporary NetworkPolicy that wraps a
// single CNP egress rule so it can be fed to baseResolver.Resolve().
//
// Networks (CIDRs) become NP IPBlock peers (one per CIDR). Namespace/Pod peers
// map to NP peers with explicit NamespaceSelector — baseResolver resolves all
// matching namespaces internally, so a single Resolve() call covers the full
// peer set without an outer namespace loop. DomainNames are skipped here and
// emitted directly by the caller.
//
// The `namespace` argument is set on the temp NP only to keep the
// NetworkPolicy object well-formed; baseResolver does not consult it for
// egress so long as every converted peer carries an explicit
// NamespaceSelector (which is true for CNP-converted peers). Egress named
// ports resolve against destination (peer) pods per-peer, and CIDR peers are
// namespace-agnostic. Callers pass the CNP's subject namespace for
// consistency with the ingress path.
func (r *clusterNetworkPolicyEndpointsResolver) convertSingleCNPEgressRuleToNP(cnp *policyinfo.ClusterNetworkPolicy, rule policyinfo.ClusterNetworkPolicyEgressRule, namespace string) *networking.NetworkPolicy {
	// Convert only CIDR/namespace/pod peers, skip domainNames
	var npPeers []networking.NetworkPolicyPeer
	for _, peer := range rule.To {
		if len(peer.Networks) > 0 {
			// Create separate NP peer for each CIDR
			for _, cidr := range peer.Networks {
				npPeer := networking.NetworkPolicyPeer{
					IPBlock: &networking.IPBlock{
						CIDR: string(cidr),
					},
				}
				npPeers = append(npPeers, npPeer)
			}
		}
		if peer.Namespaces != nil || peer.Pods != nil {
			npPeer := networking.NetworkPolicyPeer{}
			if peer.Namespaces != nil {
				npPeer.NamespaceSelector = peer.Namespaces
			}
			if peer.Pods != nil {
				npPeer.NamespaceSelector = &peer.Pods.NamespaceSelector
				npPeer.PodSelector = &peer.Pods.PodSelector
			}
			npPeers = append(npPeers, npPeer)
		}
	}

	egressRule := networking.NetworkPolicyEgressRule{
		To: npPeers,
	}
	if rule.Ports != nil {
		egressRule.Ports = r.convertCNPPortsToNPPorts(*rule.Ports)
	}

	return &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cnp.Name,
			Namespace: namespace,
		},
		Spec: networking.NetworkPolicySpec{
			Egress: []networking.NetworkPolicyEgressRule{egressRule},
		},
	}
}

func (r *clusterNetworkPolicyEndpointsResolver) resolveCNPIngressRules(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) ([]policyinfo.ClusterEndpointInfo, error) {
	// Determine subject namespace for named port resolution.
	// Named ports resolve against the policy's destination pods (subject pods).
	subjectNamespace, err := r.getSubjectNamespace(ctx, cnp)
	if err != nil {
		return nil, fmt.Errorf("resolving CNP ingress rules: %w", err)
	}

	var allIngressRules []policyinfo.ClusterEndpointInfo
	for _, rule := range cnp.Spec.Ingress {
		// Mirrors the NetworkPolicy → PolicyEndpoint behavior in
		// computeIngressEndpoints (pkg/resolvers/endpoints.go), which skips
		// an ingress rule when its named ports cannot be resolved against
		// destination pods. Here we short-circuit early when the subject
		// selector matched no namespaces, so we know there are no
		// destination pods to resolve named ports against.
		if subjectNamespace == "" && hasNamedCNPPorts(rule.Ports) {
			r.logger.Info("Skipping CNP ingress rule with named ports because subject selector matched no namespaces",
				"policy", cnp.Name, "rule", rule.Name)
			continue
		}

		tempNP := r.convertSingleCNPIngressRuleToNP(cnp, rule, subjectNamespace)
		ingressRules, _, _, err := r.baseResolver.Resolve(ctx, tempNP)
		if err != nil {
			return nil, err
		}

		for _, endpoint := range ingressRules {
			allIngressRules = append(allIngressRules, policyinfo.ClusterEndpointInfo{
				CIDR:   endpoint.CIDR,
				Ports:  endpoint.Ports,
				Action: rule.Action,
			})
		}
	}

	return r.mergeClusterEndpointInfo(allIngressRules), nil
}

// getSubjectNamespace returns a single namespace from those matching the
// CNP subject selector, used by callers for named-port resolution against
// destination pods. The namespace list is sorted before picking [0] so the
// choice is deterministic across reconciles — k8sClient.List() ordering is
// not guaranteed stable, and an unstable choice would flip resolved named
// ports between reconciles when the subject spans multiple namespaces.
//
// Note: this is a best-effort approximation. When the subject spans multiple
// namespaces with named ports declared on different containerPort values,
// only the chosen namespace's pods contribute to resolution. This is a limitation of the current CPE schema
func (r *clusterNetworkPolicyEndpointsResolver) getSubjectNamespace(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) (string, error) {
	if cnp.Spec.Subject.Pods != nil {
		namespaces, err := r.resolveNamespacesBySelector(ctx, cnp.Spec.Subject.Pods.NamespaceSelector)
		if err != nil {
			return "", fmt.Errorf("resolving subject namespaces from Pods.NamespaceSelector: %w", err)
		}
		if len(namespaces) > 0 {
			sort.Strings(namespaces)
			return namespaces[0], nil
		}
	} else if cnp.Spec.Subject.Namespaces != nil {
		namespaces, err := r.resolveNamespacesBySelector(ctx, *cnp.Spec.Subject.Namespaces)
		if err != nil {
			return "", fmt.Errorf("resolving subject namespaces from Subject.Namespaces: %w", err)
		}
		if len(namespaces) > 0 {
			sort.Strings(namespaces)
			return namespaces[0], nil
		}
	}
	// No namespace matched the subject selector. Returning empty namespace
	// lets callers decide whether they can safely proceed without a concrete
	// subject namespace.
	return "", nil
}
