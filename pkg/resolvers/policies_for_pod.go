package resolvers

import (
	"context"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *defaultPolicyReferenceResolver) getReferredPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
	policyList := &networking.NetworkPolicyList{}
	if err := r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, errors.Wrap(err, "failed to fetch policies")
	}
	processedPolicies := sets.Set[types.NamespacedName]{}
	var referredPolicies []networking.NetworkPolicy
	for _, pol := range policyList.Items {
		if r.isPodMatchesPolicySelector(pod, podOld, &pol) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
			continue
		}
		if r.isPodReferredOnIngressEgress(ctx, pod, &pol) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
			continue
		}
		if podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, &pol) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
		}
	}
	r.logger.V(1).Info("Policies referred on the same namespace", "pod", k8s.NamespacedName(pod),
		"policies", referredPolicies)

	// Cross-namespace policies: every tracked policy with a namespaceSelector peer. This loop runs on the pod
	// informer's single event-delivery goroutine for every pod event, so it must stay cheap: the pod's Namespace
	// is read once, the peers' selectors come pre-parsed from the tracker, and the policy object itself is only
	// read for the policies that match (it is what the caller enqueues).
	var nsLabels labels.Set
	nsLoaded, nsFound := false, false
	for _, ref := range r.policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList() {
		if processedPolicies.Has(ref) {
			continue
		}
		peers, indexed := r.policyTracker.GetNamespaceSelectorPeers(ref)
		if indexed {
			if !nsLoaded {
				nsLabels, nsFound = r.podNamespaceLabels(ctx, pod)
				nsLoaded = true
			}
			if !nsFound || !peersMatchPod(peers, nsLabels, pod, podOld) {
				continue
			}
		}
		policy := &networking.NetworkPolicy{}
		if err := r.k8sClient.Get(ctx, ref, policy, client.UnsafeDisableDeepCopy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, errors.Wrap(err, "failed to get policy")
			}
			r.logger.Info("Policy not found", "reference", ref)
			continue
		}
		if !indexed {
			// Tracked before its peers were indexed: match the slow way, from the policy object.
			if !r.isPodReferredOnIngressEgress(ctx, pod, policy) &&
				!(podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, policy)) {
				continue
			}
		}
		referredPolicies = append(referredPolicies, *policy)
		processedPolicies.Insert(k8s.NamespacedName(policy))
	}

	r.logger.V(1).Info("All referred policies", "pod", k8s.NamespacedName(pod), "policies", referredPolicies)
	return referredPolicies, nil
}

// podNamespaceLabels returns the labels of the pod's namespace from the cache. found is false when the namespace
// cannot be read, which makes every namespaceSelector peer a non-match, as the per-peer lookup did before. A
// namespace with no labels is found with an empty set (an empty namespaceSelector still matches it).
func (r *defaultPolicyReferenceResolver) podNamespaceLabels(ctx context.Context, pod *corev1.Pod) (nsLabels labels.Set, found bool) {
	ns := &corev1.Namespace{}
	if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns, client.UnsafeDisableDeepCopy); err != nil {
		r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
		return nil, false
	}
	return labels.Set(ns.Labels), true
}

// peersMatchPod reports whether any namespaceSelector peer selects the pod (or, on an update, the pod as it was):
// the pod's namespace labels must match the peer's namespaceSelector and, when the peer has a podSelector, the
// pod's labels must match it too.
func peersMatchPod(peers []PeerSelectors, nsLabels labels.Set, pod, podOld *corev1.Pod) bool {
	for i := range peers {
		if !peers[i].Namespace.Matches(nsLabels) {
			continue
		}
		if peers[i].Pod == nil || peers[i].Pod.Matches(labels.Set(pod.Labels)) {
			return true
		}
		if podOld != nil && peers[i].Pod.Matches(labels.Set(podOld.Labels)) {
			return true
		}
	}
	return false
}

