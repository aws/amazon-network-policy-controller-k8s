package resolvers

import (
	"sync"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/go-logr/logr"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

type PolicyTracker interface {
	UpdatePolicy(policy *networking.NetworkPolicy)
	RemovePolicy(policy *networking.NetworkPolicy)
	GetPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName]
	GetPoliciesWithEgressRules() sets.Set[types.NamespacedName]
}

func NewPolicyTracker(logger logr.Logger) PolicyTracker {
	return &defaultPolicyTracker{}
}

var _ PolicyTracker = (*defaultPolicyTracker)(nil)

type defaultPolicyTracker struct {
	logger              logr.Logger
	namespacedPolicies  sync.Map
	egressRulesPolicies sync.Map
}

// UpdatePolicy updates the policy tracker with the given policy
func (t *defaultPolicyTracker) UpdatePolicy(policy *networking.NetworkPolicy) {
	if t.containsNamespaceReference(policy) {
		t.logger.V(1).Info("policy contains ns references", "policy", k8s.NamespacedName(policy))
		t.namespacedPolicies.Store(k8s.NamespacedName(policy), true)
	} else {
		t.logger.V(1).Info("no ns references, remove tracking", "policy", k8s.NamespacedName(policy))
		t.namespacedPolicies.Delete(k8s.NamespacedName(policy))
	}
	if t.containsEgressRules(policy) {
		t.logger.V(1).Info("policy contains egress rules", "policy", k8s.NamespacedName(policy))
		t.egressRulesPolicies.Store(k8s.NamespacedName(policy), true)
	} else {
		t.logger.V(1).Info("no egress rules, remove tracking", "policy", k8s.NamespacedName(policy))
		t.egressRulesPolicies.Delete(k8s.NamespacedName(policy))
	}
}

// RemovePolicy removes the given policy from the policy tracker during deletion
func (t *defaultPolicyTracker) RemovePolicy(policy *networking.NetworkPolicy) {
	t.logger.V(1).Info("remove from tracking", "policy", k8s.NamespacedName(policy))
	t.namespacedPolicies.Delete(k8s.NamespacedName(policy))
	t.egressRulesPolicies.Delete(k8s.NamespacedName(policy))
}

// GetPoliciesWithNamespaceReferences returns the set of policies that have namespace references in the ingress/egress rules
func (t *defaultPolicyTracker) GetPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName] {
	policies := sets.Set[types.NamespacedName]{}
	t.namespacedPolicies.Range(func(k, _ interface{}) bool {
		policies.Insert(k.(types.NamespacedName))
		return true
	})
	return policies
}

// GetPoliciesWithEgressRules returns the set of policies that have egress rules
func (t *defaultPolicyTracker) GetPoliciesWithEgressRules() sets.Set[types.NamespacedName] {
	policies := sets.Set[types.NamespacedName]{}
	t.egressRulesPolicies.Range(func(k, _ interface{}) bool {
		policies.Insert(k.(types.NamespacedName))
		return true
	})
	return policies
}

func (t *defaultPolicyTracker) containsNamespaceReference(policy *networking.NetworkPolicy) bool {
	for _, ingRule := range policy.Spec.Ingress {
		for _, peer := range ingRule.From {
			if peer.NamespaceSelector != nil {
				return true
			}
		}
	}
	for _, egrRule := range policy.Spec.Egress {
		for _, peer := range egrRule.To {
			if peer.NamespaceSelector != nil {
				return true
			}
		}
	}
	return false
}

func (t *defaultPolicyTracker) containsEgressRules(policy *networking.NetworkPolicy) bool {
	return len(policy.Spec.Egress) > 0
}
