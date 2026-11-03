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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterPolicyReference is the reference to the cluster network policy resource
type ClusterPolicyReference struct {
	// Name is the name of the ClusterNetworkPolicy
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ClusterEndpointInfo defines the network endpoint information for the cluster policy ingress/egress
type ClusterEndpointInfo struct {
	// CIDR is the network address(s) of the endpoint
	CIDR NetworkAddress `json:"cidr,omitempty"`

	// Ports is the list of ports
	Ports []Port `json:"ports,omitempty"`

	// DomainName is the FQDN for the endpoint (egress-only)
	DomainName DomainName `json:"domainName,omitempty"`

	// Action from the CNP rule
	Action ClusterNetworkPolicyRuleAction `json:"action"`
}

// ClusterPolicyEndpointSpec defines the desired state of ClusterPolicyEndpoint
type ClusterPolicyEndpointSpec struct {
	// PolicyRef is a reference to the Kubernetes ClusterNetworkPolicy resource.
	PolicyRef ClusterPolicyReference `json:"policyRef"`

	// Tier from the CNP
	Tier Tier `json:"tier"`

	// Priority from the CNP
	Priority int32 `json:"priority"`

	// Subject from the CNP
	Subject ClusterNetworkPolicySubject `json:"subject"`

	// PodSelectorEndpoints contains information about the pods
	// matching the policy across all namespaces
	PodSelectorEndpoints []PodEndpoint `json:"podSelectorEndpoints,omitempty"`

	// Ingress is the list of ingress rules containing resolved network addresses
	Ingress []ClusterEndpointInfo `json:"ingress,omitempty"`

	// Egress is the list of egress rules containing resolved network addresses
	Egress []ClusterEndpointInfo `json:"egress,omitempty"`
}

// ClusterPolicyEndpointStatus defines the observed state of ClusterPolicyEndpoint
type ClusterPolicyEndpointStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=cpe,scope=Cluster

// ClusterPolicyEndpoint is the Schema for the clusterpolicyendpoints API
type ClusterPolicyEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterPolicyEndpointSpec   `json:"spec,omitempty"`
	Status ClusterPolicyEndpointStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterPolicyEndpointList contains a list of ClusterPolicyEndpoint
type ClusterPolicyEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterPolicyEndpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterPolicyEndpoint{}, &ClusterPolicyEndpointList{})
}
