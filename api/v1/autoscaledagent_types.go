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

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// AutoScaledAgentSpec defines the desired state of AutoScaledAgent
type AutoScaledAgentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// PoolName is the name of the Azure DevOps pool name
	PoolName string `json:"poolName,omitempty"`

	// OrganizationUrl is the HTTPS URL to the organization, e.g. https://dev.azure.com/foobar if you use the SaaS version
	OrganizationUrl string `json:"organizationUrl,omitempty"`

	PersonalAccessTokenSecretName string `json:"personalAccessTokenSecretName,omitempty"`

	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default:=5
	MaxTerminatedPodsToKeep *int32 `json:"maxTerminatedPodsToKeep,omitempty"`

	PodsWithCapabilities []PodsWithCapabilities `json:"podsWithCapabilities,omitempty"`
}

type PodsWithCapabilities struct {
	Capabilities map[string]string `json:"capabilities,omitempty"`

	//+kubebuilder:validation:Minimum=0
	MinCount *int32 `json:"minCount,omitempty"`

	//+kubebuilder:validation:Minimum=0
	MaxCount *int32 `json:"maxCount,omitempty"`

	PodLabels map[string]string `json:"podLabels,omitempty"`

	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	PodTemplateSpec corev1.PodTemplateSpec `json:"podTemplateSpec"`
}

// AutoScaledAgentStatus defines the observed state of AutoScaledAgent
type AutoScaledAgentStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// AutoScaledAgent is the Schema for the autoscaledagents API
type AutoScaledAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AutoScaledAgentSpec   `json:"spec,omitempty"`
	Status AutoScaledAgentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// AutoScaledAgentList contains a list of AutoScaledAgent
type AutoScaledAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AutoScaledAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AutoScaledAgent{}, &AutoScaledAgentList{})
}
