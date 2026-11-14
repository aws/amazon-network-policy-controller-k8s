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
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

const (
	// idleProbeTimeout bounds a probe without churn. It only catches a controller that never writes the
	// PolicyEndpoint; the spec's check is how much churn adds to the times measured without it.
	idleProbeTimeout = 2 * time.Minute
	// churnProbeGrace is how long a churn probe keeps waiting past its bound, so that the spec reports how late a
	// slow probe was instead of only that it missed the bound.
	churnProbeGrace = time.Minute
	// minChurnRateFraction is the fraction of NPC_CHURN_RATE the churn must reach for the result to count.
	minChurnRateFraction = 0.8
	// churnWorkers create and delete churn pods concurrently, so that API latency does not lower the churn rate.
	churnWorkers = 8
	// namespaceDeleteTimeout bounds the wait for the churn namespace to be deleted.
	namespaceDeleteTimeout = 5 * time.Minute
)

// expectPodsResolvedUnderChurn checks that new pods keep reaching their PolicyEndpoints while pods churn, once the
// controller is running (issue #288). The pod event handler resolves every pod event on the pod informer's single
// event-delivery goroutine, and checks each one against every policy with a namespaceSelector peer. A handler slower
// than the event rate builds a backlog that only grows, and new pods stop reaching their PolicyEndpoints while
// reconciles, the work queue, the lease and the health checks all look healthy.
//
// The spec creates a probe NetworkPolicy in its own namespace, with a namespaceSelector ingress peer that selects a
// second probe namespace, and measures how long probe pods take to appear in its PolicyEndpoints without churn: a
// target pod selected by its podSelector and a peer pod, in the other namespace, selected by its ingress peer. No other
// policy selects either pod, so that time is the pod event handling plus one reconcile, whatever the workload. It
// then churns pods in a namespace that no generated policy selects: each churn pod costs the pod event handler the
// same as any other pod event but changes no PolicyEndpoint. After NPC_CHURN_WARMUP it repeats the probes, and fails
// if the slowest one is more than NPC_CHURN_MAX_DELAY slower than the slowest without churn, if the median rises by
// more than NPC_CHURN_MAX_DELAY, if the churn did not reach minChurnRateFraction of NPC_CHURN_RATE, or if a controller
// pod restarted or was replaced.
func expectPodsResolvedUnderChurn(ctx context.Context) {
	if cfg.churnRate <= 0 {
		Skip("NPC_CHURN_RATE is 0")
	}
	leader := waitForLeader(ctx)

	By("creating the probe policy")
	createProbePolicy(ctx)
	DeferCleanup(deleteProbeNamespaces)

	By(fmt.Sprintf("probing the probe policy %d times without pod churn", cfg.churnProbes))
	idle := runProbes(ctx, "idle", idleProbeTimeout, true)
	Expect(idle.failures).To(BeEmpty(), "new pods did not reach their PolicyEndpoints even without pod churn")

	controllers := controllerPodRestarts(ctx)
	By(fmt.Sprintf("churning %d pods per second in namespace %s, each living %s", cfg.churnRate, churnNamespace(),
		cfg.churnPodLifetime))
	createNamespace(ctx, churnNamespace(), nil)
	DeferCleanup(func() { deleteNamespace(churnNamespace(), churnLabel) })
	churn := startPodChurn()
	DeferCleanup(func() { churn.stop() })
	before := scrapeLeaderCPU(leader)

	By(fmt.Sprintf("waiting %s for a slow pod event handler to fall behind", cfg.churnWarmup))
	sleepWithContext(ctx, cfg.churnWarmup)

	bound := idle.max() + cfg.churnMaxDelay
	By(fmt.Sprintf("probing the probe policy while pods churn, allowing each probe %s", roundDuration(bound)))
	churning := runProbes(ctx, "churn", bound+churnProbeGrace, false)
	churned := churn.stop()

	after := scrapeLeaderCPU(leader)
	leaderAfter := waitForLeader(ctx)
	cpu := "unavailable"
	if before.err == nil && after.err == nil && leaderAfter == leader {
		cpu = fmt.Sprintf("%.2f cores", (after.seconds-before.seconds)/after.at.Sub(before.at).Seconds())
	}
	AddReportEntry("pod churn", fmt.Sprintf("scenario=%s churnPods=%d churnFailures=%d rate=%.1f/s lifetime=%s "+
		"idle(probes=%d p50=%s max=%s) churn(resolved=%d of %d, p50=%s max=%s, bound=%s) leader=%s leaderCPU=%s",
		cfg.scenario, churned.created, churned.failed, churned.rate(), cfg.churnPodLifetime, len(idle.latencies),
		roundDuration(idle.median()), roundDuration(idle.max()), len(churning.latencies), 2*cfg.churnProbes,
		roundDuration(churning.median()), roundDuration(churning.max()), roundDuration(bound), leaderAfter, cpu))

	Expect(controllerPodRestarts(ctx)).To(Equal(controllers),
		"controller pods restarted or were replaced during pod churn")
	Expect(churned.rate()).To(BeNumerically(">=", minChurnRateFraction*float64(cfg.churnRate)),
		"pod churn reached %.1f pods/s, below %.0f%% of NPC_CHURN_RATE=%d (%d creates failed), so the result "+
			"does not count",
		churned.rate(), 100*minChurnRateFraction, cfg.churnRate, churned.failed)
	Expect(churning.failures).To(BeEmpty(), "new pods did not reach their PolicyEndpoints while pods churned")
	Expect(churning.max()).To(BeNumerically("<=", bound),
		"pod churn made the slowest probe take %s, more than the slowest without churn (%s) plus NPC_CHURN_MAX_DELAY",
		roundDuration(churning.max()), roundDuration(idle.max()))
	Expect(churning.median()).To(BeNumerically("<=", idle.median()+cfg.churnMaxDelay),
		"pod churn raised the median pod to PolicyEndpoint time from %s to %s, more than NPC_CHURN_MAX_DELAY",
		roundDuration(idle.median()), roundDuration(churning.median()))
}

