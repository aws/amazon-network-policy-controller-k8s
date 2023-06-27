package k8s

import (
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"testing"
)

func Test_GetPodIP(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "pod with status IP",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					PodIP: "192.168.11.22",
				},
			},
			want: "192.168.11.22",
		},
		{
			name: "pod with annotation IP",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						podIPAnnotation: "1.2.3.4",
					},
				},
			},
			want: "1.2.3.4",
		},
		{
			name: "pod without status IP or annotation IP",
			pod:  &corev1.Pod{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPodIP(tt.pod)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_LookupContainerPortAndName(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 80,
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
				{
					Ports: []corev1.ContainerPort{
						{
							Name:          "https",
							ContainerPort: 443,
							Protocol:      corev1.ProtocolTCP,
						},
						{
							ContainerPort: 8080,
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
			},
		},
	}
	type want struct {
		port int32
		name string
	}
	type args struct {
		pod      *corev1.Pod
		protocol corev1.Protocol
		port     intstr.IntOrString
	}
	tests := []struct {
		name    string
		args    args
		want    want
		wantErr string
	}{
		{
			name: "resolve numeric pod",
			args: args{
				pod:  pod,
				port: intstr.FromInt(8080),
			},
			want: want{
				port: 8080,
			},
		},
		{
			name: "numeric pod not in pod spec can still be resolved",
			args: args{
				pod:  pod,
				port: intstr.FromInt(9090),
			},
			want: want{
				port: 9090,
			},
		},
		{
			name: "lookup based on port name",
			args: args{
				pod:  pod,
				port: intstr.FromString("http"),
			},
			want: want{
				port: 80,
				name: "http",
			},
		},
		{
			name: "lookup based on port name in another container",
			args: args{
				pod:  pod,
				port: intstr.FromString("https"),
			},
			want: want{
				port: 443,
				name: "https",
			},
		},
		{
			name: "port matches, but protocol does not",
			args: args{
				pod:      pod,
				port:     intstr.FromString("https"),
				protocol: corev1.ProtocolUDP,
			},
			wantErr: "unable to find port https on pod default/pod",
		},
		{
			name: "numeric port lookup ignores the protocol",
			args: args{
				pod:      pod,
				port:     intstr.FromInt(443),
				protocol: corev1.ProtocolUDP,
			},
			want: want{
				port: 443,
			},
		},
		{
			name: "nonexistent port name",
			args: args{
				pod:  pod,
				port: intstr.FromString("nonexistent"),
			},
			wantErr: "unable to find port nonexistent on pod default/pod",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protocol := tt.args.protocol
			if len(protocol) == 0 {
				protocol = corev1.ProtocolTCP
			}
			got := want{}
			var err error
			got.port, got.name, err = LookupContainerPortAndName(tt.args.pod, tt.args.port, protocol)
			if len(tt.wantErr) > 0 {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
