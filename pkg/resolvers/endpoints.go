package resolvers

import (
	"context"
	"fmt"
	"strconv"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"golang.org/x/exp/maps"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type EndpointsResolver interface {
	// Resolve returns the resolved endpoints for the given policy ingress, egress rules and pod selector labels.
	Resolve(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo,
		[]policyinfo.PodEndpoint, error)
}

// NewEndpointsResolver constructs a new defaultEndpointsResolver
func NewEndpointsResolver(k8sClient client.Client, logger logr.Logger) *defaultEndpointsResolver {
	return &defaultEndpointsResolver{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

var _ EndpointsResolver = (*defaultEndpointsResolver)(nil)

type defaultEndpointsResolver struct {
	k8sClient client.Client
	logger    logr.Logger
}

func (r *defaultEndpointsResolver) Resolve(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo,
	[]policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error) {
	ingressEndpoints, err := r.computeIngressEndpoints(ctx, policy)
	if err != nil {
		return nil, nil, nil, err
	}
	egressEndpoints, err := r.computeEgressEndpoints(ctx, policy)
	if err != nil {
		return nil, nil, nil, err
	}
	podSelectorEndpoints, err := r.computePodSelectorEndpoints(ctx, policy)
	if err != nil {
		return nil, nil, nil, err
	}
	r.logger.Info("Resolved endpoints", "policy", k8s.NamespacedName(policy), "ingress", len(ingressEndpoints), "egress",
		len(egressEndpoints), "pod selector endpoints", len(podSelectorEndpoints))

	return ingressEndpoints, egressEndpoints, podSelectorEndpoints, nil
}

func (r *defaultEndpointsResolver) computeIngressEndpoints(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, error) {
	var ingressEndpoints []policyinfo.EndpointInfo
	for _, rule := range policy.Spec.Ingress {
		r.logger.V(1).Info("computing ingress addresses", "peers", rule.From)
		if rule.From == nil {
			ingressEndpoints = append(ingressEndpoints, r.getAllowAllNetworkPeers(rule.Ports)...)
			continue
		}
		resolvedPeers, err := r.resolveNetworkPeers(ctx, policy, rule.From, rule.Ports, networking.PolicyTypeIngress)
		if err != nil {
			return nil, errors.Wrap(err, "unable to resolve ingress network peers")
		}
		ingressEndpoints = append(ingressEndpoints, resolvedPeers...)
	}
	r.logger.V(1).Info("Resolved ingress rules", "policy", k8s.NamespacedName(policy), "addresses", ingressEndpoints)
	return ingressEndpoints, nil
}

func (r *defaultEndpointsResolver) computeEgressEndpoints(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, error) {
	var egressEndpoints []policyinfo.EndpointInfo
	for _, rule := range policy.Spec.Egress {
		r.logger.V(1).Info("computing egress addresses", "peers", rule.To)
		if rule.To == nil {
			egressEndpoints = append(egressEndpoints, r.getAllowAllNetworkPeers(rule.Ports)...)
			continue
		}
		resolvedPeers, err := r.resolveNetworkPeers(ctx, policy, rule.To, rule.Ports, networking.PolicyTypeEgress)
		if err != nil {
			return nil, errors.Wrap(err, "unable to resolve egress network peers")
		}
		resolvedClusterIPs, err := r.resolveServiceClusterIPs(ctx, rule.To, policy.Namespace, rule.Ports)
		if err != nil {
			return nil, errors.Wrap(err, "unable to resolve service cluster IPs for egress")
		}
		egressEndpoints = append(egressEndpoints, resolvedPeers...)
		egressEndpoints = append(egressEndpoints, resolvedClusterIPs...)
	}
	r.logger.V(1).Info("Resolved egress rules", "policy", k8s.NamespacedName(policy), "addresses", egressEndpoints)
	return egressEndpoints, nil
}

func (r *defaultEndpointsResolver) computePodSelectorEndpoints(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.PodEndpoint, error) {
	var podEndpoints []policyinfo.PodEndpoint
	podSelector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get pod selector")
	}
	podList := &corev1.PodList{}
	if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
		LabelSelector: podSelector,
		Namespace:     policy.Namespace,
	}); err != nil {
		r.logger.Info("Unable to List Pods", "err", err)
		return nil, err
	}
	for _, pod := range podList.Items {
		podIP := k8s.GetPodIP(&pod)
		if len(podIP) > 0 {
			podEndpoints = append(podEndpoints, policyinfo.PodEndpoint{
				PodIP:     policyinfo.NetworkAddress(podIP),
				HostIP:    policyinfo.NetworkAddress(pod.Status.HostIP),
				Name:      pod.Name,
				Namespace: pod.Namespace,
			})
		}
	}
	r.logger.V(1).Info("Resolved pod selector endpoints", "policy", k8s.NamespacedName(policy), "pod endpoints", podEndpoints)
	return podEndpoints, nil
}

