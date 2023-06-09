package backend

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ServiceResolver interface {
	GetReferredPolicies(ctx context.Context, svc, svcOld *corev1.Service) ([]networking.NetworkPolicy, error)
}

func NewServiceResolver(k8sClient client.Client, policyTracker PolicyTracker, logger logr.Logger) *defaultServiceResolver {
	return &defaultServiceResolver{
		k8sClient:     k8sClient,
		policyTracker: policyTracker,
		logger:        logger,
	}
}

var _ ServiceResolver = (*defaultServiceResolver)(nil)

type defaultServiceResolver struct {
	logger        logr.Logger
	k8sClient     client.Client
	policyTracker PolicyTracker
}

// GetReferredPolicies returns the list of policies referring to the service.
func (u *defaultServiceResolver) GetReferredPolicies(ctx context.Context, svc, svcOld *corev1.Service) ([]networking.NetworkPolicy, error) {
	return nil, errors.New("not implemented")
}
