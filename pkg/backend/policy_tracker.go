package backend

import (
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
}

func (t *defaultPolicyTracker) UpdatePolicy(policy *networking.NetworkPolicy) {
	return
}

func (t *defaultPolicyTracker) RemovePolicy(policy *networking.NetworkPolicy) {
	return
}

func (t *defaultPolicyTracker) GetPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName] {
	return nil
}

func (t *defaultPolicyTracker) GetPoliciesWithEgressRules() sets.Set[types.NamespacedName] {
	return nil
}
