package policyendpoints

import (
	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	IndexKeyPolicyReferenceName = "spec.policyRef.name"
)

// IndexFunctionPolicyReferenceName is IndexFunc for "PolicyReference" index.
func IndexFunctionPolicyReferenceName(obj client.Object) []string {
	policyEndpoint := obj.(*policyinfo.PolicyEndpoint)
	return []string{policyEndpoint.Spec.PolicyRef.Name}
}
