//go:build e2e

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

package startup

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// churnLabel marks the pods created by the pod churn spec.
const churnLabel = "networking.k8s.aws/npc-startup-scale-churn"

// expectPodsResolvedUnderChurn checks that the pod event handler keeps up with steady pod churn once the controller is
// running (issue #288). Every pod event is resolved on the pod informer's single event-delivery goroutine, so a
// handler that is slower than the event rate builds a backlog that only grows: new pods stop reaching their
// PolicyEndpoints while reconciles, the work queue, the lease and the health checks all look healthy.
//
// The spec first measures how long probe pods take to reach their policy's PolicyEndpoints with no churn: one pod
// selected by the policy's podSelector and one selected by its ingress peer, for NPC_CHURN_PROBES policies. That time
// depends on the workload (a pod that hundreds of policies select waits for all of their PolicyEndpoint writes at the
// controller's API client rate limit) and is not what this spec is about. It then churns pods in a namespace no
// generated policy selects, so the churn pods cost the handler the same as any other pod event (each is checked
// against every policy with a namespaceSelector peer) but change no PolicyEndpoint. After NPC_CHURN_WARMUP it repeats
// the same probes and fails if their median is more than NPC_CHURN_MAX_DELAY above the median without churn, or if a
// probe takes NPC_CHURN_MAX_DELAY longer than the slowest probe without churn. Medians, because a single probe's time
// varies with where its policies land in the work queue.
func expectPodsResolvedUnderChurn(ctx context.Context) {
	if cfg.churnRate <= 0 {
		Skip("NPC_CHURN_RATE is 0")
	}
	var leader string
	Eventually(func() (string, error) {
		pods, err := controllerPods(ctx)
		leader, _ = newPodLeader(ctx, pods)
		return leader, err
	}).WithTimeout(2*time.Minute).WithPolling(pollInterval).ShouldNot(BeEmpty(), "no controller pod holds the leader lease")

	By(fmt.Sprintf("probing %d policies without churn", cfg.churnProbes))
	idle := runProbes(ctx, "idle", idleProbeTimeout)
	Expect(idle.failures).To(BeEmpty(), "new pods did not reach their PolicyEndpoints even without pod churn")

	By(fmt.Sprintf("churning %d pods per second in namespace %s, each living %s", cfg.churnRate, churnNamespace(),
		cfg.churnPodLifetime))
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: churnNamespace()},
	}))).To(Succeed())
	DeferCleanup(func(ctx context.Context) {
		By("deleting the churn namespace")
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: churnNamespace()},
		}))).To(Succeed())
	})
	before, err := scrapeControllerMetrics(leader)
	Expect(err).NotTo(HaveOccurred())
	churn := startPodChurn()
	DeferCleanup(churn.stop)

	By(fmt.Sprintf("waiting %s for a slow pod event handler to fall behind", cfg.churnWarmup))
	time.Sleep(cfg.churnWarmup)

	By(fmt.Sprintf("probing the same policies while pods churn, allowing %s more than without churn", cfg.churnMaxDelay))
	churning := runProbes(ctx, "churn", idle.percentile(100)+cfg.churnMaxDelay)
	churned := churn.stop()

	after, err := scrapeControllerMetrics(leader)
	Expect(err).NotTo(HaveOccurred())
	pods, err := controllerPods(ctx)
	Expect(err).NotTo(HaveOccurred())
	for i := range pods {
		Expect(restartMessage(&pods[i])).To(BeEmpty(), "controller pod %s restarted during pod churn", pods[i].Name)
	}

	AddReportEntry("pod churn", fmt.Sprintf("scenario=%s churnPods=%d rate=%.1f/s lifetime=%s probes=%d "+
		"idle(p50=%s max=%s) churn(resolved=%d p50=%s max=%s) controllerCPU=%.2f cores",
		cfg.scenario, churned.created, float64(churned.created)/churned.duration.Seconds(), cfg.churnPodLifetime,
		len(idle.latencies), idle.percentile(50), idle.percentile(100), len(churning.latencies),
		churning.percentile(50), churning.percentile(100), (after.cpuSeconds-before.cpuSeconds)/churned.duration.Seconds()))
	Expect(churning.failures).To(BeEmpty(), "new pods did not reach their PolicyEndpoints while pods churned")
	Expect(churning.percentile(50)).To(BeNumerically("<=", idle.percentile(50)+cfg.churnMaxDelay),
		"pod churn delayed the median pod to PolicyEndpoint time by more than %s", cfg.churnMaxDelay)
}

// idleProbeTimeout bounds a probe without churn. It only guards against a controller that never writes the
// PolicyEndpoint; the spec's actual check is the delay churn adds on top of the times without churn.
const idleProbeTimeout = 5 * time.Minute

type probeKey struct {
	probe int
	kind  probeKind
}

type probeResults struct {
	latencies map[probeKey]time.Duration
	failures  []string
}

