package v1alpha1

import (
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
)

type SamplesResourceSpec struct {
	// SamplesRegistry allows for the specification of which registry is accessed
	// by the ImageStreams for their image content.  Defaults depend on the InstallType.
	// An InstallType of 'rhel' defaults to registry.redhat.io, and an InstallType of
	// 'centos' defaults to docker.io.
	SamplesRegistry string `json:"sampleRegistry,omitempty" protobuf:"bytes,1,opt,name=sampleRegistry"`

	// InstallType specifies whether to install the RHEL or Centos distributions.
	InstallType SamplesDistributionType `json:"installType,omitempty" protobuf:"bytes,2,opt,name=installType"`

	// Architectures determine which hardware architecture(s) to install, where x86_64 and ppc64le are the
	// supported choices.
	Architectures []string `json:"architectures,omitempty" protobuf:"bytes,3,opt,name=architectures"`

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
	// Conditions represents the available maintenance status of the sample
	// imagestreams and templates.
	Conditions []SamplesResourceCondition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

type SamplesResourceConditionType string

// the valid conditions of the SamplesResource

const (
	SamplesUpdateFailed    SamplesResourceConditionType = "SamplesResourceUpdateFailed"
	SecretUpdateFailed     SamplesResourceConditionType = "SecretUpdateFailed"
	ImportCredentialsExist SamplesResourceConditionType = "ImportCredentialsExists"
	SamplesExist           SamplesResourceConditionType = "SamplesExists"
)

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
	for _, rc := range s.Status.Conditions {
		if rc.Type == c && rc.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (s *SamplesResource) ConditionUpdate(c *SamplesResourceCondition) {
	for i, ec := range s.Status.Conditions {
		if ec.Type == c.Type {
			s.Status.Conditions[i] = *c
			return
		}
	}
}

func (s *SamplesResource) Condition(c SamplesResourceConditionType) *SamplesResourceCondition {
	for _, rc := range s.Status.Conditions {
		if rc.Type == c {
			return &rc
		}
	}
	newCondition := SamplesResourceCondition{
		Type:   c,
		Status: corev1.ConditionUnknown,
	}
	s.Status.Conditions = append(s.Status.Conditions, newCondition)
	return &newCondition
}