func (r *defaultPolicyReferenceResolver) isPodMatchesPolicySelector(pod *corev1.Pod, podOld *corev1.Pod, policy *networking.NetworkPolicy) bool {
	ps, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
	if err != nil {
		r.logger.Info("Unable to get pod label selector from policy", "policy", k8s.NamespacedName(policy), "err", err)
		return false
	}
	if ps.Matches(labels.Set(pod.Labels)) {
		return true
	}
	if podOld != nil && ps.Matches(labels.Set(podOld.Labels)) {
		return true
	}
	return false
}

func (r *defaultPolicyReferenceResolver) isPodReferredOnIngressEgress(ctx context.Context, pod *corev1.Pod, policy *networking.NetworkPolicy) bool {
	for _, ingRule := range policy.Spec.Ingress {
		for _, peer := range ingRule.From {
			if r.isPodLabelMatchPeer(ctx, pod, &peer, policy.Namespace) {
				return true
			}
		}
	}
	for _, egrRule := range policy.Spec.Egress {
		for _, peer := range egrRule.To {
			if r.isPodLabelMatchPeer(ctx, pod, &peer, policy.Namespace) {
				return true
			}
		}
	}
	return false
}

// TODO: Consider using generics or interfaces to reduce duplication between
// isPodLabelMatchPeer and isPodLabelMatchApplicationNetworkPolicyPeer.
// Both functions have nearly identical logic but work with different peer types.
func (r *defaultPolicyReferenceResolver) isPodLabelMatchPeer(ctx context.Context, pod *corev1.Pod, peer *networking.NetworkPolicyPeer, policyNamespace string) bool {
	if peer.NamespaceSelector != nil {
		ns := &corev1.Namespace{}
		if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns, client.UnsafeDisableDeepCopy); err != nil {
			r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
			return false
		}
		nsSelector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			r.logger.Info("Unable to get namespace selector", "selector", peer.NamespaceSelector, "err", err)
			return false
		}
		if !nsSelector.Matches(labels.Set(ns.Labels)) {
			return false
		}

		if peer.PodSelector == nil {
			r.logger.V(1).Info("nsSelector matches ns labels", "selector", nsSelector,
				"ns", ns)
			return true
		}
	} else if pod.Namespace != policyNamespace {
		r.logger.V(1).Info("Pod and policy namespace mismatch", "pod", k8s.NamespacedName(pod),
			"policy ns", policyNamespace)
		return false
	}
	podSelector, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
	if err != nil {
		r.logger.Info("Unable to get pod selector", "err", err)
		return false
	}
	if podSelector.Matches(labels.Set(pod.Labels)) {
		return true
	}
	return false
}
func (r *defaultPolicyReferenceResolver) getReferredApplicationNetworkPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]policyinfo.ApplicationNetworkPolicy, error) {
	policyList := &policyinfo.ApplicationNetworkPolicyList{}
	if err := r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, errors.Wrap(err, "failed to fetch ANP policies")
	}
	processedPolicies := sets.Set[types.NamespacedName]{}
	var referredPolicies []policyinfo.ApplicationNetworkPolicy

	// First loop: Process same-namespace ANP policies
	// Check policies in the pod's namespace for selector matches or ingress/egress references
	for _, pol := range policyList.Items {
		if r.isPodMatchesANPPolicySelector(pod, podOld, &pol) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
			continue
		}
		if r.isPodReferredOnANPIngressEgress(ctx, pod, &pol) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
			continue
		}
		if podOld != nil && r.isPodReferredOnANPIngressEgress(ctx, podOld, &pol) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
		}
	}
	r.logger.V(1).Info("ANP Policies referred on the same namespace", "pod", k8s.NamespacedName(pod),
		"policies", referredPolicies)

	// Cross-namespace ANPs: same shape as the NetworkPolicy loop above, and the same reasons to keep it cheap
	// (it runs on the pod informer's single event-delivery goroutine).
	var nsLabels labels.Set
	nsLoaded, nsFound := false, false
	for _, ref := range r.policyTracker.GetApplicationNetworkPoliciesWithNamespaceReferences().UnsortedList() {
		if processedPolicies.Has(ref) {
			continue
		}
		peers, indexed := r.policyTracker.GetANPNamespaceSelectorPeers(ref)
		if indexed {
			if !nsLoaded {
				nsLabels, nsFound = r.podNamespaceLabels(ctx, pod)
				nsLoaded = true
			}
			if !nsFound || !peersMatchPod(peers, nsLabels, pod, podOld) {
				continue
			}
		}
		policy := &policyinfo.ApplicationNetworkPolicy{}
		if err := r.k8sClient.Get(ctx, ref, policy, client.UnsafeDisableDeepCopy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, errors.Wrap(err, "failed to get ANP policy")
			}
			r.logger.Info("ANP Policy not found", "reference", ref)
			continue
		}
		if !indexed {
			// Tracked before its peers were indexed: match the slow way, from the policy object.
			if !r.isPodReferredOnANPIngressEgress(ctx, pod, policy) &&
				!(podOld != nil && r.isPodReferredOnANPIngressEgress(ctx, podOld, policy)) {
				continue
			}
		}
		referredPolicies = append(referredPolicies, *policy)
		processedPolicies.Insert(k8s.NamespacedName(policy))
	}

	r.logger.V(1).Info("All referred ANP policies", "pod", k8s.NamespacedName(pod), "policies", referredPolicies)
	return referredPolicies, nil
}

