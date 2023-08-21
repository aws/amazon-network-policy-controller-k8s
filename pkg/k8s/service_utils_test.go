package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Test_LookupServicePort(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name: "https",
					Port: 443,
					TargetPort: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "pod-https",
					},
					Protocol: corev1.ProtocolTCP,
				},
				{
					Name: "dns-lookup",
					Port: 53,
					TargetPort: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "pod-dns-lookup",
					},
					Protocol: corev1.ProtocolUDP,
				},
			},
		},
	}
	type args struct {
		svc      *corev1.Service
		port     intstr.IntOrString
		protocol corev1.Protocol
	}
	tests := []struct {
		name    string
		args    args
		want    int32
		wantErr string
	}{
		{
			name: "resolve numeric service port",
			args: args{
				svc:  svc,
				port: intstr.FromInt(8080),
			},
			want: 80,
		},
		{
			name: "numeric port protocol mismatch",
			args: args{
				svc:      svc,
				port:     intstr.FromInt(8080),
				protocol: corev1.ProtocolUDP,
			},
			wantErr: "unable to find port 8080 on service default/svc",
		},
		{
			name: "numeric port not in service spec",
			args: args{
				svc:  svc,
				port: intstr.FromInt(9090),
			},
			wantErr: "unable to find port 9090 on service default/svc",
		},
		{
			name: "resolve named service target port",
			args: args{
				svc:  svc,
				port: intstr.FromString("pod-https"),
			},
			want: 443,
		},
		{
			name: "named service target port, protocol mismatch",
			args: args{
				svc:  svc,
				port: intstr.FromString("pod-dns-lookup"),
			},
			wantErr: "unable to find port pod-dns-lookup on service default/svc",
		},
		{
			name: "nonexistent port name",
			args: args{
				svc:  svc,
				port: intstr.FromString("nonexistent"),
			},
			wantErr: "unable to find port nonexistent on service default/svc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protocol := corev1.ProtocolTCP
			if len(tt.args.protocol) > 0 {
				protocol = tt.args.protocol
			}
			got, err := LookupServiceListenPort(tt.args.svc, tt.args.port, protocol)
			if len(tt.wantErr) > 0 {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func Test_LookupListenPortFromPodSpec(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc",
			Namespace: "app",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name: "https",
					Port: 443,
					TargetPort: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "pod-https",
					},
					Protocol: corev1.ProtocolTCP,
				},
				{
					Name: "dns-lookup",
					Port: 53,
					TargetPort: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "pod-dns-lookup",
					},
					Protocol: corev1.ProtocolUDP,
				},
				{
					Name: "dns-alt",
					Port: 853,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 5853,
					},
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod",
			Namespace: "app",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Ports: []corev1.ContainerPort{
						{
							Name:          "pod-https",
							ContainerPort: 8443,
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
				{
					Ports: []corev1.ContainerPort{
						{
							Name:          "pod-dns-lookup",
							ContainerPort: 5353,
							Protocol:      corev1.ProtocolUDP,
						},
						{
							Name:          "pod-http",
							ContainerPort: 8080,
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
				{
					Ports: []corev1.ContainerPort{
						{
							Name:          "unexposed-http",
							ContainerPort: 8081,
							Protocol:      corev1.ProtocolTCP,
						},
						{
							Name:          "dns-secure",
							ContainerPort: 5853,
							Protocol:      corev1.ProtocolUDP,
						},
					},
				},
			},
		},
	}
	type args struct {
		svc      *corev1.Service
		pod      *corev1.Pod
		port     intstr.IntOrString
		protocol corev1.Protocol
	}
	tests := []struct {
		name    string
		args    args
		want    int32
		wantErr string
	}{
		{
			name: "service port target port string, input port is int matching pod target port",
			args: args{
				svc:  svc,
				pod:  pod,
				port: intstr.FromInt(8443),
			},
			want: 443,
		},
		{
			name: "service target port int, input port is string matching pod target port",
			args: args{
				svc:  svc,
				pod:  pod,
				port: intstr.FromString("pod-http"),
			},
			want: 80,
		},
		{
			name: "service and container port int, input port is string",
			args: args{
				svc:  svc,
				pod:  pod,
				port: intstr.FromString("pod-http"),
			},
			want: 80,
		},
		{
			name: "service and container port string, input port is string",
			args: args{
				svc:  svc,
				pod:  pod,
				port: intstr.FromString("pod-https"),
			},
			want: 443,
		},
		{
			name: "container port not found",
			args: args{
				svc:  svc,
				pod:  pod,
				port: intstr.FromString("port-nonexistent"),
			},
			wantErr: "unable to find port port-nonexistent on pod app/pod",
		},
		{
			name: "service port not found",
			args: args{
				svc:  svc,
				pod:  pod,
				port: intstr.FromString("unexposed-http"),
			},
			wantErr: "unable to find listener port for port unexposed-http on service app/svc",
		},
		{
			name: "protocol mismatch",
			args: args{
				svc:      svc,
				pod:      pod,
				port:     intstr.FromString("dns-secure"),
				protocol: corev1.ProtocolUDP,
			},
			wantErr: "unable to find listener port for port dns-secure on service app/svc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protocol := corev1.ProtocolTCP
			if len(tt.args.protocol) > 0 {
				protocol = tt.args.protocol
			}
			got, err := LookupListenPortFromPodSpec(tt.args.svc, tt.args.pod, tt.args.port, protocol)
			if len(tt.wantErr) > 0 {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func Test_IsServiceHeadless(t *testing.T) {
	tests := []struct {
		name string
		svc  *corev1.Service
		want bool
	}{
		{
			name: "headless service",
			svc: &corev1.Service{
				Spec: corev1.ServiceSpec{
					ClusterIP: corev1.ClusterIPNone,
				},
			},
			want: true,
		},
		{
			name: "empty IP",
			svc: &corev1.Service{
				Spec: corev1.ServiceSpec{},
			},
			want: true,
		},
		{
			name: "some cluster IP",
			svc: &corev1.Service{
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.100.0.209",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsServiceHeadless(tt.svc))
		})
	}

}
