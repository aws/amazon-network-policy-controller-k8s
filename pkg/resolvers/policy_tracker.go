package resolvers

import (
	"sync"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
	"github.com/go-logr/logr"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PolicyTracker interface {
	UpdatePolicy(policy *networking.NetworkPolicy)
	RemovePolicy(policy *networking.NetworkPolicy)
	UpdateGenericPolicy(obj client.Object)
	RemoveGenericPolicy(obj client.Object)
	GetPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName]
	GetPoliciesWithEgressRules() sets.Set[types.NamespacedName]
	GetApplicationNetworkPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName]
	GetApplicationNetworkPoliciesWithEgressRules() sets.Set[types.NamespacedName]
	// GetPolicyNamespaceSelectorPeers returns the namespaceSelector peers of a NetworkPolicy tracked with namespace
	// references, parsed when the policy was last updated. ok is false if the policy is not tracked. Callers must not
	// modify the returned slice.
	GetPolicyNamespaceSelectorPeers(ref types.NamespacedName) (peers []NamespaceSelectorPeer, ok bool)
	// GetApplicationNetworkPolicyNamespaceSelectorPeers is GetPolicyNamespaceSelectorPeers for an ANP.
	GetApplicationNetworkPolicyNamespaceSelectorPeers(ref types.NamespacedName) (peers []NamespaceSelectorPeer, ok bool)
}

// NamespaceSelectorPeer is a policy peer with a namespaceSelector, with its selectors parsed. Pod is nil when the peer
// has no podSelector, in which case it selects every pod in the namespaces it selects.
type NamespaceSelectorPeer struct {
	Namespace labels.Selector
	Pod       labels.Selector
}

// Matches reports whether the peer selects a pod with podLabels in a namespace with nsLabels.
func (p NamespaceSelectorPeer) Matches(nsLabels, podLabels labels.Set) bool {
	return p.Namespace.Matches(nsLabels) && (p.Pod == nil || p.Pod.Matches(podLabels))
}

func NewPolicyTracker(logger logr.Logger) PolicyTracker {
	return &defaultPolicyTracker{
		logger: logger,
	}
}

var _ PolicyTracker = (*defaultPolicyTracker)(nil)

type defaultPolicyTracker struct {
	logger logr.Logger
	// namespacedPolicies and namespacedANP map each policy with namespace references to its parsed
	// []NamespaceSelectorPeer, so that the pod event path does not read or parse the policy.
	namespacedPolicies  sync.Map
	egressRulesPolicies sync.Map
	namespacedANP       sync.Map
	egressRulesANP      sync.Map
	namespacedCNP       sync.Map
	egressRulesCNP      sync.Map
}

// TODO: consolidation the tracker for NP, ANP and CNP
// UpdateGenericPolicy handles NetworkPolicy, ApplicationNetworkPolicy, and ClusterNetworkPolicy
func (t *defaultPolicyTracker) UpdateGenericPolicy(obj client.Object) {
	switch policy := obj.(type) {
	case *networking.NetworkPolicy:
		t.UpdatePolicy(policy)
	case *policyinfo.ApplicationNetworkPolicy:
		t.updateANP(policy)
	case *policyinfo.ClusterNetworkPolicy:
		t.updateCNP(policy)
	}
}

// RemoveGenericPolicy handles NetworkPolicy, ApplicationNetworkPolicy, and ClusterNetworkPolicy
func (t *defaultPolicyTracker) RemoveGenericPolicy(obj client.Object) {
	switch policy := obj.(type) {
	case *networking.NetworkPolicy:
		t.RemovePolicy(policy)
	case *policyinfo.ApplicationNetworkPolicy:
		t.removeANP(policy)
	case *policyinfo.ClusterNetworkPolicy:
		t.removeCNP(policy)
	}
}

