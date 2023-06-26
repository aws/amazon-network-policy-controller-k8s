package k8s

import (
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	podIPAnnotation = "vpc.amazonaws.com/pod-ips"
)

// GetPodIP returns the pod IP from the pod status or from the pod annotation.
func GetPodIP(pod *corev1.Pod) string {
	podIP := pod.Status.PodIP
	if len(podIP) == 0 {
		if podIPFromAnnotation, exists := pod.Annotations[podIPAnnotation]; exists {
			podIP = podIPFromAnnotation
		}
	}
	return podIP
}

// LookupContainerPort returns the numerical containerPort for specific port and protocol.
func LookupContainerPort(pod *corev1.Pod, port intstr.IntOrString, protocol corev1.Protocol) (int32, error) {
	switch port.Type {
	case intstr.String:
		for _, podContainer := range pod.Spec.Containers {
			for _, podPort := range podContainer.Ports {
				if podPort.Name == port.StrVal && podPort.Protocol == protocol {
					return podPort.ContainerPort, nil
				}
			}
		}
	case intstr.Int:
		return port.IntVal, nil
	}
	return 0, errors.Errorf("unable to find port %s on pod %s", port.String(), NamespacedName(pod))
}

// LookupContainerPortAndName returns numerical containerPort and portName for specific port and protocol
func LookupContainerPortAndName(pod *corev1.Pod, port intstr.IntOrString, protocol corev1.Protocol) (int32, string, error) {
	for _, podContainer := range pod.Spec.Containers {
		for _, podPort := range podContainer.Ports {
			if podPort.Protocol != protocol {
				continue
			}
			switch port.Type {
			case intstr.String:
				if podPort.Name == port.StrVal {
					return podPort.ContainerPort, podPort.Name, nil
				}
			case intstr.Int:
				if podPort.ContainerPort == port.IntVal {
					return podPort.ContainerPort, podPort.Name, nil
				}
			}
		}
	}
	if port.Type == intstr.Int {
		return port.IntVal, "", nil
	}
	return 0, "", errors.Errorf("unable to find port %s on pod %s", port.String(), NamespacedName(pod))
}