// waitForLeader returns the controller pod that holds the leader lease. A new leader can take a few seconds to
// acquire it after the startup spec ends.
func waitForLeader(ctx context.Context) string {
	var leader string
	Eventually(func() (string, error) {
		pods, err := controllerPods(ctx)
		leader, _ = newPodLeader(ctx, pods)
		return leader, err
	}).WithTimeout(2*time.Minute).WithPolling(pollInterval).ShouldNot(BeEmpty(),
		"no controller pod holds the leader lease")
	return leader
}

// controllerPodRestarts returns the restart count of every controller pod that is not being deleted, keyed by UID, so
// that a comparison catches both a restart and a replaced pod.
func controllerPodRestarts(ctx context.Context) map[types.UID]int32 {
	pods, err := controllerPods(ctx)
	Expect(err).NotTo(HaveOccurred())
	restarts := map[types.UID]int32{}
	for _, pod := range pods {
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		restarts[pod.UID] = 0
		for _, cs := range pod.Status.ContainerStatuses {
			restarts[pod.UID] += cs.RestartCount
		}
	}
	return restarts
}

type cpuSample struct {
	seconds float64
	at      time.Time
	err     error
}

// scrapeLeaderCPU reads the leader's process CPU time. It is reported only, so a failed scrape is not an error.
func scrapeLeaderCPU(leader string) cpuSample {
	m, err := scrapeControllerMetrics(leader)
	if err != nil {
		GinkgoWriter.Printf("unable to read the metrics of %s: %v\n", leader, err)
	}
	return cpuSample{seconds: m.cpuSeconds, at: time.Now(), err: err}
}

func sleepWithContext(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func roundDuration(d time.Duration) time.Duration {
	return d.Round(100 * time.Millisecond)
}

// churnNamespace, probeNamespace and probePeerNamespace are outside the test namespace prefix (see isTestNamespace),
// since they hold no workload, and carry no label that a generated namespaceSelector selects. In particular they do not
// carry scaleTestLabel, which the broad policies select.
func churnNamespace() string {
	return "churn-" + cfg.namespace
}

func probeNamespace() string {
	return "probe-" + cfg.namespace
}

func probePeerNamespace() string {
	return "probe-peer-" + cfg.namespace
}

const (
	probePolicyName = "churn-probe"
	probeTargetApp  = "churn-probe-target"
	probePeerApp    = "churn-probe-peer"
)

// createNamespace creates the namespace once any previous one, from an earlier scenario or an interrupted run, is
// gone, since pods cannot be created in a namespace that is being deleted.
func createNamespace(ctx context.Context, name string, nsLabels map[string]string) {
	waitForNamespaceDeleted(ctx, name)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: nsLabels}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
}

