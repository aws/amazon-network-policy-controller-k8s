package resolvers

import (
	"context"

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
		if err := r.k8sClient.Get(ctx, policyRef, policy); err != nil {
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
