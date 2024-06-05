package resolvers

import (
	"context"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	adminnetworking "sigs.k8s.io/network-policy-api/apis/v1alpha1"
)

func (r *defaultPolicyReferenceResolver) getReferredPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error) {
	policyList := &networking.NetworkPolicyList{}
	adminPolicyList := &adminnetworking.AdminNetworkPolicyList{}
	noPolicy := false
	noAdminPolicy := false
	var err error
	if err = r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace)); err != nil {
		noPolicy = true
	}
	if err = r.k8sClient.List(ctx, adminPolicyList, client.InNamespace(pod.Namespace)); err != nil {
		noAdminPolicy = true
	}
	if noPolicy && noAdminPolicy {
		return nil, nil, errors.Wrap(err, "failed to fetch policies")
	}
	processedPolicies := sets.Set[types.NamespacedName]{}
	var referredPolicies []networking.NetworkPolicy
	var referredAdminPolicies []adminnetworking.AdminNetworkPolicy
	for _, pol := range policyList.Items {
		if r.isPodMatchesPolicySelector(pod, podOld, &pol, nil, false) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
			continue
		}
		if r.isPodReferredOnIngressEgress(ctx, pod, &pol, nil, false) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
			continue
		}
		if podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, &pol, nil, false) {
			referredPolicies = append(referredPolicies, pol)
			processedPolicies.Insert(k8s.NamespacedName(&pol))
		}
	}
	for _, adminPol := range adminPolicyList.Items {
		if r.isPodMatchesPolicySelector(pod, podOld, nil, &adminPol, true) {
			referredAdminPolicies = append(referredAdminPolicies, adminPol)
			processedPolicies.Insert(k8s.NamespacedName(&adminPol))
			continue
		}
		if r.isPodReferredOnIngressEgress(ctx, pod, nil, &adminPol, true) {
			referredAdminPolicies = append(referredAdminPolicies, adminPol)
			processedPolicies.Insert(k8s.NamespacedName(&adminPol))
			continue
		}
		if podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, nil, &adminPol, true) {
			referredAdminPolicies = append(referredAdminPolicies, adminPol)
			processedPolicies.Insert(k8s.NamespacedName(&adminPol))
		}
	}
	r.logger.V(1).Info("Policies referred on the same namespace", "pod", k8s.NamespacedName(pod),
		"policies", referredPolicies)
	r.logger.V(1).Info("Admin policies referred on the same namespace", "pod", k8s.NamespacedName(pod),
		"policies", referredAdminPolicies)

	for _, ref := range r.policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList() {
		r.logger.V(1).Info("Policy containing namespace selectors", "ref", ref)
		if processedPolicies.Has(ref) {
			continue
		}
		isPolicyFound := true
		isAdminPolicyFound := true
		policy := &networking.NetworkPolicy{}
		if err := r.k8sClient.Get(ctx, ref, policy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, nil, errors.Wrap(err, "failed to get policy")
			}
			isPolicyFound = false
		}
		adminPolicy := &adminnetworking.AdminNetworkPolicy{}
		if err := r.k8sClient.Get(ctx, ref, adminPolicy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, nil, errors.Wrap(err, "failed to get policy")
			}
			isAdminPolicyFound = false
		}
		if !isAdminPolicyFound && !isPolicyFound {
			r.logger.V(1).Info("Policy not found", "reference", ref)
			continue
		}
		isPolicyReferred := false
		isAdminPolicyReferred := false
		if isPolicyFound && r.isPodReferredOnIngressEgress(ctx, pod, policy, nil, false) {
			referredPolicies = append(referredPolicies, *policy)
			processedPolicies.Insert(k8s.NamespacedName(policy))
			isPolicyReferred = true
			// continue
		}

		if isAdminPolicyFound && r.isPodReferredOnIngressEgress(ctx, pod, nil, adminPolicy, true) {
			referredAdminPolicies = append(referredAdminPolicies, *adminPolicy)
			processedPolicies.Insert(k8s.NamespacedName(adminPolicy))
			isAdminPolicyReferred = false
			// continue
		}
		if isPolicyReferred && isAdminPolicyReferred {
			continue
		} else if isAdminPolicyReferred {
			if podOld != nil && isPolicyFound && r.isPodReferredOnIngressEgress(ctx, podOld, policy, nil, false) {
				referredPolicies = append(referredPolicies, *policy)
				processedPolicies.Insert(k8s.NamespacedName(policy))
			}
		} else if isPolicyReferred {
			if podOld != nil && isAdminPolicyFound && r.isPodReferredOnIngressEgress(ctx, podOld, nil, adminPolicy, true) {
				referredAdminPolicies = append(referredAdminPolicies, *adminPolicy)
				processedPolicies.Insert(k8s.NamespacedName(adminPolicy))
			}
		} else {
			if podOld != nil && isPolicyFound && r.isPodReferredOnIngressEgress(ctx, podOld, policy, nil, false) {
				referredPolicies = append(referredPolicies, *policy)
				processedPolicies.Insert(k8s.NamespacedName(policy))
			}
			if podOld != nil && isAdminPolicyFound && r.isPodReferredOnIngressEgress(ctx, podOld, nil, adminPolicy, true) {
				referredAdminPolicies = append(referredAdminPolicies, *adminPolicy)
				processedPolicies.Insert(k8s.NamespacedName(adminPolicy))
			}
		}
	}

	r.logger.V(1).Info("All referred policies", "pod", k8s.NamespacedName(pod), "policies", referredPolicies)
	r.logger.V(1).Info("All referred admin policies", "pod", k8s.NamespacedName(pod), "policies", referredAdminPolicies)
	return referredPolicies, referredAdminPolicies, nil
}

