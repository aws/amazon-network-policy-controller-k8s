package policyendpoints

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// fmtEndpointInfoKey is the previous fmt-based implementation of
// getEndpointInfoKey. It is kept as a reference: the streaming implementation
// must produce exactly the same digest for every input.
func fmtEndpointInfoKey(info policyinfo.EndpointInfo) string {
	hasher := sha256.New()
	if info.DomainName != "" {
		fmt.Fprintf(hasher, "domainName=%s|", info.DomainName)
	} else {
		fmt.Fprintf(hasher, "cidr=%s|", info.CIDR)
		for _, except := range info.Except {
			fmt.Fprintf(hasher, "except=%s|", except)
		}
	}
	for _, port := range info.Ports {
		fmt.Fprintf(hasher, "proto=%s|port=%s|endPort=%s|",
			protocolKeyPart(port.Protocol), int32PtrKeyPart(port.Port), int32PtrKeyPart(port.EndPort))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// FuzzGetEndpointInfoKeyMatchesFmt checks that getEndpointInfoKey is byte-for-byte
// identical to the fmt-based implementation. Excepts are a comma-separated list;
// proto "-" means the endpoint has no ports.
func FuzzGetEndpointInfoKeyMatchesFmt(f *testing.F) {
	f.Add("", "0.0.0.0/0", "10.0.0.1/32,10.0.0.2/32", "TCP", int32(80), int32(0), true, false)
	f.Add("", "0.0.0.0/0", "", "-", int32(0), int32(0), false, false)
	f.Add("example.com", "1.2.3.4/32", "x", "", int32(0), int32(0), false, false)
	f.Add("", "::/0", "2001:db8::/32,2001:0db8:85a3:0000:0000:8a2e:0370:7334/128", "UDP", int32(1), int32(23), true, true)
	f.Add("", "", strings.Repeat("a", 200), "SCTP", int32(123), int32(-1), true, true)

	m := &policyEndpointsManager{}
	f.Fuzz(func(t *testing.T, domain, cidr, excepts, proto string, port, endPort int32, hasPort, hasEndPort bool) {
		info := policyinfo.EndpointInfo{DomainName: policyinfo.DomainName(domain), CIDR: policyinfo.NetworkAddress(cidr)}
		if excepts != "" {
			for _, except := range strings.Split(excepts, ",") {
				info.Except = append(info.Except, policyinfo.NetworkAddress(except))
			}
		}
		if proto != "-" {
			var p policyinfo.Port
			if proto != "" {
				protocol := corev1.Protocol(proto)
				p.Protocol = &protocol
			}
			if hasPort {
				p.Port = &port
			}
			if hasEndPort {
				p.EndPort = &endPort
			}
			info.Ports = []policyinfo.Port{p, {}}
		}
		if got, want := m.getEndpointInfoKey(info), fmtEndpointInfoKey(info); got != want {
			t.Fatalf("digest mismatch for %+v: got %s, want %s", info, got, want)
		}
	})
}
