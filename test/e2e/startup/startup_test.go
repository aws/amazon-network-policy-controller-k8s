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
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

const (
	appLabelKey      = "app"
	leaderElectionID = "amazon-network-policy-controller-k8s"
	pollInterval     = 5 * time.Second
	leaseTestHolder  = "npc-startup-scale-test"
)

// This spec reproduces the controller-runtime startup barrier problem: controller-runtime waits for every
// initial informer event to be processed by the event handlers before it starts reconcile workers, and fails
// the controller if that takes longer than CacheSyncTimeout (2m by default). The pod event handler resolves
// every pod against every network policy in its namespace, so once the policy cache is populated the initial
// pod event pass is O(pods x policies). When it exceeds the timeout the manager exits and the pod restarts
// (CrashLoopBackOff); below the timeout the leader still holds the lease without reconciling anything.
//
// The expensive case is a leader that takes over with warm caches (a standby replica on failover, or any
// start where the policy informer syncs before the pod informer). A cold start from zero replicas usually
// wins the lease before the policy cache fills, which makes the pod pass cheap, so the spec holds the leader
// lease itself while the controller pods start and sync their caches, then releases it.
//
// The spec passes only when the controller starts without restarting, creates its first PolicyEndpoint within
// NPC_FIRST_RECONCILE_TIMEOUT of becoming leader, and produces endpoints for every policy within
// NPC_STARTUP_TIMEOUT of the lease being released. The first bound catches a leader that is stuck behind the
// startup barrier but has not yet hit CacheSyncTimeout, the second catches reconciliation that is too slow.
var _ = Describe("controller startup at scale", Ordered, func() {
	var (
		ctx              = context.Background()
		originalReplicas int32
	)

	BeforeAll(func() {
		By(fmt.Sprintf("using scenario=%s pods=%d policies=%d kwokNodes=%d namespace=%s %s",
			cfg.scenario, cfg.pods, cfg.policies, cfg.kwokNodes, cfg.namespace, cfg.workload))

		if !cfg.skipKwokInstall {
			preinstalled := kwokInstalled(ctx)
			By("installing kwok " + cfg.kwokVersion)
			Expect(installKwok(cfg.kwokVersion)).To(Succeed())
			if cfg.uninstallKwok && !preinstalled {
				DeferCleanup(func() {
					Expect(uninstallKwok(cfg.kwokVersion)).To(Succeed())
				})
			}
		}

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, controllerKey(), deploy)).To(Succeed())
		expectControllerRunsOnDataplane(ctx, deploy)

		By("scaling the network policy controller down so it starts against a fully populated cluster")
		originalReplicas = ptr.Deref(deploy.Spec.Replicas, 1)
		Expect(originalReplicas).To(BeNumerically(">", 0), "controller deployment is already scaled to zero")
		DeferCleanup(func() {
			By(fmt.Sprintf("restoring the controller to %d replicas", originalReplicas))
			Expect(scaleController(ctx, originalReplicas)).To(Succeed())
		})
		Expect(scaleController(ctx, 0)).To(Succeed())
		// Pods on unreachable nodes can stay Terminating indefinitely; only pods created after the scale up are
		// considered below, so it is enough that none are left running.
		Eventually(func() (int, error) {
			pods, err := controllerPods(ctx)
			running := 0
			for _, pod := range pods {
				if pod.DeletionTimestamp.IsZero() {
					running++
				}
			}
			return running, err
		}).WithTimeout(3 * time.Minute).WithPolling(pollInterval).Should(BeZero())

		By("creating kwok nodes")
		DeferCleanup(func() {
			if cfg.keepResources {
				return
			}
			By("deleting kwok nodes")
			Expect(deleteKwokNodes(ctx)).To(Succeed())
		})
		Expect(createConcurrently(cfg.kwokNodes, func(i int) client.Object { return newKwokNode(i) })).To(Succeed())
		Eventually(readyKwokNodes).WithContext(ctx).WithTimeout(3 * time.Minute).WithPolling(pollInterval).
			Should(Equal(cfg.kwokNodes))

		DeferCleanup(func() {
			if cfg.keepResources {
				return
			}
			deleteTestResources(ctx)
		})
		createWorkload(ctx)
		Eventually(runningPodsWithIP).WithContext(ctx).WithTimeout(20 * time.Minute).WithPolling(10 * time.Second).
			Should(Equal(cfg.pods))
	})

	It("starts and reconciles every policy without restarting", func() {
		By("holding the leader lease so the controller syncs its caches before it can become leader")
		scaleUpTime := time.Now()
		releaseLease := holdLease(ctx)
		DeferCleanup(releaseLease)

		By(fmt.Sprintf("scaling the controller up to %d replicas", originalReplicas))
		Expect(scaleController(ctx, originalReplicas)).To(Succeed())
		Eventually(func() (int, error) {
			pods, err := controllerPodsCreatedAfter(ctx, scaleUpTime)
			ready := 0
			for i := range pods {
				if isPodReady(&pods[i]) {
					ready++
				}
			}
			return ready, err
		}).WithTimeout(5 * time.Minute).WithPolling(pollInterval).Should(BeEquivalentTo(originalReplicas))
		By(fmt.Sprintf("waiting %s for the controller caches to sync", cfg.cacheWarmup))
		time.Sleep(cfg.cacheWarmup)

		By("releasing the leader lease")
		releaseLease()
		startTime := time.Now()

		var (
			expected         = cfg.workload.expectedPolicies()
			leader           string
			leaderAcquired   time.Duration
			firstReconciled  time.Duration
			allReconciled    time.Duration
			reconciledCount  int
			restartedPod     *corev1.Pod
			restartedMessage string
			idleFailure      string
			peak             controllerMetrics
		)
		deadline := startTime.Add(cfg.startupTimeout)
		for time.Now().Before(deadline) {
			elapsed := time.Since(startTime).Round(time.Second)

			pods, err := controllerPodsCreatedAfter(ctx, scaleUpTime)
			Expect(err).NotTo(HaveOccurred())
			for i := range pods {
				if msg := restartMessage(&pods[i]); msg != "" {
					restartedPod, restartedMessage = &pods[i], msg
				}
			}
			if restartedPod != nil {
				GinkgoWriter.Printf("[%s] controller pod %s restarted: %s\n", elapsed, restartedPod.Name, restartedMessage)
				break
			}

			if leaderAcquired == 0 {
				if leader = newPodLeader(ctx, pods); leader != "" {
					leaderAcquired = elapsed
					GinkgoWriter.Printf("[%s] leader lease acquired by new controller pod %s\n", elapsed, leader)
				}
			}

			reconciledCount, err = reconciledPolicies(ctx)
			Expect(err).NotTo(HaveOccurred())
			if reconciledCount > 0 && firstReconciled == 0 {
				firstReconciled = elapsed
				GinkgoWriter.Printf("[%s] first PolicyEndpoint created\n", elapsed)
			}

			progress := fmt.Sprintf("[%s] %d/%d policies have endpoints", elapsed, reconciledCount, expected)
			if leader != "" {
				if m, err := scrapeControllerMetrics(leader); err == nil {
					peak.residentBytes = max(peak.residentBytes, m.residentBytes)
					peak.heapInUseBytes = max(peak.heapInUseBytes, m.heapInUseBytes)
					peak.queueDepth = max(peak.queueDepth, m.queueDepth)
					progress += fmt.Sprintf(" reconciles=%.0f errors=%.0f queueDepth=%.0f activeWorkers=%.0f rss=%s",
						m.reconciles, m.reconcileErrors, m.queueDepth, m.activeWorkers, mebibytes(m.residentBytes))
				} else {
					progress += fmt.Sprintf(" (metrics unavailable: %v)", err)
				}
			}
			GinkgoWriter.Println(progress)

			if reconciledCount == expected {
				allReconciled = elapsed
				break
			}
			if firstReconciled == 0 {
				if leaderAcquired == 0 && elapsed > cfg.firstReconcileTimeout {
					idleFailure = fmt.Sprintf("no new controller pod acquired the leader lease within %s of it being released",
						cfg.firstReconcileTimeout)
					break
				}
				if leaderAcquired > 0 && elapsed-leaderAcquired > cfg.firstReconcileTimeout {
					idleFailure = fmt.Sprintf("leader %s held the lease for %s without creating a PolicyEndpoint",
						leader, cfg.firstReconcileTimeout)
					break
				}
			}
			time.Sleep(pollInterval)
		}

		// Timings are relative to the lease being released.
		AddReportEntry("startup timings", fmt.Sprintf(
			"scenario=%s leaderAcquired=%s firstPolicyEndpoint=%s allPolicyEndpoints=%s reconciled=%d/%d",
			cfg.scenario, leaderAcquired, firstReconciled, allReconciled, reconciledCount, expected))
		if leaderAcquired > 0 && firstReconciled > 0 {
			AddReportEntry("leader idle period", (firstReconciled - leaderAcquired).String())
		}
		AddReportEntry("leader peak usage", fmt.Sprintf("rss=%s heapInUse=%s queueDepth=%.0f",
			mebibytes(peak.residentBytes), mebibytes(peak.heapInUseBytes), peak.queueDepth))

		if restartedPod != nil {
			logs := previousLogs(restartedPod.Name)
			AddReportEntry("restarted controller logs", logs)
			Fail(fmt.Sprintf("controller pod %s restarted during startup (%s)\n%s",
				restartedPod.Name, restartedMessage, logs))
		}
		if idleFailure != "" {
			Fail(idleFailure)
		}
		Expect(reconciledCount).To(Equal(expected),
			"controller did not create endpoints for all policies within %s", cfg.startupTimeout)
	})
})

