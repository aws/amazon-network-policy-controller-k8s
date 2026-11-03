package resolvers

import (
	"context"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *defaultPolicyReferenceResolver) getReferredPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, error) {
	var referredPolicies []networking.NetworkPolicy
	for _, policyRef := range r.policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList() {
		policy := &networking.NetworkPolicy{}
		if err := r.k8sClient.Get(ctx, policyRef, policy, client.UnsafeDisableDeepCopy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, errors.Wrap(err, "failed to get policies")
			}
			r.logger.Info("Tracked policy not found", "reference", policyRef)
			continue
		}
		if r.isNamespaceReferredInPolicy(ns, policy) {
			referredPolicies = append(referredPolicies, *policy)
			continue
		}
		if nsOld != nil && r.isNamespaceReferredInPolicy(nsOld, policy) {
			referredPolicies = append(referredPolicies, *policy)
		}
	}

	return referredPolicies, nil
}

func (r *defaultPolicyReferenceResolver) isNamespaceReferredInPolicy(ns *corev1.Namespace, policy *networking.NetworkPolicy) bool {
	for _, ingRule := range policy.Spec.Ingress {
		for _, peer := range ingRule.From {
			if r.isNameSpaceLabelMatchPeer(ns, &peer) {
				return true
			}
		}
	}
	for _, egrRule := range policy.Spec.Egress {
		for _, peer := range egrRule.To {
			if r.isNameSpaceLabelMatchPeer(ns, &peer) {
				return true
			}
		}
	}
	return false
}

// TODO: Reduce duplication between NetworkPolicy and ANP peer matching functions using generics
func (r *defaultPolicyReferenceResolver) isNameSpaceLabelMatchPeer(ns *corev1.Namespace, peer *networking.NetworkPolicyPeer) bool {
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
	return false
}
func (r *defaultPolicyReferenceResolver) getReferredApplicationNetworkPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]policyinfo.ApplicationNetworkPolicy, error) {
	var referredPolicies []policyinfo.ApplicationNetworkPolicy
	for _, policyRef := range r.policyTracker.GetApplicationNetworkPoliciesWithNamespaceReferences().UnsortedList() {
		policy := &policyinfo.ApplicationNetworkPolicy{}
		if err := r.k8sClient.Get(ctx, policyRef, policy, client.UnsafeDisableDeepCopy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, errors.Wrap(err, "failed to get ANP policies")
			}
			r.logger.Info("Tracked ANP policy not found", "reference", policyRef)
			continue
		}
		if r.isNamespaceReferredInANPPolicy(ns, policy) {
			referredPolicies = append(referredPolicies, *policy)
			continue
		}
		if nsOld != nil && r.isNamespaceReferredInANPPolicy(nsOld, policy) {
			referredPolicies = append(referredPolicies, *policy)
		}
	}

	return referredPolicies, nil
}

func (r *defaultPolicyReferenceResolver) isNamespaceReferredInANPPolicy(ns *corev1.Namespace, policy *policyinfo.ApplicationNetworkPolicy) bool {
	for _, ingRule := range policy.Spec.Ingress {
		for _, peer := range ingRule.From {
			if r.isNameSpaceLabelMatchPeer(ns, &peer) {
				return true
			}
		}
	}
	for _, egrRule := range policy.Spec.Egress {
		for _, peer := range egrRule.To {
			if r.isNameSpaceLabelMatchApplicationNetworkPolicyPeer(ns, &peer) {
				return true
			}
		}
	}
	return false
}

func (r *defaultPolicyReferenceResolver) isNameSpaceLabelMatchApplicationNetworkPolicyPeer(ns *corev1.Namespace, peer *policyinfo.ApplicationNetworkPolicyPeer) bool {
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
	return false
}
