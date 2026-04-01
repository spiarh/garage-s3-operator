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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GarageAccessKeySpec defines the desired state of GarageAccessKey
type GarageAccessKeySpec struct {
	ClusterRef *Selector `json:"clusterRef"`
	// +optional
	OverrideName string `json:"overrideName,omitempty"`
	// +optional
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`
	// +optional
	Secret *AccessKeySecret `json:"secret,omitempty"`
}

type AccessKeySecret struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Format Format `json:"format,omitempty"`
}

type Format string

const (
	DefaultFormat   Format = "Default"
	AWSConfigFormat Format = "AWSConfig"
)

// GarageAccessKeyStatus defines the observed state of Key.
type GarageAccessKeyStatus struct {
	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties
	// +optional
	ID string `json:"id,omitempty"`

	// conditions represent the current state of the Key resource.
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

// GarageAccessKey is the Schema for the keys API
type GarageAccessKey struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of Key
	// +required
	Spec GarageAccessKeySpec `json:"spec"`

	// status defines the observed state of Key
	// +optional
	Status GarageAccessKeyStatus `json:"status,omitempty,omitzero"`
}

// TargetName returns the target name of the key in consideration of the override name
func (k GarageAccessKey) TargetName() string {
	var name string
	if k.Spec.OverrideName != "" {
		name = k.Spec.OverrideName
	} else {
		name = k.Name
	}

	return fmt.Sprintf("%s/%s_%s", k.Namespace, name, k.UID)
}

// Selector returns a Selector for the key
func (k GarageAccessKey) Selector() *Selector {
	return &Selector{
		Name:      k.Name,
		Namespace: k.Namespace,
	}
}

// +kubebuilder:object:root=true

// GarageAccessKeyList contains a list of Key
type GarageAccessKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GarageAccessKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GarageAccessKey{}, &GarageAccessKeyList{})
}
