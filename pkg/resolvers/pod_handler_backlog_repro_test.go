package resolvers

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/amazon-network-policy-controller-k8s/pkg/k8s"
)

// customerShapeFixture is the policy shape from the 2026-11-13 incident report (issue #288), scaled to 5,000
// policies: cross-namespace policies whose rules carry namespaceSelector peers, over 500 namespaces.
type customerShapeFixture struct {
	namespaces []string
	policies   []*networking.NetworkPolicy
	objects    []client.Object
}

func newCustomerShapeFixture(rng *rand.Rand, nPolicies, peersPerPolicy int) customerShapeFixture {
	var f customerShapeFixture
	for i := 0; i < 500; i++ {
		n := fmt.Sprintf("ns-%d", i)
		f.namespaces = append(f.namespaces, n)
		f.objects = append(f.objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n,
			Labels: map[string]string{"team": fmt.Sprintf("t%d", i%20)}}})
	}
	for i := 0; i < nPolicies; i++ {
		p := &networking.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i), Namespace: f.namespaces[rng.Intn(500)]},
			Spec: networking.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": fmt.Sprintf("a%d", i)}}}}
		rule := networking.NetworkPolicyIngressRule{}
		for j := 0; j < peersPerPolicy; j++ {
			rule.From = append(rule.From, networking.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": fmt.Sprintf("t%d", rng.Intn(20))}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": fmt.Sprintf("a%d", rng.Intn(nPolicies))}}})
		}
		p.Spec.Ingress = []networking.NetworkPolicyIngressRule{rule}
		f.policies = append(f.policies, p)
		f.objects = append(f.objects, p)
	}
	return f
}