func (r *defaultPolicyReferenceResolver) isPodMatchesANPPolicySelector(pod *corev1.Pod, podOld *corev1.Pod, policy *policyinfo.ApplicationNetworkPolicy) bool {
	ps, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
	if err != nil {
		r.logger.Info("Unable to get pod label selector from ANP policy", "policy", k8s.NamespacedName(policy), "err", err)
		return false
	}
	if ps.Matches(labels.Set(pod.Labels)) {
		return true
	}
	if podOld != nil && ps.Matches(labels.Set(podOld.Labels)) {
		return true
	}
	return false
}

func (r *defaultPolicyReferenceResolver) isPodReferredOnANPIngressEgress(ctx context.Context, pod *corev1.Pod, policy *policyinfo.ApplicationNetworkPolicy) bool {
	for _, ingRule := range policy.Spec.Ingress {
		for _, peer := range ingRule.From {
			if r.isPodLabelMatchPeer(ctx, pod, &peer, policy.Namespace) {
				return true
			}
		}
	}
	for _, egrRule := range policy.Spec.Egress {
		for _, peer := range egrRule.To {
			if r.isPodLabelMatchApplicationNetworkPolicyPeer(ctx, pod, &peer, policy.Namespace) {
				return true
			}
		}
	}
	return false
}

func (r *defaultPolicyReferenceResolver) isPodLabelMatchApplicationNetworkPolicyPeer(ctx context.Context, pod *corev1.Pod, peer *policyinfo.ApplicationNetworkPolicyPeer, policyNamespace string) bool {
	if peer.NamespaceSelector != nil {
		ns := &corev1.Namespace{}
		if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns, client.UnsafeDisableDeepCopy); err != nil {
			r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
			return false
		}
		nsSelector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			r.logger.Info("Unable to get namespace selector", "selector", peer.NamespaceSelector, "err", err)
			return false
		}
		if !nsSelector.Matches(labels.Set(ns.Labels)) {
			return false
		}

		if peer.PodSelector == nil {
			r.logger.Info("nsSelector matches ns labels", "selector", nsSelector,
				"ns", ns)
			return true
		}
	} else if pod.Namespace != policyNamespace {
		r.logger.Info("Pod and ANP policy namespace mismatch", "pod", k8s.NamespacedName(pod),
			"policy ns", policyNamespace)
		return false
	}
	podSelector, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
	if err != nil {
		r.logger.Info("Unable to get pod selector", "err", err)
		return false
	}
	if podSelector.Matches(labels.Set(pod.Labels)) {
		return true
	}
	return false
}
