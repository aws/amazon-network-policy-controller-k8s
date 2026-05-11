package resolvers

import (
	"context"
	stderrors "errors"
	"fmt"
	"strconv"
	"sync"

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
	var (
		ingressEndpoints     []policyinfo.EndpointInfo
		egressEndpoints      []policyinfo.EndpointInfo
		podSelectorEndpoints []policyinfo.PodEndpoint
		ingressErr           error
		egressErr            error
		podSelectorErr       error
		wg                   sync.WaitGroup
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		ingressEndpoints, ingressErr = r.computeIngressEndpoints(ctx, policy)
	}()
	go func() {
		defer wg.Done()
		egressEndpoints, egressErr = r.computeEgressEndpoints(ctx, policy)
	}()
	go func() {
		defer wg.Done()
		podSelectorEndpoints, podSelectorErr = r.computePodSelectorEndpoints(ctx, policy)
	}()
	wg.Wait()

	if err := stderrors.Join(ingressErr, egressErr, podSelectorErr); err != nil {
		return nil, nil, nil, err
	}

	r.logger.Info("Resolved endpoints", "policy", k8s.NamespacedName(policy), "ingress", len(ingressEndpoints), "egress",
		len(egressEndpoints), "pod selector endpoints", len(podSelectorEndpoints))

	return ingressEndpoints, egressEndpoints, podSelectorEndpoints, nil
}

// computeIngressEndpoints resolves ingress rules following the NetworkPolicy schema structure.
// In the NP schema, ports and from (peers) are siblings at the rule level — ports apply equally
// to all peers. This function resolves ports once per rule, then resolves peers using those ports.
func (r *defaultEndpointsResolver) computeIngressEndpoints(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, error) {
	var ingressEndpoints []policyinfo.EndpointInfo
	for _, rule := range policy.Spec.Ingress {
		r.logger.V(1).Info("computing ingress addresses", "peers", rule.From)

		// Step 1: Resolve ports once per rule.
		// Named ports are resolved against the policy's destination pods.
		// Numeric and nil (protocol-only) ports are resolved without pod lookups.
		resolvedPorts := r.resolveIngressPorts(ctx, policy, rule.Ports)
		if len(rule.Ports) != 0 && len(resolvedPorts) == 0 {
			r.logger.Info("Couldn't resolve any ports for ingress rule, skipping",
				"policy", k8s.NamespacedName(policy), "ports", rule.Ports)
			continue
		}

		// Step 2: Resolve peers using the pre-computed ports.
		if rule.From == nil {
			// nil From means allow from all sources
			ingressEndpoints = append(ingressEndpoints, r.buildAllowAllIngress(resolvedPorts)...)
			continue
		}

		for _, peer := range rule.From {
			if peer.IPBlock != nil {
				ingressEndpoints = append(ingressEndpoints, r.buildIPBlockEndpoint(peer.IPBlock, resolvedPorts))
				continue
			}

			namespaces, err := r.resolvePeerNamespaces(ctx, policy.Namespace, peer)
			if err != nil {
				return nil, fmt.Errorf("resolving ingress peers: %w", err)
			}

			for _, ns := range namespaces {
				pods, err := r.listMatchingPods(ctx, ns, peer.PodSelector)
				if err != nil {
					r.logger.Info("Unable to List Pods", "err", err)
					continue
				}
				for _, pod := range pods {
					if !k8s.IsPodNetworkReady(&pod) {
						continue
					}
					ingressEndpoints = append(ingressEndpoints, policyinfo.EndpointInfo{
						CIDR:  policyinfo.NetworkAddress(k8s.GetPodIP(&pod)),
						Ports: resolvedPorts,
					})
				}
			}
		}
	}
	r.logger.V(1).Info("Resolved ingress rules", "policy", k8s.NamespacedName(policy), "addresses", ingressEndpoints)
	return ingressEndpoints, nil
}

// resolveIngressPorts resolves the port list for an ingress rule. Named ports are resolved against
// the destination pods (pods the policy applies to).
func (r *defaultEndpointsResolver) resolveIngressPorts(ctx context.Context, policy *networking.NetworkPolicy,
	ports []networking.NetworkPolicyPort) []policyinfo.Port {
	if len(ports) == 0 {
		return nil
	}

	hasNamedPorts := false
	for _, port := range ports {
		if port.Port != nil && port.Port.Type == intstr.String {
			hasNamedPorts = true
			break
		}
	}

	// If all ports are numeric or nil, resolve without listing pods
	if !hasNamedPorts {
		var portList []policyinfo.Port
		for _, port := range ports {
			portInfo := r.convertToPolicyInfoPortForCIDRs(port)
			if portInfo != nil {
				portList = append(portList, *portInfo)
			}
		}
		return portList
	}

	// Has named ports — resolve from destination pods
	return r.getIngressRulesPorts(ctx, policy.Namespace, &policy.Spec.PodSelector, ports)
}

