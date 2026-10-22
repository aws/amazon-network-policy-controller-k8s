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
	firstReconcileTimeout time.Duration
	cacheWarmup           time.Duration
	keepResources         bool
	skipKwokInstall       bool
	uninstallKwok         bool
	createConcurrency     int
}

var (
	cfg       testConfig
	k8sClient client.Client
)

func TestStartupScale(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Network Policy Controller Startup Scale Suite")
}

var _ = BeforeSuite(func() {
	cfg = loadTestConfig()

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(policyinfo.AddToScheme(scheme)).To(Succeed())

	restCFG, err := ctrl.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	// Creating tens of thousands of objects with the default client QPS of 5 takes far too long.
	restCFG.QPS = 200
	restCFG.Burst = 400

	k8sClient, err = client.New(restCFG, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
})

func loadTestConfig() testConfig {
	c := testConfig{
		kwokVersion:           envOrDefault("KWOK_VERSION", "v0.8.0"),
		kwokNodes:             envIntOrDefault("NPC_SCALE_KWOK_NODES", 20),
		pods:                  envIntOrDefault("NPC_SCALE_PODS", 10000),
		policies:              envIntOrDefault("NPC_SCALE_POLICIES", 10000),
		appLabels:             envIntOrDefault("NPC_SCALE_APP_LABELS", 50),
		namespace:             envOrDefault("NPC_SCALE_NAMESPACE", "npc-startup-scale"),
		scenario:              envOrDefault("NPC_SCALE_SCENARIO", "mixed"),
		controllerNS:          envOrDefault("NPC_NAMESPACE", "kube-system"),
		controllerName:        envOrDefault("NPC_DEPLOYMENT", "amazon-network-policy-controller-k8s"),
		startupTimeout:        envDurationOrDefault("NPC_STARTUP_TIMEOUT", 20*time.Minute),
		firstReconcileTimeout: envDurationOrDefault("NPC_FIRST_RECONCILE_TIMEOUT", 3*time.Minute),
		cacheWarmup:           envDurationOrDefault("NPC_CACHE_WARMUP", time.Minute),
		keepResources:         envBool("NPC_SCALE_KEEP_RESOURCES"),
		skipKwokInstall:       envBool("KWOK_SKIP_INSTALL"),
		uninstallKwok:         envBool("KWOK_UNINSTALL"),
		createConcurrency:     envIntOrDefault("NPC_SCALE_CREATE_CONCURRENCY", 64),
	}

	w, ok := scenarios[c.scenario]
	Expect(ok).To(BeTrue(), "unknown NPC_SCALE_SCENARIO %q, expected one of %s",
		c.scenario, strings.Join(scenarioNames(), ", "))
	w.namespaces = envIntOrDefault("NPC_SCALE_NAMESPACES", w.namespaces)
	w.namespaceGroups = envIntOrDefault("NPC_SCALE_NAMESPACE_GROUPS", w.namespaceGroups)
	w.nsSelectorPercent = envIntOrDefault("NPC_SCALE_NS_SELECTOR_PERCENT", w.nsSelectorPercent)
	w.broadPolicies = envIntOrDefault("NPC_SCALE_BROAD_POLICIES", w.broadPolicies)
	w.servicesPerNamespace = envIntOrDefault("NPC_SCALE_SERVICES_PER_NAMESPACE", w.servicesPerNamespace)
	w.anpPercent = envIntOrDefault("NPC_SCALE_ANP_PERCENT", w.anpPercent)
	w.cnps = envIntOrDefault("NPC_SCALE_CNPS", w.cnps)
	w.ipBlocksPerPolicy = envIntOrDefault("NPC_SCALE_IPBLOCKS_PER_POLICY", w.ipBlocksPerPolicy)
	c.workload = w

	Expect(w.namespaces).To(BeNumerically(">", 0), "NPC_SCALE_NAMESPACES must be positive")
	Expect(w.namespaceGroups).To(BeNumerically(">", 0), "NPC_SCALE_NAMESPACE_GROUPS must be positive")
	Expect(c.appLabels).To(BeNumerically(">", 0), "NPC_SCALE_APP_LABELS must be positive")
	// kwok assigns pod IPs from the node's /22.
	Expect(c.kwokNodes*kwokPodsPerNode).To(BeNumerically(">=", c.pods),
		"%d kwok nodes hold at most %d pods, raise NPC_SCALE_KWOK_NODES", c.kwokNodes, c.kwokNodes*kwokPodsPerNode)
	return c
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
