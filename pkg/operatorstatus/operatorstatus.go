package operator

import (
	"fmt"
	"reflect"

	"github.com/sirupsen/logrus"

	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"

	v1 "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
)

const (
	ClusterOperatorName = "openshift-samples"
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

func (o *ClusterOperatorHandler) UpdateOperatorStatus(cfg *v1.Config) error {
	var err error
	degraded, degradedReason, degradedDetail := cfg.ClusterOperatorStatusDegradedCondition()
	err = o.setOperatorStatus(configv1.OperatorDegraded,
		degraded,
		degradedDetail,
		"",
		degradedReason)
	if err != nil {
		return err
	}

	available, reasonForAvailable, msgForAvailable := cfg.ClusterOperatorStatusAvailableCondition()
	// if we're setting the operator status to available, also set the operator version
	// to the current version.
	err = o.setOperatorStatus(configv1.OperatorAvailable,
		available,
		msgForAvailable,
		cfg.Status.Version,
		reasonForAvailable)
	if err != nil {
		return err
	}

	progressing, reasonForProgressing, msgForProgressing := cfg.ClusterOperatorStatusProgressingCondition(degradedReason, available)
	err = o.setOperatorStatus(configv1.OperatorProgressing,
		progressing,
		msgForProgressing,
		"",
		reasonForProgressing)
	if err != nil {
		return nil
	}
	return nil
}

func (o *ClusterOperatorHandler) setOperatorStatus(condtype configv1.ClusterStatusConditionType, status configv1.ConditionStatus, msg, currentVersion, reason string) error {
	logrus.Debugf("setting clusteroperator status condition %s to %s with version %s", condtype, status, currentVersion)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		state, err := o.ClusterOperatorWrapper.Get(ClusterOperatorName)
		if err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("failed to get cluster operator resource %s/%s: %s", state.ObjectMeta.Namespace, state.ObjectMeta.Name, err)
			}

			state = &configv1.ClusterOperator{}
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
					Type:               configv1.OperatorDegraded,
					Status:             configv1.ConditionUnknown,
					LastTransitionTime: metaapi.Now(),
				},
			}

			if len(currentVersion) > 0 {
				state.Status.Versions = []configv1.OperandVersion{
					{
						Name:    "operator",
						Version: currentVersion,
					},
				}
			}

			o.updateOperatorCondition(state, &configv1.ClusterOperatorStatusCondition{
				Type:               condtype,
				Status:             status,
				Message:            msg,
				Reason:             reason,
				LastTransitionTime: metaapi.Now(),
			})

			return o.ClusterOperatorWrapper.Create(state)
		}
		modified := o.updateOperatorCondition(state, &configv1.ClusterOperatorStatusCondition{
			Type:               condtype,
			Status:             status,
			Message:            msg,
			Reason:             reason,
			LastTransitionTime: metaapi.Now(),
		})

		// set a new current version when it is provided
		if len(currentVersion) > 0 {
			oldVersions := state.Status.Versions
			state.Status.Versions = []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: currentVersion,
				},
			}
			if !reflect.DeepEqual(state.Status.Versions, oldVersions) {
				modified = true
			}
		}

		if !modified {
			return nil
		}

		state.Status.RelatedObjects = []configv1.ObjectReference{
			{Group: v1.GroupName, Resource: "configs", Name: "cluster"},
			{Resource: "namespaces", Name: "openshift-cluster-samples-operator"},
			{Resource: "namespaces", Name: "openshift"},
		}

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
		if condition.Message != c.Message {
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
