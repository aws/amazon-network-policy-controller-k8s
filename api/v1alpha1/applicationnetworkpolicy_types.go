/*
Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ApplicationNetworkPolicySpec defines the desired state of ApplicationNetworkPolicy
type ApplicationNetworkPolicySpec struct {
	// PodSelector selects the pods to which this ApplicationNetworkPolicy object applies.
	PodSelector metav1.LabelSelector `json:"podSelector"`

	// PolicyTypes is a list of rule types that the ApplicationNetworkPolicy relates to.
	// Valid options are ["Ingress"], ["Egress"], or ["Ingress", "Egress"].
	// If this field is not specified, it will default based on the existence of ingress or egress rules.
	// +optional
	PolicyTypes []networking.PolicyType `json:"policyTypes,omitempty"`

	// Ingress is a list of ingress rules to be applied to the selected pods.
	// Traffic is allowed to a pod if there are no ApplicationNetworkPolicies selecting the pod
	// (and cluster policy otherwise allows the traffic), OR if the traffic source is
	// the pod's local node, OR if the traffic matches at least one ingress rule
	// across all of the ApplicationNetworkPolicy objects whose podSelector matches the pod.
	// +optional
	Ingress []networking.NetworkPolicyIngressRule `json:"ingress,omitempty"`

	// Egress is a list of egress rules to be applied to the selected pods. Outgoing traffic
	// is allowed if there are no ApplicationNetworkPolicies selecting the pod (and cluster policy
	// otherwise allows the traffic), OR if the traffic matches at least one egress rule
	// across all of the ApplicationNetworkPolicy objects whose podSelector matches the pod.
	// +optional
	Egress []ApplicationNetworkPolicyEgressRule `json:"egress,omitempty"`
}

// DomainName describes one or more domain names to be used as a peer.
//
// DomainName can be an exact match, or use the wildcard specifier '*' to match
// one or more labels.
//
// '*', the wildcard specifier, matches one or more entire labels. It does not
// support partial matches. '*' may only be specified as a prefix.
//
//	Examples:
//	  - `kubernetes.io` matches only `kubernetes.io`.
//	    It does not match "www.kubernetes.io", "blog.kubernetes.io",
//	    "my-kubernetes.io", or "wikipedia.org".
//	  - `blog.kubernetes.io` matches only "blog.kubernetes.io".
//	    It does not match "www.kubernetes.io" or "kubernetes.io".
//	  - `*.kubernetes.io` matches subdomains of kubernetes.io.
//	    "www.kubernetes.io", "blog.kubernetes.io", and
//	    "latest.blog.kubernetes.io" match, however "kubernetes.io", and
//	    "wikipedia.org" do not.
//
// +kubebuilder:validation:Pattern=`^(\*\.)?([a-zA-Z0-9]([-a-zA-Z0-9_]*[a-zA-Z0-9])?\.)+[a-zA-Z0-9]([-a-zA-Z0-9_]*[a-zA-Z0-9])?\.?$`
type DomainName string

// ApplicationNetworkPolicyPeer describes a peer to allow traffic to/from.
// Only certain combinations of fields are allowed
// +kubebuilder:validation:XValidation:rule="!(has(self.ipBlock) && has(self.domainNames))",message="ipBlock and domainNames are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.podSelector) && has(self.domainNames))",message="podSelector and domainNames are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.namespaceSelector) && has(self.domainNames))",message="namespaceSelector and domainNames are mutually exclusive"
type ApplicationNetworkPolicyPeer struct {
	// PodSelector is a label selector which selects pods. This field follows standard label
	// selector semantics; if present but empty, it selects all pods.
	//
	// If namespaceSelector is also set, then the NetworkPolicyPeer as a whole selects
	// the pods matching podSelector in the Namespaces selected by NamespaceSelector.
	// Otherwise it selects the pods matching podSelector in the policy's own namespace.
	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`

	// NamespaceSelector selects namespaces using cluster-scoped labels. This field follows
	// standard label selector semantics; if present but empty, it selects all namespaces.
	//
	// If podSelector is also set, then the NetworkPolicyPeer as a whole selects
	// the pods matching podSelector in the namespaces selected by namespaceSelector.
	// Otherwise it selects all pods in the namespaces selected by namespaceSelector.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// IPBlock defines policy on a particular IPBlock. If this field is set then
	// neither of the other fields can be.
	// +optional
	IPBlock *networking.IPBlock `json:"ipBlock,omitempty"`

	// DomainNames provides a way to specify domain names as peers.
	//
	// DomainNames is only supported for Allow rules. In order to control
	// access, DomainNames Allow rules should be used with a lower priority
	// egress deny -- this allows the admin to maintain an explicit "allowlist"
	// of reachable domains.
	//
	// This field is mutually exclusive with PodSelector, NamespaceSelector, and IPBlock.
	// FQDN rules are ALLOW-only and do not support DENY semantics.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	DomainNames []DomainName `json:"domainNames,omitempty"`
}

// ApplicationNetworkPolicyEgressRule describes a particular set of traffic that is allowed out of pods
// matched by an ApplicationNetworkPolicySpec's podSelector. The traffic must match both ports and to.
type ApplicationNetworkPolicyEgressRule struct {
	// Ports is a list of destination ports for outgoing traffic.
	// Each item in this list is combined using a logical OR. If this field is
	// empty or missing, this rule matches all ports (traffic not restricted by port).
	// If this field is present and contains at least one item, then this rule allows
	// traffic only if the traffic matches at least one port in the list.
	// +optional
	Ports []networking.NetworkPolicyPort `json:"ports,omitempty"`

	// To is a list of destinations for outgoing traffic of pods selected for this rule.
	// Items in this list are combined using a logical OR operation. If this field is
	// empty or missing, this rule matches all destinations (traffic not restricted by
	// destination). If this field is present and contains at least one item, this rule
	// allows traffic only if the traffic matches at least one item in the to list.
	// +optional
	To []ApplicationNetworkPolicyPeer `json:"to,omitempty"`
}

// ApplicationNetworkPolicyStatus defines the observed state of ApplicationNetworkPolicy
type ApplicationNetworkPolicyStatus struct {
	// Conditions represent the latest available observations of the ApplicationNetworkPolicy's current state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=anp

// ApplicationNetworkPolicy is the Schema for the applicationnetworkpolicies API
type ApplicationNetworkPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApplicationNetworkPolicySpec   `json:"spec,omitempty"`
	Status ApplicationNetworkPolicyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ApplicationNetworkPolicyList contains a list of ApplicationNetworkPolicy
type ApplicationNetworkPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApplicationNetworkPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApplicationNetworkPolicy{}, &ApplicationNetworkPolicyList{})
}