// expectControllerRunsOnDataplane fails the spec unless the deployment under test can become leader. The EKS
// control plane controller competes for the same lease while enable-network-policy-controller is "true", and
// with --enable-configmap-check=true the dataplane controller disables itself when that value is not "true".
// scripts/deploy-controller-on-dataplane.sh configures both.
func expectControllerRunsOnDataplane(ctx context.Context, deploy *appsv1.Deployment) {
	cm := &corev1.ConfigMap{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: "amazon-vpc-cni"}, cm)
	Expect(client.IgnoreNotFound(err)).To(Succeed())
	Expect(cm.Data["enable-network-policy-controller"]).NotTo(Equal("true"),
		"the EKS control plane network policy controller is enabled and holds the leader lease; "+
			"run scripts/deploy-controller-on-dataplane.sh first")
	for _, c := range deploy.Spec.Template.Spec.Containers {
		Expect(c.Args).NotTo(ContainElement("--enable-configmap-check=true"),
			"controller has --enable-configmap-check=true; run scripts/deploy-controller-on-dataplane.sh first")
	}
}

func controllerKey() types.NamespacedName {
	return types.NamespacedName{Namespace: cfg.controllerNS, Name: cfg.controllerName}
}

func scaleController(ctx context.Context, replicas int32) error {
	deploy := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, controllerKey(), deploy); err != nil {
		return err
	}
	patch := client.MergeFrom(deploy.DeepCopy())
	deploy.Spec.Replicas = ptr.To(replicas)
	return k8sClient.Patch(ctx, deploy, patch)
}