func (r *defaultEndpointsResolver) getAllowAllNetworkPeers(ports []networking.NetworkPolicyPort) []policyinfo.EndpointInfo {
	var portList []policyinfo.Port
	for _, port := range ports {
		if port := r.convertToPolicyInfoPortForCIDRs(port); port != nil {
			portList = append(portList, *port)
		}
	}
	if len(ports) != 0 && len(portList) == 0 {
		return nil
	}
	return []policyinfo.EndpointInfo{
		{
			CIDR:  "0.0.0.0/0",
			Ports: portList,
		},
		{
			CIDR:  "::/0",
			Ports: portList,
		},
	}
}

func (r *defaultEndpointsResolver) resolveNetworkPeers(ctx context.Context, policy *networking.NetworkPolicy,
	peers []networking.NetworkPolicyPeer, ports []networking.NetworkPolicyPort, policyType networking.PolicyType) ([]policyinfo.EndpointInfo, error) {
	var networkPeers []policyinfo.EndpointInfo
	for _, peer := range peers {
		if peer.IPBlock != nil {
			var except []policyinfo.NetworkAddress
			for _, ea := range peer.IPBlock.Except {
				except = append(except, policyinfo.NetworkAddress(ea))
			}
			var portList []policyinfo.Port
			for _, port := range ports {
				if port := r.convertToPolicyInfoPortForCIDRs(port); port != nil {
					portList = append(portList, *port)
				}
			}
			// A non-empty input port list would imply the user wants to allow traffic only on the specified ports.
			// However, in this case we are not able to resolve any of the ports from the CIDR list alone. In this
			// case we do not add the CIDR to the list of resolved peers to prevent allow all ports.
			if len(ports) != 0 && len(portList) == 0 {
				r.logger.Info("Couldn't resolve ports from given CIDR list and will skip this rule", "peer", peer)
				continue
			}
			networkPeers = append(networkPeers, policyinfo.EndpointInfo{
				CIDR:   policyinfo.NetworkAddress(peer.IPBlock.CIDR),
				Except: except,
				Ports:  portList,
			})
			continue
		}
		var namespaces []string
		if peer.NamespaceSelector != nil {
			var err error
			if namespaces, err = r.resolveNamespaces(ctx, peer.NamespaceSelector); err != nil {
				return nil, err
			}
		} else {
			namespaces = []string{policy.Namespace}
		}

		for _, ns := range namespaces {
			networkPeers = append(networkPeers, r.getMatchingPodAddresses(ctx, peer.PodSelector, ns, policy, ports, policyType)...)
		}

	}
	return networkPeers, nil
}

func (r *defaultEndpointsResolver) getIngressRulesPorts(ctx context.Context, policyNamespace string, policyPodSelector *metav1.LabelSelector, ports []networking.NetworkPolicyPort) []policyinfo.Port {
	podList := &corev1.PodList{}
	if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
		LabelSelector: r.createPodLabelSelector(policyPodSelector),
		Namespace:     policyNamespace,
	}); err != nil {
		r.logger.Info("Unable to List Pods", "err", err)
		return nil
	}

	r.logger.V(2).Info("list pods for ingress", "podList", *podList, "namespace", policyNamespace, "selector", *policyPodSelector)
	var portList []policyinfo.Port
	for _, pod := range podList.Items {
		portList = append(portList, r.getPortList(pod, ports)...)
		r.logger.Info("Got ingress port from pod", "pod", types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}.String())
	}

	// since we pull ports from dst pods, we should deduplicate them
	dedupedPorts := dedupPorts(portList)
	r.logger.Info("Got ingress ports from dst pods", "port", dedupedPorts)

	return dedupedPorts
}

func (r *defaultEndpointsResolver) getPortList(pod corev1.Pod, ports []networking.NetworkPolicyPort) []policyinfo.Port {
	var portList []policyinfo.Port
	for _, port := range ports {
		var portPtr *int32
		if port.Port != nil {
			portVal, _, err := k8s.LookupContainerPortAndName(&pod, *port.Port, *port.Protocol)
			if err != nil {
				// Isolate the pod for the port if we are unable to resolve the named port
				r.logger.Info("Unable to lookup container port", "pod", k8s.NamespacedName(&pod),
					"port", *port.Port, "err", err)
				continue
			}
			portPtr = &portVal
		}
		portList = append(portList, policyinfo.Port{
			Protocol: port.Protocol,
			Port:     portPtr,
			EndPort:  port.EndPort,
		})
	}
	return portList
}

