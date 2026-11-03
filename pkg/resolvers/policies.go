package resolvers

import (
	"context"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PolicyReferenceResolver resolves the referred network policies for a given pod, namespace or service.
//
// It runs in the event handlers for every pod, namespace and service event, so it reads from the informer cache
// with client.UnsafeDisableDeepCopy: the objects it reads and the policies it returns are shared with the cache.
// They must be treated as read-only, by the resolver and by every caller; DeepCopy before mutating anything.
// TestPolicyReferenceResolverDoesNotMutateCache enforces this.
type PolicyReferenceResolver interface {
	GetReferredPoliciesForPod(ctx context.Context, pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error)
	GetReferredPoliciesForNamespace(ctx context.Context, ns, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, error)
	GetReferredPoliciesForService(ctx context.Context, svc, svcOld *corev1.Service) ([]networking.NetworkPolicy, error)
	GetReferredApplicationNetworkPoliciesForPod(ctx context.Context, pod, podOld *corev1.Pod) ([]policyinfo.ApplicationNetworkPolicy, error)
	GetReferredApplicationNetworkPoliciesForNamespace(ctx context.Context, ns, nsOld *corev1.Namespace) ([]policyinfo.ApplicationNetworkPolicy, error)
	GetReferredApplicationNetworkPoliciesForService(ctx context.Context, svc, svcOld *corev1.Service) ([]policyinfo.ApplicationNetworkPolicy, error)
	GetReferredClusterPoliciesForPod(ctx context.Context, pod, podOld *corev1.Pod) ([]policyinfo.ClusterNetworkPolicy, error)
	GetReferredClusterPoliciesForNamespace(ctx context.Context, ns, nsOld *corev1.Namespace) ([]policyinfo.ClusterNetworkPolicy, error)
	GetReferredClusterPoliciesForService(ctx context.Context, svc, svcOld *corev1.Service) ([]policyinfo.ClusterNetworkPolicy, error)
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
func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
	return r.getReferredPoliciesForPod(ctx, pod, podOld)
}

// GetReferredPoliciesForNamespace returns the network policies matching the namespace's labels in the ingress or egress
// rules. The nsOld resources is to account for the namespace label changes during update.
func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, error) {
	return r.getReferredPoliciesForNamespace(ctx, ns, nsOld)
}

// GetReferredPoliciesForService returns the network policies matching the service's pod selector in the egress rules.
// The svcOld resource is to account for the service label changes during update.
func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForService(ctx context.Context, svc *corev1.Service, svcOld *corev1.Service) ([]networking.NetworkPolicy, error) {
	return r.getReferredPoliciesForService(ctx, svc, svcOld)
}

// GetReferredApplicationNetworkPoliciesForPod returns the application network policies matching the pod's labels in ingress or egress rules.
// The podOld resource is used to determine the policies to reconcile for label changes.
func (r *defaultPolicyReferenceResolver) GetReferredApplicationNetworkPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]policyinfo.ApplicationNetworkPolicy, error) {
	return r.getReferredApplicationNetworkPoliciesForPod(ctx, pod, podOld)
}

// GetReferredApplicationNetworkPoliciesForNamespace returns the application network policies matching the namespace's labels in ingress or egress rules.
// The nsOld resource is to account for namespace label changes during update.
func (r *defaultPolicyReferenceResolver) GetReferredApplicationNetworkPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]policyinfo.ApplicationNetworkPolicy, error) {
	return r.getReferredApplicationNetworkPoliciesForNamespace(ctx, ns, nsOld)
}

// GetReferredApplicationNetworkPoliciesForService returns the application network policies matching the service's pod selector in the egress rules.
// The svcOld resource is to account for the service label changes during update.
func (r *defaultPolicyReferenceResolver) GetReferredApplicationNetworkPoliciesForService(ctx context.Context, svc *corev1.Service, svcOld *corev1.Service) ([]policyinfo.ApplicationNetworkPolicy, error) {
	return r.getReferredApplicationNetworkPoliciesForService(ctx, svc, svcOld)
}

// GetReferredClusterPoliciesForPod returns the cluster network policies that might be affected by pod changes.
func (r *defaultPolicyReferenceResolver) GetReferredClusterPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]policyinfo.ClusterNetworkPolicy, error) {
	// For CNP, any pod change could affect cluster policies, so return all CNPs
	// this could be optimized to only return CNPs that actually select this pod
	cnpList := &policyinfo.ClusterNetworkPolicyList{}
	if err := r.k8sClient.List(ctx, cnpList, client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	return cnpList.Items, nil
}

// GetReferredClusterPoliciesForNamespace returns the cluster network policies that might be affected by namespace changes.
func (r *defaultPolicyReferenceResolver) GetReferredClusterPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]policyinfo.ClusterNetworkPolicy, error) {
	// For CNP, namespace changes could affect cluster policies, so return all CNPs
	cnpList := &policyinfo.ClusterNetworkPolicyList{}
	if err := r.k8sClient.List(ctx, cnpList, client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	return cnpList.Items, nil
}

// GetReferredClusterPoliciesForService returns the cluster network policies that might be affected by service changes.
func (r *defaultPolicyReferenceResolver) GetReferredClusterPoliciesForService(ctx context.Context, svc *corev1.Service, svcOld *corev1.Service) ([]policyinfo.ClusterNetworkPolicy, error) {
	// For CNP, service changes could affect cluster policies, so return all CNPs
	cnpList := &policyinfo.ClusterNetworkPolicyList{}
	if err := r.k8sClient.List(ctx, cnpList, client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	return cnpList.Items, nil
}
