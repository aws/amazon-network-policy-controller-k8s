package policyendpoints

import (
	"context"
	"errors"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"

	"github.com/go-logr/logr"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PolicyEndpointsManager interface {
	Reconcile(ctx context.Context, policy *networking.NetworkPolicy) error
	Cleanup(ctx context.Context, policy *networking.NetworkPolicy) error
}

// NewPolicyEndpointsManager constructs a new policyEndpointsManager
func NewPolicyEndpointsManager(k8sClient client.Client, endpointChunkSize int, logger logr.Logger) *policyEndpointsManager {
	endpointsResolver := resolvers.NewEndpointsResolver(k8sClient, logger.WithName("endpoints-resolver"))
	return &policyEndpointsManager{
		k8sClient:         k8sClient,
		endpointsResolver: endpointsResolver,
		endpointChunkSize: endpointChunkSize,
		logger:            logger,
	}
}

var _ PolicyEndpointsManager = (*policyEndpointsManager)(nil)

type policyEndpointsManager struct {
	k8sClient         client.Client
	endpointsResolver resolvers.EndpointsResolver
	endpointChunkSize int
	logger            logr.Logger
}

func (m *policyEndpointsManager) Reconcile(ctx context.Context, policy *networking.NetworkPolicy) error {
	return errors.New("not implemented")
}

func (m *policyEndpointsManager) Cleanup(ctx context.Context, policy *networking.NetworkPolicy) error {
	return errors.New("not implemented")
}
