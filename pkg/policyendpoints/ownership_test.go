package policyendpoints

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

// A policy that is deleted and recreated under the same name leaves PolicyEndpoints owned by the
// old instance until the garbage collector removes them. These tests pin that the manager neither
// reuses nor cleans up endpoints that belong to a different instance (or kind) of the same name.

const (
	oldPolicyUID = types.UID("old-policy-uid")
	newPolicyUID = types.UID("new-policy-uid")
)

var testRule = []policyinfo.EndpointInfo{{CIDR: "10.0.0.1/32"}}
var testPods = []policyinfo.PodEndpoint{{Name: "a", Namespace: "x", PodIP: "192.168.0.10"}}

type stubEndpointsResolver struct{}

func (stubEndpointsResolver) Resolve(context.Context, *networking.NetworkPolicy) ([]policyinfo.EndpointInfo,
	[]policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error) {
	return testRule, nil, testPods, nil
}

func (stubEndpointsResolver) ResolveGroups(context.Context, *networking.NetworkPolicy) ([]resolvers.IngressGroup,
	[]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, error) {
	return []resolvers.IngressGroup{{Ingress: testRule, PodEndpoints: testPods}}, nil, nil, nil
}

type stubANPResolver struct{}

func (stubANPResolver) ResolveApplicationNetworkPolicy(context.Context, *policyinfo.ApplicationNetworkPolicy) (
	[]policyinfo.EndpointInfo, []policyinfo.EndpointInfo, []policyinfo.PodEndpoint, error) {
	return testRule, nil, testPods, nil
}

// opRecorder records mutating calls in order, e.g. "create:new-policy-uid" or "delete:base-old".
type opRecorder struct {
	ops []string
	// goneOnDelete names objects the garbage collector removes first: deleting them returns NotFound.
	goneOnDelete map[string]bool
}

func (r *opRecorder) funcs() interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			uid := ""
			if refs := obj.GetOwnerReferences(); len(refs) > 0 {
				uid = string(refs[0].UID)
			}
			r.ops = append(r.ops, "create:"+uid)
			return c.Create(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			r.ops = append(r.ops, "patch:"+obj.GetName())
			return c.Patch(ctx, obj, p, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			r.ops = append(r.ops, "update:"+obj.GetName())
			return c.Update(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			r.ops = append(r.ops, "delete:"+obj.GetName())
			if r.goneOnDelete[obj.GetName()] {
				if err := c.Delete(ctx, obj, opts...); err != nil {
					return err
				}
				return apierrors.NewNotFound(schema.GroupResource{Group: policyinfo.GroupVersion.Group}, obj.GetName())
			}
			return c.Delete(ctx, obj, opts...)
		},
	}
}

func newFakeClient(t *testing.T, rec *opRecorder, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, policyinfo.AddToScheme(scheme))
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithIndex(&policyinfo.PolicyEndpoint{}, IndexKeyPolicyReferenceName, IndexFunctionPolicyReferenceName).
		WithIndex(&policyinfo.ClusterPolicyEndpoint{}, IndexKeyClusterPolicyReferenceName, IndexFunctionClusterPolicyReferenceName).
		WithInterceptorFuncs(rec.funcs()).
		Build()
}

func ownedPE(name, policyName string, owner types.UID, kind string) *policyinfo.PolicyEndpoint {
	apiVersion := "networking.k8s.io/v1"
	if kind == "ApplicationNetworkPolicy" {
		apiVersion = "networking.k8s.aws/v1alpha1"
	}
	return &policyinfo.PolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "x",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: apiVersion, Kind: kind, Name: policyName, UID: owner,
				Controller: func() *bool { b := true; return &b }(),
			}},
		},
		Spec: policyinfo.PolicyEndpointSpec{
			PolicyRef:            policyinfo.PolicyReference{Namespace: "x", Name: policyName},
			PodIsolation:         []networking.PolicyType{networking.PolicyTypeIngress},
			Ingress:              testRule,
			PodSelectorEndpoints: testPods,
		},
	}
}

// withIngress gives an endpoint rules that differ from what the stub resolvers return, so
// reusing it for the current policy has to patch it.
func withIngress(pe *policyinfo.PolicyEndpoint, cidr string) *policyinfo.PolicyEndpoint {
	pe.Spec.Ingress = []policyinfo.EndpointInfo{{CIDR: policyinfo.NetworkAddress(cidr)}}
	return pe
}

func peSpec(t *testing.T, c client.Client, name string) policyinfo.PolicyEndpointSpec {
	t.Helper()
	pe := &policyinfo.PolicyEndpoint{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "x", Name: name}, pe))
	return pe.Spec
}

func ownedCPE(name, policyName string, owner types.UID) *policyinfo.ClusterPolicyEndpoint {
	return &policyinfo.ClusterPolicyEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "networking.k8s.aws/v1alpha1", Kind: "ClusterNetworkPolicy", Name: policyName, UID: owner,
				Controller: func() *bool { b := true; return &b }(),
			}},
		},
		Spec: policyinfo.ClusterPolicyEndpointSpec{
			PolicyRef:            policyinfo.ClusterPolicyReference{Name: policyName},
			Tier:                 policyinfo.AdminTier,
			Priority:             100,
			Ingress:              []policyinfo.ClusterEndpointInfo{{CIDR: "10.0.0.1/32"}},
			PodSelectorEndpoints: testPods,
		},
	}
}

