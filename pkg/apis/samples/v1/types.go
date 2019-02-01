package v1

import (
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/pkg/version"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []Config `json:"items"`
}

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type Config struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              ConfigSpec   `json:"spec"`
	Status            ConfigStatus `json:"status,omitempty"`
}

const (
	// SamplesRegistryCredentials is the name for a secret that contains a username+password/token
	// for the registry, where if the secret is present, will be used for authentication.
	// The corresponding secret is required to already be formatted as a
	// dockerconfig secret so that it can just be copied
	// to the openshift namespace
	// for use during imagestream import.
	SamplesRegistryCredentials = "samples-registry-credentials"
	// ConfigName is the name/identifier of the static, singleton operator employed for the samples.
	ConfigName = "cluster"
	// X86Architecture is the value used to specify the x86_64 hardware architecture
	// in the Architectures array field.
	X86Architecture = "x86_64"
	// PPCArchitecture is the value used to specify the ppc64le hardware architecture
	// in the Architectures array field.
	PPCArchitecture = "ppc64le"
	// ConfigFinalizer is the text added to the Config.Finalizer field
	// to enable finalizer processing.
	ConfigFinalizer = GroupName + "/finalizer"
	// SamplesManagedLabel is the key for a label added to all the imagestreams and templates
	// in the openshift namespace that the Config is managing.  This label is adjusted
	// when changes to the SkippedImagestreams and SkippedTemplates fields are made.
	SamplesManagedLabel = GroupName + "/managed"
	// SamplesVersionAnnotation is the key for an annotation set on the imagestreams, templates,
	// and secret that this operator manages that signifies the version of the operator that
	// last managed the particular resource.
	SamplesVersionAnnotation = GroupName + "/version"
	// SamplesRecreateCredentialAnnotation is the key for an annotation set on the secret used
	// for authentication when configuration moves from Removed to Managed but the associated secret
	// in the openshift namespace does not exist.  This will initiate creation of the credential
	// in the openshift namespace.
	SamplesRecreateCredentialAnnotation = GroupName + "/recreate"
)

func GitVersionString() string {
	vinfo := version.Get()
	versionString := "4.0.0-alpha1-"
	switch {
	case len(vinfo.GitVersion) > 0:
		versionString = string(vinfo.GitVersion) + "-"
		fallthrough
	case len(vinfo.GitCommit) > 0:
		c := string(vinfo.GitCommit)[0:9]
		versionString = versionString + c
	default:
		versionString = "4.0.0-was-not-built-properly"
	}
	return versionString
}

