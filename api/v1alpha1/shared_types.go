// Selector selects a resource across namespaces
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/types"
)

type Selector struct {
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// String returns the general purpose string representation
func (s Selector) String() string {
	return s.Namespace + "/" + s.Name
}

// NamespacedName returns a types.NamespacedName
func (s Selector) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Name:      s.Name,
		Namespace: s.Namespace,
	}
}
