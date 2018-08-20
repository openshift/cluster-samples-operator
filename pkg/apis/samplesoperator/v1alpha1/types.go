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

type SamplesResourceSpec struct {
	// defaults to registry.redhat.io
	SamplesRegistry string
	// whether to install the rhel imagestreams
	InstallRHEL bool
	// whether to install the centos imagestreams
	InstallCentos bool
	// which architecture(s) to install
	Architectures []string
	// reference to a secret containing a username+password/token
	// for the registry.  Will be turned into a dockerconfig secret
	// for use during imagestream import.
	RegistryCredentials corev1.LocalObjectReference

	// Names of content that should NOT be created/recreated if
	// missing.  Admins can use this to allow them to delete content
	// they don’t want.  They will still have to manually delete the
	// content but the operator will not recreate(or updated) anything
	// listed here.
	SkippedImagestreams []string
	SkippedTemplates    []string
}
type SamplesResourceStatus struct {
	// Conditions represents the available maintenance status of the sample
	// imagestreams and templates.
	Conditions []SamplesResourceCondition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,8,rep,name=conditions"`
}

type SamplesResourceConditionType string

// the valid conditions of the SamplesResource

const (
	SamplesUpToDate SamplesResourceConditionType = "UpToDate"

	SamplesUpdateFailed SamplesResourceConditionType = "UpdateFailed"
)

type SamplesResourceCondition struct {
	// Type of condition.
	Type SamplesResourceConditionType `json:"type" protobuf:"bytes,1,opt,name=type,casttype=SamplesResourceConditionType"`
	// Status of the condition, one of True, False, Unknown.
	Status corev1.ConditionStatus `json:"status" protobuf:"bytes,2,opt,name=status,casttype=k8s.io/kubernetes/pkg/api/v1.ConditionStatus"`
	// The last time this condition was updated.
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty" protobuf:"bytes,6,opt,name=lastUpdateTime"`
	// The last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty" protobuf:"bytes,3,opt,name=lastTransitionTime"`
	// The reason for the condition's last transition.
	Reason string `json:"reason,omitempty" protobuf:"bytes,4,opt,name=reason"`
	// A human readable message indicating details about the transition.
	Message string `json:"message,omitempty" protobuf:"bytes,5,opt,name=message"`
}
