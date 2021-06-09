package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NegociatedAPIResource describes the result of either the normalization of
// any number of imports of an API resource from external clusters (either physical or logical),
// or the the manual application of a CRD version for the corresponding GVR.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type NegociatedAPIResource struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec NegociatedAPIResourceSpec `json:"spec,omitempty"`

	// +optional
	Status NegociatedAPIResourceStatus `json:"status,omitempty"`
}

// NegociatedAPIResourceSpec holds the desired state of the NegociatedAPIResource (from the client).
type NegociatedAPIResourceSpec struct {
	CommonAPIResourceSpec `json:",inline"`
	Publish               bool `json:"publish,omitempty"`
}

// NegociatedAPIResourceConditionType is a valid value for NegociatedAPIResourceCondition.Type
type NegociatedAPIResourceConditionType string

const (
	// Submitted means that this negociated API Resource has been submitted
	// to the logical cluster as an applied CRD
	Submitted NegociatedAPIResourceConditionType = "Submitted"

	// Published means that this negociated API Resource has been published
	// to the logical cluster as an installed and accepted CRD
	// If the API Resource has been submitted
	// to the logical cluster as an applied CRD, but the CRD could not be published
	// correctly due to various reasons (non-structural schema, non-accepted names, ...)
	// then the Published condition will be false
	Published NegociatedAPIResourceConditionType = "Published"

	// Enforced means that a CRD version for the same GVR has been manually applied,
	// so that the current schema of the negociated api resource has been forcbly
	// replaced by the schema of the manually-applied CRD.
	// In such a condition, changes in `APIResourceImport`s would not, by any mean,
	// impact the negociated schema: no LCD will be used, and the schema comparison will only
	// serve to known whether the schema of an API Resource import would be compatible with the
	// enforced CRD schema, and flag the API Resource import (and possibly the corresponding cluster location)
	// accordingly.
	Enforced NegociatedAPIResourceConditionType = "Enforced"
)

// NegociatedAPIResourceCondition contains details for the current condition of this negociated api resource.
type NegociatedAPIResourceCondition struct {
	// Type is the type of the condition. Types include Submitted, Published, Refused and Enforced.
	Type NegociatedAPIResourceConditionType `json:"type"`
	// Status is the status of the condition.
	// Can be True, False, Unknown.
	Status metav1.ConditionStatus `json:"status"`
	// Last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// Unique, one-word, CamelCase reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Human-readable message indicating details about last transition.
	// +optional
	Message string `json:"message,omitempty"`
}

// NegociatedAPIResourceStatus communicates the observed state of the NegociatedAPIResource (from the controller).
type NegociatedAPIResourceStatus struct {
	Conditions []NegociatedAPIResourceCondition `json:"conditions,omitempty"`
}

// NegociatedAPIResourceList is a list of NegociatedAPIResource resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type NegociatedAPIResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []NegociatedAPIResource `json:"items"`
}
