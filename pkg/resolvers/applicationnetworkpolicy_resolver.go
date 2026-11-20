package resolvers

import (
	"context"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ApplicationNetworkPolicyEndpointsResolver interface {
	// ResolveApplicationNetworkPolicy returns the resolved endpoints for the given ApplicationNetworkPolicy
	ResolveApplicationNetworkPolicy(ctx context.Context, applicationNetworkPolicy *policyinfo.ApplicationNetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error)
}

// NewApplicationNetworkPolicyEndpointsResolver constructs a new applicationNetworkPolicyEndpointsResolver
func NewApplicationNetworkPolicyEndpointsResolver(k8sClient client.Client, logger logr.Logger) *applicationNetworkPolicyEndpointsResolver {
	baseResolver := NewEndpointsResolver(k8sClient, logger.WithName("base-resolver"))
	return &applicationNetworkPolicyEndpointsResolver{
		k8sClient:    k8sClient,
		baseResolver: baseResolver,
		logger:       logger,
	}
}

var _ ApplicationNetworkPolicyEndpointsResolver = (*applicationNetworkPolicyEndpointsResolver)(nil)

type applicationNetworkPolicyEndpointsResolver struct {
	k8sClient    client.Client
	baseResolver EndpointsResolver
	logger       logr.Logger
}

func (r *applicationNetworkPolicyEndpointsResolver) ResolveApplicationNetworkPolicy(ctx context.Context, applicationNetworkPolicy *policyinfo.ApplicationNetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error) {
	// Convert ApplicationNetworkPolicy to NetworkPolicy for ingress rules (reuse existing logic)
	networkPolicy := r.convertApplicationNetworkPolicyToNetworkPolicyWithNoEgress(applicationNetworkPolicy)

	// Resolve ingress using existing resolver
	ingressEndpoints, _, podSelectorEndpoints, err := r.baseResolver.Resolve(ctx, networkPolicy)
	if err != nil {
		return nil, nil, nil, err
	}

	// Resolve egress endpoints
	egressEndpoints, err := r.computeApplicationNetworkPolicyEgressEndpoints(ctx, applicationNetworkPolicy)
	if err != nil {
		return nil, nil, nil, err
	}

	r.logger.Info("Resolved ApplicationNetworkPolicy endpoints", "policy", k8s.NamespacedName(applicationNetworkPolicy), "ingress", len(ingressEndpoints), "egress", len(egressEndpoints), "pod selector endpoints", len(podSelectorEndpoints))

	return ingressEndpoints, egressEndpoints, podSelectorEndpoints, nil
}

func (r *applicationNetworkPolicyEndpointsResolver) convertApplicationNetworkPolicyToNetworkPolicyWithNoEgress(applicationNetworkPolicy *policyinfo.ApplicationNetworkPolicy) *networking.NetworkPolicy {
	return &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      applicationNetworkPolicy.Name,
			Namespace: applicationNetworkPolicy.Namespace,
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: applicationNetworkPolicy.Spec.PodSelector,
			PolicyTypes: applicationNetworkPolicy.Spec.PolicyTypes,
			Ingress:     applicationNetworkPolicy.Spec.Ingress,
			// Egress will be handled separately for FQDN support
		},
	}
}