// buildAllowAllIngress returns EndpointInfo entries for 0.0.0.0/0 and ::/0 with the given ports.
func (r *defaultEndpointsResolver) buildAllowAllIngress(ports []policyinfo.Port) []policyinfo.EndpointInfo {
	return []policyinfo.EndpointInfo{
		{CIDR: "0.0.0.0/0", Ports: ports},
		{CIDR: "::/0", Ports: ports},
	}
}

// buildIPBlockEndpoint converts an IPBlock peer into an EndpointInfo entry with the given ports.
func (r *defaultEndpointsResolver) buildIPBlockEndpoint(ipBlock *networking.IPBlock, ports []policyinfo.Port) policyinfo.EndpointInfo {
	var except []policyinfo.NetworkAddress
	for _, ea := range ipBlock.Except {
		except = append(except, policyinfo.NetworkAddress(ea))
	}
	return policyinfo.EndpointInfo{
		CIDR:   policyinfo.NetworkAddress(ipBlock.CIDR),
		Except: except,
		Ports:  ports,
	}
}

// computeEgressEndpoints resolves egress rules following the NetworkPolicy schema structure.
// Ports and to (peers) are siblings at the rule level. Unlike ingress, named ports for egress
// are resolved per destination pod since different pods may expose different container ports.
// Service ClusterIP resolution is fused into the same peer/namespace iteration to avoid
// duplicate resolveNamespaces calls.
func (r *defaultEndpointsResolver) computeEgressEndpoints(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, error) {
	var egressEndpoints []policyinfo.EndpointInfo
	for _, rule := range policy.Spec.Egress {
		r.logger.V(1).Info("computing egress addresses", "peers", rule.To)

		// nil To means allow to all destinations
		if rule.To == nil {
			egressEndpoints = append(egressEndpoints, r.buildAllowAllEgress(rule.Ports)...)
			continue
		}

		// Pre-compute CIDR-compatible ports once per rule (named ports excluded
		// since there's no specific destination to resolve them against).
		var cidrPorts []policyinfo.Port
		for _, port := range rule.Ports {
			if portInfo := r.convertToPolicyInfoPortForCIDRs(port); portInfo != nil {
				cidrPorts = append(cidrPorts, *portInfo)
			}
		}

		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				if len(rule.Ports) != 0 && len(cidrPorts) == 0 {
					r.logger.Info("Couldn't resolve ports from given CIDR list, skipping peer", "peer", peer)
					continue
				}
				egressEndpoints = append(egressEndpoints, r.buildIPBlockEndpoint(peer.IPBlock, cidrPorts))
				continue
			}

			namespaces, err := r.resolvePeerNamespaces(ctx, policy.Namespace, peer)
			if err != nil {
				return nil, fmt.Errorf("resolving egress peers: %w", err)
			}

			for _, ns := range namespaces {
				// Pod endpoints: list matching pods, resolve ports per destination pod
				pods, err := r.listMatchingPods(ctx, ns, peer.PodSelector)
				if err != nil {
					r.logger.Info("Unable to List Pods", "err", err)
				} else {
					for _, pod := range pods {
						if !k8s.IsPodNetworkReady(&pod) {
							continue
						}
						portList := r.getPortList(pod, rule.Ports)
						if len(rule.Ports) != len(portList) && len(portList) == 0 {
							r.logger.Info("Couldn't get matched port list from the pod",
								"pod", k8s.NamespacedName(&pod), "expectedPorts", rule.Ports)
							continue
						}
						egressEndpoints = append(egressEndpoints, policyinfo.EndpointInfo{
							CIDR:  policyinfo.NetworkAddress(k8s.GetPodIP(&pod)),
							Ports: portList,
						})
					}
				}

				// Service ClusterIP resolution for the same namespace.
				// Services whose selector matches the NP peer's podSelector should have
				// their ClusterIPs added to handle eBPF probe limitations post-DNAT.
				egressEndpoints = append(egressEndpoints, r.getMatchingServiceClusterIPs(ctx, peer.PodSelector, ns, rule.Ports)...)
			}
		}
	}
	r.logger.V(1).Info("Resolved egress rules", "policy", k8s.NamespacedName(policy), "addresses", egressEndpoints)
	return egressEndpoints, nil
}

