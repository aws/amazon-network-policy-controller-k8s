/*
Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package eventhandlers

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/config"
	"github.com/aws/amazon-network-policy-controller-k8s/pkg/resolvers"
)

// TestPolicyReferenceResolverDoesNotMutateCache guards the client.UnsafeDisableDeepCopy reads in the policy
// reference resolver. Those reads hand out the informer cache's own objects, so any write to them, by the
// resolver or by the event handlers that consume its results, silently corrupts the cache for every other reader.
//
// The test loads a workload that reaches every resolver path (pod, namespace and ipBlock peers, namespace
// selectors, services, ApplicationNetworkPolicies and ClusterNetworkPolicies) into a real controller-runtime cache
// backed by envtest, snapshots every cached object, runs the pod, namespace and service handlers for create, update
// and delete events built from the cached objects themselves (as controller-runtime delivers them), feeds the
// policies they emit through the policy handler, calls every resolver method directly, and then requires every
// cached object to equal its snapshot.
func TestPolicyReferenceResolverDoesNotMutateCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer func() { _ = env.Stop() }()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := policyinfo.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	apiClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range cacheTestObjects() {
		if err := apiClient.Create(ctx, obj); err != nil {
			t.Fatalf("creating %T %s: %v", obj, obj.GetName(), err)
		}
		if pod, ok := obj.(*corev1.Pod); ok {
			// The controller ignores pods without an IP, and there is no kubelet to assign one.
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: podIPs[pod.Name],
				PodIPs: []corev1.PodIP{{IP: podIPs[pod.Name]}}}
			if err := apiClient.Status().Update(ctx, pod); err != nil {
				t.Fatalf("setting status of pod %s: %v", pod.Name, err)
			}
		}
	}

	// The controller's own cache configuration, including the pod transform and field selector.
	cacheOpts := config.BuildCacheOptions()
	cacheOpts.Scheme = scheme
	informerCache, err := cache.New(restCfg, cacheOpts)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range cachedTypes() {
		if _, err := informerCache.GetInformer(ctx, obj); err != nil {
			t.Fatalf("starting informer for %T: %v", obj, err)
		}
	}
	go func() { _ = informerCache.Start(ctx) }()
	if !informerCache.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	cachedClient, err := client.New(restCfg, client.Options{Scheme: scheme, Cache: &client.CacheOptions{Reader: informerCache}})
	if err != nil {
		t.Fatal(err)
	}
	waitForCachedPods(t, ctx, cachedClient)

	snapshot := snapshotCache(t, ctx, cachedClient)

	logger := logr.Discard()
	tracker := resolvers.NewPolicyTracker(logger)
	resolver := resolvers.NewPolicyReferenceResolver(cachedClient, tracker, logger)
	policyHandler := NewEnqueueRequestForPolicyEvent(tracker, 0, logger)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()

	// Policy creates populate the tracker, so the namespace-selector paths of the resolver run.
	nps := &networking.NetworkPolicyList{}
	anps := &policyinfo.ApplicationNetworkPolicyList{}
	cnps := &policyinfo.ClusterNetworkPolicyList{}
	for _, list := range []client.ObjectList{nps, anps, cnps} {
		if err := cachedClient.List(ctx, list, client.UnsafeDisableDeepCopy); err != nil {
			t.Fatal(err)
		}
	}
	for i := range nps.Items {
		policyHandler.Create(ctx, event.CreateEvent{Object: &nps.Items[i]}, queue)
	}
	for i := range anps.Items {
		policyHandler.Create(ctx, event.CreateEvent{Object: &anps.Items[i]}, queue)
	}
	for i := range cnps.Items {
		policyHandler.Create(ctx, event.CreateEvent{Object: &cnps.Items[i]}, queue)
	}

	// Policies emitted by the pod, namespace and service handlers go through the policy handler, as in the
	// controller.
	policyEvents := make(chan event.GenericEvent)
	emitted := make(chan int)
	go func() {
		n := 0
		for e := range policyEvents {
			policyHandler.Generic(ctx, e, queue)
			n++
		}
		emitted <- n
	}()
	handlers := []struct {
		name    string
		handler interface {
			Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request])
			Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request])
			Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request])
		}
		list client.ObjectList
	}{
		{"pod", NewEnqueueRequestForPodEvent(policyEvents, cachedClient, resolver, logger), &corev1.PodList{}},
		{"namespace", NewEnqueueRequestForNamespaceEvent(policyEvents, cachedClient, resolver, logger), &corev1.NamespaceList{}},
		{"service", NewEnqueueRequestForServiceEvent(policyEvents, cachedClient, resolver, logger), &corev1.ServiceList{}},
	}
	for _, h := range handlers {
		if err := cachedClient.List(ctx, h.list, client.UnsafeDisableDeepCopy); err != nil {
			t.Fatal(err)
		}
		objs, err := objectsOf(h.list)
		if err != nil {
			t.Fatal(err)
		}
		for _, obj := range objs {
			// The new object of an update differs in labels so selector matches change between old and new.
			changed := obj.DeepCopyObject().(client.Object)
			changed.SetLabels(map[string]string{"app": "changed", "tier": "changed"})
			h.handler.Create(ctx, event.CreateEvent{Object: obj}, queue)
			h.handler.Update(ctx, event.UpdateEvent{ObjectOld: obj, ObjectNew: changed}, queue)
			h.handler.Update(ctx, event.UpdateEvent{ObjectOld: changed, ObjectNew: obj}, queue)
			h.handler.Delete(ctx, event.DeleteEvent{Object: obj}, queue)
		}
	}
	close(policyEvents)
	if n := <-emitted; n == 0 {
		t.Fatal("the handlers emitted no policies, so the workload does not exercise the resolver")
	}

	// Every resolver method, directly.
	pods, namespaces, services := &corev1.PodList{}, &corev1.NamespaceList{}, &corev1.ServiceList{}
	for _, list := range []client.ObjectList{pods, namespaces, services} {
		if err := cachedClient.List(ctx, list, client.UnsafeDisableDeepCopy); err != nil {
			t.Fatal(err)
		}
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		mustResolve(t, errOf(resolver.GetReferredPoliciesForPod(ctx, pod, pod)))
		mustResolve(t, errOf(resolver.GetReferredApplicationNetworkPoliciesForPod(ctx, pod, pod)))
		mustResolve(t, errOf(resolver.GetReferredClusterPoliciesForPod(ctx, pod, pod)))
	}
	for i := range namespaces.Items {
		ns := &namespaces.Items[i]
		mustResolve(t, errOf(resolver.GetReferredPoliciesForNamespace(ctx, ns, ns)))
		mustResolve(t, errOf(resolver.GetReferredApplicationNetworkPoliciesForNamespace(ctx, ns, ns)))
		mustResolve(t, errOf(resolver.GetReferredClusterPoliciesForNamespace(ctx, ns, ns)))
	}
	for i := range services.Items {
		svc := &services.Items[i]
		mustResolve(t, errOf(resolver.GetReferredPoliciesForService(ctx, svc, svc)))
		mustResolve(t, errOf(resolver.GetReferredApplicationNetworkPoliciesForService(ctx, svc, svc)))
		mustResolve(t, errOf(resolver.GetReferredClusterPoliciesForService(ctx, svc, svc)))
	}

	after := snapshotCache(t, ctx, cachedClient)
	if diff := cmp.Diff(snapshot, after); diff != "" {
		t.Fatalf("the policy reference resolver or the event handlers modified objects in the informer cache "+
			"(-before +after):\n%s", diff)
	}
}

func errOf[T any](_ []T, err error) error { return err }

func mustResolve(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func cachedTypes() []client.Object {
	return []client.Object{&corev1.Pod{}, &corev1.Namespace{}, &corev1.Service{}, &networking.NetworkPolicy{},
		&policyinfo.ApplicationNetworkPolicy{}, &policyinfo.ClusterNetworkPolicy{}}
}

// snapshotCache returns a deep copy of every cached object of the types the resolver reads, keyed by type and
// name. Reading with the default deep copy means a later snapshot shows whatever the cache holds by then.
func snapshotCache(t *testing.T, ctx context.Context, c client.Client) map[string]client.Object {
	t.Helper()
	snapshot := map[string]client.Object{}
	for _, list := range []client.ObjectList{&corev1.PodList{}, &corev1.NamespaceList{}, &corev1.ServiceList{},
		&networking.NetworkPolicyList{}, &policyinfo.ApplicationNetworkPolicyList{}, &policyinfo.ClusterNetworkPolicyList{}} {
		if err := c.List(ctx, list); err != nil {
			t.Fatal(err)
		}
		objs, err := objectsOf(list)
		if err != nil {
			t.Fatal(err)
		}
		for _, obj := range objs {
			snapshot[fmt.Sprintf("%T %s/%s", obj, obj.GetNamespace(), obj.GetName())] = obj
		}
	}
	return snapshot
}

func objectsOf(list client.ObjectList) ([]client.Object, error) {
	var objs []client.Object
	switch l := list.(type) {
	case *corev1.PodList:
		for i := range l.Items {
			objs = append(objs, &l.Items[i])
		}
	case *corev1.NamespaceList:
		for i := range l.Items {
			objs = append(objs, &l.Items[i])
		}
	case *corev1.ServiceList:
		for i := range l.Items {
			objs = append(objs, &l.Items[i])
		}
	case *networking.NetworkPolicyList:
		for i := range l.Items {
			objs = append(objs, &l.Items[i])
		}
	case *policyinfo.ApplicationNetworkPolicyList:
		for i := range l.Items {
			objs = append(objs, &l.Items[i])
		}
	case *policyinfo.ClusterNetworkPolicyList:
		for i := range l.Items {
			objs = append(objs, &l.Items[i])
		}
	default:
		return nil, fmt.Errorf("unexpected list type %T", list)
	}
	return objs, nil
}

func waitForCachedPods(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		pods := &corev1.PodList{}
		if err := c.List(ctx, pods); err != nil {
			t.Fatal(err)
		}
		withIP := 0
		for _, pod := range pods.Items {
			if pod.Status.PodIP != "" {
				withIP++
			}
		}
		if withIP == len(podIPs) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the cache did not observe every pod with its IP")
}

var podIPs = map[string]string{"web-a": "10.0.0.1", "web-b": "10.0.0.2", "db-a": "10.0.1.1", "api-a": "10.0.2.1"}

// cacheTestObjects is a small workload that reaches every resolver path.
func cacheTestObjects() []client.Object {
	port := intstr.FromInt32(8080)
	tcp := ptr.To(corev1.ProtocolTCP)
	ports := []networking.NetworkPolicyPort{{Protocol: tcp, Port: &port}}
	appSelector := func(app string) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}
	}
	tierSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "backend"}}
	pod := func(ns, name, app string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": app}},
			Spec: corev1.PodSpec{
				// The controller's cache only holds scheduled pods.
				NodeName:   "node-1",
				Containers: []corev1.Container{{Name: "app", Image: "app", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}},
			},
		}
	}
	service := func(ns, name, app string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app": app},
				Ports: []corev1.ServicePort{{Port: 80, TargetPort: port, Protocol: corev1.ProtocolTCP}}},
		}
	}
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Labels: map[string]string{"tier": "frontend"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "backend", Labels: map[string]string{"tier": "backend"}}},
		pod("frontend", "web-a", "web"),
		pod("frontend", "web-b", "web"),
		pod("backend", "db-a", "db"),
		pod("backend", "api-a", "api"),
		service("backend", "db", "db"),
		service("backend", "api", "api"),
		// Pod selector peer in the same namespace, egress to a service backend.
		&networking.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "backend", Name: "db-from-api"},
			Spec: networking.NetworkPolicySpec{
				PodSelector: *appSelector("db"),
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
				Ingress:     []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{{PodSelector: appSelector("api")}}, Ports: ports}},
				Egress:      []networking.NetworkPolicyEgressRule{{To: []networking.NetworkPolicyPeer{{PodSelector: appSelector("api")}}, Ports: ports}},
			},
		},
		// Namespace selector peer, tracked cluster-wide, with and without a pod selector.
		&networking.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: "web-to-backend"},
			Spec: networking.NetworkPolicySpec{
				PodSelector: *appSelector("web"),
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeEgress},
				Egress: []networking.NetworkPolicyEgressRule{{To: []networking.NetworkPolicyPeer{
					{NamespaceSelector: tierSelector, PodSelector: appSelector("api")},
					{NamespaceSelector: tierSelector},
				}, Ports: ports}},
			},
		},
		// Ingress from another namespace by selector, and an ipBlock peer.
		&networking.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "backend", Name: "api-from-frontend"},
			Spec: networking.NetworkPolicySpec{
				PodSelector: *appSelector("api"),
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress},
				Ingress: []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{
					{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "frontend"}}},
					{IPBlock: &networking.IPBlock{CIDR: "172.16.0.0/16", Except: []string{"172.16.1.0/24"}}},
				}, Ports: ports}},
			},
		},
		&policyinfo.ApplicationNetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: "web-anp"},
			Spec: policyinfo.ApplicationNetworkPolicySpec{
				PodSelector: *appSelector("web"),
				PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress},
				Ingress:     []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{{PodSelector: appSelector("web")}}, Ports: ports}},
				Egress: []policyinfo.ApplicationNetworkPolicyEgressRule{{To: []policyinfo.ApplicationNetworkPolicyPeer{
					{NamespaceSelector: tierSelector, PodSelector: appSelector("db")},
				}, Ports: ports}},
			},
		},
		&policyinfo.ClusterNetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "backend-baseline"},
			Spec: policyinfo.ClusterNetworkPolicySpec{
				Tier:     policyinfo.AdminTier,
				Priority: 10,
				Subject:  policyinfo.ClusterNetworkPolicySubject{Namespaces: tierSelector},
				Ingress: []policyinfo.ClusterNetworkPolicyIngressRule{{
					Name:   "from-frontend",
					Action: policyinfo.ClusterNetworkPolicyRuleActionAccept,
					From: []policyinfo.ClusterNetworkPolicyIngressPeer{{
						Namespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "frontend"}},
					}},
				}},
			},
		},
	}
}