// deleteNamespace deletes the pods carrying podLabel without a grace period, removes the finalizers of the
// NetworkPolicies, deletes the namespace and waits for it to be gone, so that the next scenario can create it again.
// It runs even when NPC_SCALE_KEEP_RESOURCES is set: these namespaces hold only what the pod churn spec created, which
// the next run does not reuse.
func deleteNamespace(name, podLabel string) {
	ctx := context.Background() // the spec's context is done by the time cleanup runs
	By("deleting namespace " + name)
	Expect(client.IgnoreNotFound(k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(name),
		client.MatchingLabels{podLabel: "true"}, client.GracePeriodSeconds(0)))).To(Succeed())
	policies := &networking.NetworkPolicyList{}
	Expect(k8sClient.List(ctx, policies, client.InNamespace(name))).To(Succeed())
	for i := range policies.Items {
		policy := &policies.Items[i]
		patch := client.MergeFrom(policy.DeepCopy())
		policy.SetFinalizers(nil)
		Expect(client.IgnoreNotFound(k8sClient.Patch(ctx, policy, patch))).To(Succeed())
	}
	Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}))).To(Succeed())
	waitForNamespaceDeleted(ctx, name)
}

func waitForNamespaceDeleted(ctx context.Context, name string) {
	Eventually(func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &corev1.Namespace{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}).WithContext(ctx).WithTimeout(namespaceDeleteTimeout).WithPolling(pollInterval).Should(BeTrue(),
		"namespace %s was not deleted within %s", name, namespaceDeleteTimeout)
}

// createProbePolicy creates the probe namespaces and the probe NetworkPolicy: in probeNamespace, it selects app
// probeTargetApp pods and accepts ingress from app probePeerApp pods in namespaces labelled probeLabel, which only
// probePeerNamespace is. It waits for the policy's first PolicyEndpoint.
func createProbePolicy(ctx context.Context) {
	createNamespace(ctx, probeNamespace(), nil)
	createNamespace(ctx, probePeerNamespace(), map[string]string{probeLabel: "true"})
	policy := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: probePolicyName, Namespace: probeNamespace()},
		Spec: networking.NetworkPolicySpec{
			PodSelector: *appSelector(probeTargetApp),
			PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress},
			Ingress: []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{probeLabel: "true"}},
				PodSelector:       appSelector(probePeerApp),
			}}}},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	Eventually(func(ctx context.Context) ([]string, error) {
		return policyEndpointNames(ctx, probePolicyKey())
	}).WithContext(ctx).WithTimeout(2*time.Minute).WithPolling(time.Second).ShouldNot(BeEmpty(),
		"the probe policy has no PolicyEndpoint")
}

func deleteProbeNamespaces() {
	deleteNamespace(probeNamespace(), probeLabel)
	deleteNamespace(probePeerNamespace(), probeLabel)
}

func probePolicyKey() types.NamespacedName {
	return types.NamespacedName{Namespace: probeNamespace(), Name: probePolicyName}
}

type probeKind string

const (
	// probeTarget is a pod that the probe policy selects through its podSelector; it must appear in the policy's
	// podSelectorEndpoints.
	probeTarget probeKind = "target"
	// probePeer is a pod in another namespace that the probe policy's ingress peer selects; it must appear in the
	// policy's ingress endpoints. Resolving it goes through the policies with namespace references.
	probePeer probeKind = "peer"
)

// probePod builds probe p of the given kind and phase. Each probe pod of a run gets its own kwok node, and so its own
// pod CIDR, so that a churn probe cannot be assigned the IP of an idle probe whose deletion a slow controller has not
// processed yet.
func probePod(phase string, p int, kind probeKind) *corev1.Pod {
	ns, app, slot := probeNamespace(), probeTargetApp, p
	if kind == probePeer {
		ns, app, slot = probePeerNamespace(), probePeerApp, p+cfg.churnProbes
	}
	if phase != "idle" {
		slot += 2 * cfg.churnProbes
	}
	pod := newKwokPod(cfg.pods + slot)
	pod.Name = fmt.Sprintf("probe-%s-%s-%d", phase, kind, p)
	pod.Namespace = ns
	pod.Labels = map[string]string{appLabelKey: app, probeLabel: "true"}
	return pod
}