// buildAllowAllEgress returns EndpointInfo entries for 0.0.0.0/0 and ::/0 with the given ports.
// Named ports are skipped since there's no specific destination to resolve them against.
func (r *defaultEndpointsResolver) buildAllowAllEgress(ports []networking.NetworkPolicyPort) []policyinfo.EndpointInfo {
	var portList []policyinfo.Port
	for _, port := range ports {
		if portInfo := r.convertToPolicyInfoPortForCIDRs(port); portInfo != nil {
			portList = append(portList, *portInfo)
		}
	}
	if len(ports) != 0 && len(portList) == 0 {
		return nil
	}
	return []policyinfo.EndpointInfo{
		{CIDR: "0.0.0.0/0", Ports: portList},
		{CIDR: "::/0", Ports: portList},
	}
}

func (r *defaultEndpointsResolver) computePodSelectorEndpoints(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.PodEndpoint, error) {
	var podEndpoints []policyinfo.PodEndpoint
	var skippedHostNetworkPods []string
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
		if pod.Spec.HostNetwork {
			podName := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
			skippedHostNetworkPods = append(skippedHostNetworkPods, podName)
			continue
		}
		if k8s.IsPodNetworkReady(&pod) {
			podIP := k8s.GetPodIP(&pod)
			podEndpoints = append(podEndpoints, policyinfo.PodEndpoint{
				PodIP:     policyinfo.NetworkAddress(podIP),
				HostIP:    policyinfo.NetworkAddress(pod.Status.HostIP),
				Name:      pod.Name,
				Namespace: pod.Namespace,
			})
		}
	}
	if len(skippedHostNetworkPods) > 0 {
		r.logger.Info("Skipped hostNetwork pods", "policy", k8s.NamespacedName(policy), "pods", skippedHostNetworkPods)
	}
	r.logger.V(1).Info("Resolved pod selector endpoints", "policy", k8s.NamespacedName(policy), "pod endpoints", podEndpoints)
	return podEndpoints, nil
}

// getIngressRulesPorts resolves named ports in ingress rules by looking up the actual container ports
// on the policy's destination pods (pods matching policyPodSelector in policyNamespace).
//
// Named ports like "http" are resolved to numeric values via the containerPort declarations of the
// destination pods. Returns a deduplicated list of resolved policyinfo.Port entries.
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
		if !k8s.IsPodNetworkReady(&pod) {
			continue
		}
		portList = append(portList, r.getPortList(pod, ports)...)
		r.logger.V(1).Info("Got ingress port from pod", "pod", types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}.String())
	}

	// since we pull ports from dst pods, we should deduplicate them
	dedupedPorts := dedupPorts(portList)
	r.logger.V(1).Info("Got ingress ports from dst pods", "port", dedupedPorts)

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

func (r *defaultEndpointsResolver) resolvePeerNamespaces(ctx context.Context, policyNamespace string,
	peer networking.NetworkPolicyPeer) ([]string, error) {
	if peer.NamespaceSelector == nil {
		return []string{policyNamespace}, nil
	}
	return r.resolveNamespaces(ctx, peer.NamespaceSelector)
}

