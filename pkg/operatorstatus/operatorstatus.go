package operator

import (
	"fmt"

	"github.com/sirupsen/logrus"

	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/util/retry"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"

	"github.com/openshift/cluster-samples-operator/pkg/apis/samplesoperator/v1alpha1"
)

const (
	ClusterOperatorName = "openshift-cluster-samples-operator"
)

// ClusterOperatorHandler allows for wrappering access to configv1.ClusterOperator
type ClusterOperatorHandler struct {
	ClusterOperatorWrapper ClusterOperatorWrapper
}

// NewClusterOperatorHandler is the default initializer function for CVOOperatorStatusHandler
func NewClusterOperatorHandler(cfgclient *configv1client.ConfigV1Client) *ClusterOperatorHandler {
	handler := ClusterOperatorHandler{}
	handler.ClusterOperatorWrapper = &defaultClusterStatusWrapper{configclient: cfgclient}
	return &handler
}

type defaultClusterStatusWrapper struct {
	configclient *configv1client.ConfigV1Client
}

func (g *defaultClusterStatusWrapper) Get(name string) (*configv1.ClusterOperator, error) {
	return g.configclient.ClusterOperators().Get(name, metaapi.GetOptions{})
}

func (g *defaultClusterStatusWrapper) UpdateStatus(state *configv1.ClusterOperator) error {
	_, err := g.configclient.ClusterOperators().UpdateStatus(state)
	return err
}

func (g *defaultClusterStatusWrapper) Create(state *configv1.ClusterOperator) error {
	_, err := g.configclient.ClusterOperators().Create(state)
	return err
}

// ClusterOperatorWrapper is the interface that wrappers actuall access to github.com/operator-framework/operator-sdk/pkg/sdk
type ClusterOperatorWrapper interface {
	Get(name string) (*configv1.ClusterOperator, error)
	UpdateStatus(state *configv1.ClusterOperator) (err error)
	Create(state *configv1.ClusterOperator) (err error)
}

func (o *ClusterOperatorHandler) UpdateOperatorStatus(sampleResource *v1alpha1.SamplesResource) error {
	var err error
	switch {
	case sampleResource.ConditionTrue(v1alpha1.SamplesExist):
		err = o.setOperatorStatus(configv1.OperatorAvailable, configv1.ConditionTrue, "Samples exist in the openshift project")
	case sampleResource.ConditionFalse(v1alpha1.SamplesExist):
		err = o.setOperatorStatus(configv1.OperatorAvailable, configv1.ConditionFalse, "Samples do not exist in the openshift project")
	default:
		err = o.setOperatorStatus(configv1.OperatorAvailable, configv1.ConditionUnknown, "The presence of the samples in the openshift project is unknown")
	}
	if err != nil {
		return err
	}

	switch {
	case sampleResource.ConditionTrue(v1alpha1.ConfigurationValid):
		err = o.setOperatorStatus(configv1.OperatorFailing, configv1.ConditionFalse, "The samples operator configuration is valid")
	case sampleResource.ConditionFalse(v1alpha1.ConfigurationValid):
		err = o.setOperatorStatus(configv1.OperatorFailing, configv1.ConditionTrue, "The samples operator configuration is invalid")
	default:
		err = o.setOperatorStatus(configv1.OperatorFailing, configv1.ConditionUnknown, "The samples operator configuration state is unknown")
	}
	if err != nil {
		return err
	}

	switch {
	case sampleResource.ConditionTrue(v1alpha1.ImageChangesInProgress):
		err = o.setOperatorStatus(configv1.OperatorProgressing, configv1.ConditionTrue, "The samples operator is in the middle of changing the imagestreams")
	case sampleResource.ConditionFalse(v1alpha1.ImageChangesInProgress):
		err = o.setOperatorStatus(configv1.OperatorProgressing, configv1.ConditionFalse, "The samples operator is not in the process of initiating changes to the imagestreams")
	default:
		err = o.setOperatorStatus(configv1.OperatorProgressing, configv1.ConditionUnknown, "The samples operator in progress state is unknown")
	}

	return err
}

func (o *ClusterOperatorHandler) setOperatorStatus(condtype configv1.ClusterStatusConditionType, status configv1.ConditionStatus, msg string) error {
	logrus.Debugf("setting clusteroperator status condition %s to %s", condtype, status)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		state, err := o.ClusterOperatorWrapper.Get(ClusterOperatorName)
		if err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("failed to get cluster operator resource %s/%s: %s", state.ObjectMeta.Namespace, state.ObjectMeta.Name, err)
			}

			state.Name = ClusterOperatorName

			state.Status.Conditions = []configv1.ClusterOperatorStatusCondition{
				{
					Type:               configv1.OperatorAvailable,
					Status:             configv1.ConditionUnknown,
					LastTransitionTime: metaapi.Now(),
				},
				{
					Type:               configv1.OperatorProgressing,
					Status:             configv1.ConditionUnknown,
					LastTransitionTime: metaapi.Now(),
				},
				{
					Type:               configv1.OperatorFailing,
					Status:             configv1.ConditionUnknown,
					LastTransitionTime: metaapi.Now(),
				},
			}

			o.updateOperatorCondition(state, &configv1.ClusterOperatorStatusCondition{
				Type:               condtype,
				Status:             status,
				Message:            msg,
				LastTransitionTime: metaapi.Now(),
			})

			return o.ClusterOperatorWrapper.Create(state)
		}
		modified := o.updateOperatorCondition(state, &configv1.ClusterOperatorStatusCondition{
			Type:               condtype,
			Status:             status,
			Message:            msg,
			LastTransitionTime: metaapi.Now(),
		})
		if !modified {
			return nil
		}
		vinfo := version.Get()
		versionString := ""
		switch {
		case len(vinfo.GitVersion) > 0:
			versionString = string(vinfo.GitVersion) + "-"
			fallthrough
		case len(vinfo.GitCommit) > 0:
			versionString = versionString + string(vinfo.GitCommit)
		default:
			versionString = "v0.0.0-was-not-built-properly"
		}
		state.Status.Version = versionString
		v1alpha1.CodeLevel = versionString
		return o.ClusterOperatorWrapper.UpdateStatus(state)
	})
}

func (o *ClusterOperatorHandler) updateOperatorCondition(op *configv1.ClusterOperator, condition *configv1.ClusterOperatorStatusCondition) (modified bool) {
	found := false
	conditions := []configv1.ClusterOperatorStatusCondition{}

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
