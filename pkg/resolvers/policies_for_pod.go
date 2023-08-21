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

func (r *defaultPolicyReferenceResolver) getReferredPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
	policyList := &networking.NetworkPolicyList{}
	if err := r.k8sClient.List(ctx, policyList, client.InNamespace(pod.Namespace)); err != nil {
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

	for _, ref := range r.policyTracker.GetPoliciesWithNamespaceReferences().UnsortedList() {
		r.logger.V(1).Info("Policy containing namespace selectors", "ref", ref)
		if processedPolicies.Has(ref) {
			continue
		}
		policy := &networking.NetworkPolicy{}
		if err := r.k8sClient.Get(ctx, ref, policy); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, errors.Wrap(err, "failed to get policy")
			}
			r.logger.V(1).Info("Policy not found", "reference", ref)
			continue
		}

		if r.isPodReferredOnIngressEgress(ctx, pod, policy) {
			referredPolicies = append(referredPolicies, *policy)
			processedPolicies.Insert(k8s.NamespacedName(policy))
			continue
		}
		if podOld != nil && r.isPodReferredOnIngressEgress(ctx, podOld, policy) {
			referredPolicies = append(referredPolicies, *policy)
			processedPolicies.Insert(k8s.NamespacedName(policy))
		}
	}

	r.logger.V(1).Info("All referred policies", "pod", k8s.NamespacedName(pod), "policies", referredPolicies)
	return referredPolicies, nil
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

func (r *defaultPolicyReferenceResolver) isPodLabelMatchPeer(ctx context.Context, pod *corev1.Pod, peer *networking.NetworkPolicyPeer, policyNamespace string) bool {
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
	return false
}