func (r *defaultEndpointsResolver) listMatchingPods(ctx context.Context, namespace string,
	podSelector *metav1.LabelSelector) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
		LabelSelector: r.createPodLabelSelector(podSelector),
		Namespace:     namespace,
	}); err != nil {
		return nil, err
	}
	return podList.Items, nil
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
func (r *defaultEndpointsResolver) getMatchingServiceClusterIPs(ctx context.Context, npPodSelector *metav1.LabelSelector, namespace string,
	npPorts []networking.NetworkPolicyPort) []policyinfo.EndpointInfo {
	var networkPeers []policyinfo.EndpointInfo
	var headlessServiceList []string
	var nonMatchingPodSelectorList []string
	var headlessServiceCount int
	var nonMatchingPodSelectorCount int
	const maxSampleSize = 50
	if npPodSelector == nil {
		npPodSelector = &metav1.LabelSelector{}
	}
	svcSelector, err := metav1.LabelSelectorAsSelector(npPodSelector)
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

	// Lazily fetch all pods in namespace once to avoid repeated cache iterations
	var allPods []corev1.Pod
	getAllPods := func() ([]corev1.Pod, error) {
		if allPods != nil {
			return allPods, nil
		}
		podList := &corev1.PodList{}
		if err := r.k8sClient.List(ctx, podList, &client.ListOptions{
			Namespace: namespace,
		}); err != nil {
			return nil, err
		}
		allPods = podList.Items
		return allPods, nil
	}

	for i := range svcList.Items {
		svc := &svcList.Items[i]
		// do not add headless services to policy endpoints
		if k8s.IsServiceHeadless(svc) {
			if headlessServiceCount < maxSampleSize {
				headlessServiceList = append(headlessServiceList, fmt.Sprintf("%s/%s", svc.Namespace, svc.Name))
			}
			headlessServiceCount++
			r.logger.V(1).Info("skipping headless service when populating EndpointInfo", "serviceName", svc.Name, "serviceNamespace", svc.Namespace)
			continue
		}
		// do not add services if their pod selector is not matching with the pod selector defined in the network policy
		if !svcSelector.Matches(labels.Set(svc.Spec.Selector)) {
			if nonMatchingPodSelectorCount < maxSampleSize {
				nonMatchingPodSelectorList = append(nonMatchingPodSelectorList, fmt.Sprintf("%s/%s (expectedPS: %s)", svc.Namespace, svc.Name, svcSelector.String()))
			}
			nonMatchingPodSelectorCount++
			r.logger.V(1).Info("skipping pod selector mismatched service when populating EndpointInfo", "serviceName", svc.Name, "serviceNamespace", svc.Namespace, "expectedPS", svcSelector)
			continue
		}

		var portList []policyinfo.Port
		for _, npPort := range npPorts {
			var portPtr *int32
			if npPort.Port != nil {
				npProto := corev1.ProtocolTCP
				if npPort.Protocol != nil {
					npProto = *npPort.Protocol
				}
				portVal, err := r.getMatchingServicePort(svc, npPort.Port, npProto, getAllPods)
				if err != nil {
					r.logger.Info("Unable to lookup service port", "err", err)
					continue
				}
				// When the NP specifies a numeric port, check if the matched service port
				// uses a named targetPort that could resolve to a different container port
				// on some backing pods, bypassing the policy's port restriction via DNAT.
				if npPort.Port.Type == intstr.Int {
					if r.hasTargetPortBypass(svc, portVal, npProto, npPorts, getAllPods) {
						r.logger.Info("Skipping service ClusterIP port (named targetPort may bypass policy port restriction)",
							"serviceName", svc.Name, "serviceNamespace", svc.Namespace,
							"servicePort", portVal)
						continue
					}
				}
				portPtr = &portVal
			}
			portList = append(portList, policyinfo.Port{
				Protocol: npPort.Protocol,
				Port:     portPtr,
				EndPort:  npPort.EndPort,
			})
		}
		if len(npPorts) != len(portList) && len(portList) == 0 {
			r.logger.V(1).Info("Couldn't find matching port for the service", "service", k8s.NamespacedName(svc))
			continue
		}
		networkPeers = append(networkPeers, policyinfo.EndpointInfo{
			CIDR:  policyinfo.NetworkAddress(svc.Spec.ClusterIP),
			Ports: portList,
		})
	}
	if headlessServiceCount > 0 {
		r.logger.Info("Skipped headless services", "count", headlessServiceCount)
		r.logger.Info("Sample of headless services (up to 50)", "services", headlessServiceList)
	}
	if nonMatchingPodSelectorCount > 0 {
		r.logger.Info("Skipped pod selector mismatched services", "count", nonMatchingPodSelectorCount)
		r.logger.Info("Sample of pod selector mismatched services (up to 50)", "services", nonMatchingPodSelectorList)
	}
	return networkPeers
}

