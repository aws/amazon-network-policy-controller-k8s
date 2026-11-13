package controllers

import (
	"context"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	mock_client "github.com/aws/amazon-network-policy-controller-k8s/mocks/controller-runtime/client"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

func Test_policyReconciler_reconcileApplicationNetworkPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	mockFinalizerManager := &mockFinalizerManager{}
	mockPolicyEndpointsManager := &mockPolicyEndpointsManager{}

	reconciler := &policyReconciler{
		k8sClient:              mockClient,
		finalizerManager:       mockFinalizerManager,
		policyEndpointsManager: mockPolicyEndpointsManager,
		logger:                 zap.New(),
	}

	anp := &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-anp",
			Namespace: "default",
		},
	}

	err := reconciler.reconcileApplicationNetworkPolicy(context.Background(), anp)

	assert.NoError(t, err)
	assert.True(t, mockFinalizerManager.addFinalizersCalled)
	assert.True(t, mockPolicyEndpointsManager.reconcileANPCalled)
}

func Test_policyReconciler_cleanupApplicationNetworkPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	mockFinalizerManager := &mockFinalizerManager{}
	mockPolicyEndpointsManager := &mockPolicyEndpointsManager{}
	mockPolicyTracker := &mockPolicyTracker{}

	reconciler := &policyReconciler{
		k8sClient:              mockClient,
		finalizerManager:       mockFinalizerManager,
		policyEndpointsManager: mockPolicyEndpointsManager,
		policyTracker:          mockPolicyTracker,
		logger:                 zap.New(),
	}

	anp := &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-anp",
			Namespace:  "default",
			Finalizers: []string{anpFinalizerName},
		},
	}

	err := reconciler.cleanupApplicationNetworkPolicy(context.Background(), anp)

	assert.NoError(t, err)
	assert.True(t, mockPolicyEndpointsManager.cleanupANPCalled)
	assert.True(t, mockFinalizerManager.removeFinalizersCalled)
}

func Test_policyReconciler_reconcileNetworkPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	mockFinalizerManager := &mockFinalizerManager{}
	mockPolicyEndpointsManager := &mockPolicyEndpointsManager{}

	reconciler := &policyReconciler{
		k8sClient:              mockClient,
		finalizerManager:       mockFinalizerManager,
		policyEndpointsManager: mockPolicyEndpointsManager,
		logger:                 zap.New(),
	}

	np := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-np",
			Namespace: "default",
		},
	}

	err := reconciler.reconcileNetworkPolicy(context.Background(), np)

	assert.NoError(t, err)
	assert.True(t, mockFinalizerManager.addFinalizersCalled)
	assert.True(t, mockPolicyEndpointsManager.reconcileCalled)
}

func Test_policyReconciler_cleanupNetworkPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	mockFinalizerManager := &mockFinalizerManager{}
	mockPolicyEndpointsManager := &mockPolicyEndpointsManager{}
	mockPolicyTracker := &mockPolicyTracker{}

	reconciler := &policyReconciler{
		k8sClient:              mockClient,
		finalizerManager:       mockFinalizerManager,
		policyEndpointsManager: mockPolicyEndpointsManager,
		policyTracker:          mockPolicyTracker,
		logger:                 zap.New(),
	}

	np := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-np",
			Namespace:  "default",
			Finalizers: []string{"networking.k8s.aws/resources"},
		},
	}

	err := reconciler.cleanupNetworkPolicy(context.Background(), np)

	assert.NoError(t, err)
	assert.True(t, mockPolicyEndpointsManager.cleanupCalled)
	assert.True(t, mockFinalizerManager.removeFinalizersCalled)
	assert.True(t, mockPolicyTracker.removePolicyCalled)
}

// Mock implementations for testing
type mockFinalizerManager struct {
	addFinalizersCalled    bool
	removeFinalizersCalled bool
}

func (m *mockFinalizerManager) AddFinalizers(ctx context.Context, obj client.Object, finalizers ...string) error {
	m.addFinalizersCalled = true
	return nil
}

func (m *mockFinalizerManager) RemoveFinalizers(ctx context.Context, obj client.Object, finalizers ...string) error {
	m.removeFinalizersCalled = true
	return nil
}

type mockPolicyEndpointsManager struct {
	reconcileANPCalled bool
	cleanupANPCalled   bool
	reconcileCalled    bool
	cleanupCalled      bool
	reconcileCNPCalled bool
	cleanupCNPCalled   bool
}