func peOwners(t *testing.T, c client.Client) map[string]types.UID {
	t.Helper()
	list := &policyinfo.PolicyEndpointList{}
	require.NoError(t, c.List(context.Background(), list, client.InNamespace("x")))
	owners := map[string]types.UID{}
	for _, pe := range list.Items {
		owners[pe.Name] = pe.OwnerReferences[0].UID
	}
	return owners
}

func cpeOwners(t *testing.T, c client.Client) map[string]types.UID {
	t.Helper()
	list := &policyinfo.ClusterPolicyEndpointList{}
	require.NoError(t, c.List(context.Background(), list))
	owners := map[string]types.UID{}
	for _, cpe := range list.Items {
		owners[cpe.Name] = cpe.OwnerReferences[0].UID
	}
	return owners
}

func newManager(c client.Client) *policyEndpointsManager {
	return &policyEndpointsManager{
		k8sClient:            c,
		endpointsResolver:    stubEndpointsResolver{},
		anpEndpointsResolver: stubANPResolver{},
		cnpEndpointsResolver: &mockCNPEndpointsResolver{
			ingressRules: []policyinfo.ClusterEndpointInfo{{CIDR: "10.0.0.1/32"}},
			podEndpoints: testPods,
		},
		endpointChunkSize: 100,
		logger:            logr.Discard(),
	}
}

func TestReconcile_DoesNotReuseEndpointsOfPreviousPolicyInstance(t *testing.T) {
	rec := &opRecorder{}
	// The old instance had different rules, so reusing its endpoint means patching it.
	c := newFakeClient(t, rec, withIngress(ownedPE("base-old", "base", oldPolicyUID, "NetworkPolicy"), "10.9.9.9/32"))
	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: newPolicyUID},
		Spec:       networking.NetworkPolicySpec{PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress}},
	}

	require.NoError(t, newManager(c).Reconcile(context.Background(), policy))

	owners := peOwners(t, c)
	assert.Len(t, owners, 1, "only the new instance's endpoint should remain: %v", owners)
	for name, uid := range owners {
		assert.Equal(t, newPolicyUID, uid, "endpoint %s must be owned by the current policy instance", name)
	}
	assert.Equal(t, []string{"create:" + string(newPolicyUID), "delete:base-old"}, rec.ops,
		"the old instance's endpoint is not patched, and is deleted only after the replacement exists")
}

func TestReconcile_LeavesSameNamedEndpointsOfOtherKindsAlone(t *testing.T) {
	rec := &opRecorder{}
	anpEndpoint := withIngress(ownedPE("base-anp", "base", "anp-uid", "ApplicationNetworkPolicy"), "10.9.9.9/32")
	anpSpec := *anpEndpoint.Spec.DeepCopy()
	c := newFakeClient(t, rec, anpEndpoint)
	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: newPolicyUID},
		Spec:       networking.NetworkPolicySpec{PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress}},
	}

	require.NoError(t, newManager(c).Reconcile(context.Background(), policy))

	assert.Equal(t, []string{"create:" + string(newPolicyUID)}, rec.ops,
		"a NetworkPolicy must not patch or delete an ApplicationNetworkPolicy's endpoint of the same name")
	assert.Equal(t, anpSpec, peSpec(t, c, "base-anp"))
}

func TestReconcileANP_LeavesSameNamedEndpointsOfOtherKindsAlone(t *testing.T) {
	rec := &opRecorder{}
	npEndpoint := withIngress(ownedPE("base-np", "base", "np-uid", "NetworkPolicy"), "10.9.9.9/32")
	npSpec := *npEndpoint.Spec.DeepCopy()
	c := newFakeClient(t, rec, npEndpoint)
	anp := &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: newPolicyUID},
		Spec:       policyinfo.ApplicationNetworkPolicySpec{PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress}},
	}

	require.NoError(t, newManager(c).ReconcileANP(context.Background(), anp))

	assert.Equal(t, []string{"create:" + string(newPolicyUID)}, rec.ops,
		"an ApplicationNetworkPolicy must not patch or delete a NetworkPolicy's endpoint of the same name")
	assert.Equal(t, npSpec, peSpec(t, c, "base-np"))
}

func TestReconcile_ToleratesStaleEndpointAlreadyGarbageCollected(t *testing.T) {
	rec := &opRecorder{goneOnDelete: map[string]bool{"base-old": true}}
	c := newFakeClient(t, rec, withIngress(ownedPE("base-old", "base", oldPolicyUID, "NetworkPolicy"), "10.9.9.9/32"))
	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: newPolicyUID},
		Spec:       networking.NetworkPolicySpec{PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress}},
	}

	assert.NoError(t, newManager(c).Reconcile(context.Background(), policy))
}