func (t *defaultPolicyTracker) updateANP(policy *policyinfo.ApplicationNetworkPolicy) {
	if t.containsANPNamespaceReference(policy) {
		t.logger.V(1).Info("ANP contains ns references", "policy", k8s.NamespacedName(policy))
		t.namespacedANP.Store(k8s.NamespacedName(policy), t.anpNamespaceSelectorPeers(policy))
	} else {
		t.logger.V(1).Info("ANP no ns references, remove tracking", "policy", k8s.NamespacedName(policy))
		t.namespacedANP.Delete(k8s.NamespacedName(policy))
	}
	if t.containsANPEgressRules(policy) {
		t.logger.V(1).Info("ANP contains egress rules", "policy", k8s.NamespacedName(policy))
		t.egressRulesANP.Store(k8s.NamespacedName(policy), true)
	} else {
		t.logger.V(1).Info("ANP no egress rules, remove tracking", "policy", k8s.NamespacedName(policy))
		t.egressRulesANP.Delete(k8s.NamespacedName(policy))
	}
}

func (t *defaultPolicyTracker) removeANP(policy *policyinfo.ApplicationNetworkPolicy) {
	t.logger.V(1).Info("remove ANP from tracking", "policy", k8s.NamespacedName(policy))
	t.namespacedANP.Delete(k8s.NamespacedName(policy))
	t.egressRulesANP.Delete(k8s.NamespacedName(policy))
}

