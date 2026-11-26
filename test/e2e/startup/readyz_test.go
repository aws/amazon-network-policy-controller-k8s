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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

const healthProbePort = 8081

// probeEndpoint calls /healthz or /readyz on a controller pod via the API server proxy.
// Returns the HTTP status code, treating a kubectl non-zero exit as a non-200 response.
func probeEndpoint(podName, endpoint string) (int, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy/%s",
		cfg.controllerNS, podName, healthProbePort, endpoint)
	out, err := kubectlOutput("get", "--raw", path)
	if err != nil {
		combined := out + err.Error()
		switch {
		case strings.Contains(combined, "503"):
			return 503, nil
		case strings.Contains(combined, "500"):
			return 500, nil
		}
		return 0, err
	}
	return 200, nil
}

var _ = Describe("readyz probe gates on informer cache sync", Ordered, func() {
	var (
		ctx              = context.Background()
		originalReplicas int32
		startTime        time.Time
	)

	BeforeAll(func() {
		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, controllerKey(), deploy)).To(Succeed())
		expectControllerRunsOnDataplane(ctx, deploy)

		originalReplicas = ptr.Deref(deploy.Spec.Replicas, 1)
		Expect(originalReplicas).To(BeNumerically(">", 0), "controller deployment is already scaled to zero")

		DeferCleanup(func() {
			By(fmt.Sprintf("restoring controller to %d replicas", originalReplicas))
			Expect(scaleController(ctx, originalReplicas)).To(Succeed())
		})

		By("scaling the controller down")
		Expect(scaleController(ctx, 0)).To(Succeed())
		Eventually(func() (int, error) {
			pods, err := controllerPods(ctx)
			running := 0
			for _, p := range pods {
				if p.DeletionTimestamp.IsZero() {
					running++
				}
			}
			return running, err
		}).WithTimeout(2 * time.Minute).WithPolling(pollInterval).Should(BeZero())
	})

	It("returns unhealthy on /readyz during cache sync, healthy on /healthz throughout", func() {
		// Hold the leader lease while the pod starts so it syncs caches before becoming leader.
		// This extends the window where /readyz should be unhealthy to at least cfg.cacheWarmup.
		releaseLease := holdLease(ctx)
		DeferCleanup(releaseLease)

		startTime = time.Now()
		By(fmt.Sprintf("scaling controller back up to %d replicas", originalReplicas))
		Expect(scaleController(ctx, originalReplicas)).To(Succeed())

		// Wait for the new pod to appear and reach Running phase (not yet Ready).
		var newPod corev1.Pod
		Eventually(func() ([]corev1.Pod, error) {
			return controllerPodsCreatedAfter(ctx, startTime)
		}).WithTimeout(2 * time.Minute).WithPolling(pollInterval).
			Should(HaveLen(int(originalReplicas)))

		pods, err := controllerPodsCreatedAfter(ctx, startTime)
		Expect(err).NotTo(HaveOccurred())
		newPod = pods[0]

		// Wait until the pod's container is running so probes can reach it.
		Eventually(func() bool {
			current, _ := controllerPodsCreatedAfter(ctx, startTime)
			for i := range current {
				if current[i].Name == newPod.Name {
					return current[i].Status.Phase == corev1.PodRunning
				}
			}
			return false
		}).WithTimeout(2 * time.Minute).WithPolling(pollInterval).Should(BeTrue())

		By("verifying /readyz is unhealthy while the leader lease is held (cache sync window)")
		Consistently(func() bool {
			current, _ := controllerPodsCreatedAfter(ctx, startTime)
			for i := range current {
				if current[i].Name == newPod.Name {
					return isPodReady(&current[i])
				}
			}
			return false
		}).WithTimeout(10*time.Second).WithPolling(2*time.Second).Should(BeFalse(),
			"/readyz should return unhealthy before informer caches sync")

		By("verifying /healthz returns 200 during cache sync")
		status, err := probeEndpoint(newPod.Name, "healthz")
		Expect(err).NotTo(HaveOccurred(), "failed to probe /healthz")
		Expect(status).To(Equal(200), "/healthz must stay healthy during cache sync to avoid restart loops")

		By("verifying /readyz returns non-200 during cache sync")
		status, err = probeEndpoint(newPod.Name, "readyz")
		Expect(err).NotTo(HaveOccurred(), "failed to probe /readyz")
		Expect(status).NotTo(Equal(200), "/readyz should be unhealthy until caches are synced")

		By("releasing the leader lease so the pod becomes leader and /readyz becomes healthy")
		releaseLease()

		Eventually(func() bool {
			current, _ := controllerPodsCreatedAfter(ctx, startTime)
			for i := range current {
				if current[i].Name == newPod.Name {
					Expect(restartMessage(&current[i])).To(BeEmpty(),
						"controller pod restarted — /healthz may have incorrectly returned unhealthy")
					return isPodReady(&current[i])
				}
			}
			return false
		}).WithTimeout(3*time.Minute).WithPolling(pollInterval).Should(BeTrue(),
			"/readyz should return healthy once informer caches are synced")

		By("verifying /readyz returns 200 after caches sync")
		status, err = probeEndpoint(newPod.Name, "readyz")
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "/readyz must be healthy after cache sync completes")
	})
})
