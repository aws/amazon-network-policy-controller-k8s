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
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// testConfig holds the tunables of the startup scale test. Every field can be
// overridden through the environment variable named in loadTestConfig.
type testConfig struct {
	kwokVersion           string
	kwokNodes             int
	pods                  int
	policies              int
	appLabels             int
	namespace             string
	scenario              string
	workload              workload
	controllerNS          string
	controllerName        string
	startupTimeout        time.Duration
	startupBarrierTimeout time.Duration
	firstReconcileTimeout time.Duration
	cacheWarmup           time.Duration
	keepResources         bool
	skipKwokInstall       bool
	uninstallKwok         bool
	createConcurrency     int
	podCreateRate         int
	churnRate             int
	churnPodLifetime      time.Duration
	churnWarmup           time.Duration
	churnProbes           int
	churnMaxDelay         time.Duration
}

// cacheSyncTimeout is controller-runtime's default CacheSyncTimeout, which the policy controller does not
// override. Keep it in sync if the controller starts setting one.
const cacheSyncTimeout = 2 * time.Minute

var (
	cfg       testConfig
	k8sClient client.Client
	// startupScenarios are the workload presets the suite runs, in order. They are read when the spec tree is
	// built, which is before BeforeSuite loads the rest of the configuration. baseline puts every policy in one
	// namespace, which maximizes the policies each pod event is checked against and so the cost of a pod
	// handler regression; mixed exercises the namespace, service, ANP and CNP handlers that baseline does not.
	startupScenarios = splitList(envOrDefault("NPC_SCALE_SCENARIOS", "baseline,mixed"))
)