func (f customerShapeFixture) newClient(tb testing.TB) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(tb, clientgoscheme.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.objects...).Build()
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// TestReproPodHandlerBacklog reproduces issue #288 without a cluster: the pod event handler runs on the single
// client-go processorListener goroutine of the pod informer, so when one event costs more than the gap between
// events the backlog grows for as long as events keep coming. The test drives a real SharedIndexInformer (fake
// clientset) whose handler resolves each pod event the way internal/eventhandlers/pod.go does, emits pods at a
// fixed rate, and reports handler throughput, the backlog at the end of the emission window, and the lag of the
// last event.
//
// It is skipped unless NPC_REPRO_BACKLOG=1 because the reference run takes minutes by design.
//
//	NPC_REPRO_BACKLOG=1 go test ./pkg/resolvers/ -run TestReproPodHandlerBacklog -v -timeout 30m
//
// Knobs: NPC_REPRO_PODS (default 120), NPC_REPRO_PODS_PER_SEC (default 20), NPC_REPRO_POLICIES (5000),
// NPC_REPRO_PEERS (10).
func TestReproPodHandlerBacklog(t *testing.T) {
	if os.Getenv("NPC_REPRO_BACKLOG") != "1" {
		t.Skip("set NPC_REPRO_BACKLOG=1 to run the issue #288 reproducer (slow on purpose)")
	}
	nPods := envInt("NPC_REPRO_PODS", 120)
	perSec := envInt("NPC_REPRO_PODS_PER_SEC", 20)
	fx := newCustomerShapeFixture(rand.New(rand.NewSource(1)), envInt("NPC_REPRO_POLICIES", 5000), envInt("NPC_REPRO_PEERS", 10))
	nullLogger := logr.New(&log.NullLogSink{})

	type variant struct {
		name    string
		resolve func(c client.Client) func(ctx context.Context, pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error)
	}
	variants := []variant{
		{"reference-scan", func(c client.Client) func(context.Context, *corev1.Pod, *corev1.Pod) ([]networking.NetworkPolicy, error) {
			tracker := &defaultPolicyTracker{logger: nullLogger}
			for _, p := range fx.policies {
				tracker.namespacedPolicies.Store(k8s.NamespacedName(p), true)
			}
			r := &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: nullLogger}
			return func(ctx context.Context, pod, podOld *corev1.Pod) ([]networking.NetworkPolicy, error) {
				return referenceGetReferredPoliciesForPod(ctx, r, pod, podOld)
			}
		}},
		{"indexed", func(c client.Client) func(context.Context, *corev1.Pod, *corev1.Pod) ([]networking.NetworkPolicy, error) {
			tracker := &defaultPolicyTracker{logger: nullLogger}
			for _, p := range fx.policies {
				tracker.UpdatePolicy(p)
			}
			r := &defaultPolicyReferenceResolver{k8sClient: c, policyTracker: tracker, logger: nullLogger}
			return r.GetReferredPoliciesForPod
		}},
	}
	if only := os.Getenv("NPC_REPRO_VARIANT"); only != "" {
		var keep []variant
		for _, v := range variants {
			if v.name == only {
				keep = append(keep, v)
			}
		}
		variants = keep
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolve := v.resolve(fx.newClient(t))

			// A real client-go informer: one processorListener per handler, events delivered in order on one goroutine.
			cs := k8sfake.NewSimpleClientset()
			factory := informers.NewSharedInformerFactory(cs, 0)
			podInformer := factory.Core().V1().Pods().Informer()

			var handled atomic.Int64
			var busy atomic.Int64 // nanoseconds the handler goroutine spent resolving
			var mu sync.Mutex
			var lastHandled time.Time
			handle := func(pod, podOld *corev1.Pod) {
				if pod.Status.PodIP == "" {
					return // pod.go: "Pod does not have an IP yet"
				}
				t0 := time.Now()
				if _, err := resolve(ctx, pod, podOld); err != nil {
					t.Errorf("resolve: %v", err)
				}
				busy.Add(int64(time.Since(t0)))
				handled.Add(1)
				mu.Lock()
				lastHandled = time.Now()
				mu.Unlock()
			}
			_, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj interface{}) { handle(obj.(*corev1.Pod), nil) },
				UpdateFunc: func(oldObj, newObj interface{}) { handle(newObj.(*corev1.Pod), oldObj.(*corev1.Pod)) },
			})
			require.NoError(t, err)
			factory.Start(ctx.Done())
			require.True(t, cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced))

			// Emit nPods creates (with an IP, like a pod that just got its sandbox) at perSec.
			start := time.Now()
			tick := time.NewTicker(time.Second / time.Duration(perSec))
			for i := 0; i < nPods; i++ {
				<-tick.C
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%d", i), Namespace: fx.namespaces[i%len(fx.namespaces)],
					Labels: map[string]string{"app": fmt.Sprintf("a%d", i%len(fx.policies))}},
					Status: corev1.PodStatus{PodIP: fmt.Sprintf("10.0.%d.%d", i/250, i%250)}}
				_, err := cs.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			tick.Stop()
			emitted := time.Since(start)
			backlogAtEnd := int64(nPods) - handled.Load()

			// Wait for the handler to drain.
			deadline := time.Now().Add(25 * time.Minute)
			for handled.Load() < int64(nPods) && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			mu.Lock()
			drained := lastHandled.Sub(start)
			mu.Unlock()
			n := handled.Load()
			perEvent := time.Duration(busy.Load()) / time.Duration(max(n, 1))

			t.Logf("%s: %d pod events emitted over %.1fs (%d/s); handler finished %d of them %.1fs after the first was emitted"+
				" -> %.0f ms of handler work per event (max %.1f events/s on one goroutine); backlog when emission ended: %d; lag of the last event: %.1fs",
				v.name, nPods, emitted.Seconds(), perSec, n, drained.Seconds(),
				float64(perEvent)/float64(time.Millisecond), float64(time.Second)/float64(max(perEvent, 1)), backlogAtEnd, (drained - emitted).Seconds())
			require.Equal(t, int64(nPods), n, "handler did not drain within the deadline")
			if v.name == "indexed" {
				// The fix must keep up with the emission rate: everything handled within one extra second.
				require.Less(t, (drained - emitted).Seconds(), 1.0, "indexed handler fell behind the emission rate")
			}
		})
	}
}