type ConfigSpec struct {
	// ManagementState is top level on/off type of switch for all operators.
	// When "Managed", this operator processes config and manipulates the samples accordingly.
	// When "Unmanaged", this operator ignores any updates to the resources it watches.
	// When "Removed", it reacts that same wasy as it does if the Config object
	// is deleted, meaning any ImageStreams or Templates it manages (i.e. it honors the skipped
	// lists) and the registry secret are deleted, along with the ConfigMap in the operator's
	// namespace that represents the last config used to manipulate the samples,
	ManagementState operatorv1.ManagementState `json:"managementState,omitempty" protobuf:"bytes,1,opt,name=managementState"`

	// SamplesRegistry allows for the specification of which registry is accessed
	// by the ImageStreams for their image content.  Defaults depend on the InstallType.
	// An InstallType of 'rhel' defaults to registry.redhat.io, and an InstallType of
	// 'centos' defaults to docker.io.
	SamplesRegistry string `json:"samplesRegistry,omitempty" protobuf:"bytes,2,opt,name=samplesRegistry"`

	// Architectures determine which hardware architecture(s) to install, where x86_64 and ppc64le are the
	// supported choices.
	Architectures []string `json:"architectures,omitempty" protobuf:"bytes,4,opt,name=architectures"`

	// SkippedImagestreams specifies names of image streams that should NOT be
	// created/updated.  Admins can use this to allow them to delete content
	// they don’t want.  They will still have to manually delete the
	// content but the operator will not recreate(or update) anything
	// listed here.
	SkippedImagestreams []string `json:"skippedImagestreams,omitempty" protobuf:"bytes,5,opt,name=skippedImagestreams"`

	// SkippedTemplates specifies names of templates that should NOT be
	// created/updated.  Admins can use this to allow them to delete content
	// they don’t want.  They will still have to manually delete the
	// content but the operator will not recreate(or update) anything
	// listed here.
	SkippedTemplates []string `json:"skippedTemplates,omitempty" protobuf:"bytes,6,opt,name=skippedTemplates"`
}
type ConfigStatus struct {
	// operatorv1.ManagementState reflects the current operational status of the on/off switch for
	// the operator.  This operator compares the ManagementState as part of determining that we are turning
	// the operator back on (i.e. "Managed") when it was previously "Unmanaged".
	ManagementState operatorv1.ManagementState `json:"managementState,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=managementState"`
	// Conditions represents the available maintenance status of the sample
	// imagestreams and templates.
	Conditions []ConfigCondition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,2,rep,name=conditions"`

	// SamplesRegistry allows for the specification of which registry is accessed
	// by the ImageStreams for their image content.  Defaults depend on the InstallType.
	// An InstallType of 'rhel' defaults to registry.redhat.io, and an InstallType of
	// 'centos' defaults to docker.io.
	SamplesRegistry string `json:"samplesRegistry,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,3,rep,name=samplesRegistry"`

	// Architectures determine which hardware architecture(s) to install, where x86_64 and ppc64le are the
	// supported choices.
	Architectures []string `json:"architectures,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,5,rep,name=architectures"`

	// SkippedImagestreams specifies names of image streams that should NOT be
	// created/updated.  Admins can use this to allow them to delete content
	// they don’t want.  They will still have to manually delete the
	// content but the operator will not recreate(or update) anything
	// listed here.
	SkippedImagestreams []string `json:"skippedImagestreams,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,6,rep,name=skippedImagestreams"`

	// SkippedTemplates specifies names of templates that should NOT be
	// created/updated.  Admins can use this to allow them to delete content
	// they don’t want.  They will still have to manually delete the
	// content but the operator will not recreate(or update) anything
	// listed here.
	SkippedTemplates []string `json:"skippedTemplates,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,7,rep,name=skippedTemplates"`

	// Version is the value of the operator's git based version indicator when it was last successfully processed
	Version string `json:"version,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,8,rep,name=version"`
}

type ConfigConditionType string

// the valid conditions of the Config

const (
	// ImportCredentialsExist represents the state of any credentials specified by
	// the SamplesRegistry field in the Spec.
	ImportCredentialsExist ConfigConditionType = "ImportCredentialsExist"
	// SamplesExist represents whether an incoming Config has been successfully
	// processed or not all, or whether the last Config to come in has been
	// successfully processed.
	SamplesExist ConfigConditionType = "SamplesExist"
	// ConfigurationValid represents whether the latest Config to come in
	// tried to make a support configuration change.  Currently, changes to the
	// InstallType and Architectures list after initial processing is not allowed.
	ConfigurationValid ConfigConditionType = "ConfigurationValid"
	// ImageChangesInProgress represents the state between where the samples operator has
	// started updating the imagestreams and when the spec and status generations for each
	// tag match.  The list of imagestreams that are still in progress will be stored
	// in the Reason field of the condition.  The Reason field being empty corresponds
	// with this condition being marked true.
	ImageChangesInProgress ConfigConditionType = "ImageChangesInProgress"
	// RemovePending represents whether the Config ManagementState
	// has been set to Removed while a samples creation/update cycle is still in progress.  In other
	// words, when ImageChangesInProgress is True.  We
	// do not want to the create/updates and deletes of the samples to be occurring in parallel.
	// So the actual Removed processing will be initated only after ImageChangesInProgress is set
	// to false.  NOTE:  the optimistic update contention between the imagestream watch trying to
	// update ImageChangesInProgress and the sampleresource watch simply returning an error an initiating
	// a retry when ManagementState was set to Removed lead to a prolonged, sometimes seemingly unresolved,
	// period of circular contention
	RemovePending ConfigConditionType = "RemovePending"
	// MigrationInProgress represents the special case where the operator is running off of
	// a new version of its image, and samples are deployed of a previous version.  This condition
	// facilitates the maintenance of this operator's ClusterOperator object.
	MigrationInProgress ConfigConditionType = "MigrationInProgress"
	// ImportImageErrorsExist registers any image import failures, separate from ImageChangeInProgress,
	// so that we can a) indicate a problem to the ClusterOperator status, b) mark the current
	// change cycle as complete in both ClusterOperator and Config; retry on import will
	// occur by the next relist interval if it was an intermittent issue;
	ImportImageErrorsExist ConfigConditionType = "ImportImageErrorsExist"
)

