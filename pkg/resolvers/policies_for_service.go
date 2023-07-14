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
)

// getReferredPoliciesForService returns the list of policies that refer to the service.
func (r *defaultPolicyReferenceResolver) getReferredPoliciesForService(ctx context.Context, svc, svcOld *corev1.Service) ([]networking.NetworkPolicy, error) {
	if k8s.IsServiceHeadless(svc) {
		r.logger.V(1).Info("Ignoring headless service", "svc", k8s.NamespacedName(svc))
		return nil, nil
	}
	policiesWithEgressRules := r.policyTracker.GetPoliciesWithEgressRules()
	potentialMatches := sets.Set[types.NamespacedName]{}
	for pol := range policiesWithEgressRules {
		if pol.Namespace == svc.Namespace {
			potentialMatches.Insert(pol)
		}
	}
	namespacedPoliciesSet := r.policyTracker.GetPoliciesWithNamespaceReferences()
	potentialMatches = potentialMatches.Union(policiesWithEgressRules.Intersection(namespacedPoliciesSet))
	r.logger.V(1).Info("Potential matches", "policies", potentialMatches.UnsortedList(), "svc", k8s.NamespacedName(svc))
	var networkPolicyList []networking.NetworkPolicy
	for policyRef := range potentialMatches {
		r.logger.V(1).Info("Checking policy", "reference", policyRef)
		policy := &networking.NetworkPolicy{}
		if err := r.k8sClient.Get(ctx, policyRef, policy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, errors.Wrap(err, "failed to get policy")
			}
			r.logger.V(1).Info("Policy not found", "reference", policyRef)
			continue
		}
		if r.isServiceReferredOnEgress(ctx, svc, policy) {
			networkPolicyList = append(networkPolicyList, *policy)
			continue
		}
		if svcOld != nil && r.isServiceReferredOnEgress(ctx, svcOld, policy) {
			networkPolicyList = append(networkPolicyList, *policy)
		}

	}
	return networkPolicyList, nil
}

// isServiceReferredOnEgress returns true if the service is referred in the policy
func (r *defaultPolicyReferenceResolver) isServiceReferredOnEgress(ctx context.Context, svc *corev1.Service, policy *networking.NetworkPolicy) bool {
	for _, egressRule := range policy.Spec.Egress {
		for _, peer := range egressRule.To {
			r.logger.V(1).Info("Checking peer for service reference on egress", "peer", peer)
			if peer.PodSelector != nil || peer.NamespaceSelector != nil {
				if r.isServiceMatchLabelSelector(ctx, svc, &peer, policy.Namespace) {
					return true
				}
			}
		}
	}
	return false
}

// isServiceMatchLabelSelector returns true if the service is referred in the list of peers
func (r *defaultPolicyReferenceResolver) isServiceMatchLabelSelector(ctx context.Context, svc *corev1.Service, peer *networking.NetworkPolicyPeer, policyNamespace string) bool {
	if peer.NamespaceSelector != nil {
		ns := &corev1.Namespace{}
		if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: svc.Namespace}, ns); err != nil {
			r.logger.Info("Failed to get namespace", "namespace", svc.Namespace, "err", err)
			return false
		}
		nsSelector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			r.logger.Info("Failed to convert namespace selector to selector", "namespace", peer.NamespaceSelector, "err", err)
			return false
		}
		if !nsSelector.Matches(labels.Set(ns.Labels)) {
			return false
		}
		if peer.PodSelector == nil {
			return true
		}
	} else if svc.Namespace != policyNamespace {
		r.logger.V(1).Info("Svc and policy namespace does not match", "namespace", svc.Namespace)
		return false
	}
	if svc.Spec.Selector == nil {
		r.logger.V(1).Info("Ignoring service without selector", "service", k8s.NamespacedName(svc))
		return false
	}
	svcSelector, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
	if err != nil {
		r.logger.Info("Failed to convert pod selector to selector", "podSelector", peer.PodSelector, "err", err)
		return false
	}
	if svcSelector.Matches(labels.Set(svc.Spec.Selector)) {
		return true
	}
	return false
}
