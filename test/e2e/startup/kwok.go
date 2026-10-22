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
	"io"
	"net/http"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kwokNodeAnnotation = "kwok.x-k8s.io/node"
	kwokTaintKey       = "kwok.x-k8s.io/node"
	kwokNodeTypeLabel  = "type"
	kwokNodeTypeValue  = "kwok"
	// scaleTestLabel marks every object created by this test so that cleanup only touches our objects.
	scaleTestLabel = "networking.k8s.aws/npc-startup-scale-test"
	// kwokPodsPerNode is how many pods fit in a kwok node's /22 pod CIDR, with headroom.
	kwokPodsPerNode = 1000
)

func kwokManifestURLs(version string) []string {
	base := fmt.Sprintf("https://github.com/kubernetes-sigs/kwok/releases/download/%s", version)
	return []string{base + "/kwok.yaml", base + "/stage-fast.yaml"}
}

// kwokInstalled reports whether a kwok controller deployment already exists in kube-system.
func kwokInstalled(ctx context.Context) bool {
	deploy := &appsv1.Deployment{}
	return k8sClient.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: "kwok-controller"}, deploy) == nil
}

func kubectl(args ...string) error {
	out, err := kubectlOutput(args...)
	GinkgoWriter.Printf("%s\n", out)
	return err
}

func kubectlOutput(args ...string) (string, error) {
	GinkgoWriter.Printf("kubectl %s\n", strings.Join(args, " "))
	return kubectlQuiet(args...)
}

// kubectlQuiet runs kubectl without logging the command, for calls made on every poll.
func kubectlQuiet(args ...string) (string, error) {
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// installKwok applies the kwok controller and fast stage manifests. The FlowSchema shipped with kwok is
// dropped because EKS rejects FlowSchemas that reference the exempt priority level.
func installKwok(version string) error {
	for _, url := range kwokManifestURLs(version) {
		manifest, err := download(url)
		if err != nil {
			return err
		}
		if err := kubectlApply(withoutKind(manifest, "FlowSchema")); err != nil {
			return err
		}
	}
	return kubectl("-n", "kube-system", "rollout", "status", "deployment/kwok-controller", "--timeout=5m")
}

func download(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func withoutKind(manifest, kind string) string {
	var docs []string
	for _, doc := range strings.Split(manifest, "\n---\n") {
		if !strings.Contains(doc, "\nkind: "+kind+"\n") && !strings.HasPrefix(doc, "kind: "+kind+"\n") {
			docs = append(docs, doc)
		}
	}
	return strings.Join(docs, "\n---\n")
}

func kubectlApply(manifest string) error {
	GinkgoWriter.Printf("kubectl apply --server-side -f -\n")
	cmd := exec.Command("kubectl", "apply", "--server-side", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	GinkgoWriter.Printf("%s\n", out)
	if err != nil {
		return fmt.Errorf("kubectl apply: %w", err)
	}
	return nil
}

func uninstallKwok(version string) error {
	urls := kwokManifestURLs(version)
	for i := len(urls) - 1; i >= 0; i-- {
		if err := kubectl("delete", "--ignore-not-found", "-f", urls[i]); err != nil {
			return err
		}
	}
	return nil
}

func kwokNodeName(i int) string {
	return fmt.Sprintf("kwok-npc-scale-%d", i)
}

// newKwokNode builds a fake node managed by kwok. Each node gets its own /22 pod CIDR, which
// kwok uses to assign pod IPs; the controller ignores pods without an IP. kwok fills in the node
// status (capacity defaults to 1M pods) once it picks the node up.
func newKwokNode(i int) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: kwokNodeName(i),
			Annotations: map[string]string{
				kwokNodeAnnotation: "fake",
			},
			Labels: map[string]string{
				"kubernetes.io/hostname": kwokNodeName(i),
				"kubernetes.io/os":       "linux",
				"kubernetes.io/arch":     "amd64",
				"kubernetes.io/role":     "agent",
				kwokNodeTypeLabel:        kwokNodeTypeValue,
				scaleTestLabel:           "true",
			},
		},
		Spec: corev1.NodeSpec{
			PodCIDR:  fmt.Sprintf("10.%d.%d.0/22", 200+i/64, (i%64)*4),
			PodCIDRs: []string{fmt.Sprintf("10.%d.%d.0/22", 200+i/64, (i%64)*4)},
			Taints: []corev1.Taint{{
				Key:    kwokTaintKey,
				Value:  "fake",
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
	}
}

func deleteKwokNodes(ctx context.Context) error {
	return k8sClient.DeleteAllOf(ctx, &corev1.Node{}, client.MatchingLabels{scaleTestLabel: "true"})
}