func TestCleanups_TolerateEndpointsAlreadyGarbageCollected(t *testing.T) {
	gone := map[string]bool{"base-mine": true}
	ctx := context.Background()

	c := newFakeClient(t, &opRecorder{goneOnDelete: gone}, ownedPE("base-mine", "base", oldPolicyUID, "NetworkPolicy"))
	assert.NoError(t, newManager(c).Cleanup(ctx,
		&networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: oldPolicyUID}}))

	c = newFakeClient(t, &opRecorder{goneOnDelete: gone}, ownedPE("base-mine", "base", oldPolicyUID, "ApplicationNetworkPolicy"))
	assert.NoError(t, newManager(c).CleanupANP(ctx,
		&policyinfo.ApplicationNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: oldPolicyUID}}))

	c = newFakeClient(t, &opRecorder{goneOnDelete: gone}, ownedCPE("base-mine", "base", oldPolicyUID))
	assert.NoError(t, newManager(c).CleanupCNP(ctx,
		&policyinfo.ClusterNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "base", UID: oldPolicyUID}}))
}

func TestCleanup_DeletesOnlyEndpointsOwnedByThisPolicyInstance(t *testing.T) {
	rec := &opRecorder{}
	c := newFakeClient(t, rec,
		ownedPE("base-mine", "base", oldPolicyUID, "NetworkPolicy"),
		ownedPE("base-newer", "base", newPolicyUID, "NetworkPolicy"),
		ownedPE("base-anp", "base", "anp-uid", "ApplicationNetworkPolicy"),
	)
	policy := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: oldPolicyUID}}

	require.NoError(t, newManager(c).Cleanup(context.Background(), policy))

	assert.Equal(t, map[string]types.UID{"base-newer": newPolicyUID, "base-anp": "anp-uid"}, peOwners(t, c),
		"cleanup deletes the policy's own endpoints and leaves a recreated policy's and other kinds' alone")
}

func TestReconcileANP_DoesNotReuseEndpointsOfPreviousPolicyInstance(t *testing.T) {
	rec := &opRecorder{}
	c := newFakeClient(t, rec, withIngress(ownedPE("base-old", "base", oldPolicyUID, "ApplicationNetworkPolicy"), "10.9.9.9/32"))
	anp := &policyinfo.ApplicationNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: newPolicyUID},
		Spec:       policyinfo.ApplicationNetworkPolicySpec{PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress}},
	}

	require.NoError(t, newManager(c).ReconcileANP(context.Background(), anp))

	owners := peOwners(t, c)
	assert.Len(t, owners, 1, "only the new instance's endpoint should remain: %v", owners)
	for name, uid := range owners {
		assert.Equal(t, newPolicyUID, uid, "endpoint %s must be owned by the current policy instance", name)
	}
	assert.Equal(t, []string{"create:" + string(newPolicyUID), "delete:base-old"}, rec.ops)
}

func TestCleanupANP_DeletesOnlyEndpointsOwnedByThisPolicyInstance(t *testing.T) {
	rec := &opRecorder{}
	c := newFakeClient(t, rec,
		ownedPE("base-mine", "base", oldPolicyUID, "ApplicationNetworkPolicy"),
		ownedPE("base-np", "base", "np-uid", "NetworkPolicy"),
	)
	anp := &policyinfo.ApplicationNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "x", UID: oldPolicyUID}}

	require.NoError(t, newManager(c).CleanupANP(context.Background(), anp))

	assert.Equal(t, map[string]types.UID{"base-np": "np-uid"}, peOwners(t, c))
}

func TestReconcileCNP_DoesNotReuseEndpointsOfPreviousPolicyInstance(t *testing.T) {
	rec := &opRecorder{}
	staleCPE := ownedCPE("base-old", "base", oldPolicyUID)
	staleCPE.Spec.Ingress = []policyinfo.ClusterEndpointInfo{{CIDR: "10.9.9.9/32"}}
	c := newFakeClient(t, rec, staleCPE)
	cnp := &policyinfo.ClusterNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "base", UID: newPolicyUID},
		Spec:       policyinfo.ClusterNetworkPolicySpec{Tier: policyinfo.AdminTier, Priority: 100},
	}

	require.NoError(t, newManager(c).ReconcileCNP(context.Background(), cnp))

	owners := cpeOwners(t, c)
	assert.Len(t, owners, 1, "only the new instance's endpoint should remain: %v", owners)
	for name, uid := range owners {
		assert.Equal(t, newPolicyUID, uid, "endpoint %s must be owned by the current policy instance", name)
	}
	assert.Equal(t, []string{"create:" + string(newPolicyUID), "delete:base-old"}, rec.ops)
}

func TestCleanupCNP_DeletesOnlyEndpointsOwnedByThisPolicyInstance(t *testing.T) {
	rec := &opRecorder{}
	c := newFakeClient(t, rec,
		ownedCPE("base-mine", "base", oldPolicyUID),
		ownedCPE("base-newer", "base", newPolicyUID),
	)
	cnp := &policyinfo.ClusterNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "base", UID: oldPolicyUID}}

	require.NoError(t, newManager(c).CleanupCNP(context.Background(), cnp))

	assert.Equal(t, map[string]types.UID{"base-newer": newPolicyUID}, cpeOwners(t, c))
}
