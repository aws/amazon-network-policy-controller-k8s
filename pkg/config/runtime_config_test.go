package config

import (
	. "github.com/onsi/gomega"
	"testing"
)

func Test_buildCacheOptions(t *testing.T) {
	cacheOptions := BuildCacheOptions()
	g := NewWithT(t)
	g.Expect(cacheOptions.ReaderFailOnMissingInformer).To(BeTrue())
	g.Expect(cacheOptions.ByObject).To(HaveLen(2))
}