func (t *defaultPolicyTracker) containsANPNamespaceReference(policy *policyinfo.ApplicationNetworkPolicy) bool {
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

func (t *defaultPolicyTracker) containsANPEgressRules(policy *policyinfo.ApplicationNetworkPolicy) bool {
	return len(policy.Spec.Egress) > 0
}

// anpNamespaceSelectorPeers parses the namespaceSelector peers of an ANP.
func (t *defaultPolicyTracker) anpNamespaceSelectorPeers(
	policy *policyinfo.ApplicationNetworkPolicy) []NamespaceSelectorPeer {
	var peers []NamespaceSelectorPeer
	for _, ingRule := range policy.Spec.Ingress {
		for _, peer := range ingRule.From {
			peers = t.appendNamespaceSelectorPeer(peers, policy, peer.NamespaceSelector, peer.PodSelector)
		}
	}
	for _, egrRule := range policy.Spec.Egress {
		for _, peer := range egrRule.To {
			peers = t.appendNamespaceSelectorPeer(peers, policy, peer.NamespaceSelector, peer.PodSelector)
		}
	}
	return peers
}

// UpdatePolicy updates the policy tracker with the given policy
func (t *defaultPolicyTracker) UpdatePolicy(policy *networking.NetworkPolicy) {
	if t.containsNamespaceReference(policy) {
		t.logger.V(1).Info("policy contains ns references", "policy", k8s.NamespacedName(policy))
		t.namespacedPolicies.Store(k8s.NamespacedName(policy), t.namespaceSelectorPeers(policy))
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

// GetPolicyNamespaceSelectorPeers implements PolicyTracker.
func (t *defaultPolicyTracker) GetPolicyNamespaceSelectorPeers(
	ref types.NamespacedName) ([]NamespaceSelectorPeer, bool) {
	return loadNamespaceSelectorPeers(&t.namespacedPolicies, ref)
}

// GetApplicationNetworkPolicyNamespaceSelectorPeers implements PolicyTracker.
func (t *defaultPolicyTracker) GetApplicationNetworkPolicyNamespaceSelectorPeers(
	ref types.NamespacedName) ([]NamespaceSelectorPeer, bool) {
	return loadNamespaceSelectorPeers(&t.namespacedANP, ref)
}

func loadNamespaceSelectorPeers(m *sync.Map, ref types.NamespacedName) ([]NamespaceSelectorPeer, bool) {
	v, ok := m.Load(ref)
	if !ok {
		return nil, false
	}
	peers, ok := v.([]NamespaceSelectorPeer)
	return peers, ok
}

// namespaceSelectorPeers parses the namespaceSelector peers of a NetworkPolicy.
func (t *defaultPolicyTracker) namespaceSelectorPeers(policy *networking.NetworkPolicy) []NamespaceSelectorPeer {
	var peers []NamespaceSelectorPeer
	for _, ingRule := range policy.Spec.Ingress {
		for _, peer := range ingRule.From {
			peers = t.appendNamespaceSelectorPeer(peers, policy, peer.NamespaceSelector, peer.PodSelector)
		}
	}
	for _, egrRule := range policy.Spec.Egress {
		for _, peer := range egrRule.To {
			peers = t.appendNamespaceSelectorPeer(peers, policy, peer.NamespaceSelector, peer.PodSelector)
		}
	}
	return peers
}

// appendNamespaceSelectorPeer appends the peer with the given selectors if it has a namespaceSelector. A peer whose
// selectors do not parse is left out, since it cannot select any pod.
func (t *defaultPolicyTracker) appendNamespaceSelectorPeer(peers []NamespaceSelectorPeer, policy client.Object,
	nsSelector, podSelector *metav1.LabelSelector) []NamespaceSelectorPeer {
	if nsSelector == nil {
		return peers
	}
	ns, err := metav1.LabelSelectorAsSelector(nsSelector)
	if err != nil {
		t.logger.Info("Unable to get namespace selector", "policy", k8s.NamespacedName(policy), "selector", nsSelector,
			"err", err)
		return peers
	}
	peer := NamespaceSelectorPeer{Namespace: ns}
	if podSelector != nil {
		if peer.Pod, err = metav1.LabelSelectorAsSelector(podSelector); err != nil {
			t.logger.Info("Unable to get pod selector", "policy", k8s.NamespacedName(policy), "selector", podSelector,
				"err", err)
			return peers
		}
	}
	return append(peers, peer)
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

// GetApplicationNetworkPoliciesWithNamespaceReferences returns the set of ANPs that have namespace references
func (t *defaultPolicyTracker) GetApplicationNetworkPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName] {
	policies := sets.Set[types.NamespacedName]{}
	t.namespacedANP.Range(func(k, _ interface{}) bool {
		policies.Insert(k.(types.NamespacedName))
		return true
	})
	return policies
}

// GetApplicationNetworkPoliciesWithEgressRules returns the set of ANPs that have egress rules
func (t *defaultPolicyTracker) GetApplicationNetworkPoliciesWithEgressRules() sets.Set[types.NamespacedName] {
	policies := sets.Set[types.NamespacedName]{}
	t.egressRulesANP.Range(func(k, _ interface{}) bool {
		policies.Insert(k.(types.NamespacedName))
		return true
	})
	return policies
}

func (t *defaultPolicyTracker) updateCNP(policy *policyinfo.ClusterNetworkPolicy) {
	policyName := types.NamespacedName{Name: policy.Name}
	// CNP is cluster-wide, always assume it has namespace references
	t.logger.V(1).Info("CNP is cluster-wide, tracking for ns references", "policy", policyName)
	t.namespacedCNP.Store(policyName, true)

	if t.containsCNPEgressRules(policy) {
		t.logger.V(1).Info("CNP contains egress rules", "policy", policyName)
		t.egressRulesCNP.Store(policyName, true)
	} else {
		t.logger.V(1).Info("CNP no egress rules, remove tracking", "policy", policyName)
		t.egressRulesCNP.Delete(policyName)
	}
}

func (t *defaultPolicyTracker) removeCNP(policy *policyinfo.ClusterNetworkPolicy) {
	policyName := types.NamespacedName{Name: policy.Name}
	t.logger.V(1).Info("remove CNP from tracking", "policy", policyName)
	t.namespacedCNP.Delete(policyName)
	t.egressRulesCNP.Delete(policyName)
}

func (t *defaultPolicyTracker) containsCNPEgressRules(policy *policyinfo.ClusterNetworkPolicy) bool {
	return len(policy.Spec.Egress) > 0
}
