package util

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"os"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8snet "k8s.io/apimachinery/pkg/util/net"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	samplev1 "github.com/openshift/api/samples/v1"
)

var (
	// conditionUpsertLock helps us avoid duplicate entries in the condition array when mutliple
	// events come in concurrently ... most noticeable on the secret watch and ImportCredentialsExists
	conditionUpsertLock = sync.Mutex{}
)

func ConditionTrue(s *samplev1.Config, c samplev1.ConfigConditionType) bool {
	if s.Status.Conditions == nil {
		return false
	}
	for _, rc := range s.Status.Conditions {
		if rc.Type == c && rc.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func ConditionFalse(s *samplev1.Config, c samplev1.ConfigConditionType) bool {
	if s.Status.Conditions == nil {
		return false
	}
	for _, rc := range s.Status.Conditions {
		if rc.Type == c && rc.Status == corev1.ConditionFalse {
			return true
		}
	}
	return false
}

func ConditionUnknown(s *samplev1.Config, c samplev1.ConfigConditionType) bool {
	if s.Status.Conditions == nil {
		return false
	}
	for _, rc := range s.Status.Conditions {
		if rc.Type == c && rc.Status == corev1.ConditionUnknown {
			return true
		}
	}
	return false
}

func AnyConditionUnknown(s *samplev1.Config) (bool, string) {
	for _, rc := range s.Status.Conditions {
		if rc.Status == corev1.ConditionUnknown {
			return true, rc.Reason
		}
	}
	return false, ""
}

func ConditionsMessages(s *samplev1.Config) string {
	consolidatedMessage := ""
	for _, c := range s.Status.Conditions {
		if len(c.Message) > 0 {
			consolidatedMessage = consolidatedMessage + c.Message + ";"
		}
	}
	return consolidatedMessage
}

func ConditionUpdate(s *samplev1.Config, c *samplev1.ConfigCondition) {
	conditionUpsertLock.Lock()
	defer conditionUpsertLock.Unlock()

	if s.Status.Conditions == nil {
		return
	}
	for i, ec := range s.Status.Conditions {
		if ec.Type == c.Type {
			s.Status.Conditions[i] = *c
			return
		}
	}
}

func Condition(s *samplev1.Config, c samplev1.ConfigConditionType) *samplev1.ConfigCondition {
	conditionUpsertLock.Lock()
	defer conditionUpsertLock.Unlock()

	if s.Status.Conditions != nil {
		for _, rc := range s.Status.Conditions {
			if rc.Type == c {
				return &rc
			}
		}
	}
	now := metav1.Now()
	newCondition := samplev1.ConfigCondition{
		Type:               c,
		Status:             corev1.ConditionFalse,
		LastTransitionTime: now,
		LastUpdateTime:     now,
	}
	s.Status.Conditions = append(s.Status.Conditions, newCondition)
	return &newCondition
}

const (
	noInstallDetailed      = "Samples installation in error at %s: %s"
	installed              = "Samples installation successful at %s"
	installedButNotManaged = "Samples installation was previously successful at %s but the samples operator is now %s"
	moving                 = "Samples processing to %s"
	removing               = "Deleting samples at %s"
	doneImportsFailed      = "Samples installed at %s, with image import failures for these imagestreams: %s; last import attempt %s"
	failedImageImports     = "FailedImageImports"
	currentlyNotManaged    = "Currently%s"
	// numConfigConditionType is a helper constant that captures the number possible conditions
	// defined above in this const block
	numconfigConditionType = 7
	IST2ImageMap           = "imagestreamtag-to-image"
	ImageStreamErrorLabel  = "samples.openshift.io/imagestream-error"
)

// ClusterOperatorStatusAvailableCondition return values are as follows:
// 1) the value to set on the ClusterOperator Available condition
// 2) string is the reason to set on the Available condition
// 3) string is the message to set on the Available condition
func ClusterOperatorStatusAvailableCondition(s *samplev1.Config) (configv1.ConditionStatus, string, string) {
	//notAtAnyVersionYet := len(s.Status.Version) == 0

	falseRC := configv1.ConditionFalse

	// after online starter upgrade attempts while this operator was not set to managed,
	// group arch discussion has decided that we report the Available=true if removed/unmanaged
	if s.Status.ManagementState == operatorv1.Removed ||
		s.Status.ManagementState == operatorv1.Unmanaged {
		state := string(s.Status.ManagementState)
		return configv1.ConditionTrue, fmt.Sprintf(currentlyNotManaged, state), fmt.Sprintf(installedButNotManaged, s.Status.Version, state)
	}

	// REMINDER: the intital config is always valid, as this operator generates it;
	// only config changes after by a human cluster admin after
	// the initial install result in ConfigurationValid == CondtitionFalse
	// Next, if say bad config is injected after installing at a certain level,
	// the samples are still available at the old config setting; the
	// config issues will be highlighted in the progressing/degraded messages, per
	// https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusteroperator.md#conditions

	if !ConditionTrue(s, samplev1.SamplesExist) {
		// return false for the initial state; don't set any messages yet
		return falseRC, "", ""
	}

	// otherwise version of last successful install
	versionToNote := s.Status.Version
	if len(versionToNote) == 0 {
		// initial install is still in progress, but we are far
		// enough along that we report this version to the cluster operator
		// we still don't set the version on Config until images in progress
		// flushes out
		versionToNote = os.Getenv("RELEASE_VERSION")
	}
	return configv1.ConditionTrue, "", fmt.Sprintf(installed, versionToNote)

}

// ClusterOperatorStatusDegradedCondition return values are as follows:
// 1) the value to set on the ClusterOperator Degraded condition
// 2) the first string is the succinct text to apply to the Degraded condition reason field
// 3) the second string is the fully detailed text to apply the the Degraded condition message field
func ClusterOperatorStatusDegradedCondition(s *samplev1.Config) (configv1.ConditionStatus, string, string) {
	// do not start checking for bad config and needed cred until we've iterated through
	// the credential / config processing to actually processed a config
	if len(s.Status.Conditions) < numconfigConditionType {
		return configv1.ConditionFalse, "", ""
	}

	// after online starter upgrade attempts while this operator was not set to managed,
	// group arch discussion has decided that we report the Degraded==false if removed/unmanaged
	if s.Status.ManagementState == operatorv1.Removed ||
		s.Status.ManagementState == operatorv1.Unmanaged {
		state := string(s.Status.ManagementState)
		return configv1.ConditionFalse, fmt.Sprintf(currentlyNotManaged, state), fmt.Sprintf(installedButNotManaged, s.Status.Version, state)
	}

	// the ordering here is not random; an invalid config will be caught first;
	// the lack of credenitials will be caught second; any hiccups manipulating API objects
	// will be potentially anywhere in the process
	trueRC := configv1.ConditionTrue
	if ConditionFalse(s, samplev1.ConfigurationValid) {
		return trueRC,
			"InvalidConfiguration",
			fmt.Sprintf(noInstallDetailed, os.Getenv("RELEASE_VERSION"), Condition(s, samplev1.ConfigurationValid).Message)
	}

	// right now, any condition being unknown is indicative of a failure
	// condition, either api server interaction or file system interaction;
	// Conversely, those errors result in a ConditionUnknown setting on one
	// of the conditions;
	// If for some reason that ever changes, we'll need to adjust this
	if exists, reason := AnyConditionUnknown(s); exists {
		return trueRC, reason, ConditionsMessages(s)
	}
	// return the initial state, don't set any messages.
	return configv1.ConditionFalse, "", ""

}

// ClusterOperatorStatusProgressingCondition has the following parameters
// 1) degradedState, the succinct text from ClusterOperatorStatusDegradedCondition() to use when
//    progressing but failed per https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusteroperator.md#conditions
// 2) whether the Config is in available state
// and the following return values:
// 1) is the value to set on the ClusterOperator Progressing condition
// 2) string is the reason to set on the condition if needed
// 3) string is the message to set on the condition
func ClusterOperatorStatusProgressingCondition(s *samplev1.Config, degradedState string, available configv1.ConditionStatus, activeStreams []string) (configv1.ConditionStatus, string, string) {

	// after online starter upgrade attempts while this operator was not set to managed,
	// group arch discussion has decided that we report the Progressing==false if removed/unmanaged
	if s.Status.ManagementState == operatorv1.Removed ||
		s.Status.ManagementState == operatorv1.Unmanaged {
		state := string(s.Status.ManagementState)
		return configv1.ConditionFalse, fmt.Sprintf(currentlyNotManaged, state), fmt.Sprintf(installedButNotManaged, s.Status.Version, state)
	}

	if len(degradedState) > 0 {
		return configv1.ConditionTrue, "", fmt.Sprintf(noInstallDetailed, os.Getenv("RELEASE_VERSION"), degradedState)
	}
	if ConditionTrue(s, samplev1.RemovePending) {
		return configv1.ConditionTrue, "", fmt.Sprintf(removing, os.Getenv("RELEASE_VERSION"))
	}
	if available == configv1.ConditionTrue {
		msg := fmt.Sprintf(installed, s.Status.Version)
		reason := ""
		if ConditionTrue(s, samplev1.ImportImageErrorsExist) {
			importErrors := Condition(s, samplev1.ImportImageErrorsExist)
			msg = fmt.Sprintf(doneImportsFailed, s.Status.Version, strings.Join(activeStreams, ","), importErrors.LastUpdateTime.String())
			reason = failedImageImports
		}
		return configv1.ConditionFalse, reason, msg
	}
	if ConditionTrue(s, samplev1.ImageChangesInProgress) {
		return configv1.ConditionTrue, "", fmt.Sprintf(moving, os.Getenv("RELEASE_VERSION"))
	}
	return configv1.ConditionFalse, "", ""
}

// IsUnsupportedArch let's us know if this is something other than x86_64/amd/s390x/ppc
func IsUnsupportedArch(cfg *samplev1.Config) bool {
	if len(cfg.Spec.Architectures) == 0 {
		return false
	}
	switch cfg.Spec.Architectures[0] {
	case samplev1.AMDArchitecture:
		return false
	case samplev1.X86Architecture:
		return false
	case samplev1.S390Architecture:
		return false
	case samplev1.PPCArchitecture:
		return false
	default:
		return true
	}
}

// IsIPv6 let's us know if this is a ipv6 env (assumes single stack)
func IsIPv6() bool {
	ip, err := k8snet.ChooseHostInterface()
	if err != nil {
		logrus.Printf("IPv6 determination: ChooseHostInterface err: %s", err.Error())
		return false
	}
	if ip.To4() != nil {
		return false
	}
	logrus.Printf("based on host %s this is an ipv6 cluster", ip.String())
	return true
}

func ImageStreamErrorExists(cm *corev1.ConfigMap) bool {
	if value, exists := cm.Labels[ImageStreamErrorLabel]; !exists || value != "true" {
		return false
	}
	return true
}

type Event struct {
	Object  runtime.Object
	Deleted bool
}