// hasTargetPortBypass checks if a specific matched service port's named targetPort resolves
// to a container port not allowed by the policy on any backing pod. This prevents a bypass
// where traffic to ClusterIP:X gets DNAT'd by kube-proxy to a pod container port that the
// NetworkPolicy did not intend to allow.
func (r *defaultEndpointsResolver) hasTargetPortBypass(svc *corev1.Service, matchedListenPort int32,
	protocol corev1.Protocol, policyPorts []networking.NetworkPolicyPort,
	getAllPods func() ([]corev1.Pod, error)) bool {
	// Find the named targetPort for the matched service port
	var targetPortName string
	for _, sp := range svc.Spec.Ports {
		spProto := sp.Protocol
		if spProto == "" {
			spProto = corev1.ProtocolTCP
		}
		if sp.Port == matchedListenPort && spProto == protocol && sp.TargetPort.Type == intstr.String {
			targetPortName = sp.TargetPort.StrVal
			break
		}
	}
	if targetPortName == "" {
		return false // numeric targetPort, no bypass risk
	}

	allPods, err := getAllPods()
	if err != nil {
		r.logger.Error(err, "Unable to list pods for bypass check")
		return true // conservative: assume bypass on error
	}

	svcSelector := labels.Set(svc.Spec.Selector).AsSelector()
	for i := range allPods {
		pod := &allPods[i]
		if !svcSelector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		// Resolve the named port using the first matching container port, matching
		// Kubernetes EndpointSlice controller behavior for svc resolution.
		cp, containerName, found := firstNamedPort(pod, targetPortName, protocol)
		if !found {
			continue
		}
		if !isNumericPortAllowed(cp.ContainerPort, protocol, policyPorts) {
			r.logger.Info("Named port resolves to disallowed container port",
				"service", k8s.NamespacedName(svc),
				"pod", k8s.NamespacedName(pod),
				"container", containerName,
				"portName", cp.Name,
				"containerPort", cp.ContainerPort, "protocol", protocol)
			return true
		}
	}
	return false
}

// isNumericPortAllowed checks whether a given numeric port+protocol is allowed by the policy
// ports list. A nil-port entry (protocol only, no port number) means "allow all ports" for
// that protocol. Numeric entries are checked directly, including port ranges via EndPort.
// Named port entries (e.g., port: "http") are skipped because they cannot be compared to a
// numeric port without pod context to resolve the name. This is safe because the caller
// only invokes the bypass check for numeric NP ports; named NP ports bypass it entirely.
func isNumericPortAllowed(port int32, protocol corev1.Protocol, policyPorts []networking.NetworkPolicyPort) bool {
	for _, pp := range policyPorts {
		ppProto := corev1.ProtocolTCP
		if pp.Protocol != nil {
			ppProto = *pp.Protocol
		}
		if ppProto != protocol {
			continue
		}
		// nil port means "allow all ports" for this protocol
		if pp.Port == nil {
			return true
		}
		if pp.Port.Type != intstr.Int {
			continue
		}
		startPort := pp.Port.IntVal
		endPort := startPort
		if pp.EndPort != nil {
			endPort = *pp.EndPort
		}
		if port >= startPort && port <= endPort {
			return true
		}
	}
	return false
}

// firstNamedPort returns the first container port matching the given name and protocol in a pod.
// This mirrors how Kubernetes EndpointSlice controller resolves named targetPorts — it picks the
// first matching container port across all containers in declaration order.
func firstNamedPort(pod *corev1.Pod, portName string, protocol corev1.Protocol) (corev1.ContainerPort, string, bool) {
	for i := range pod.Spec.Containers {
		for j := range pod.Spec.Containers[i].Ports {
			cp := pod.Spec.Containers[i].Ports[j]
			cpProto := cp.Protocol
			if cpProto == "" {
				cpProto = corev1.ProtocolTCP
			}
			if cp.Name == portName && cpProto == protocol {
				return cp, pod.Spec.Containers[i].Name, true
			}
		}
	}
	return corev1.ContainerPort{}, "", false
}

// getMatchingServicePort finds the service listen port that matches the given port specification.
// It first tries direct lookup, then falls back to checking pod specs.
func (r *defaultEndpointsResolver) getMatchingServicePort(svc *corev1.Service, npPort *intstr.IntOrString, npProtocol corev1.Protocol, getAllPods func() ([]corev1.Pod, error)) (int32, error) {
	if npPort == nil {
		return 0, errors.New("unable to lookup service listen port, input port is nil")
	}
	if portVal, err := k8s.LookupServiceListenPort(svc, *npPort, npProtocol); err == nil {
		return portVal, nil
	} else {
		r.logger.Info("Unable to lookup service port", "err", err)
	}

	allPods, err := getAllPods()
	if err != nil {
		return 0, err
	}

	// Filter pods in-memory using service selector
	svcSelector := labels.Set(svc.Spec.Selector).AsSelector()
	for i := range allPods {
		pod := &allPods[i]
		if !svcSelector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		if portVal, err := k8s.LookupListenPortFromPodSpec(svc, pod, *npPort, npProtocol); err == nil {
			return portVal, nil
		} else {
			r.logger.V(1).Info("The pod doesn't have port matched", "err", err, "pod", pod.Name)
		}
	}
	return 0, fmt.Errorf("unable to find matching service listen port %s for service %s", npPort.String(), k8s.NamespacedName(svc))
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