type probeResults struct {
	latencies []time.Duration // sorted
	failures  []string
}

func (r probeResults) median() time.Duration { return nearestRank(r.latencies, 50) }

func (r probeResults) max() time.Duration { return nearestRank(r.latencies, 100) }

// nearestRank returns the q-th percentile of sorted, or 0 when it is empty.
func nearestRank(sorted []time.Duration, q int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (len(sorted)*q + 99) / 100
	return sorted[min(max(idx-1, 0), len(sorted)-1)]
}

// runProbes runs cfg.churnProbes target and peer probes, each allowed timeout, and stops at the first failure when
// stopAtFirstFailure is set.
func runProbes(ctx context.Context, phase string, timeout time.Duration, stopAtFirstFailure bool) probeResults {
	var res probeResults
	for p := 0; p < cfg.churnProbes; p++ {
		for _, kind := range []probeKind{probeTarget, probePeer} {
			latency, err := probeLatency(ctx, phase, p, kind, timeout)
			if err != nil {
				res.failures = append(res.failures, err.Error())
				if stopAtFirstFailure {
					return res
				}
				continue
			}
			GinkgoWriter.Printf("%s %s probe %d reached the probe policy's PolicyEndpoints in %s\n", phase, kind, p,
				roundDuration(latency))
			res.latencies = append(res.latencies, latency)
		}
	}
	slices.Sort(res.latencies)
	return res
}

// probeLatency creates the probe pod, waits for its IP, and returns how long the IP took to appear in the probe
// policy's PolicyEndpoints, or an error after timeout. The pod is deleted afterwards.
func probeLatency(ctx context.Context, phase string, p int, kind probeKind, timeout time.Duration) (time.Duration,
	error) {
	// Find the PolicyEndpoints to watch before the probe exists, so the measured time only covers the controller.
	policy := probePolicyKey()
	names, err := policyEndpointNames(ctx, policy)
	if err != nil {
		return 0, fmt.Errorf("finding the PolicyEndpoints of %s: %w", policy, err)
	}
	pod := probePod(phase, p, kind)
	key := client.ObjectKeyFromObject(pod)
	// A probe pod left by an interrupted run would make Create fail.
	if err := deletePodAndWait(ctx, key); err != nil {
		return 0, err
	}
	if err := k8sClient.Create(ctx, pod); err != nil {
		return 0, fmt.Errorf("creating %s probe %s: %w", kind, key, err)
	}
	defer func() {
		_ = client.IgnoreNotFound(k8sClient.Delete(context.Background(), pod, client.GracePeriodSeconds(0)))
	}()

	ip, err := waitForPodIP(ctx, key)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	for polls := 1; time.Since(start) < timeout && ctx.Err() == nil; polls++ {
		found, err := policyEndpointsContain(ctx, policy.Namespace, names, ip, kind)
		if err != nil {
			GinkgoWriter.Printf("reading the PolicyEndpoints of %s: %v\n", policy, err)
		}
		if found {
			return time.Since(start), nil
		}
		sleepWithContext(ctx, 500*time.Millisecond)
		// The controller adds a PolicyEndpoint when a policy's endpoints outgrow a chunk.
		if polls%20 == 0 {
			if refreshed, err := policyEndpointNames(ctx, policy); err == nil {
				names = refreshed
			}
		}
	}
	return 0, fmt.Errorf("%s probe %s (IP %s) was not in the PolicyEndpoints of %s within %s", kind, key, ip,
		policy, roundDuration(timeout))
}

// deletePodAndWait deletes the pod without a grace period if it exists, and waits until it is gone.
func deletePodAndWait(ctx context.Context, key types.NamespacedName) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if err := client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))); err != nil {
		return fmt.Errorf("deleting pod %s: %w", key, err)
	}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); sleepWithContext(ctx, time.Second) {
		if err := k8sClient.Get(ctx, key, &corev1.Pod{}); apierrors.IsNotFound(err) {
			return nil
		}
	}
	return fmt.Errorf("pod %s was not deleted within 1m", key)
}