func (r *defaultPolicyReferenceResolver) isPodMatchesPolicySelector(pod *corev1.Pod, podOld *corev1.Pod, policy *networking.NetworkPolicy, adminPolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool) bool {
	var ps labels.Selector
	var err error
	if isAdmin {
		if adminPolicy.Spec.Subject.Namespaces != nil {
			ps, err = metav1.LabelSelectorAsSelector(adminPolicy.Spec.Subject.Namespaces)
		} else {
			ps, err = metav1.LabelSelectorAsSelector(&adminPolicy.Spec.Subject.Pods.NamespaceSelector)
		}
	} else {
		ps, err = metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
	}
	if err != nil {
		if isAdmin {
			r.logger.Info("Unable to get pod label selector from adminpolicy", "adminpolicy", k8s.NamespacedName(adminPolicy), "err", err)
		} else {
			r.logger.Info("Unable to get pod label selector from policy", "policy", k8s.NamespacedName(policy), "err", err)
		}
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

func (r *defaultPolicyReferenceResolver) isPodReferredOnIngressEgress(ctx context.Context, pod *corev1.Pod, policy *networking.NetworkPolicy, adminPolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool) bool {
	if isAdmin {
		namespaces, _ := r.podSelectorNamespaces(ctx, adminPolicy)
		for _, ns := range namespaces {
			for _, ingRule := range adminPolicy.Spec.Ingress {
				for _, peer := range ingRule.From {
					if r.isPodLabelMatchPeer(ctx, pod, nil, &peer, nil, ns.Name, isAdmin) {
						return true
					}
				}
			}
			for _, egrRule := range adminPolicy.Spec.Egress {
				for _, peer := range egrRule.To {
					if r.isPodLabelMatchPeer(ctx, pod, nil, nil, &peer, ns.Name, isAdmin) {
						return true
					}
				}
			}
		}
	} else {
		for _, ingRule := range policy.Spec.Ingress {
			for _, peer := range ingRule.From {
				if r.isPodLabelMatchPeer(ctx, pod, &peer, nil, nil, policy.Namespace, isAdmin) {
					return true
				}
			}
		}
		for _, egrRule := range policy.Spec.Egress {
			for _, peer := range egrRule.To {
				if r.isPodLabelMatchPeer(ctx, pod, &peer, nil, nil, policy.Namespace, isAdmin) {
					return true
				}
			}
		}
	}
	return false
}

func (r *defaultPolicyReferenceResolver) isPodLabelMatchPeer(ctx context.Context, pod *corev1.Pod, peer *networking.NetworkPolicyPeer, ingressPeer *adminnetworking.AdminNetworkPolicyIngressPeer, egressPeer *adminnetworking.AdminNetworkPolicyEgressPeer, policyNamespace string, isAdmin bool) bool {
	if isAdmin {
		if ingressPeer != nil {
			if ingressPeer.Namespaces != nil {
				ns := &corev1.Namespace{}
				if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns); err != nil {
					r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
					return false
				}
				nsSelector, err := metav1.LabelSelectorAsSelector(ingressPeer.Namespaces)
				if err != nil {
					r.logger.Info("Unable to get namespace selector", "selector", ingressPeer, "err", err)
					return false
				}
				if !nsSelector.Matches(labels.Set(ns.Labels)) {
					r.logger.V(1).Info("nsSelector does not match ns labels", "selector", nsSelector,
						"ns", ns)
					return false
				}
				return true
			} else {
				ns := &corev1.Namespace{}
				if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns); err != nil {
					r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
					return false
				}
				nsSelector, err := metav1.LabelSelectorAsSelector(ingressPeer.Namespaces)
				if err != nil {
					r.logger.Info("Unable to get namespace selector", "selector", ingressPeer, "err", err)
					return false
				}
				if !nsSelector.Matches(labels.Set(ns.Labels)) {
					r.logger.V(1).Info("nsSelector does not match ns labels", "selector", nsSelector,
						"ns", ns)
					return false
				}
				selectAll := metav1.LabelSelector{}
				if ingressPeer.Pods.PodSelector.String() == selectAll.String() {
					r.logger.V(1).Info("nsSelector matches ns labels", "selector", nsSelector,
						"ns", ns)
					return true
				}
				podSelector, err := metav1.LabelSelectorAsSelector(&ingressPeer.Pods.PodSelector)
				if err != nil {
					r.logger.Info("Unable to get pod selector", "err", err)
					return false
				}
				if podSelector.Matches(labels.Set(pod.Labels)) {
					return true
				}
			}
		} else {
			if egressPeer.Namespaces != nil {
				ns := &corev1.Namespace{}
				if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns); err != nil {
					r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
					return false
				}
				nsSelector, err := metav1.LabelSelectorAsSelector(egressPeer.Namespaces)
				if err != nil {
					r.logger.Info("Unable to get namespace selector", "selector", egressPeer, "err", err)
					return false
				}
				if !nsSelector.Matches(labels.Set(ns.Labels)) {
					r.logger.V(1).Info("nsSelector does not match ns labels", "selector", nsSelector,
						"ns", ns)
					return false
				}
				return true
			} else {
				ns := &corev1.Namespace{}
				if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns); err != nil {
					r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
					return false
				}
				nsSelector, err := metav1.LabelSelectorAsSelector(egressPeer.Namespaces)
				if err != nil {
					r.logger.Info("Unable to get namespace selector", "selector", egressPeer, "err", err)
					return false
				}
				if !nsSelector.Matches(labels.Set(ns.Labels)) {
					r.logger.V(1).Info("nsSelector does not match ns labels", "selector", nsSelector,
						"ns", ns)
					return false
				}
				selectAll := metav1.LabelSelector{}
				if egressPeer.Pods.PodSelector.String() == selectAll.String() {
					r.logger.V(1).Info("nsSelector matches ns labels", "selector", nsSelector,
						"ns", ns)
					return true
				}
				podSelector, err := metav1.LabelSelectorAsSelector(&egressPeer.Pods.PodSelector)
				if err != nil {
					r.logger.Info("Unable to get pod selector", "err", err)
					return false
				}
				if podSelector.Matches(labels.Set(pod.Labels)) {
					return true
				}
			}
		}

	} else {
		if peer.NamespaceSelector != nil {
			ns := &corev1.Namespace{}
			if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: pod.Namespace}, ns); err != nil {
				r.logger.Info("Unable to get namespace", "ns", pod.Namespace, "err", err)
				return false
			}
			nsSelector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
			if err != nil {
				r.logger.Info("Unable to get namespace selector", "selector", peer.NamespaceSelector, "err", err)
				return false
			}
			if !nsSelector.Matches(labels.Set(ns.Labels)) {
				r.logger.V(1).Info("nsSelector does not match ns labels", "selector", nsSelector,
					"ns", ns)
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
	}
	return false
}

func (r *defaultPolicyReferenceResolver) podSelectorNamespaces(ctx context.Context, adminpolicy *adminnetworking.AdminNetworkPolicy) ([]corev1.Namespace, error) {
	var nsSelector labels.Selector
	var err error
	if adminpolicy.Spec.Subject.Namespaces != nil {
		nsSelector, err = metav1.LabelSelectorAsSelector(adminpolicy.Spec.Subject.Namespaces)
		if err != nil {
			return nil, errors.Wrap(err, "unable to get admin namespace selector")
		}
	} else {
		nsSelector, err = metav1.LabelSelectorAsSelector(&adminpolicy.Spec.Subject.Pods.NamespaceSelector)
		if err != nil {
			return nil, errors.Wrap(err, "unable to get admin namespace selector")
		}
	}
	// All namespaces
	if nsSelector.String() == "" {
		return nil, nil
	}
	nsList := &corev1.NamespaceList{}
	if err := r.k8sClient.List(ctx, nsList, &client.ListOptions{
		LabelSelector: nsSelector,
	}); err != nil {
		r.logger.Info("Unable to List admin NS", "err", err)
		return nil, err
	}
	return nsList.Items, nil
}
