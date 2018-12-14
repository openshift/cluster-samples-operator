package v1alpha1

import (
	operatorv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type SamplesResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []SamplesResource `json:"items"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type SamplesResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              SamplesResourceSpec   `json:"spec"`
	Status            SamplesResourceStatus `json:"status,omitempty"`
}

type SamplesDistributionType string

const (
	RHELSamplesDistribution   = SamplesDistributionType("rhel")
	CentosSamplesDistribution = SamplesDistributionType("centos")
)

const (
	// SamplesRegistryCredentials is the name for a secret that contains a username+password/token
	// for the registry, where if the secret is present, will be used for authentication.
	// The corresponding secret is required to already be formatted as a
	// dockerconfig secret so that it can just be copied
	// to the openshift namespace
	// for use during imagestream import.
	SamplesRegistryCredentials = "samples-registry-credentials"
	// SamplesResourceName is the name/identifier of the static, singleton operator employed for the samples.
	SamplesResourceName = "openshift-samples"
	// X86Architecture is the value used to specify the x86_64 hardware architecture
	// in the Architectures array field.
	X86Architecture = "x86_64"
	// PPCArchitecture is the value used to specify the ppc64le hardware architecture
	// in the Architectures array field.
	PPCArchitecture = "ppc64le"
	// SamplesResourceFinalizer is the text added to the SamplesResource.Finalizer field
	// to enable finalizer processing.
	SamplesResourceFinalizer = GroupName + "/finalizer"
	// SamplesManagedLabel is the key for a label added to all the imagestreams and templates
	// in the openshift namespace that the SamplesResource is managing.  This label is adjusted
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

var (
	// CodeLevel is a precise code version level that the operator will use to determine whether it ias
	// been upgraded, and by extension, whether the samples should be updated.  It will be set at the same
	// time the ClusterOperator API Object for the samples operator is set.
	CodeLevel string
)

type SamplesResourceSpec struct {
	// ManagementState is top level on/off type of switch for all operators.
	// When "Managed", this operator processes config and manipulates the samples accordingly.
	// When "Unmanaged", this operator ignores any updates to the resources it watches.
	// When "Removed", it reacts that same wasy as it does if the SamplesResource object
	// is deleted, meaning any ImageStreams or Templates it manages (i.e. it honors the skipped
	// lists) and the registry secret are deleted, along with the ConfigMap in the operator's
	// namespace that represents the last config used to manipulate the samples,
	ManagementState operatorv1.ManagementState `json:"managementState,omitempty" protobuf:"bytes,1,opt,name=managementState"`

	// SamplesRegistry allows for the specification of which registry is accessed
	// by the ImageStreams for their image content.  Defaults depend on the InstallType.
	// An InstallType of 'rhel' defaults to registry.redhat.io, and an InstallType of
	// 'centos' defaults to docker.io.
	SamplesRegistry string `json:"samplesRegistry,omitempty" protobuf:"bytes,2,opt,name=samplesRegistry"`

	// InstallType specifies whether to install the RHEL or Centos distributions.
	InstallType SamplesDistributionType `json:"installType,omitempty" protobuf:"bytes,3,opt,name=installType"`

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
type SamplesResourceStatus struct {
	// operatorv1.ManagementState reflects the current operational status of the on/off switch for
	// the operator.  This operator compares the ManagementState as part of determining that we are turning
	// the operator back on (i.e. "Managed") when it was previously "Unmanaged".  The "Removed" to "Managed"
	// transition is currently handled by the fact that our config map is missing.
	// TODO when we ditch the config map and store current config in the operator's status, we'll most likely
	// need to track "Removed" to "Managed" transitions via compares here as well.
	ManagementState operatorv1.ManagementState `json:"managementState,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=managementState"`
	// Conditions represents the available maintenance status of the sample
	// imagestreams and templates.
	Conditions []SamplesResourceCondition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,2,rep,name=conditions"`

	// SamplesRegistry allows for the specification of which registry is accessed
	// by the ImageStreams for their image content.  Defaults depend on the InstallType.
	// An InstallType of 'rhel' defaults to registry.redhat.io, and an InstallType of
	// 'centos' defaults to docker.io.
	SamplesRegistry string `json:"samplesRegistry,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,3,rep,name=samplesRegistry"`

	// InstallType specifies whether to install the RHEL or Centos distributions.
	InstallType SamplesDistributionType `json:"installType,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,4,rep,name=installType"`

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
}

type SamplesResourceConditionType string

// the valid conditions of the SamplesResource

const (
	// ImportCredentialsExists represents the state of any credentials specified by
	// the SamplesRegistry field in the Spec.
	ImportCredentialsExist SamplesResourceConditionType = "ImportCredentialsExists"
	// SamplesExist represents whether an incoming SamplesResource has been successfully
	// processed or not all, or whether the last SamplesResource to come in has been
	// successfully processed.
	SamplesExist SamplesResourceConditionType = "SamplesExist"
	// ConfigurationValid represents whether the latest SamplesResource to come in
	// tried to make a support configuration change.  Currently, changes to the
	// InstallType and Architectures list after initial processing is not allowed.
	ConfigurationValid SamplesResourceConditionType = "ConfigurationValid"
	// ImageChangesInProgress represents the state between where the samples operator has
	// started updating the imagestreams and when the spec and status generations for each
	// tag match.  The list of imagestreams that are still in progress will be stored
	// in the Reason field of the condition.  The Reason field being empty corresponds
	// with this condition being marked true.
	ImageChangesInProgress SamplesResourceConditionType = "ChangesInProgress"
	// RemovedManagementStateOnHold represents whether the SamplesResource ManagementState
	// has been set to Removed while a samples creation/update cycle is still in progress.  In other
	// words, when ImageChangesInProgress is True.  We
	// do not want to the create/updates and deletes of the samples to be occurring in parallel.
	// So the actual Removed processing will be initated only after ImageChangesInProgress is set
	// to false.  NOTE:  the optimistic update contention between the imagestream watch trying to
	// update ImageChangesInProgress and the sampleresource watch simply returning an error an initiating
	// a retry when ManagementState was set to Removed lead to a prolonged, sometimes seemingly unresolved,
	// period of circular contention
	RemovedManagementStateOnHold SamplesResourceConditionType = "PendingRemove"
)

// SamplesResourceCondition captures various conditions of the SamplesResource
// as entries are processed.
type SamplesResourceCondition struct {
	// Type of condition.
	Type SamplesResourceConditionType `json:"type" protobuf:"bytes,1,opt,name=type,casttype=SamplesResourceConditionType"`
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

func (s *SamplesResource) ConditionTrue(c SamplesResourceConditionType) bool {
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

func (s *SamplesResource) ConditionFalse(c SamplesResourceConditionType) bool {
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

func (s *SamplesResource) ConditionUpdate(c *SamplesResourceCondition) {
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

func (s *SamplesResource) Condition(c SamplesResourceConditionType) *SamplesResourceCondition {
	if s.Status.Conditions != nil {
		for _, rc := range s.Status.Conditions {
			if rc.Type == c {
				return &rc
			}
		}
	}
	newCondition := SamplesResourceCondition{
		Type: c,
	}
	s.Status.Conditions = append(s.Status.Conditions, newCondition)
	return &newCondition
}
