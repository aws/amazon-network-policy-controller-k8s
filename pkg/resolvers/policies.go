package resolvers

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	adminnetworking "sigs.k8s.io/network-policy-api/apis/v1alpha1"
)

// PolicyReferenceResolver resolves the referred network policies for a given pod, namespace or service.
type PolicyReferenceResolver interface {
	GetReferredPoliciesForPod(ctx context.Context, pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error)
	GetReferredPoliciesForNamespace(ctx context.Context, ns, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error)
	GetReferredPoliciesForService(ctx context.Context, svc, svcOld *corev1.Service) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error)
}

func NewPolicyReferenceResolver(k8sClient client.Client, policyTracker PolicyTracker, logger logr.Logger) *defaultPolicyReferenceResolver {
	return &defaultPolicyReferenceResolver{
		k8sClient:     k8sClient,
		policyTracker: policyTracker,
		logger:        logger,
	}
}

var _ PolicyReferenceResolver = (*defaultPolicyReferenceResolver)(nil)

type defaultPolicyReferenceResolver struct {
	logger        logr.Logger
	k8sClient     client.Client
	policyTracker PolicyTracker
}

// GetReferredPoliciesForPod returns the network policies matching the pod's labels. The podOld resource is the old
// resource for update events and is used to determine the policies to reconcile for the label changes.
// In case of the pods, the pod labels are matched against the policy's podSelector or the ingress or egress rules.
func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error) {
	return r.getReferredPoliciesForPod(ctx, pod, podOld)
}

// GetReferredPoliciesForNamespace returns the network policies matching the namespace's labels in the ingress or egress
// rules. The nsOld resources is to account for the namespace label changes during update.
func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error) {
	return r.getReferredPoliciesForNamespace(ctx, ns, nsOld)
}

// GetReferredPoliciesForService returns the network policies matching the service's pod selector in the egress rules.
// The svcOld resource is to account for the service label changes during update.
func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForService(ctx context.Context, svc *corev1.Service, svcOld *corev1.Service) ([]networking.NetworkPolicy, []adminnetworking.AdminNetworkPolicy, error) {
	return r.getReferredPoliciesForService(ctx, svc, svcOld)
}
