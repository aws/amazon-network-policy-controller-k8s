package config

import (
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

func Test_buildCacheOptions(t *testing.T) {
	cacheOptions := BuildCacheOptions()
	g := NewWithT(t)
	g.Expect(cacheOptions.ReaderFailOnMissingInformer).To(BeTrue())
	g.Expect(cacheOptions.ByObject).To(HaveLen(8))

	var svcField string
	for obj, cfg := range cacheOptions.ByObject {
		if _, ok := obj.(*corev1.Service); ok {
			g.Expect(cfg.Field).ToNot(BeNil())
			svcField = cfg.Field.String()
		}
	}
	g.Expect(svcField).To(Equal("spec.clusterIP!=" + corev1.ClusterIPNone))
}
