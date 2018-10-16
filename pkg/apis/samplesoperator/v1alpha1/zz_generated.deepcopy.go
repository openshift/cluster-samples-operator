// +build !ignore_autogenerated

// Code generated by deepcopy-gen. DO NOT EDIT.

package v1alpha1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SamplesResource) DeepCopyInto(out *SamplesResource) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SamplesResource.
func (in *SamplesResource) DeepCopy() *SamplesResource {
	if in == nil {
		return nil
	}
	out := new(SamplesResource)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *SamplesResource) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SamplesResourceCondition) DeepCopyInto(out *SamplesResourceCondition) {
	*out = *in
	in.LastUpdateTime.DeepCopyInto(&out.LastUpdateTime)
	in.LastTransitionTime.DeepCopyInto(&out.LastTransitionTime)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SamplesResourceCondition.
func (in *SamplesResourceCondition) DeepCopy() *SamplesResourceCondition {
	if in == nil {
		return nil
	}
	out := new(SamplesResourceCondition)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SamplesResourceList) DeepCopyInto(out *SamplesResourceList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]SamplesResource, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SamplesResourceList.
func (in *SamplesResourceList) DeepCopy() *SamplesResourceList {
	if in == nil {
		return nil
	}
	out := new(SamplesResourceList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *SamplesResourceList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SamplesResourceSpec) DeepCopyInto(out *SamplesResourceSpec) {
	*out = *in
	if in.Architectures != nil {
		in, out := &in.Architectures, &out.Architectures
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.SkippedImagestreams != nil {
		in, out := &in.SkippedImagestreams, &out.SkippedImagestreams
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.SkippedTemplates != nil {
		in, out := &in.SkippedTemplates, &out.SkippedTemplates
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SamplesResourceSpec.
func (in *SamplesResourceSpec) DeepCopy() *SamplesResourceSpec {
	if in == nil {
		return nil
	}
	out := new(SamplesResourceSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SamplesResourceStatus) DeepCopyInto(out *SamplesResourceStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]SamplesResourceCondition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SamplesResourceStatus.
func (in *SamplesResourceStatus) DeepCopy() *SamplesResourceStatus {
	if in == nil {
		return nil
	}
	out := new(SamplesResourceStatus)
	in.DeepCopyInto(out)
	return out
}