func controllerPods(ctx context.Context) ([]corev1.Pod, error) {
	deploy := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, controllerKey(), deploy); err != nil {
		return nil, err
	}
	pods := &corev1.PodList{}
	if err := k8sClient.List(ctx, pods, client.InNamespace(cfg.controllerNS),
		client.MatchingLabels(deploy.Spec.Selector.MatchLabels)); err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func controllerPodsCreatedAfter(ctx context.Context, t time.Time) ([]corev1.Pod, error) {
	pods, err := controllerPods(ctx)
	if err != nil {
		return nil, err
	}
	var created []corev1.Pod
	for _, pod := range pods {
		// creationTimestamp has second granularity.
		if !pod.CreationTimestamp.Time.Before(t.Truncate(time.Second)) {
			created = append(created, pod)
		}
	}
	return created, nil
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// holdLease takes the controller's leader lease under a test identity and keeps renewing it until the
// returned release function is called. Releasing clears the holder so a waiting candidate acquires it on
// its next retry. The release function is safe to call more than once.
func holdLease(ctx context.Context) func() {
	key := types.NamespacedName{Namespace: cfg.controllerNS, Name: leaderElectionID}
	renew := func() error {
		lease := &coordinationv1.Lease{}
		err := k8sClient.Get(ctx, key, lease)
		if apierrors.IsNotFound(err) {
			lease.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}
			lease.Spec = leaseHolderSpec()
			return k8sClient.Create(ctx, lease)
		}
		if err != nil {
			return err
		}
		if ptr.Deref(lease.Spec.HolderIdentity, "") != leaseTestHolder {
			lease.Spec.LeaseTransitions = ptr.To(ptr.Deref(lease.Spec.LeaseTransitions, 0) + 1)
		}
		transitions := lease.Spec.LeaseTransitions
		lease.Spec = leaseHolderSpec()
		lease.Spec.LeaseTransitions = transitions
		return k8sClient.Update(ctx, lease)
	}
	Eventually(renew).WithTimeout(time.Minute).WithPolling(time.Second).Should(Succeed())

	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(stopped)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := renew(); err != nil {
					GinkgoWriter.Printf("failed to renew test lease: %v\n", err)
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-stopped
			Eventually(func() error {
				lease := &coordinationv1.Lease{}
				if err := k8sClient.Get(ctx, key, lease); err != nil {
					return client.IgnoreNotFound(err)
				}
				if ptr.Deref(lease.Spec.HolderIdentity, "") != leaseTestHolder {
					return nil
				}
				lease.Spec.HolderIdentity = ptr.To("")
				return k8sClient.Update(ctx, lease)
			}).WithTimeout(time.Minute).WithPolling(time.Second).Should(Succeed())
		})
	}
}