func (r probeResults) percentile(q int) time.Duration {
	sorted := make([]time.Duration, 0, len(r.latencies))
	for _, l := range r.latencies {
		sorted = append(sorted, l)
	}
	slices.Sort(sorted)
	return percentile(sorted, q)
}

// runProbes runs a target and a peer probe for each of cfg.churnProbes policies, each allowed timeout.
func runProbes(ctx context.Context, phase string, timeout time.Duration) probeResults {
	res := probeResults{latencies: map[probeKey]time.Duration{}}
	for p := 0; p < cfg.churnProbes; p++ {
		i := probePolicy(p)
		for _, kind := range []probeKind{probeTarget, probePeer} {
			key := probeKey{probe: p, kind: kind}
			latency, err := probeLatency(ctx, i, phase, p, kind, timeout)
			if err != nil {
				res.failures = append(res.failures, err.Error())
				continue
			}
			GinkgoWriter.Printf("%s %s probe %d for %s reached its PolicyEndpoint in %s\n", phase, kind, p, policyKey(i),
				latency.Round(100*time.Millisecond))
			res.latencies[key] = latency
		}
	}
	return res
}

func churnNamespace() string {
	return cfg.namespace + "-churn"
}

type probeKind string

const (
	// probeTarget is a pod that the policy selects through its podSelector, so it must appear in the policy's
	// podSelectorEndpoints.
	probeTarget probeKind = "target"
	// probePeer is a pod that the policy's ingress peer selects, so it must appear in the policy's ingress endpoints.
	probePeer probeKind = "peer"
)

// probePolicy returns the index of the NetworkPolicy probed by probe p. Probes are spread over the policies and,
// when the workload has them, use policies whose ingress peer has a namespaceSelector, which is the path the pod
// handler pays for on every event.
func probePolicy(p int) int {
	start := (p*7919 + 13) % cfg.policies
	for k := 0; k < cfg.policies; k++ {
		i := (start + k) % cfg.policies
		if !isANP(i) && (usesNamespaceSelector(i) || cfg.workload.nsSelectorPercent == 0) {
			return i
		}
	}
	Fail("the workload has no NetworkPolicy to probe; NPC_SCALE_ANP_PERCENT must be below 100")
	return 0
}

func policyKey(i int) types.NamespacedName {
	return types.NamespacedName{Namespace: testNamespace(i), Name: fmt.Sprintf("scale-policy-%d", i)}
}

// probePod builds the probe pod of the given kind for policy i: for a target, a pod with the policy's app label in
// its namespace; for a peer, a pod with the ingress peer's app label in a namespace the peer selects.
func probePod(i int, phase string, p int, kind probeKind) *corev1.Pod {
	ns, app := testNamespace(i), appLabel(i)
	if kind == probePeer {
		app = nextApp(i, 1)
		if usesNamespaceSelector(i) {
			// Namespace n belongs to group n % namespaceGroups; pick the first one in the peer's group.
			ns = testNamespace((i + 1) % cfg.workload.namespaceGroups)
		}
	}
	// Every probe pod of a run gets its own kwok node, and so its own pod CIDR: a churn probe can never reuse the IP
	// of an idle probe whose deletion a slow controller has not processed yet, which would look like a pass.
	slot := 0
	if phase != "idle" {
		slot += 2
	}
	if kind == probePeer {
		slot++
	}
	pod := newKwokPod(cfg.pods + slot*cfg.churnProbes + p)
	pod.Name = fmt.Sprintf("scale-probe-%s-%s-%d", phase, kind, p)
	pod.Namespace = ns
	pod.Labels = map[string]string{appLabelKey: app, scaleTestLabel: "true"}
	return pod
}

// probeLatency creates the probe pod, waits for its IP, and returns how long the IP took to appear in the
// PolicyEndpoints of policy i, failing after timeout. The pod is deleted afterwards.
func probeLatency(ctx context.Context, i int, phase string, p int, kind probeKind, timeout time.Duration) (time.Duration, error) {
	// Resolve which PolicyEndpoints to watch before the probe exists, so the time below only covers the controller.
	names, err := policyEndpointNames(ctx, policyKey(i))
	if err != nil {
		return 0, fmt.Errorf("finding the PolicyEndpoints of %s: %w", policyKey(i), err)
	}
	pod := probePod(i, phase, p, kind)
	if err := k8sClient.Create(ctx, pod); err != nil {
		return 0, fmt.Errorf("creating %s probe %d: %w", kind, p, err)
	}
	defer func() {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))
	}()

	var ip string
	Eventually(func() (string, error) {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)
		ip = pod.Status.PodIP
		return ip, err
	}).WithTimeout(time.Minute).WithPolling(200*time.Millisecond).ShouldNot(BeEmpty(),
		"kwok did not assign an IP to probe pod %s", client.ObjectKeyFromObject(pod))

	start := time.Now()
	deadline := start.Add(timeout)
	for polls := 1; time.Now().Before(deadline); polls++ {
		found, err := policyEndpointsContain(ctx, policyKey(i).Namespace, names, ip, kind)
		if err != nil {
			GinkgoWriter.Printf("reading the PolicyEndpoints of %s: %v\n", policyKey(i), err)
		}
		if found {
			return time.Since(start), nil
		}
		time.Sleep(500 * time.Millisecond)
		// The controller adds a PolicyEndpoint when a policy's endpoints outgrow a chunk; pick it up.
		if polls%20 == 0 {
			if refreshed, err := policyEndpointNames(ctx, policyKey(i)); err == nil {
				names = refreshed
			}
		}
	}
	return 0, fmt.Errorf("%s probe %s (IP %s) was not in the PolicyEndpoints of %s after %s", kind,
		client.ObjectKeyFromObject(pod), ip, policyKey(i), timeout.Round(100*time.Millisecond))
}

