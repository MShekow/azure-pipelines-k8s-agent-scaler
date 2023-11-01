//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Code generated by controller-gen. DO NOT EDIT.

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AutoScaledAgent) DeepCopyInto(out *AutoScaledAgent) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AutoScaledAgent.
func (in *AutoScaledAgent) DeepCopy() *AutoScaledAgent {
	if in == nil {
		return nil
	}
	out := new(AutoScaledAgent)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *AutoScaledAgent) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AutoScaledAgentList) DeepCopyInto(out *AutoScaledAgentList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]AutoScaledAgent, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AutoScaledAgentList.
func (in *AutoScaledAgentList) DeepCopy() *AutoScaledAgentList {
	if in == nil {
		return nil
	}
	out := new(AutoScaledAgentList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *AutoScaledAgentList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AutoScaledAgentSpec) DeepCopyInto(out *AutoScaledAgentSpec) {
	*out = *in
	if in.MaxTerminatedPodsToKeep != nil {
		in, out := &in.MaxTerminatedPodsToKeep, &out.MaxTerminatedPodsToKeep
		*out = new(int32)
		**out = **in
	}
	if in.DummyAgentGarbageCollectionInterval != nil {
		in, out := &in.DummyAgentGarbageCollectionInterval, &out.DummyAgentGarbageCollectionInterval
		*out = new(metav1.Duration)
		**out = **in
	}
	if in.DummyAgentDeletionMinAge != nil {
		in, out := &in.DummyAgentDeletionMinAge, &out.DummyAgentDeletionMinAge
		*out = new(metav1.Duration)
		**out = **in
	}
	if in.NormalOfflineAgentDeletionMinAge != nil {
		in, out := &in.NormalOfflineAgentDeletionMinAge, &out.NormalOfflineAgentDeletionMinAge
		*out = new(metav1.Duration)
		**out = **in
	}
	if in.ReusableCacheVolumes != nil {
		in, out := &in.ReusableCacheVolumes, &out.ReusableCacheVolumes
		*out = make([]ReusableCacheVolume, len(*in))
		copy(*out, *in)
	}
	if in.PodsWithCapabilities != nil {
		in, out := &in.PodsWithCapabilities, &out.PodsWithCapabilities
		*out = make([]PodsWithCapabilities, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AutoScaledAgentSpec.
func (in *AutoScaledAgentSpec) DeepCopy() *AutoScaledAgentSpec {
	if in == nil {
		return nil
	}
	out := new(AutoScaledAgentSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AutoScaledAgentStatus) DeepCopyInto(out *AutoScaledAgentStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AutoScaledAgentStatus.
func (in *AutoScaledAgentStatus) DeepCopy() *AutoScaledAgentStatus {
	if in == nil {
		return nil
	}
	out := new(AutoScaledAgentStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PodsWithCapabilities) DeepCopyInto(out *PodsWithCapabilities) {
	*out = *in
	if in.Capabilities != nil {
		in, out := &in.Capabilities, &out.Capabilities
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	if in.MinCount != nil {
		in, out := &in.MinCount, &out.MinCount
		*out = new(int32)
		**out = **in
	}
	if in.MaxCount != nil {
		in, out := &in.MaxCount, &out.MaxCount
		*out = new(int32)
		**out = **in
	}
	if in.PodLabels != nil {
		in, out := &in.PodLabels, &out.PodLabels
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	if in.PodAnnotations != nil {
		in, out := &in.PodAnnotations, &out.PodAnnotations
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	in.PodTemplateSpec.DeepCopyInto(&out.PodTemplateSpec)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PodsWithCapabilities.
func (in *PodsWithCapabilities) DeepCopy() *PodsWithCapabilities {
	if in == nil {
		return nil
	}
	out := new(PodsWithCapabilities)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ReusableCacheVolume) DeepCopyInto(out *ReusableCacheVolume) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ReusableCacheVolume.
func (in *ReusableCacheVolume) DeepCopy() *ReusableCacheVolume {
	if in == nil {
		return nil
	}
	out := new(ReusableCacheVolume)
	in.DeepCopyInto(out)
	return out
}
