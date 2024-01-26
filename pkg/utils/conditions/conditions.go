package conditions

import (
	"context"
	"time"

	policyinfo "github.com/aws/amazon-network-policy-controller-k8s/api/v1alpha1"
	"github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	jitterWaitTime = time.Millisecond * 100
)

func CreatePEInitCondition(ctx context.Context, k8sClient client.Client, key types.NamespacedName, log logr.Logger) {
	// using a goroutine to add the condition with jitter wait.
	go func() {
		// since adding an init condition immediate after the PE is created
		// waiting for a small time before calling
		time.Sleep(wait.Jitter(jitterWaitTime, 0.25))
		err := retry.OnError(
			wait.Backoff{
				Duration: time.Millisecond * 100,
				Factor:   3.0,
				Jitter:   0.1,
				Steps:    5,
				Cap:      time.Second * 10,
			},
			func(err error) bool { return errors.IsNotFound(err) },
			func() error {
				pe := &policyinfo.PolicyEndpoint{}
				var err error
				if err = k8sClient.Get(ctx, key, pe); err != nil {
					log.Error(err, "getting PE for conditions update failed", "PEName", pe.Name, "PENamespace", pe.Namespace)
				} else {
					copy := pe.DeepCopy()
					copy.StatusConditions()
					if err = k8sClient.Status().Patch(ctx, copy, client.MergeFrom(pe)); err != nil {
						log.Error(err, "creating PE init status failed", "PEName", pe.Name, "PENamespace", pe.Namespace)
					}
				}
				return err
			},
		)
		if err != nil {
			log.Error(err, "adding PE init condition failed after retries", "PENamespacedName", key)
		} else {
			log.Info("added PE init condition", "PENamespacedName", key)
		}
	}()
}

func UpdatePEConditions(ctx context.Context, k8sClient client.Client, key types.NamespacedName, log logr.Logger,
	cType policyinfo.PolicyEndpointConditionType,
	cStatus metav1.ConditionStatus,
	cReason string,
	cMsg string,
	keepConditions bool) error {
	pe := &policyinfo.PolicyEndpoint{}
	var err error
	if err = k8sClient.Get(ctx, key, pe); err != nil {
		log.Error(err, "getting PE for conditions update failed", "PEName", pe.Name, "PENamespace", pe.Namespace)
	} else {
		copy := pe.DeepCopy()
		cond := status.Condition{
			Type:               string(cType),
			Status:             cStatus,
			LastTransitionTime: metav1.Now(),
			Reason:             cReason,
			Message:            cMsg,
		}
		if keepConditions {
			// not overwrite old conditions that have the same type
			conds := copy.GetConditions()
			conds = append(conds, cond)
			copy.SetConditions(conds)
		} else {
			// overwrite old conditions that have the same type
			copy.StatusConditions().Set(cond)
		}
		log.Info("the controller added condition to PE", "PEName", copy.Name, "PENamespace", copy.Namespace, "Conditions", copy.Status.Conditions)
		if err = k8sClient.Status().Patch(ctx, copy, client.MergeFrom(pe)); err != nil {
			log.Error(err, "updating PE status failed", "PEName", pe.Name, "PENamespace", pe.Namespace)
		}
	}

	return err
}