func (r *applicationNetworkPolicyEndpointsResolver) convertApplicationNetworkPolicyToNetworkPolicy(applicationNetworkPolicy *policyinfo.ApplicationNetworkPolicy) *networking.NetworkPolicy {
	// Convert ANP egress rules to standard NetworkPolicy egress rules (CIDR only)
	var egressRules []networking.NetworkPolicyEgressRule
	for _, rule := range applicationNetworkPolicy.Spec.Egress {
		// An empty destination list means all destinations. Preserve the rule so
		// the base resolver emits the corresponding all-destination endpoints.
		if len(rule.To) == 0 {
			egressRules = append(egressRules, networking.NetworkPolicyEgressRule{
				Ports: rule.Ports,
			})
			continue
		}

		var networkPolicyPeers []networking.NetworkPolicyPeer
		for _, peer := range rule.To {
			// Only include peers that don't have domain names (CIDR/pod/namespace selectors only)
			if len(peer.DomainNames) == 0 {
				networkPolicyPeers = append(networkPolicyPeers, networking.NetworkPolicyPeer{
					PodSelector:       peer.PodSelector,
					NamespaceSelector: peer.NamespaceSelector,
					IPBlock:           peer.IPBlock,
				})
			}
		}
		if len(networkPolicyPeers) > 0 {
			egressRules = append(egressRules, networking.NetworkPolicyEgressRule{
				Ports: rule.Ports,
				To:    networkPolicyPeers,
			})
		}
	}

	return &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      applicationNetworkPolicy.Name,
			Namespace: applicationNetworkPolicy.Namespace,
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: applicationNetworkPolicy.Spec.PodSelector,
			PolicyTypes: applicationNetworkPolicy.Spec.PolicyTypes,
			Ingress:     applicationNetworkPolicy.Spec.Ingress,
			Egress:      egressRules,
		},
	}
}

func (r *applicationNetworkPolicyEndpointsResolver) computeApplicationNetworkPolicyEgressEndpoints(ctx context.Context, applicationNetworkPolicy *policyinfo.ApplicationNetworkPolicy) ([]policyinfo.EndpointInfo, error) {
	var egressEndpoints []policyinfo.EndpointInfo

	// Check if we have CIDR-based egress rules
	hasCIDREgress := false
	for _, rule := range applicationNetworkPolicy.Spec.Egress {
		if len(rule.To) == 0 {
			hasCIDREgress = true
			break
		}
		for _, peer := range rule.To {
			if len(peer.DomainNames) == 0 {
				hasCIDREgress = true
				break
			}
		}
		if hasCIDREgress {
			break
		}
	}

	// If we have CIDR egress rules, use baseResolver
	if hasCIDREgress {
		networkPolicyWithEgress := r.convertApplicationNetworkPolicyToNetworkPolicy(applicationNetworkPolicy)
		_, cidrEgressEndpoints, _, err := r.baseResolver.Resolve(ctx, networkPolicyWithEgress)
		if err != nil {
			return nil, err
		}
		egressEndpoints = append(egressEndpoints, cidrEgressEndpoints...)
	}

	// Handle FQDN rules
	for _, rule := range applicationNetworkPolicy.Spec.Egress {
		for _, peer := range rule.To {
			if len(peer.DomainNames) > 0 {
				fqdnEndpoints := r.resolveFQDNRules(peer.DomainNames, rule.Ports)
				egressEndpoints = append(egressEndpoints, fqdnEndpoints...)
			}
		}
	}

	return egressEndpoints, nil
}

func (r *applicationNetworkPolicyEndpointsResolver) resolveFQDNRules(domainNames []policyinfo.DomainName, ports []networking.NetworkPolicyPort) []policyinfo.EndpointInfo {
	var endpoints []policyinfo.EndpointInfo

	// Convert ports to policyinfo.Port format
	var portList []policyinfo.Port
	for _, port := range ports {
		protocol := corev1.ProtocolTCP
		if port.Protocol != nil {
			protocol = *port.Protocol
		}

		var portPtr *int32
		if port.Port != nil && port.Port.Type == 0 { // IntVal
			portVal := int32(port.Port.IntValue())
			portPtr = &portVal
		}

		portList = append(portList, policyinfo.Port{
			Protocol: &protocol,
			Port:     portPtr,
			EndPort:  port.EndPort,
		})
	}

	// Create endpoint for each domain name (pass-through approach)
	for _, domain := range domainNames {
		endpoints = append(endpoints, policyinfo.EndpointInfo{
			DomainName: domain,
			Ports:      portList,
		})
	}

	return endpoints
}
