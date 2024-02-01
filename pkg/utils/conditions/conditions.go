package conditions

import (
	"context"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func UpdatePEConditions(ctx context.Context, k8sClient client.Client, key types.NamespacedName, log logr.Logger,
	cType policyinfo.PolicyEndpointConditionType,
	cStatus corev1.ConditionStatus,
	cReason string,
	cMsg string) error {
	pe := &policyinfo.PolicyEndpoint{}
	var err error
	if err = k8sClient.Get(ctx, key, pe); err != nil {
		log.Error(err, "getting PE for conditions update failed", "PEName", pe.Name, "PENamespace", pe.Namespace)
	} else {
		copy := pe.DeepCopy()
		cond := policyinfo.PolicyEndpointCondition{
			Type:               cType,
			Status:             cStatus,
			LastTransitionTime: metav1.Now(),
			Reason:             cReason,
			Message:            cMsg,
		}
		copy.Status.Conditions = append(copy.Status.Conditions, cond)
		log.Info("the controller added condition to PE", "PEName", copy.Name, "PENamespace", copy.Namespace, "Conditions", copy.Status.Conditions)
		if err = k8sClient.Status().Patch(ctx, copy, client.MergeFrom(pe)); err != nil {
			log.Error(err, "updating PE status failed", "PEName", pe.Name, "PENamespace", pe.Namespace)
		}
	}

	return err
}
