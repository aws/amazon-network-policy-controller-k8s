package backend

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodResolver resolves the network policies referring the given pod.
type PodResolver interface {
	GetReferredPolicies(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error)
}

func NewPodResolver(k8sClient client.Client, policyTracker PolicyTracker, logger logr.Logger) *defaultPodResolver {
	return &defaultPodResolver{}
}

var _ PodResolver = (*defaultPodResolver)(nil)

type defaultPodResolver struct {
}

func (u *defaultPodResolver) GetReferredPolicies(ctx context.Context, pod *corev1.Pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
	return nil, errors.New("not implemented")
}