func waitForPodIP(ctx context.Context, key types.NamespacedName) (string, error) {
	deadline := time.Now().Add(time.Minute)
	for ; time.Now().Before(deadline); sleepWithContext(ctx, 200*time.Millisecond) {
		pod := &corev1.Pod{}
		if err := k8sClient.Get(ctx, key, pod); err == nil && pod.Status.PodIP != "" {
			return pod.Status.PodIP, nil
		}
	}
	return "", fmt.Errorf("kwok did not assign an IP to probe pod %s within 1m", key)
}

// policyEndpointNames returns the names of the PolicyEndpoints that the NetworkPolicy owns. It lists metadata only.
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

// policyEndpointsContain reports whether one of the named PolicyEndpoints lists ip as a selected pod (target) or as an
// ingress peer (peer). Pod IPs are written without a prefix length.
func policyEndpointsContain(ctx context.Context, namespace string, names []string, ip string,
	kind probeKind) (bool, error) {
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
			if string(ep.CIDR) == ip {
				return true, nil
			}
		}
	}
	return false, nil
}

type churnResult struct {
	created  int64
	failed   int64
	duration time.Duration
}

func (r churnResult) rate() float64 {
	if r.duration <= 0 {
		return 0
	}
	return float64(r.created) / r.duration.Seconds()
}

type churnPod struct {
	pod     *corev1.Pod
	created time.Time
}

type podChurn struct {
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	start   time.Time
	created atomic.Int64
	failed  atomic.Int64
	once    sync.Once
	result  churnResult
}

// startPodChurn creates cfg.churnRate pods per second in the churn namespace and deletes each one after
// cfg.churnPodLifetime. Creates and deletes run on churnWorkers goroutines each, paced by a rate limiter, so that the
// rate holds when API requests are slow. A create that fails is counted; the spec checks the rate actually reached.
func startPodChurn() *podChurn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &podChurn{cancel: cancel, start: time.Now()}
	limiter := rate.NewLimiter(rate.Limit(cfg.churnRate), 1)
	alive := make(chan churnPod, cfg.churnRate*(int(cfg.churnPodLifetime.Seconds())+2))
	var next atomic.Int64
	for w := 0; w < churnWorkers; w++ {
		c.wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer c.wg.Done()
			for limiter.Wait(ctx) == nil {
				pod := newChurnPod(int(next.Add(1) - 1))
				if err := k8sClient.Create(ctx, pod); err != nil {
					if ctx.Err() == nil {
						c.failed.Add(1)
						GinkgoWriter.Printf("creating churn pod %s: %v\n", pod.Name, err)
					}
					continue
				}
				c.created.Add(1)
				select {
				case alive <- churnPod{pod: pod, created: time.Now()}:
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() {
			defer GinkgoRecover()
			defer c.wg.Done()
			for {
				var p churnPod
				select {
				case p = <-alive:
				case <-ctx.Done():
					return
				}
				sleepWithContext(ctx, time.Until(p.created.Add(cfg.churnPodLifetime)))
				if ctx.Err() != nil {
					return
				}
				err := client.IgnoreNotFound(k8sClient.Delete(ctx, p.pod, client.GracePeriodSeconds(0)))
				if err != nil && ctx.Err() == nil {
					GinkgoWriter.Printf("deleting churn pod %s: %v\n", p.pod.Name, err)
				}
			}
		}()
	}
	return c
}

// stop ends the churn and returns how many pods it created over how long. Pods still alive are left for
// deleteNamespace. It is safe to call more than once.
func (c *podChurn) stop() churnResult {
	c.once.Do(func() {
		c.cancel()
		c.wg.Wait()
		c.result = churnResult{created: c.created.Load(), failed: c.failed.Load(), duration: time.Since(c.start)}
	})
	return c.result
}

// newChurnPod builds churn pod n. It carries churnLabel and no label that a generated policy selects.
func newChurnPod(n int) *corev1.Pod {
	pod := newKwokPod(n)
	pod.Name = fmt.Sprintf("scale-churn-%d", n)
	pod.Namespace = churnNamespace()
	pod.Labels = map[string]string{appLabelKey: "churn", churnLabel: "true"}
	return pod
}
