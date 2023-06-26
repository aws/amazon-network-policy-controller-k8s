package k8s

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NamespacedName returns the namespaced name for k8s objects
func NamespacedName(obj client.Object) client.ObjectKey {
	return client.ObjectKeyFromObject(obj)
}