func leaseHolderSpec() coordinationv1.LeaseSpec {
	now := metav1.NewMicroTime(time.Now())
	return coordinationv1.LeaseSpec{
		HolderIdentity:       ptr.To(leaseTestHolder),
		LeaseDurationSeconds: ptr.To[int32](15),
		AcquireTime:          &now,
		RenewTime:            &now,
	}
}

// restartMessage returns a description of the last container termination if the pod has restarted.
func restartMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount == 0 {
			continue
		}
		msg := fmt.Sprintf("restarts=%d", cs.RestartCount)
		if t := cs.LastTerminationState.Terminated; t != nil {
			msg += fmt.Sprintf(" reason=%s exitCode=%d runtime=%s", t.Reason, t.ExitCode,
				t.FinishedAt.Sub(t.StartedAt.Time).Round(time.Second))
		}
		if w := cs.State.Waiting; w != nil {
			msg += " waiting=" + w.Reason
		}
		return msg
	}
	return ""
}

func previousLogs(podName string) string {
	out, err := kubectlOutput("-n", cfg.controllerNS, "logs", podName, "--previous", "--tail=30")
	if err != nil {
		return fmt.Sprintf("unable to fetch previous logs: %v\n%s", err, out)
	}
	return out
}

// newPodLeader returns the name of the pod in pods that holds the leader lease, or "" if none does.
// controller-runtime sets the holder identity to <hostname>_<uuid>, and a pod's hostname is its name.
func newPodLeader(ctx context.Context, pods []corev1.Pod) string {
	lease := &coordinationv1.Lease{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: cfg.controllerNS, Name: leaderElectionID}, lease); err != nil {
		return ""
	}
	holder, _, _ := strings.Cut(ptr.Deref(lease.Spec.HolderIdentity, ""), "_")
	for _, pod := range pods {
		if holder == pod.Name {
			return pod.Name
		}
	}
	return ""
}