func (r *defaultEndpointsResolver) resolveServiceClusterIPs(ctx context.Context, peers []networking.NetworkPolicyPeer, policyNamespace string,
	ports []networking.NetworkPolicyPort) ([]policyinfo.EndpointInfo, error) {
	var networkPeers []policyinfo.EndpointInfo
	for _, peer := range peers {
		var namespaces []string
		if peer.IPBlock != nil {
			continue
		}
		namespaces = append(namespaces, policyNamespace)
		if peer.NamespaceSelector != nil {
			var err error
			namespaces, err = r.resolveNamespaces(ctx, peer.NamespaceSelector)
			if err != nil {
				return nil, err
			}
		}
		r.logger.Info("Populated namespaces for service clusterIP lookup", "list", namespaces)
		for _, ns := range namespaces {
			networkPeers = append(networkPeers, r.getMatchingServiceClusterIPs(ctx, peer.PodSelector, ns, ports)...)
		}
	}
	return networkPeers, nil
}

// convertToPolicyInfoPortForCIDRs converts the NetworkPolicyPort to policyinfo.Port. This is used for CIDR based
// rules where it is not possible to resolve the named ports.
func (r *defaultEndpointsResolver) convertToPolicyInfoPortForCIDRs(port networking.NetworkPolicyPort) *policyinfo.Port {
	protocol := *port.Protocol
	switch {
	case port.Port == nil:
		return &policyinfo.Port{
			Protocol: &protocol,
		}
	case port.Port.Type == intstr.String:
		return nil
	default:
		startPort := int32(port.Port.IntValue())
		return &policyinfo.Port{
			Port:     &startPort,
			Protocol: &protocol,
			EndPort:  port.EndPort,
		}
	}
}

func (r *defaultEndpointsResolver) resolveNamespaces(ctx context.Context, ls *metav1.LabelSelector) ([]string, error) {
	var namespaces []string
	nsSelector, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get namespace selector")
	}
	nsList := &corev1.NamespaceList{}
	if err := r.k8sClient.List(ctx, nsList, &client.ListOptions{
		LabelSelector: nsSelector,
	}); err != nil {
		return nil, errors.Wrap(err, "unable to list namespaces")
	}
	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	return namespaces, nil
}

func (r *defaultEndpointsResolver) getMatchingPodAddresses(ctx context.Context, ls *metav1.LabelSelector, namespace string,
	policy *networking.NetworkPolicy, rulePorts []networking.NetworkPolicyPort, policyType networking.PolicyType) []policyinfo.EndpointInfo {
	var addresses []policyinfo.EndpointInfo

	var portList []policyinfo.Port
	// populate the policy applied targets' ports
	// only populate ports for Ingress and from network policy namespaces as destination ports
	if policyType == networking.PolicyTypeIngress {
		portList = r.getIngressRulesPorts(ctx, policy.Namespace, &policy.Spec.PodSelector, rulePorts)
		if len(rulePorts) != len(portList) && len(portList) == 0 {
			r.logger.Info("Couldn't get matched port list from ingress of policy", "policy", types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}.String(),
				"ingressPorts", rulePorts, "derivedPorts", portList)
			return nil
		}
	}

	// populate src pods for ingress and dst pods for egress
	podList := &corev1.PodList{}
	if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
		LabelSelector: r.createPodLabelSelector(ls),
		Namespace:     namespace,
	}); err != nil {
		r.logger.Info("Unable to List Pods", "err", err)
		return nil
	}
	r.logger.V(1).Info("Got pods for label selector", "count", len(podList.Items), "selector", ls.String())

	for _, pod := range podList.Items {
		podIP := k8s.GetPodIP(&pod)
		if len(podIP) == 0 {
			r.logger.Info("pod IP not assigned yet", "pod", k8s.NamespacedName(&pod))
			continue
		}

		if policyType == networking.PolicyTypeEgress {
			portList = r.getPortList(pod, rulePorts)
			if len(rulePorts) != len(portList) && len(portList) == 0 {
				r.logger.Info("Couldn't get matched port list from the pod", "pod", k8s.NamespacedName(&pod), "expectedPorts", rulePorts)
				continue
			}
		}

		addresses = append(addresses, policyinfo.EndpointInfo{
			CIDR:  policyinfo.NetworkAddress(podIP),
			Ports: portList,
		})
	}

	return addresses
}

func (r *defaultEndpointsResolver) createPodLabelSelector(ls *metav1.LabelSelector) labels.Selector {
	var podSelector labels.Selector
	if ls == nil {
		podSelector = labels.Everything()
	} else {
		var err error
		if podSelector, err = metav1.LabelSelectorAsSelector(ls); err != nil {
			r.logger.Info("Unable to get pod selector", "err", err)
			return nil
		}
	}
	return podSelector
}

