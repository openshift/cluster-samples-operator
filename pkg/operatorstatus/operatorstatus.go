package operator

import (
	"fmt"

	"github.com/sirupsen/logrus"

	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"

	ocfgapi "github.com/openshift/cluster-version-operator/pkg/apis/config.openshift.io/v1"
	osapi "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"
	"github.com/operator-framework/operator-sdk/pkg/sdk"

	"github.com/openshift/cluster-samples-operator/pkg/apis/samplesoperator/v1alpha1"
)

// CVOOperatorStatusHandler allows for wrappering sdk access to ocfgapi.ClusterVersion
type CVOOperatorStatusHandler struct {
	SDKwrapper OperatorStatusSDKWrapper
	Namespace  string
}

// NewCVOOperatorStatusHandler is the default initializer function for CVOOperatorStatusHandler
func NewCVOOperatorStatusHandler(namespace string) *CVOOperatorStatusHandler {
	return &CVOOperatorStatusHandler{SDKwrapper: &defaultOperatorStatusSDKWrapper{}, Namespace: namespace}
}

// OperatorStatusSDKWrapper is the interface that wrappers actuall access to github.com/operator-framework/operator-sdk/pkg/sdk
type OperatorStatusSDKWrapper interface {
	Get(name, namespace string) (*ocfgapi.ClusterVersion, error)
	Update(state *ocfgapi.ClusterVersion) (err error)
	Create(state *ocfgapi.ClusterVersion) (err error)
}

type defaultOperatorStatusSDKWrapper struct{}

func (s *defaultOperatorStatusSDKWrapper) Get(name, namespace string) (*ocfgapi.ClusterVersion, error) {
	state := &ocfgapi.ClusterVersion{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: ocfgapi.SchemeGroupVersion.String(),
			Kind:       "ClusterVersion",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	err := sdk.Get(state)
	return state, err
}

func (s *defaultOperatorStatusSDKWrapper) Update(state *ocfgapi.ClusterVersion) error {
	return sdk.Update(state)
}

func (s *defaultOperatorStatusSDKWrapper) Create(state *ocfgapi.ClusterVersion) error {
	return sdk.Create(state)
}

func (o *CVOOperatorStatusHandler) UpdateOperatorStatus(sampleResource *v1alpha1.SamplesResource) error {
	var err error
	switch {
	case sampleResource.ConditionTrue(v1alpha1.SamplesExist):
		err = o.setOperatorStatus(sampleResource.Name, osapi.OperatorAvailable, osapi.ConditionTrue, "Samples exist in the openshift project")
	case sampleResource.ConditionFalse(v1alpha1.SamplesExist):
		err = o.setOperatorStatus(sampleResource.Name, osapi.OperatorAvailable, osapi.ConditionFalse, "Samples do not exist in the openshift project")
	default:
		err = o.setOperatorStatus(sampleResource.Name, osapi.OperatorAvailable, osapi.ConditionUnknown, "The presence of the samples in the openshift project is unknown")
	}
	if err != nil {
		return err
	}

	switch {
	case sampleResource.ConditionTrue(v1alpha1.ConfigurationValid):
		err = o.setOperatorStatus(sampleResource.Name, osapi.OperatorFailing, osapi.ConditionFalse, "The samples operator configuration is valid")
	case sampleResource.ConditionFalse(v1alpha1.ConfigurationValid):
		err = o.setOperatorStatus(sampleResource.Name, osapi.OperatorFailing, osapi.ConditionTrue, "The samples operator configuration is invalid")
	default:
		err = o.setOperatorStatus(sampleResource.Name, osapi.OperatorFailing, osapi.ConditionUnknown, "The samples operator configuration state is unknown")
	}
	if err != nil {
		return err
	}

	// We have no meaningful "progressing" status, everything we do basically either instantly succeeds or fails.
	err = o.setOperatorStatus(sampleResource.Name, osapi.OperatorProgressing, osapi.ConditionFalse, "")
	return err
}

func (o *CVOOperatorStatusHandler) setOperatorStatus(name string, condtype osapi.ClusterStatusConditionType, status osapi.ConditionStatus, msg string) error {
	logrus.Debugf("setting clusteroperator status condition %s to %s", condtype, status)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		state, err := o.SDKwrapper.Get(name, o.Namespace)
		if err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("failed to get cluster operator resource %s/%s: %s", state.ObjectMeta.Namespace, state.ObjectMeta.Name, err)
			}

			state.Status.VersionHash = v1alpha1.CodeLevel

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

func (o *CVOOperatorStatusHandler) updateOperatorCondition(op *ocfgapi.ClusterVersion, condition *osapi.ClusterOperatorStatusCondition) (modified bool) {
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
