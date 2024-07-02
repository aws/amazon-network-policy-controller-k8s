package resolvers

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	adminnetworking "sigs.k8s.io/network-policy-api/apis/v1alpha1"
)

func (r *defaultPolicyReferenceResolver) getReferredPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error) {
	var referredPolicies []networking.NetworkPolicy
	var referredAdminPolicies []adminnetworking.AdminNetworkPolicy
	for _, policyRef := range r.policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList() {
		policy := &networking.NetworkPolicy{}
		adminPolicy := &adminnetworking.AdminNetworkPolicy{}
		policyTracked := true
		adminPolicyTracked := true
		if err := r.k8sClient.Get(ctx, policyRef, policy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, nil, errors.Wrap(err, "failed to get policies")
			}
			policyTracked = false
		}
		if err := r.k8sClient.Get(ctx, policyRef, adminPolicy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, nil, errors.Wrap(err, "failed to get policies")
			}
			adminPolicyTracked = false
		}
		if !adminPolicyTracked && !policyTracked {
			r.logger.Info("Tracked policy or admin policy not found", "reference", policyRef)
			continue
		}
		if r.isNamespaceReferredInPolicy(ns, policy, adminPolicy, adminPolicyTracked) {
			if !adminPolicyTracked {
				referredPolicies = append(referredPolicies, *policy)
			} else {
				referredAdminPolicies = append(referredAdminPolicies, *adminPolicy)
			}
			continue
		}
		if nsOld != nil && r.isNamespaceReferredInPolicy(nsOld, policy, adminPolicy, adminPolicyTracked) {
			if !adminPolicyTracked {
				referredPolicies = append(referredPolicies, *policy)
			} else {
				referredAdminPolicies = append(referredAdminPolicies, *adminPolicy)
			}
		}
	}

	return referredPolicies, referredAdminPolicies, nil
}

func (r *defaultPolicyReferenceResolver) isNamespaceReferredInPolicy(ns *corev1.Namespace, policy *networking.NetworkPolicy, adminPolicy *adminnetworking.AdminNetworkPolicy, isAdmin bool) bool {

	if !isAdmin {
		for _, ingRule := range policy.Spec.Ingress {
			for _, peer := range ingRule.From {
				if r.isNameSpaceLabelMatchPeer(ns, &peer, nil, nil, isAdmin) {
					return true
				}
			}
		}
		for _, egrRule := range policy.Spec.Egress {
			for _, peer := range egrRule.To {
				if r.isNameSpaceLabelMatchPeer(ns, &peer, nil, nil, isAdmin) {
					return true
				}
			}
		}
	} else {
		for _, ingRule := range adminPolicy.Spec.Ingress {
			for _, peer := range ingRule.From {
				if r.isNameSpaceLabelMatchPeer(ns, nil, &peer, nil, isAdmin) {
					return true
				}
			}
		}

		for _, ingRule := range adminPolicy.Spec.Egress {
			for _, peer := range ingRule.To {
				if r.isNameSpaceLabelMatchPeer(ns, nil, nil, &peer, isAdmin) {
					return true
				}
			}
		}
	}
	return false
}

func (r *defaultPolicyReferenceResolver) isNameSpaceLabelMatchPeer(ns *corev1.Namespace, peer *networking.NetworkPolicyPeer, ingressPeer *adminnetworking.AdminNetworkPolicyIngressPeer, egressPeer *adminnetworking.AdminNetworkPolicyEgressPeer, isAdmin bool) bool {
	if !isAdmin {
		if peer.NamespaceSelector == nil {
			return false
		}
		nsSelector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			r.logger.Error(err, "unable to get namespace selector")
			return false
		}
		if nsSelector.Matches(labels.Set(ns.Labels)) {
			return true
		}
	} else {
		if ingressPeer != nil {
			var ls *metav1.LabelSelector
			if ingressPeer.Namespaces != nil {
				ls = ingressPeer.Namespaces
			} else {
				ls = &ingressPeer.Pods.NamespaceSelector
			}
			nsSelector, err := metav1.LabelSelectorAsSelector(ls)
			if err != nil {
				r.logger.Error(err, "unable to get namespace selector")
				return false
			}
			if nsSelector.Matches(labels.Set(ns.Labels)) {
				return true
			}
		} else {
			var ls *metav1.LabelSelector
			if egressPeer.Namespaces != nil {
				ls = egressPeer.Namespaces
			} else {
				ls = &egressPeer.Pods.NamespaceSelector
			}
			nsSelector, err := metav1.LabelSelectorAsSelector(ls)
			if err != nil {
				r.logger.Error(err, "unable to get namespace selector")
				return false
			}
			if nsSelector.Matches(labels.Set(ns.Labels)) {
				return true
			}
		}
	}
	return false
}
