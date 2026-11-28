package policyendpoints

import (
	"context"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kindNetworkPolicy            = "NetworkPolicy"
	kindApplicationNetworkPolicy = "ApplicationNetworkPolicy"
	kindClusterNetworkPolicy     = "ClusterNetworkPolicy"
)

// partitionByOwner splits endpoints listed by policy name into those owned by the policy instance
// with the given UID and those left behind by an earlier instance of the same kind and name.
//
// A policy that is deleted and recreated under the same name gets a new UID, but its old endpoints
// stay until the garbage collector removes them. Reusing one of those keeps the old owner reference,
// so the garbage collector deletes it seconds later and agents see the policy's endpoints vanish.
// Endpoints of another kind that share the name (a NetworkPolicy and an ApplicationNetworkPolicy
// both index on spec.policyRef.name) are in neither group and must be left alone.
func partitionByOwner[T any, PT interface {
	*T
	metav1.Object
}](items []T, kind string, uid types.UID) (owned, stale []T) {
	for i := range items {
		refs := PT(&items[i]).GetOwnerReferences()
		switch {
		case ownedBy(refs, uid):
			owned = append(owned, items[i])
		case ownedByKind(refs, kind):
			stale = append(stale, items[i])
		}
	}
	return owned, stale
}

// deleteStaleEndpoints removes endpoints left by an earlier instance of a policy. Reconcile calls it
// after creating the current instance's endpoints, so agents never see the policy with none.
func deleteStaleEndpoints[T any, PT interface {
	*T
	client.Object
}](ctx context.Context, c client.Client, logger logr.Logger, stale []T) error {
	for i := range stale {
		obj := PT(&stale[i])
		if err := c.Delete(ctx, obj); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return err
			}
			continue
		}
		keysAndValues := []any{"name", obj.GetName()}
		if namespace := obj.GetNamespace(); namespace != "" {
			keysAndValues = append(keysAndValues, "namespace", namespace)
		}
		logger.Info("Deleted endpoint left by a previous instance of the policy", keysAndValues...)
	}
	return nil
}

func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range refs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

func ownedByKind(refs []metav1.OwnerReference, kind string) bool {
	for _, ref := range refs {
		if ref.Kind == kind {
			return true
		}
	}
	return false
}