// getMatchingServiceClusterIPs returns the clusterIPs of the services with service.spec.Selector matching the pod selector
// in the egress rules. This serves as a workaround for the network agent in case of the policy enforcement for egress traffic
// from the pod to the cluster IPs. The network agent limitation arises since it attaches the ebpf probes to the TC hook of the
// pod veth interface and thus unable to see the pod IP after the DNAT happens for the clusterIPs. The current version is limited
// to tracking the services where the service.spec.Selector matches the pod selector in the egress rules.
func (r *defaultEndpointsResolver) getMatchingServiceClusterIPs(ctx context.Context, ls *metav1.LabelSelector, namespace string,
	ports []networking.NetworkPolicyPort) []policyinfo.EndpointInfo {
	var networkPeers []policyinfo.EndpointInfo
	if ls == nil {
		ls = &metav1.LabelSelector{}
	}
	svcSelector, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		r.logger.Info("Unable to get pod selector", "err", err)
		return nil
	}
	svcList := &corev1.ServiceList{}
	if err := r.k8sClient.List(ctx, svcList, &client.ListOptions{
		Namespace: namespace,
	}); err != nil {
		r.logger.Info("Unable to list services", "err", err)
		return nil
	}
	for _, svc := range svcList.Items {
		// do not add headless services to policy endpoints
		if k8s.IsServiceHeadless(&svc) {
			r.logger.Info("skipping headless service when populating EndpointInfo", "serviceName", svc.Name, "serviceNamespace", svc.Namespace)
			continue
		}
		// do not add services if their pod selector is not matching with the pod selector defined in the network policy
		if !svcSelector.Matches(labels.Set(svc.Spec.Selector)) {
			r.logger.Info("skipping pod selector mismatched service when populating EndpointInfo", "serviceName", svc.Name, "serviceNamespace", svc.Namespace, "expectedPS", svcSelector)
			continue
		}

		var portList []policyinfo.Port
		for _, port := range ports {
			var portPtr *int32
			if port.Port != nil {
				portVal, err := r.getMatchingServicePort(ctx, &svc, port.Port, *port.Protocol)
				if err != nil {
					r.logger.Info("Unable to lookup service port", "err", err)
					continue
				}
				portPtr = &portVal
			}
			portList = append(portList, policyinfo.Port{
				Protocol: port.Protocol,
				Port:     portPtr,
				EndPort:  port.EndPort,
			})
		}
		if len(ports) != len(portList) && len(portList) == 0 {
			r.logger.Info("Couldn't find matching port for the service", "service", k8s.NamespacedName(&svc))
			continue
		}
		networkPeers = append(networkPeers, policyinfo.EndpointInfo{
			CIDR:  policyinfo.NetworkAddress(svc.Spec.ClusterIP),
			Ports: portList,
		})
	}
	return networkPeers
}

func (r *defaultEndpointsResolver) getMatchingServicePort(ctx context.Context, svc *corev1.Service, port *intstr.IntOrString, protocol corev1.Protocol) (int32, error) {
	if port == nil {
		return 0, errors.New("unable to lookup service listen port, input port is nil")
	}
	if portVal, err := k8s.LookupServiceListenPort(svc, *port, protocol); err == nil {
		return portVal, nil
	} else {
		r.logger.Info("Unable to lookup service port", "err", err)
	}
	// List pods matching the svc selector
	podSelector, err := metav1.LabelSelectorAsSelector(metav1.SetAsLabelSelector(svc.Spec.Selector))
	if err != nil {
		return 0, err
	}
	podList := &corev1.PodList{}
	if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
		LabelSelector: podSelector,
		Namespace:     svc.Namespace,
	}); err != nil {
		r.logger.Info("Unable to List Pods", "err", err)
		return 0, err
	}
	for i := range podList.Items {
		if portVal, err := k8s.LookupListenPortFromPodSpec(svc, &podList.Items[i], *port, protocol); err == nil {
			return portVal, nil
		} else {
			r.logger.Info("The pod doesn't have port matched", "err", err, "pod", podList.Items[i])
		}
	}
	return 0, errors.Errorf("unable to find matching service listen port %s for service %s", port.String(), k8s.NamespacedName(svc))
}

func dedupPorts(policyPorts []policyinfo.Port) []policyinfo.Port {
	ports := make(map[string]policyinfo.Port)
	for _, port := range policyPorts {
		prot, p, ep := "", "", ""
		if port.Protocol != nil {
			prot = string(*port.Protocol)
		}
		if port.Port != nil {
			p = strconv.FormatInt(int64(*port.Port), 10)
		}
		if port.EndPort != nil {
			ep = strconv.FormatInt(int64(*port.EndPort), 10)
		}

		ports[fmt.Sprintf("%s@%s@%s", prot, p, ep)] = port
	}
	if len(ports) > 0 {
		return maps.Values(ports)
	}
	return nil
}