func (m *mockPolicyEndpointsManager) Reconcile(ctx context.Context, policy *networking.NetworkPolicy) error {
	m.reconcileCalled = true
	return nil
}

func (m *mockPolicyEndpointsManager) Cleanup(ctx context.Context, policy *networking.NetworkPolicy) error {
	m.cleanupCalled = true
	return nil
}

func (m *mockPolicyEndpointsManager) ReconcileANP(ctx context.Context, anp *policyinfo.ApplicationNetworkPolicy) error {
	m.reconcileANPCalled = true
	return nil
}

func (m *mockPolicyEndpointsManager) CleanupANP(ctx context.Context, anp *policyinfo.ApplicationNetworkPolicy) error {
	m.cleanupANPCalled = true
	return nil
}

func (m *mockPolicyEndpointsManager) ReconcileCNP(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) error {
	m.reconcileCNPCalled = true
	return nil
}

func (m *mockPolicyEndpointsManager) CleanupCNP(ctx context.Context, cnp *policyinfo.ClusterNetworkPolicy) error {
	m.cleanupCNPCalled = true
	return nil
}

type mockPolicyTracker struct {
	removePolicyCalled bool
}

func (m *mockPolicyTracker) UpdatePolicy(policy *networking.NetworkPolicy) {}

func (m *mockPolicyTracker) RemovePolicy(policy *networking.NetworkPolicy) {
	m.removePolicyCalled = true
}

func (m *mockPolicyTracker) UpdateGenericPolicy(obj client.Object) {}

func (m *mockPolicyTracker) RemoveGenericPolicy(obj client.Object) {
	m.removePolicyCalled = true
}

func (m *mockPolicyTracker) GetPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName] {
	return sets.Set[types.NamespacedName]{}
}

func (m *mockPolicyTracker) GetPoliciesWithEgressRules() sets.Set[types.NamespacedName] {
	return sets.Set[types.NamespacedName]{}
}

func (m *mockPolicyTracker) GetApplicationNetworkPoliciesWithNamespaceReferences() sets.Set[types.NamespacedName] {
	return sets.Set[types.NamespacedName]{}
}

func (m *mockPolicyTracker) GetApplicationNetworkPoliciesWithEgressRules() sets.Set[types.NamespacedName] {
	return sets.Set[types.NamespacedName]{}
}

func (m *mockPolicyTracker) GetNamespaceSelectorPeers(types.NamespacedName) ([]resolvers.PeerSelectors, bool) {
	return nil, false
}

func (m *mockPolicyTracker) GetANPNamespaceSelectorPeers(types.NamespacedName) ([]resolvers.PeerSelectors, bool) {
	return nil, false
}

func Test_policyReconciler_reconcileClusterNetworkPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	mockFinalizerManager := &mockFinalizerManager{}
	mockPolicyEndpointsManager := &mockPolicyEndpointsManager{}

	reconciler := &policyReconciler{
		k8sClient:              mockClient,
		finalizerManager:       mockFinalizerManager,
		policyEndpointsManager: mockPolicyEndpointsManager,
		logger:                 zap.New(),
	}

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cnp",
		},
	}

	err := reconciler.reconcileClusterNetworkPolicy(context.Background(), cnp)

	assert.NoError(t, err)
	assert.True(t, mockFinalizerManager.addFinalizersCalled)
	assert.True(t, mockPolicyEndpointsManager.reconcileCNPCalled)
}

func Test_policyReconciler_cleanupClusterNetworkPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_client.NewMockClient(ctrl)
	mockFinalizerManager := &mockFinalizerManager{}
	mockPolicyEndpointsManager := &mockPolicyEndpointsManager{}
	mockPolicyTracker := &mockPolicyTracker{}

	reconciler := &policyReconciler{
		k8sClient:              mockClient,
		finalizerManager:       mockFinalizerManager,
		policyEndpointsManager: mockPolicyEndpointsManager,
		policyTracker:          mockPolicyTracker,
		logger:                 zap.New(),
	}

	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cnp",
			Finalizers: []string{cnpFinalizerName},
		},
	}

	err := reconciler.cleanupClusterNetworkPolicy(context.Background(), cnp)

	assert.NoError(t, err)
	assert.True(t, mockPolicyEndpointsManager.cleanupCNPCalled)
	assert.True(t, mockFinalizerManager.removeFinalizersCalled)
}