// ConfigCondition captures various conditions of the Config
// as entries are processed.
type ConfigCondition struct {
	// Type of condition.
	Type ConfigConditionType `json:"type" protobuf:"bytes,1,opt,name=type,casttype=ConfigConditionType"`
	// Status of the condition, one of True, False, Unknown.
	Status corev1.ConditionStatus `json:"status" protobuf:"bytes,2,opt,name=status,casttype=k8s.io/kubernetes/pkg/api/v1.ConditionStatus"`
	// The last time this condition was updated.
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty" protobuf:"bytes,3,opt,name=lastUpdateTime"`
	// The last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty" protobuf:"bytes,4,opt,name=lastTransitionTime"`
	// The reason for the condition's last transition.
	Reason string `json:"reason,omitempty" protobuf:"bytes,5,opt,name=reason"`
	// A human readable message indicating details about the transition.
	Message string `json:"message,omitempty" protobuf:"bytes,6,opt,name=message"`
}

func (s *Config) ConditionTrue(c ConfigConditionType) bool {
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

func (s *Config) ConditionFalse(c ConfigConditionType) bool {
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

func (s *Config) ConditionUnknown(c ConfigConditionType) bool {
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

func (s *Config) AnyConditionUnknown() bool {
	for _, rc := range s.Status.Conditions {
		if rc.Status == corev1.ConditionUnknown {
			return true
		}
	}
	return false
}

func (s *Config) ConditionsMessages() string {
	consolidatedMessage := ""
	for _, c := range s.Status.Conditions {
		if len(c.Message) > 0 {
			consolidatedMessage = consolidatedMessage + c.Message + ";"
		}
	}
	return consolidatedMessage
}

func (s *Config) ConditionUpdate(c *ConfigCondition) {
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

func (s *Config) Condition(c ConfigConditionType) *ConfigCondition {
	if s.Status.Conditions != nil {
		for _, rc := range s.Status.Conditions {
			if rc.Type == c {
				return &rc
			}
		}
	}
	now := metav1.Now()
	newCondition := ConfigCondition{
		Type:               c,
		Status:             corev1.ConditionFalse,
		LastTransitionTime: now,
		LastUpdateTime:     now,
	}
	s.Status.Conditions = append(s.Status.Conditions, newCondition)
	return &newCondition
}

func (s *Config) NameInReason(reason, name string) bool {
	switch {
	case strings.Index(reason, name+" ") == 0:
		// if the first entry is name + " "
		return true
	case strings.Contains(reason, " "+name+" "):
		// otherwise, for a subsequent entry, it must have a preceding space,
		// to account for 'jenkins-agent-nodejs' vs. 'nodejs'
		return true
	default:
		return false
	}
}

func (s *Config) ClearNameInReason(reason, name string) string {
	switch {
	case strings.Index(reason, name+" ") == 0:
		return strings.Replace(reason, name+" ", "", 1)
	case strings.Contains(reason, " "+name+" "):
		return strings.Replace(reason, " "+name+" ", " ", 1)
	default:
		return reason
	}
}

const (
	noInstallDetailed = "Samples installation in error at %s: %s"
	installed         = "Samples installation successful at %s"
	moving            = "Samples processing to %s"
)

// ClusterOperatorStatusAvailableCondition return values are as follows:
// 1) the value to set on the ClusterOperator Available condition
// 2) string is the message to set on the Available condition
func (s *Config) ClusterOperatorStatusAvailableCondition() (configv1.ConditionStatus, string) {
	//notAtAnyVersionYet := len(s.Status.Version) == 0

	falseRC := configv1.ConditionFalse

	// REMINDER: the intital config is always valid, as this operator generates it;
	// only config changes after by a human cluster admin after
	// the initial install result in ConfigurationValid == CondtitionFalse
	// Next, if say bad config is injected after installing at a certain level,
	// the samples are still available at the old config setting; the
	// config issues will be highlighted in the progressing/failing messages, per
	// https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusteroperator.md#conditions

	if !s.ConditionTrue(SamplesExist) { // notAtAnyVersionYet {
		// return false for the initial state; don't set any messages yet
		return falseRC, ""
	}

	// otherwise version of last successful install
	versionToNote := s.Status.Version
	if len(versionToNote) == 0 {
		// initial install is still in progress, but we are far
		// enough along that we report this version to the cluster operator
		// we still don't set the version on Config until images in progress
		// flushes out
		versionToNote = GitVersionString()
	}
	return configv1.ConditionTrue, fmt.Sprintf(installed, versionToNote) //s.Status.Version)

}

// ClusterOperatorStatusFailingCondition return values are as follows:
// 1) the value to set on the ClusterOperator Failing condition
// 2) the first string is the succinct text to apply to the Progressing condition on failure
// 3) the second string is the fully detailed text to apply the the Failing condition
func (s *Config) ClusterOperatorStatusFailingCondition() (configv1.ConditionStatus, string, string) {
	// the ordering here is not random; an invalid config will be caught first;
	// the lack of credenitials will be caught second; any hiccups manipulating API objects
	// will be potentially anywhere in the process
	trueRC := configv1.ConditionTrue
	if s.ConditionFalse(ConfigurationValid) {
		return trueRC,
			"invalid configuration",
			fmt.Sprintf(noInstallDetailed, GitVersionString(), s.Condition(ConfigurationValid).Message)
	}
	if s.ClusterNeedsCreds() {
		return trueRC,
			"image pull credentials needed",
			fmt.Sprintf(noInstallDetailed, GitVersionString(), s.Condition(ImportCredentialsExist).Message)
	}
	/*if s.ConditionTrue(ImportImageErrorsExist) {
		return trueRC,
			"image import problem",
			fmt.Sprintf(noInstallDetailed, GitVersionString(), s.Condition(ImportImageErrorsExist).Message)
	}*/
	// right now, any condition being unknown is indicative of a failure
	// condition, either api server interaction or file system interaction;
	// Conversely, those errors result in a ConditionUnknown setting on one
	// of the conditions;
	// If for some reason that ever changes, we'll need to adjust this
	if s.AnyConditionUnknown() {
		return trueRC, "bad API object operation", s.ConditionsMessages()
	}
	// return the initial state, don't set any messages.
	return configv1.ConditionFalse, "", ""

}

// ClusterOperatorStatusProgressingCondition has the following parameters
// 1) failingState, the succinct text from ClusterOperatorStatusFailingCondition() to use when
//    progressing but failed per https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusteroperator.md#conditions
// 2) whether the Config is in available state
// and the following return values:
// 1) is the value to set on the ClusterOperator Progressing condition
// 2) string is the message to set on the condition
func (s *Config) ClusterOperatorStatusProgressingCondition(failingState string, available configv1.ConditionStatus) (configv1.ConditionStatus, string) {
	if len(failingState) > 0 {
		return configv1.ConditionTrue, fmt.Sprintf(noInstallDetailed, GitVersionString(), failingState)
	}
	if s.ConditionTrue(ImageChangesInProgress) {
		return configv1.ConditionTrue, fmt.Sprintf(moving, GitVersionString())
	}
	if available == configv1.ConditionTrue {
		return configv1.ConditionFalse, fmt.Sprintf(installed, s.Status.Version)
	}
	return configv1.ConditionFalse, ""
}

// ClusterNeedsCreds checks the conditions that drive whether the operator complains about
// needing credentials to import RHEL content
func (s *Config) ClusterNeedsCreds() bool {
	if s.Spec.ManagementState == operatorv1.Removed ||
		s.Spec.ManagementState == operatorv1.Unmanaged {
		return false
	}
	return s.ConditionFalse(ImportCredentialsExist) && (s.Spec.SamplesRegistry == "" || s.Spec.SamplesRegistry == "registry.redhat.io")
}

type Event struct {
	Object  runtime.Object
	Deleted bool
}
