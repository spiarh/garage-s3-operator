/*
Copyright 2025 spiarh.

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GarageClusterSpec defines the desired state of GarageCluster
type GarageClusterSpec struct {
	Endpoint            Endpoint                  `json:"endpoint"`
	AdminTokenSecretRef *corev1.SecretKeySelector `json:"adminTokenSecretRef"`
}

type Endpoint struct {
	URL                   string     `json:"url"`
	InsecureSkipTLSVerify bool       `json:"insecureSkipTLSVerify,omitempty"`
	CA                    EndpointCA `json:"endpointCA,omitempty"`
}

type EndpointCA struct {
	SecretRef    *corev1.SecretKeySelector    `json:"secretRef,omitempty"`
	ConfigMapRef *corev1.ConfigMapKeySelector `json:"configMapRef,omitempty"`
}

// GarageClusterStatus defines the observed state of GarageCluster.
type GarageClusterStatus struct {
	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the GarageCluster resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// GarageCluster is the Schema for the clusters API
type GarageCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of GarageCluster
	// +required
	Spec GarageClusterSpec `json:"spec"`

	// status defines the observed state of GarageCluster
	// +optional
	Status GarageClusterStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// GarageClusterList contains a list of GarageCluster
type GarageClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GarageCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GarageCluster{}, &GarageClusterList{})
}