// reconciledPolicies returns the number of test policies, of any kind, that have at least one PolicyEndpoint or
// ClusterPolicyEndpoint. Endpoints are counted by their controller owner reference, using metadata-only lists so
// polling stays cheap when there are thousands of large endpoint objects.
func reconciledPolicies(ctx context.Context) (int, error) {
	owners := make(map[types.UID]struct{})

	pes := &metav1.PartialObjectMetadataList{}
	pes.SetGroupVersionKind(policyinfo.GroupVersion.WithKind("PolicyEndpointList"))
	if err := k8sClient.List(ctx, pes); err != nil {
		return 0, err
	}
	for _, pe := range pes.Items {
		if !isTestNamespace(pe.Namespace) {
			continue
		}
		for _, ref := range pe.OwnerReferences {
			owners[ref.UID] = struct{}{}
		}
	}

	if cfg.workload.cnps > 0 {
		cpes := &metav1.PartialObjectMetadataList{}
		cpes.SetGroupVersionKind(policyinfo.GroupVersion.WithKind("ClusterPolicyEndpointList"))
		if err := k8sClient.List(ctx, cpes); err != nil {
			return 0, err
		}
		for _, cpe := range cpes.Items {
			for _, ref := range cpe.OwnerReferences {
				if strings.HasPrefix(ref.Name, cnpNamePrefix) {
					owners[ref.UID] = struct{}{}
				}
			}
		}
	}
	return len(owners), nil
}

func readyKwokNodes(ctx context.Context) (int, error) {
	nodes := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodes, client.MatchingLabels{scaleTestLabel: "true"}); err != nil {
		return 0, err
	}
	ready := 0
	for _, node := range nodes.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready++
			}
		}
	}
	return ready, nil
}

func runningPodsWithIP(ctx context.Context) (int, error) {
	pods := &corev1.PodList{}
	if err := k8sClient.List(ctx, pods, client.MatchingLabels{scaleTestLabel: "true"}); err != nil {
		return 0, err
	}
	running := 0
	for _, pod := range pods.Items {
		if isTestNamespace(pod.Namespace) && pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			running++
		}
	}
	GinkgoWriter.Printf("%d/%d kwok pods running with an IP\n", running, cfg.pods)
	return running, nil
}

// createWorkload creates the namespaces, services, policies, and pods described by cfg.workload. Policies are
// created before pods so the controller's policy cache is populated when the pod events are replayed.
func createWorkload(ctx context.Context) {
	w := cfg.workload

	By(fmt.Sprintf("creating %d namespaces", w.namespaces))
	Expect(createConcurrently(w.namespaces, func(i int) client.Object { return newTestNamespace(i) })).To(Succeed())

	if n := w.namespaces * w.servicesPerNamespace; n > 0 {
		By(fmt.Sprintf("creating %d services", n))
		Expect(createConcurrently(n, func(i int) client.Object {
			return newService(i%w.namespaces, i/w.namespaces)
		})).To(Succeed())
	}

	By(fmt.Sprintf("creating %d policies (%d%% ApplicationNetworkPolicies)", cfg.policies, w.anpPercent))
	Expect(createConcurrently(cfg.policies, newPolicy)).To(Succeed())

	if w.broadPolicies > 0 {
		By(fmt.Sprintf("creating %d broad policies", w.broadPolicies))
		Expect(createConcurrently(w.broadPolicies, func(i int) client.Object { return newBroadPolicy(i) })).To(Succeed())
	}

	if w.cnps > 0 {
		By(fmt.Sprintf("creating %d ClusterNetworkPolicies", w.cnps))
		Expect(createConcurrently(w.cnps, func(i int) client.Object { return newClusterNetworkPolicy(i) })).To(Succeed())
	}

	By(fmt.Sprintf("creating %d kwok pods", cfg.pods))
	Expect(createConcurrently(cfg.pods, func(i int) client.Object { return newKwokPod(i) })).To(Succeed())
}

