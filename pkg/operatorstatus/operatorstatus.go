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

// CVOOperatorStatusHandler allows for wrappering sdk access to osapi.ClusterOperator
type CVOOperatorStatusHandler struct {
	SDKwrapper OperatorStatusSDKWrapper
}

// NewCVOOperatorStatusHandler is the default initializer function for CVOOperatorStatusHandler
func NewCVOOperatorStatusHandler() *CVOOperatorStatusHandler {
	return &CVOOperatorStatusHandler{SDKwrapper: &defaultOperatorStatusSDKWrapper{}}
}

// OperatorStatusSDKWrapper is the interface that wrappers actuall access to github.com/operator-framework/operator-sdk/pkg/sdk
type OperatorStatusSDKWrapper interface {
	Get(name, namespace string) (*osapi.ClusterOperator, error)
	Update(state *osapi.ClusterOperator) (err error)
	Create(state *osapi.ClusterOperator) (err error)
}

type defaultOperatorStatusSDKWrapper struct{}

func (s *defaultOperatorStatusSDKWrapper) Get(name, namespace string) (*osapi.ClusterOperator, error) {
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
	err := sdk.Get(state)
	return state, err
}

func (s *defaultOperatorStatusSDKWrapper) Update(state *osapi.ClusterOperator) error {
	return sdk.Update(state)
}

func (s *defaultOperatorStatusSDKWrapper) Create(state *osapi.ClusterOperator) error {
	return sdk.Create(state)
}

func (o *CVOOperatorStatusHandler) UpdateOperatorStatus(sampleResource *v1alpha1.SamplesResource) error {
	var err error
	if sampleResource.ConditionTrue(v1alpha1.SamplesExist) {
		err = o.setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorAvailable, osapi.ConditionTrue, "Samples exist in the openshift project")
	} else {
		err = o.setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorAvailable, osapi.ConditionFalse, "Samples do not exist in the openshift project")
	}
	if err != nil {
		return err
	}

	if sampleResource.ConditionTrue(v1alpha1.ConfigurationValid) {
		err = o.setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorFailing, osapi.ConditionFalse, "The samples operator configuration is invalid")
	} else {
		err = o.setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorFailing, osapi.ConditionTrue, "The samples operator configuraiton is valid")
	}
	if err != nil {
		return err
	}

	// We have no meaningful "progressing" status, everything we do basically either instantly succeeds or fails.
	err = o.setOperatorStatus(sampleResource.Namespace, sampleResource.Name, osapi.OperatorProgressing, osapi.ConditionFalse, "")
	return err
}

func (o *CVOOperatorStatusHandler) updateOperatorCondition(op *osapi.ClusterOperator, condition *osapi.ClusterOperatorStatusCondition) (modified bool) {
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

func (o *CVOOperatorStatusHandler) setOperatorStatus(namespace, name string, condtype osapi.ClusterStatusConditionType, status osapi.ConditionStatus, msg string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		state, err := o.SDKwrapper.Get(name, namespace)
		if err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("failed to get cluster operator resource %s/%s: %s", state.ObjectMeta.Namespace, state.ObjectMeta.Name, err)
			}

			//TODO eventually we will want this off of the commit or something that correlates to versions in the code repo
			state.Status.Version = "v0.0.1"

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

			o.updateOperatorCondition(state, &osapi.ClusterOperatorStatusCondition{
				Type:               condtype,
				Status:             status,
				Message:            msg,
				LastTransitionTime: metaapi.Now(),
			})

			return o.SDKwrapper.Create(state)
		}

		modified := o.updateOperatorCondition(state, &osapi.ClusterOperatorStatusCondition{
			Type:               condtype,
			Status:             status,
			Message:            msg,
			LastTransitionTime: metaapi.Now(),
		})
		if !modified {
			return nil
		}
		return o.SDKwrapper.Update(state)
	})
}
