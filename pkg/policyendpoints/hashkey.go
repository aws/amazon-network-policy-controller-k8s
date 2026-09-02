package policyendpoints

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
)

// hashKeyWriter builds injective hash input for endpoint keys: fields are tagged
// and byte-length-prefixed, integers are decimal. A cluster rule encodes as
//
//	cidr=13:10.200.0.0/16;domain=0:;action=4:Deny;ports=1[proto=3:TCP;port=80;endport=nil;,];
type hashKeyWriter struct {
	buf []byte
}

func newHashKeyWriter() *hashKeyWriter {
	return &hashKeyWriter{buf: make([]byte, 0, 128)}
}

func (w *hashKeyWriter) str(tag, val string) {
	w.buf = append(w.buf, tag...)
	w.buf = append(w.buf, '=')
	w.buf = strconv.AppendInt(w.buf, int64(len(val)), 10)
	w.buf = append(w.buf, ':')
	w.buf = append(w.buf, val...)
	w.buf = append(w.buf, ';')
}

func (w *hashKeyWriter) optInt32(tag string, val *int32) {
	w.buf = append(w.buf, tag...)
	w.buf = append(w.buf, '=')
	if val == nil {
		w.buf = append(w.buf, "nil;"...)
		return
	}
	w.buf = strconv.AppendInt(w.buf, int64(*val), 10)
	w.buf = append(w.buf, ';')
}

func (w *hashKeyWriter) strs(tag string, vals []policyinfo.NetworkAddress) {
	sorted := vals
	if !slices.IsSorted(vals) {
		sorted = slices.Clone(vals)
		slices.Sort(sorted)
	}

	w.buf = append(w.buf, tag...)
	w.buf = append(w.buf, '=')
	w.buf = strconv.AppendInt(w.buf, int64(len(sorted)), 10)
	w.buf = append(w.buf, '[')
	for _, v := range sorted {
		w.str("v", string(v))
	}
	w.buf = append(w.buf, "];"...)
}

// ports hashes a canonical ordering; the caller's slice is not reordered.
func (w *hashKeyWriter) ports(ports []policyinfo.Port) {
	sorted := ports
	if !slices.IsSortedFunc(ports, comparePorts) {
		sorted = slices.Clone(ports)
		slices.SortFunc(sorted, comparePorts)
	}

	w.buf = slices.Grow(w.buf, len(sorted)*48)
	w.buf = append(w.buf, "ports="...)
	w.buf = strconv.AppendInt(w.buf, int64(len(sorted)), 10)
	w.buf = append(w.buf, '[')
	for _, p := range sorted {
		if p.Protocol != nil {
			w.str("proto", string(*p.Protocol))
		} else {
			w.buf = append(w.buf, "proto=nil;"...)
		}
		w.optInt32("port", p.Port)
		w.optInt32("endport", p.EndPort)
		w.buf = append(w.buf, ',')
	}
	w.buf = append(w.buf, "];"...)
}

func (w *hashKeyWriter) sum() string {
	sum := sha256.Sum256(w.buf)
	return hex.EncodeToString(sum[:])
}

func comparePorts(a, b policyinfo.Port) int {
	if c := strings.Compare(protoOrEmpty(a.Protocol), protoOrEmpty(b.Protocol)); c != 0 {
		return c
	}
	if c := cmp.Compare(intOrNeg(a.Port), intOrNeg(b.Port)); c != 0 {
		return c
	}
	return cmp.Compare(intOrNeg(a.EndPort), intOrNeg(b.EndPort))
}

func protoOrEmpty(p *corev1.Protocol) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

// nil sorts ahead of every valid port.
func intOrNeg(v *int32) int64 {
	if v == nil {
		return -1
	}
	return int64(*v)
}
