package operator

import (
	"context"
	"fmt"
	"reflect"

	"github.com/sirupsen/logrus"

	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/retry"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"

	v1 "github.com/openshift/api/samples/v1"
	"github.com/openshift/cluster-samples-operator/pkg/metrics"
	"github.com/openshift/cluster-samples-operator/pkg/util"
)

const (
	ClusterOperatorName = "openshift-samples"
	doingDelete         = "DeletionInProgress"
	unsuppArch              = "UnsupportHWArchitecture"
	ipv6                = "IPv6Platform"
	TBR                 = "TermsBasedRegistryUnreacahable"
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
	return g.configclient.ClusterOperators().Get(context.TODO(), name, metaapi.GetOptions{})
}

func (g *defaultClusterStatusWrapper) UpdateStatus(state *configv1.ClusterOperator) error {
	_, err := g.configclient.ClusterOperators().UpdateStatus(context.TODO(), state, metaapi.UpdateOptions{})
	return err
}

func (g *defaultClusterStatusWrapper) Create(state *configv1.ClusterOperator) error {
	_, err := g.configclient.ClusterOperators().Create(context.TODO(), state, metaapi.CreateOptions{})
	return err
}

// ClusterOperatorWrapper is the interface that wrappers actuall access to github.com/operator-framework/operator-sdk/pkg/sdk
type ClusterOperatorWrapper interface {
	Get(name string) (*configv1.ClusterOperator, error)
	UpdateStatus(state *configv1.ClusterOperator) (err error)
	Create(state *configv1.ClusterOperator) (err error)
}

// this method ensures that Available==true and Degraded==false, regardless of other conditions; it currently
// allows Progressing to be explicitly set to true or false based on the scenario the caller is addressing
func (o *ClusterOperatorHandler) setOperatorStatusWithoutInterrogatingConfig(progressing configv1.ConditionStatus, cfg *v1.Config, reason string) {
	err := o.setOperatorStatus(configv1.OperatorAvailable, configv1.ConditionTrue, "", cfg.Status.Version, reason)
	if err != nil {
		logrus.Warningf("error occurred while attempting to set available condition: %s", err.Error())
	}
	err = o.setOperatorStatus(configv1.OperatorProgressing, progressing, "", cfg.Status.Version, reason)
	if err != nil {
		logrus.Warningf("error occurred while attempting to set progressing condition: %s", err.Error())
	}
	err = o.setOperatorStatus(configv1.OperatorDegraded, configv1.ConditionFalse, "", cfg.Status.Version, reason)
	if err != nil {
		logrus.Warningf("error occurred while attempting to set degraded condition: %s", err.Error())
	}

}

func (o *ClusterOperatorHandler) UpdateOperatorStatus(cfg *v1.Config, deletionInProgress, tbrInaccessible bool) error {
	if deletionInProgress {
		o.setOperatorStatusWithoutInterrogatingConfig(configv1.ConditionTrue, cfg, doingDelete)
		// will ignore errors in delete path, but we at least log them above
		return nil
	}
	if util.IsUnsupportedArch(cfg) {
		o.setOperatorStatusWithoutInterrogatingConfig(configv1.ConditionFalse, cfg, unsuppArch)
		// will ignore errors in unsupported arch path, but we at least log them above
		return nil

	}
	if tbrInaccessible {
		o.setOperatorStatusWithoutInterrogatingConfig(configv1.ConditionFalse, cfg, TBR)
		metrics.TBRInaccessibleOnBoot(true)
		return nil
	}
	metrics.TBRInaccessibleOnBoot(false)

	errs := []error{}
	degraded, degradedReason, degradedDetail := util.ClusterOperatorStatusDegradedCondition(cfg)
	err := o.setOperatorStatus(configv1.OperatorDegraded,
		degraded,
		degradedDetail,
		"",
		degradedReason)
	if err != nil {
		// note error but try the other conditions
		errs = append(errs, err)
		logrus.Warningf("error occurred while attempting to set degraded condition: %s", err.Error())
	}
	flagForMetric := false
	if degraded == configv1.ConditionTrue {
		flagForMetric = true
	}
	metrics.Degraded(flagForMetric)

	available, reasonForAvailable, msgForAvailable := util.ClusterOperatorStatusAvailableCondition(cfg)
	// if we're setting the operator status to available, also set the operator version
	// to the current version.
	err = o.setOperatorStatus(configv1.OperatorAvailable,
		available,
		msgForAvailable,
		cfg.Status.Version,
		reasonForAvailable)
	if err != nil {
		// note error but try the other conditions
		errs = append(errs, err)
		logrus.Warningf("error occurred while attempting to set available condition: %s", err.Error())
	}

	progressing, reasonForProgressing, msgForProgressing := util.ClusterOperatorStatusProgressingCondition(cfg, degradedReason, available)
	err = o.setOperatorStatus(configv1.OperatorProgressing,
		progressing,
		msgForProgressing,
		"",
		reasonForProgressing)
	if err != nil {
		// note error but try the other conditions
		errs = append(errs, err)
		logrus.Warningf("error occurred while attempting to set progressing condition: %s", err.Error())
	}
	return utilerrors.NewAggregate(errs)
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
			{Resource: "namespaces", Name: v1.OperatorNamespace},
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
		if condition.Reason != c.Reason {
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