// policyEndpointNames returns the names of the PolicyEndpoints owned by the policy. It lists metadata only: a full
// list returns every endpoint of every policy in the namespace, which takes tens of seconds when one namespace holds
// all of the workload's policies.
func policyEndpointNames(ctx context.Context, policy types.NamespacedName) ([]string, error) {
	pes := &metav1.PartialObjectMetadataList{}
	pes.SetGroupVersionKind(policyinfo.GroupVersion.WithKind("PolicyEndpointList"))
	if err := k8sClient.List(ctx, pes, client.InNamespace(policy.Namespace)); err != nil {
		return nil, err
	}
	var names []string
	for _, pe := range pes.Items {
		for _, ref := range pe.OwnerReferences {
			if ref.Kind == "NetworkPolicy" && ref.Name == policy.Name {
				names = append(names, pe.Name)
			}
		}
	}
	return names, nil
}

// policyEndpointsContain reports whether any of the named PolicyEndpoints lists ip as a selected pod (target) or as
// an ingress peer (peer).
func policyEndpointsContain(ctx context.Context, namespace string, names []string, ip string, kind probeKind) (bool, error) {
	for _, name := range names {
		pe := &policyinfo.PolicyEndpoint{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pe); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if kind == probeTarget {
			for _, ep := range pe.Spec.PodSelectorEndpoints {
				if string(ep.PodIP) == ip {
					return true, nil
				}
			}
			continue
		}
		for _, ep := range pe.Spec.Ingress {
			// Peer pod IPs are written without a prefix length.
			if string(ep.CIDR) == ip || strings.TrimSuffix(string(ep.CIDR), "/32") == ip {
				return true, nil
			}
		}
	}
	return false, nil
}

type churnResult struct {
	created  int
	duration time.Duration
}

type podChurn struct {
	stopCh  chan struct{}
	done    chan churnResult
	once    sync.Once
	stopped churnResult
}

// startPodChurn creates cfg.churnRate pods per second in the churn namespace and deletes each one once it has lived
// cfg.churnPodLifetime. The pods carry no label that a generated policy selects.
func startPodChurn() *podChurn {
	c := &podChurn{stopCh: make(chan struct{}), done: make(chan churnResult, 1)}
	go func() {
		defer GinkgoRecover()
		ctx := context.Background()
		type live struct {
			pod     *corev1.Pod
			created time.Time
		}
		var (
			alive []live
			n     int
			start = time.Now()
		)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.stopCh:
				c.done <- churnResult{created: n, duration: time.Since(start)}
				return
			case <-ticker.C:
			}
			for k := 0; k < cfg.churnRate; k++ {
				pod := newKwokPod(n)
				pod.Name = fmt.Sprintf("scale-churn-%d", n)
				pod.Namespace = churnNamespace()
				pod.Labels = map[string]string{appLabelKey: "churn", churnLabel: "true"}
				n++
				if err := k8sClient.Create(ctx, pod); err != nil {
					GinkgoWriter.Printf("creating churn pod %s: %v\n", pod.Name, err)
					continue
				}
				alive = append(alive, live{pod: pod, created: time.Now()})
			}
			for len(alive) > 0 && time.Since(alive[0].created) >= cfg.churnPodLifetime {
				if err := client.IgnoreNotFound(k8sClient.Delete(ctx, alive[0].pod, client.GracePeriodSeconds(0))); err != nil {
					GinkgoWriter.Printf("deleting churn pod %s: %v\n", alive[0].pod.Name, err)
				}
				alive = alive[1:]
			}
		}
	}()
	return c
}

// stop ends the churn and returns how many pods it created over how long. It is safe to call more than once.
func (c *podChurn) stop() churnResult {
	c.once.Do(func() {
		close(c.stopCh)
		c.stopped = <-c.done
	})
	return c.stopped
}

// percentile returns the q-th percentile of sorted durations, or 0 when there are none.
func percentile(sorted []time.Duration, q int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (len(sorted)*q + 99) / 100
	idx = min(max(idx-1, 0), len(sorted)-1)
	return sorted[idx].Round(100 * time.Millisecond)
}
