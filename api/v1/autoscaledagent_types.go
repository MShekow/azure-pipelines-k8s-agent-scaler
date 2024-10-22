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

	// Stores the name of a secret that has a key called "pat" whose value is the Azure DevOps Personal Access Token
	PersonalAccessTokenSecretName string `json:"personalAccessTokenSecretName,omitempty"`

	// How many of the most recently started agent Pods that are now in completed / terminated state should be kept.
	// The controller automatically deletes all other terminated agent pods exceeding this count
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default:=5
	MaxTerminatedPodsToKeep *int32 `json:"maxTerminatedPodsToKeep,omitempty"`

	// If the controller detects a Pod with an idle AZP agent container, how long (after the first detection) should
	// the controller wait before it terminates the Pod? The controller only terminates it if the AZP agent
	// container was idle during the entire period.
	// Expects an unsigned duration string of decimal numbers each with optional
	// fraction and a unit suffix, eg "300ms", "1.5h" or "2h45m".
	// Valid time units are "ns", "us" (or "µs"), "ms", "s", "m", "h".
	// +kubebuilder:default:="5m"
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:validation:Type:=string
	// +optional
	AgentMinIdlePeriod *metav1.Duration `json:"agentMinIdlePeriod,omitempty"`

	// How often the garbage collection (=deleting offline Azure DevOps agents) is run.
	// Expects an unsigned duration string of decimal numbers each with optional
	// fraction and a unit suffix, eg "300ms", "1.5h" or "2h45m".
	// Valid time units are "ns", "us" (or "µs"), "ms", "s", "m", "h".
	// +kubebuilder:default:="30m"
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:validation:Type:=string
	// +optional
	DummyAgentGarbageCollectionInterval *metav1.Duration `json:"dummyAgentGarbageCollectionInterval,omitempty"`

	// "Dummy agents" refer to agents registered at run-time by the agent registrator CLI companion tool.
	// The agent garbage collection only affects dummy agents that have at least this age.
	// Expects an unsigned duration string of decimal numbers each with optional
	// fraction and a unit suffix, eg "300ms", "1.5h" or "2h45m".
	// Valid time units are "ns", "us" (or "µs"), "ms", "s", "m", "h".
	// +kubebuilder:default:="2h"
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:validation:Type:=string
	// +optional
	DummyAgentDeletionMinAge *metav1.Duration `json:"dummyAgentDeletionMinAge,omitempty"`

	// "Normal offline agents" refer to agents that were really running in the K8s cluster (some time ago), but were
	// not properly unregistered (e.g. because the Pod crashed).
	// The agent garbage collection only deletes normal offline agents that have at least this age.
	// Expects an unsigned duration string of decimal numbers each with optional
	// fraction and a unit suffix, eg "300ms", "1.5h" or "2h45m".
	// Valid time units are "ns", "us" (or "µs"), "ms", "s", "m", "h".
	// +kubebuilder:default:="5h"
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:validation:Type:=string
	// +optional
	NormalOfflineAgentDeletionMinAge *metav1.Duration `json:"normalOfflineAgentDeletionMinAge,omitempty"`

	// +optional
	ReusableCacheVolumes []ReusableCacheVolume `json:"reusableCacheVolumes,omitempty"`

	PodsWithCapabilities []PodsWithCapabilities `json:"podsWithCapabilities,omitempty"`
}

type ReusableCacheVolume struct {
	Name             string `json:"name,omitempty"`
	StorageClassName string `json:"storageClassName,omitempty"`
	RequestedStorage string `json:"requestedStorage,omitempty"`
}

type PodsWithCapabilities struct {
	Capabilities map[string]string `json:"capabilities,omitempty"`

	// +kubebuilder:validation:Minimum=0
	MinCount *int32 `json:"minCount,omitempty"`

	// +kubebuilder:validation:Minimum=0
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

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AutoScaledAgent is the Schema for the autoscaledagents API
type AutoScaledAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AutoScaledAgentSpec   `json:"spec,omitempty"`
	Status AutoScaledAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AutoScaledAgentList contains a list of AutoScaledAgent
type AutoScaledAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AutoScaledAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AutoScaledAgent{}, &AutoScaledAgentList{})
}