// createConcurrently creates n objects built by newObj. Objects that already exist are left alone so an
// interrupted run can be resumed with NPC_SCALE_KEEP_RESOURCES.
func createConcurrently(n int, newObj func(i int) client.Object) error {
	return forEachConcurrently(n, func(ctx context.Context, i int) error {
		return client.IgnoreAlreadyExists(k8sClient.Create(ctx, newObj(i)))
	})
}

// forEachConcurrently calls fn for every index below n using cfg.createConcurrency workers, and stops at the
// first error.
func forEachConcurrently(n int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	indexes := make(chan int)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for w := 0; w < cfg.createConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			for i := range indexes {
				if err := fn(ctx, i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := 0; i < n && ctx.Err() == nil; i++ {
		indexes <- i
	}
	close(indexes)
	wg.Wait()
	return firstErr
}

// deleteTestResources deletes the ClusterNetworkPolicies and namespaces created by the test. The controller adds
// finalizers to every policy and removes them at its client QPS, which takes many minutes for thousands of
// policies, so the finalizers are stripped straight away. Namespace deletion then garbage collects the
// PolicyEndpoints, and ClusterPolicyEndpoints are collected through their owner references.
func deleteTestResources(ctx context.Context) {
	By("deleting test ClusterNetworkPolicies and namespaces")
	Expect(client.IgnoreNotFound(k8sClient.DeleteAllOf(ctx, &policyinfo.ClusterNetworkPolicy{},
		client.MatchingLabels{scaleTestLabel: "true"}))).To(Succeed())
	names := testNamespaceNames()
	Expect(forEachConcurrently(len(names), func(ctx context.Context, i int) error {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: names[i]}}
		return client.IgnoreNotFound(k8sClient.Delete(ctx, ns))
	})).To(Succeed())

	By("removing finalizers from test policies")
	var policies []client.Object
	nps := &networking.NetworkPolicyList{}
	Expect(k8sClient.List(ctx, nps, client.MatchingLabels{scaleTestLabel: "true"})).To(Succeed())
	for i := range nps.Items {
		policies = append(policies, &nps.Items[i])
	}
	anps := &policyinfo.ApplicationNetworkPolicyList{}
	Expect(client.IgnoreNotFound(k8sClient.List(ctx, anps, client.MatchingLabels{scaleTestLabel: "true"}))).To(Succeed())
	for i := range anps.Items {
		policies = append(policies, &anps.Items[i])
	}
	cnps := &policyinfo.ClusterNetworkPolicyList{}
	Expect(client.IgnoreNotFound(k8sClient.List(ctx, cnps, client.MatchingLabels{scaleTestLabel: "true"}))).To(Succeed())
	for i := range cnps.Items {
		policies = append(policies, &cnps.Items[i])
	}
	Expect(forEachConcurrently(len(policies), func(ctx context.Context, i int) error {
		policy := policies[i]
		if len(policy.GetFinalizers()) == 0 {
			return nil
		}
		patch := client.MergeFrom(policy.DeepCopyObject().(client.Object))
		policy.SetFinalizers(nil)
		return client.IgnoreNotFound(k8sClient.Patch(ctx, policy, patch))
	})).To(Succeed())

	Eventually(func(ctx context.Context) (int, error) {
		remaining := 0
		for _, name := range names {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &corev1.Namespace{})
			if err == nil {
				remaining++
			} else if !apierrors.IsNotFound(err) {
				return 0, err
			}
		}
		return remaining, nil
	}).WithContext(ctx).WithTimeout(15 * time.Minute).WithPolling(10 * time.Second).Should(BeZero())
}
