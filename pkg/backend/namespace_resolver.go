package backend

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NamespaceResolver resolves the network policies referring the given namespace.
type NamespaceResolver interface {
	GetReferredPolicies(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, error)
}

func NewNamespaceResolver(k8sClient client.Client, policyTracker PolicyTracker, logger logr.Logger) *defaultNamespaceResolver {
	return &defaultNamespaceResolver{}
}

var _ NamespaceResolver = (*defaultNamespaceResolver)(nil)

type defaultNamespaceResolver struct {
}

func (n *defaultNamespaceResolver) GetReferredPolicies(ctx context.Context, ns *corev1.Namespace, nsOld *corev1.Namespace) ([]networking.NetworkPolicy, error) {
	return nil, errors.New("not implemented")
}
