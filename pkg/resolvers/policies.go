package resolvers

import (
	"context"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PolicyReferenceResolver resolves the referred network policies for a given pod, namespace or service.
type PolicyReferenceResolver interface {
	GetReferredPoliciesForPod(ctx context.Context, pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error)
	GetReferredPoliciesForNamespace(ctx context.Context, ns, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, error)
	GetReferredPoliciesForService(ctx context.Context, svc, svcOld *corev1.Service) ([]networking.NetworkPolicy, error)
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

func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForPod(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
	return r.getReferredPoliciesForPod(ctx, pod, podOld)
}

func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForNamespace(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, error) {
	return r.getReferredPoliciesForNamespace(ctx, ns, nsOld)
}

func (r *defaultPolicyReferenceResolver) GetReferredPoliciesForService(ctx context.Context, svc *corev1.Service, svcOld *corev1.Service) ([]networking.NetworkPolicy, error) {
	return r.getReferredPoliciesForService(ctx, svc, svcOld)
}
