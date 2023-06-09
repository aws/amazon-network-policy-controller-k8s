package policyendpoints

import (
	"context"
	"errors"
	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/go-logr/logr"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type EndpointsResolver interface {
	// Resolve returns the resolved endpoints for the given policy create, update and delete list
	Resolve(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo, []policyinfo.EndpointInfo,
		[]policyinfo.PodEndpoint, []networking.PolicyType, error)
}

// NewEndpointsResolver constructs a new defaultEndpointsResolver
func NewEndpointsResolver(k8sClient client.Client, logger logr.Logger) *defaultEndpointsResolver {
	return &defaultEndpointsResolver{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

var _ EndpointsResolver = (*defaultEndpointsResolver)(nil)

type defaultEndpointsResolver struct {
	k8sClient client.Client
	logger    logr.Logger
}

func (r *defaultEndpointsResolver) Resolve(ctx context.Context, policy *networking.NetworkPolicy) ([]policyinfo.EndpointInfo,
	[]policyinfo.EndpointInfo, []policyinfo.PodEndpoint, []networking.PolicyType, error) {
	return nil, nil, nil, nil, errors.New("not implemented")
}