func splitList(s string) []string {
	var items []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func TestStartupScale(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Network Policy Controller Startup Scale Suite")
}

var _ = BeforeSuite(func() {
	cfg = loadTestConfig()
	for _, name := range startupScenarios {
		_ = workloadFor(name)
	}

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(policyinfo.AddToScheme(scheme)).To(Succeed())

	restCFG, err := ctrl.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	// Creating tens of thousands of objects with the default client QPS of 5 takes far too long.
	restCFG.QPS = 200
	restCFG.Burst = 400

	c, err := client.New(restCFG, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	k8sClient = retryingClient{Client: c}

	if !cfg.skipKwokInstall {
		ctx := context.Background()
		preinstalled := kwokInstalled(ctx)
		By("installing kwok " + cfg.kwokVersion)
		Expect(installKwok(cfg.kwokVersion)).To(Succeed())
		if cfg.uninstallKwok && !preinstalled {
			DeferCleanup(func() {
				Expect(uninstallKwok(cfg.kwokVersion)).To(Succeed())
			})
		}
	}
})

func loadTestConfig() testConfig {
	c := testConfig{
		kwokVersion:           envOrDefault("KWOK_VERSION", "v0.8.0"),
		kwokNodes:             envIntOrDefault("NPC_SCALE_KWOK_NODES", 20),
		pods:                  envIntOrDefault("NPC_SCALE_PODS", 10000),
		policies:              envIntOrDefault("NPC_SCALE_POLICIES", 10000),
		appLabels:             envIntOrDefault("NPC_SCALE_APP_LABELS", 50),
		namespace:             envOrDefault("NPC_SCALE_NAMESPACE", "npc-startup-scale"),
		controllerNS:          envOrDefault("NPC_NAMESPACE", "kube-system"),
		controllerName:        envOrDefault("NPC_DEPLOYMENT", "amazon-network-policy-controller-k8s"),
		startupTimeout:        envDurationOrDefault("NPC_STARTUP_TIMEOUT", 20*time.Minute),
		startupBarrierTimeout: envDurationOrDefault("NPC_STARTUP_BARRIER_TIMEOUT", 15*time.Second),
		firstReconcileTimeout: envDurationOrDefault("NPC_FIRST_RECONCILE_TIMEOUT", 30*time.Second),
		cacheWarmup:           envDurationOrDefault("NPC_CACHE_WARMUP", time.Minute),
		keepResources:         envBool("NPC_SCALE_KEEP_RESOURCES"),
		skipKwokInstall:       envBool("KWOK_SKIP_INSTALL"),
		uninstallKwok:         envBool("KWOK_UNINSTALL"),
		createConcurrency:     envIntOrDefault("NPC_SCALE_CREATE_CONCURRENCY", 64),
		// Far below the 167 pods/s the tier-xl control plane schedules, since kwok also writes the status of every
		// pod it starts. 10k pods take about 7 minutes.
		podCreateRate: envIntOrDefault("NPC_SCALE_POD_CREATE_RATE", 25),
		// Pod churn after startup (issue #288). Each churn pod produces about three resolved pod events (IP assigned,
		// Running, deleted), so the default is about 75 pod events per second, the same create rate the control plane
		// already absorbs while the workload is created.
		churnRate:        envIntOrDefault("NPC_CHURN_RATE", 25),
		churnPodLifetime: envDurationOrDefault("NPC_CHURN_POD_LIFETIME", 30*time.Second),
		churnWarmup:      envDurationOrDefault("NPC_CHURN_WARMUP", 2*time.Minute),
		churnProbes:      envIntOrDefault("NPC_CHURN_PROBES", 3),
		churnMaxDelay:    envDurationOrDefault("NPC_CHURN_MAX_DELAY", 15*time.Second),
	}

	Expect(c.appLabels).To(BeNumerically(">", 0), "NPC_SCALE_APP_LABELS must be positive")
	// A stall longer than CacheSyncTimeout restarts the pod, so a bound at or above it can never fail.
	Expect(c.startupBarrierTimeout).To(BeNumerically("<", cacheSyncTimeout),
		"NPC_STARTUP_BARRIER_TIMEOUT must be below the controller's CacheSyncTimeout (%s) to detect a startup stall",
		cacheSyncTimeout)
	Expect(c.firstReconcileTimeout).To(BeNumerically("<", cacheSyncTimeout),
		"NPC_FIRST_RECONCILE_TIMEOUT must be below the controller's CacheSyncTimeout (%s) to detect a startup stall",
		cacheSyncTimeout)
	// kwok assigns pod IPs from the node's /22.
	Expect(c.kwokNodes*kwokPodsPerNode).To(BeNumerically(">=", c.pods),
		"%d kwok nodes hold at most %d pods, raise NPC_SCALE_KWOK_NODES", c.kwokNodes, c.kwokNodes*kwokPodsPerNode)
	if c.churnRate > 0 {
		Expect(c.churnProbes).To(BeNumerically(">", 0), "NPC_CHURN_PROBES must be positive")
		Expect(4*c.churnProbes).To(BeNumerically("<=", c.kwokNodes),
			"pod churn gives each of its %d probe pods its own kwok node; lower NPC_CHURN_PROBES or raise NPC_SCALE_KWOK_NODES",
			4*c.churnProbes)
		Expect(c.churnMaxDelay).To(BeNumerically(">", 0), "NPC_CHURN_MAX_DELAY must be positive")
		live := c.pods + c.churnRate*int(c.churnPodLifetime.Seconds()+1) + 4*c.churnProbes
		Expect(c.kwokNodes*kwokPodsPerNode).To(BeNumerically(">=", live),
			"%d kwok nodes hold at most %d pods but pod churn keeps up to %d alive, raise NPC_SCALE_KWOK_NODES or lower "+
				"NPC_CHURN_RATE", c.kwokNodes, c.kwokNodes*kwokPodsPerNode, live)
	}
	return c
}

// workloadFor returns the preset of the named scenario with the NPC_SCALE_* overrides applied.
func workloadFor(name string) workload {
	w, ok := scenarios[name]
	Expect(ok).To(BeTrue(), "unknown scenario %q in NPC_SCALE_SCENARIOS, expected one of %s",
		name, strings.Join(scenarioNames(), ", "))
	w.namespaces = envIntOrDefault("NPC_SCALE_NAMESPACES", w.namespaces)
	w.namespaceGroups = envIntOrDefault("NPC_SCALE_NAMESPACE_GROUPS", w.namespaceGroups)
	w.nsSelectorPercent = envIntOrDefault("NPC_SCALE_NS_SELECTOR_PERCENT", w.nsSelectorPercent)
	w.broadPolicies = envIntOrDefault("NPC_SCALE_BROAD_POLICIES", w.broadPolicies)
	w.servicesPerNamespace = envIntOrDefault("NPC_SCALE_SERVICES_PER_NAMESPACE", w.servicesPerNamespace)
	w.anpPercent = envIntOrDefault("NPC_SCALE_ANP_PERCENT", w.anpPercent)
	w.cnps = envIntOrDefault("NPC_SCALE_CNPS", w.cnps)
	w.ipBlocksPerPolicy = envIntOrDefault("NPC_SCALE_IPBLOCKS_PER_POLICY", w.ipBlocksPerPolicy)
	Expect(w.namespaces).To(BeNumerically(">", 0), "NPC_SCALE_NAMESPACES must be positive")
	Expect(w.namespaceGroups).To(BeNumerically(">", 0), "NPC_SCALE_NAMESPACE_GROUPS must be positive")
	return w
}

func envOrDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	v, err := strconv.Atoi(envOrDefault(key, strconv.Itoa(def)))
	Expect(err).NotTo(HaveOccurred(), "invalid value for %s", key)
	return v
}

func envDurationOrDefault(key string, def time.Duration) time.Duration {
	v, err := time.ParseDuration(envOrDefault(key, def.String()))
	Expect(err).NotTo(HaveOccurred(), "invalid value for %s", key)
	return v
}

func envBool(key string) bool {
	v, _ := strconv.ParseBool(os.Getenv(key))
	return v
}
