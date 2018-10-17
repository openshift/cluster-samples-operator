package operator

import (
	"fmt"

	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"

	osapi "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"
	"github.com/operator-framework/operator-sdk/pkg/sdk"

	"github.com/openshift/cluster-samples-operator/pkg/apis/samplesoperator/v1alpha1"
)

func UpdateOperatorStatus(sampleResource *v1alpha1.SamplesResource) error {
	var err error
	if sampleResource.ConditionTrue(v1alpha1.SamplesExist) {
		err = setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorAvailable, osapi.ConditionTrue, "Samples exist in the openshift project")
	} else {
		err = setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorAvailable, osapi.ConditionFalse, "Samples do not exist in the openshift project")
	}
	if err != nil {
		return err
	}

	if sampleResource.ConditionTrue(v1alpha1.ConfigurationValid) {
		err = setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorFailing, osapi.ConditionFalse, "The samples operator configuration is invalid")
	} else {
		err = setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorFailing, osapi.ConditionTrue, "The samples operator configuraiton is valid")
	}
	if err != nil {
		return err
	}

	// We have no meaningful "progressing" status, everything we do basically either instantly succeeds or fails.
	err = setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorProgressing, osapi.ConditionFalse, "")
	return err
}

func updateOperatorCondition(op *osapi.ClusterOperator, condition *osapi.ClusterOperatorStatusCondition) (modified bool) {
	found := false
	conditions := []osapi.ClusterOperatorStatusCondition{}

	for _, c := range op.Status.Conditions {
		if condition.Type != c.Type {
			conditions = append(conditions, c)
			continue
		}
		if condition.Status != c.Status {
			modified = true
		}
		conditions = append(conditions, *condition)
		found = true
	}

	if !found {
		conditions = append(conditions, *condition)
		modified = true
	}

	op.Status.Conditions = conditions
	return
}

func setOperatorStatus(namespace, name string, condtype osapi.ClusterStatusConditionType, status osapi.ConditionStatus, msg string) error {
	state := &osapi.ClusterOperator{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: osapi.SchemeGroupVersion.String(),
			Kind:       "ClusterOperator",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := sdk.Get(state)
		if err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("failed to get cluster operator resource %s/%s: %s", state.ObjectMeta.Namespace, state.ObjectMeta.Name, err)
			}

			state.Status.Conditions = []osapi.ClusterOperatorStatusCondition{
				{
					Type:               osapi.OperatorAvailable,
					Status:             osapi.ConditionUnknown,
					LastTransitionTime: metaapi.Now(),
				},
				{
					Type:               osapi.OperatorProgressing,
					Status:             osapi.ConditionUnknown,
					LastTransitionTime: metaapi.Now(),
				},
				{
					Type:               osapi.OperatorFailing,
					Status:             osapi.ConditionUnknown,
					LastTransitionTime: metaapi.Now(),
				},
			}

			updateOperatorCondition(state, &osapi.ClusterOperatorStatusCondition{
				Type:               condtype,
				Status:             status,
				Message:            msg,
				LastTransitionTime: metaapi.Now(),
			})

			return sdk.Create(state)
		}

		modified := updateOperatorCondition(state, &osapi.ClusterOperatorStatusCondition{
			Type:               condtype,
			Status:             status,
			Message:            msg,
			LastTransitionTime: metaapi.Now(),
		})
		if !modified {
			return nil
		}
		return sdk.Update(state)
	})
}
