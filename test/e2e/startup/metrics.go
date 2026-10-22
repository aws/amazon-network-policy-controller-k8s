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
	"fmt"
	"strconv"
	"strings"
)

const controllerMetricsPort = 8080

// controllerMetrics is the subset of the controller's Prometheus metrics the test uses.
type controllerMetrics struct {
	reconciles      float64
	queueDepth      float64
	activeWorkers   float64
	residentBytes   float64
	heapInUseBytes  float64
	reconcileErrors float64
}

// scrapeControllerMetrics reads the metrics endpoint of a controller pod through the API server proxy.
func scrapeControllerMetrics(podName string) (controllerMetrics, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy/metrics", cfg.controllerNS, podName, controllerMetricsPort)
	out, err := kubectlQuiet("get", "--raw", path)
	if err != nil {
		return controllerMetrics{}, err
	}
	var m controllerMetrics
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, ok := parseSample(line)
		if !ok {
			continue
		}
		switch {
		case name == "controller_runtime_reconcile_total" && strings.Contains(labels, `controller="policy"`):
			m.reconciles += value
			if strings.Contains(labels, `result="error"`) {
				m.reconcileErrors += value
			}
		case name == "workqueue_depth" && strings.Contains(labels, `name="policy"`):
			m.queueDepth = value
		case name == "controller_runtime_active_workers" && strings.Contains(labels, `controller="policy"`):
			m.activeWorkers = value
		case name == "process_resident_memory_bytes":
			m.residentBytes = value
		case name == "go_memstats_heap_inuse_bytes":
			m.heapInUseBytes = value
		}
	}
	return m, nil
}

// parseSample splits a Prometheus text exposition sample into its name, label string, and value.
func parseSample(line string) (name, labels string, value float64, ok bool) {
	space := strings.LastIndexByte(line, ' ')
	if space < 0 {
		return "", "", 0, false
	}
	v, err := strconv.ParseFloat(line[space+1:], 64)
	if err != nil {
		return "", "", 0, false
	}
	series := line[:space]
	if brace := strings.IndexByte(series, '{'); brace >= 0 {
		return series[:brace], series[brace:], v, true
	}
	return series, "", v, true
}

func mebibytes(b float64) string {
	return fmt.Sprintf("%.0fMiB", b/(1<<20))
}
