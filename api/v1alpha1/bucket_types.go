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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BucketSpec defines the desired state of Bucket
type BucketSpec struct {
	ClusterRef *Selector `json:"clusterRef"`
	// +optional
	OverrideName string `json:"overrideName,omitempty"`
	// +optional
	BucketAccessKey *BucketAccessKey `json:"bucketAccessKey,omitempty"`
	// +optional
	Quotas *BucketQuotas `json:"quotas,omitempty"`
	// +optional
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

type ReclaimPolicy string

const (
	Retain ReclaimPolicy = "Retain"
	Delete ReclaimPolicy = "Delete"
)

type BucketAccessKey struct {
	Enabled bool `json:"enabled"`
	// +optional
	Permissions *Permissions `json:"permissions,omitempty"`
	// +optional
	Format Format `json:"format,omitempty"`
}

type BucketQuotas struct {
	// +optional
	MaxObjects int64 `json:"maxObjects,omitempty"`
	// +optional
	MaxSize int64 `json:"maxSize,omitempty"`
}

// GarageBucketStatus defines the observed state of Bucket.
type GarageBucketStatus struct {
	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties
	// +optional
	ID string `json:"id,omitempty"`

	// conditions represent the current state of the Bucket resource.
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

// GarageBucket is the Schema for the buckets API
type GarageBucket struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of Bucket
	// +required
	Spec BucketSpec `json:"spec"`

	// status defines the observed state of Bucket
	// +optional
	Status GarageBucketStatus `json:"status,omitempty,omitzero"`
}

// TargetName returns the target name of the bucket in consideration of the override name
func (b GarageBucket) TargetName() string {
	if b.Spec.OverrideName != "" {
		return b.Spec.OverrideName
	}
	return b.Name
}

// Selector returns a Selector for the Bucket
func (b GarageBucket) Selector() *Selector {
	return &Selector{
		Name:      b.Name,
		Namespace: b.Namespace,
	}
}

// +kubebuilder:object:root=true

// GarageBucketList contains a list of GarageBucket
type GarageBucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GarageBucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GarageBucket{}, &GarageBucketList{})
}
