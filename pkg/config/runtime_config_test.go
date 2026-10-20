package config

import (
	"testing"

	. "github.com/onsi/gomega"
	"github.com/spf13/pflag"
)

func Test_buildCacheOptions(t *testing.T) {
	cacheOptions := BuildCacheOptions()
	g := NewWithT(t)
	g.Expect(cacheOptions.ReaderFailOnMissingInformer).To(BeTrue())
	g.Expect(cacheOptions.ByObject).To(HaveLen(8))
}

func Test_bindFlagsKubeAPIRateLimits(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantQPS   float32
		wantBurst int
	}{
		{
			name:      "defaults",
			args:      []string{},
			wantQPS:   defaultQPS,
			wantBurst: defaultBurst,
		},
		{
			name:      "overridden",
			args:      []string{"--kube-api-qps=100", "--kube-api-burst=200"},
			wantQPS:   100,
			wantBurst: 200,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			cfg := RuntimeConfig{}
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			cfg.BindFlags(fs)
			g.Expect(fs.Parse(tt.args)).To(Succeed())
			g.Expect(cfg.KubeAPIQPS).To(Equal(tt.wantQPS))
			g.Expect(cfg.KubeAPIBurst).To(Equal(tt.wantBurst))
		})
	}
}
