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
	// GetNamespaceSelectorPeers returns the policy's ingress/egress peers that carry a namespaceSelector, with
	// both selectors parsed once at tracking time. ok is false when the policy is not tracked with namespace
	// references (callers then fall back to reading the policy). The slice is shared and must not be mutated.
	GetNamespaceSelectorPeers(ref types.NamespacedName) (peers []PeerSelectors, ok bool)
	// GetANPNamespaceSelectorPeers is GetNamespaceSelectorPeers for ApplicationNetworkPolicies.
	GetANPNamespaceSelectorPeers(ref types.NamespacedName) (peers []PeerSelectors, ok bool)
}

// PeerSelectors is one namespaceSelector peer of a tracked policy. Namespace is never nil; Pod is nil when the
// peer has no podSelector, which matches every pod in a matching namespace.
type PeerSelectors struct {
	Namespace labels.Selector
	Pod       labels.Selector
}

func NewPolicyTracker(logger logr.Logger) PolicyTracker {
	return &defaultPolicyTracker{
		logger: logger,
	}
}

var _ PolicyTracker = (*defaultPolicyTracker)(nil)

type defaultPolicyTracker struct {
	logger              logr.Logger
	namespacedPolicies  sync.Map
	peerSelectors       sync.Map // types.NamespacedName -> []PeerSelectors, for namespacedPolicies only
	egressRulesPolicies sync.Map
	namespacedANP       sync.Map
	peerSelectorsANP    sync.Map // types.NamespacedName -> []PeerSelectors, for namespacedANP only
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
		// Parse the peer selectors here, once per policy change, so the pod event path never re-reads the policy
		// or re-parses a selector. Store the peers before the reference so a reader never sees the reference
		// without them.
		t.peerSelectorsANP.Store(k8s.NamespacedName(policy), t.parseANPNamespaceSelectorPeers(policy))
		t.namespacedANP.Store(k8s.NamespacedName(policy), true)
	} else {
		t.logger.V(1).Info("ANP no ns references, remove tracking", "policy", k8s.NamespacedName(policy))
		t.namespacedANP.Delete(k8s.NamespacedName(policy))
		t.peerSelectorsANP.Delete(k8s.NamespacedName(policy))
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
	t.peerSelectorsANP.Delete(k8s.NamespacedName(policy))
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

// GetANPNamespaceSelectorPeers implements PolicyTracker.
func (t *defaultPolicyTracker) GetANPNamespaceSelectorPeers(ref types.NamespacedName) ([]PeerSelectors, bool) {
	v, ok := t.peerSelectorsANP.Load(ref)
	if !ok {
		return nil, false
	}
	return v.([]PeerSelectors), true
}

// parseANPNamespaceSelectorPeers is parseNamespaceSelectorPeers for an ApplicationNetworkPolicy: its ingress
// peers are NetworkPolicyPeers, its egress peers carry the same two selectors on ApplicationNetworkPolicyPeer.
func (t *defaultPolicyTracker) parseANPNamespaceSelectorPeers(policy *policyinfo.ApplicationNetworkPolicy) []PeerSelectors {
	var out []PeerSelectors
	add := func(nsSelector, podSelector *metav1.LabelSelector) {
		if ps, ok := t.parsePeerSelectors(k8s.NamespacedName(policy), nsSelector, podSelector); ok {
			out = append(out, ps)
		}
	}
	for i := range policy.Spec.Ingress {
		for j := range policy.Spec.Ingress[i].From {
			add(policy.Spec.Ingress[i].From[j].NamespaceSelector, policy.Spec.Ingress[i].From[j].PodSelector)
		}
	}
	for i := range policy.Spec.Egress {
		for j := range policy.Spec.Egress[i].To {
			add(policy.Spec.Egress[i].To[j].NamespaceSelector, policy.Spec.Egress[i].To[j].PodSelector)
		}
	}
	return out
}

// UpdatePolicy updates the policy tracker with the given policy
func (t *defaultPolicyTracker) UpdatePolicy(policy *networking.NetworkPolicy) {
	if t.containsNamespaceReference(policy) {
		t.logger.V(1).Info("policy contains ns references", "policy", k8s.NamespacedName(policy))
		// Parse the peer selectors here, once per policy change, so the pod event handler matches against
		// parsed selectors instead of re-parsing every peer of every tracked policy on every pod event.
		t.peerSelectors.Store(k8s.NamespacedName(policy), t.parseNamespaceSelectorPeers(policy))
		t.namespacedPolicies.Store(k8s.NamespacedName(policy), true)
	} else {
		t.logger.V(1).Info("no ns references, remove tracking", "policy", k8s.NamespacedName(policy))
		t.namespacedPolicies.Delete(k8s.NamespacedName(policy))
		t.peerSelectors.Delete(k8s.NamespacedName(policy))
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
	t.peerSelectors.Delete(k8s.NamespacedName(policy))
	t.egressRulesPolicies.Delete(k8s.NamespacedName(policy))
}

// GetNamespaceSelectorPeers implements PolicyTracker.
func (t *defaultPolicyTracker) GetNamespaceSelectorPeers(ref types.NamespacedName) ([]PeerSelectors, bool) {
	v, ok := t.peerSelectors.Load(ref)
	if !ok {
		return nil, false
	}
	return v.([]PeerSelectors), true
}

// parseNamespaceSelectorPeers returns the peers of the policy that carry a namespaceSelector, selectors parsed.
// A peer whose selector does not parse is dropped, which is what the per-event matcher did for it (never a match).
func (t *defaultPolicyTracker) parseNamespaceSelectorPeers(policy *networking.NetworkPolicy) []PeerSelectors {
	var out []PeerSelectors
	add := func(peer *networking.NetworkPolicyPeer) {
		if ps, ok := t.parsePeerSelectors(k8s.NamespacedName(policy), peer.NamespaceSelector, peer.PodSelector); ok {
			out = append(out, ps)
		}
	}
	for i := range policy.Spec.Ingress {
		for j := range policy.Spec.Ingress[i].From {
			add(&policy.Spec.Ingress[i].From[j])
		}
	}
	for i := range policy.Spec.Egress {
		for j := range policy.Spec.Egress[i].To {
			add(&policy.Spec.Egress[i].To[j])
		}
	}
	return out
}

// parsePeerSelectors parses one peer's selectors. ok is false for a peer without a namespaceSelector (not a
// cross-namespace peer) and for a selector that does not parse (never a match, as before).
func (t *defaultPolicyTracker) parsePeerSelectors(policy types.NamespacedName, nsSelector, podSelector *metav1.LabelSelector) (PeerSelectors, bool) {
	if nsSelector == nil {
		return PeerSelectors{}, false
	}
	nsSel, err := metav1.LabelSelectorAsSelector(nsSelector)
	if err != nil {
		t.logger.Info("Unable to get namespace selector", "policy", policy, "selector", nsSelector, "err", err)
		return PeerSelectors{}, false
	}
	ps := PeerSelectors{Namespace: nsSel}
	if podSelector != nil {
		podSel, err := metav1.LabelSelectorAsSelector(podSelector)
		if err != nil {
			t.logger.Info("Unable to get pod selector", "policy", policy, "selector", podSelector, "err", err)
			return PeerSelectors{}, false
		}
		ps.Pod = podSel
	}
	return ps, true
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
