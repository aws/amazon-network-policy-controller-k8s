package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("CRD Self-Healing Controller", func() {
	ctx := context.Background()
	crdName := "policyendpoints.networking.k8s.aws"
	It("should reinstall CRD after deletion", func() {
		// 1. Verify CRD initially exists
		original := &apiextensionsv1.CustomResourceDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, original)).To(Succeed())
		// 2. Delete the CRD
		Expect(k8sClient.Delete(ctx, original)).To(Succeed())
		// 3. Wait for the controller to restore it
		recreated := &apiextensionsv1.CustomResourceDefinition{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, recreated)
		}, 10*time.Second, 1*time.Second).Should(Succeed())
		// 4. Optional: Validate its spec or metadata
		Expect(recreated.Name).To(Equal(crdName))
		Expect(recreated.Spec.Names.Kind).To(Equal("PolicyEndpoint"))
		Expect(recreated.Spec.Group).To(Equal("networking.k8s.aws"))
		Expect(recreated.Spec.Versions[0].Name).To(Equal("v1alpha1"))
	})
})
