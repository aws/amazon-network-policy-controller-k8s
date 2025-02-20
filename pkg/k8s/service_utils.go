package k8s

import (
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// LookupServiceListenPort returns the numerical port for the service listen port if the target port name matches
// or the port number and the protocol matches the target port. If no matching port is found, it returns a 0 and an error.
func LookupServiceListenPort(svc *corev1.Service, port intstr.IntOrString, protocol corev1.Protocol) (int32, error) {
	for _, svcPort := range svc.Spec.Ports {
		if svcPort.TargetPort.Type == port.Type && svcPort.TargetPort.String() == port.String() && svcPort.Protocol == protocol {
			return svcPort.Port, nil
		}
	}
	return 0, errors.Errorf("unable to find port %s on service %s", port.String(), NamespacedName(svc))
}

// LookupListenPortFromPodSpec returns the numerical listener port from the service spec if the input port matches the target port
// in the pod spec
func LookupListenPortFromPodSpec(svc *corev1.Service, pod *corev1.Pod, port intstr.IntOrString, protocol corev1.Protocol) (int32, error) {
	containerPort, containerPortName, err := LookupContainerPortAndName(pod, port, protocol)
	if err != nil {
		return 0, err
	}
	for _, svcPort := range svc.Spec.Ports {
		if svcPort.Protocol != protocol {
			continue
		}
		switch svcPort.TargetPort.Type {
		case intstr.String:
			if containerPortName == svcPort.TargetPort.StrVal {
				return svcPort.Port, nil
			}

		case intstr.Int:
			if containerPort == svcPort.TargetPort.IntVal {
				return svcPort.Port, nil
			}
		}
	}
	return 0, errors.Errorf("unable to find listener port for port %s on service %s", port.String(), NamespacedName(svc))
}

// IsServiceHeadless returns true if the service is headless
func IsServiceHeadless(svc *corev1.Service) bool {
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return true
	}
	return false
}

// StripDownServiceTransformFunc is a transform function that strips down service to reduce memory usage.
// see details in [stripDownServiceObject].
func StripDownServiceTransformFunc(obj interface{}) (interface{}, error) {
	if service, ok := obj.(*corev1.Service); ok {
		return stripDownServiceObject(service), nil
	}
	return obj, nil
}

// stripDownServiceObject provides an stripDown version of service to reduce memory usage.
// NOTE: if the controller needs to refer to more service fields in the future
// these fields need to be added to the cache
func stripDownServiceObject(service *corev1.Service) *corev1.Service {
	service.ObjectMeta = metav1.ObjectMeta{
		Name:              service.Name,
		Namespace:         service.Namespace,
		UID:               service.UID,
		DeletionTimestamp: service.DeletionTimestamp,
		ResourceVersion:   service.ResourceVersion,
		Finalizers:        service.Finalizers,
	}
	service.Spec = corev1.ServiceSpec{
		Selector:   service.Spec.Selector,
		ClusterIP:  service.Spec.ClusterIP,
		ClusterIPs: service.Spec.ClusterIPs,
		Ports:      service.Spec.Ports,
		Type:       service.Spec.Type,
	}
	service.Status = corev1.ServiceStatus{
		LoadBalancer: service.Status.LoadBalancer,
	}
	return service
}
