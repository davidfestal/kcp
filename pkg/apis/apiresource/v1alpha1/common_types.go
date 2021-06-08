package v1alpha1

import (
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ColumnDefinition struct {
	metav1.TableColumnDefinition

	JSONPath *string `json:"jsonPath"`
}

type SubResource struct {
	Name string `json:"name"`
}

const (
	ScaleSubResourceName string = "scale"
	StatusSubResourceName string = "status"
)

// CommonAPIResourceSpec holds the common content of both NegociatedAPIResourceSpec
// and APIResourceImportSpec.
type CommonAPIResourceSpec struct {
	metav1.GroupVersion

	// Plural is the plural name of the resource to serve.  It must match the name of the CustomResourceDefinition-registration
	// too: plural.group and it must be all lowercase.
	Plural string `json:"plural"`
	// Singular is the singular name of the resource.  It must be all lowercase  Defaults to lowercased <kind>
	Singular string `json:"singular"`
	// ShortNames are short names for the resource.  It must be all lowercase.
	// +optional
	ShortNames []string `json:"shortNames,omitempty"`
	// Kind is the serialized kind of the resource.  It is normally CamelCase and singular.
	Kind string `json:"kind"`
	// ListKind is the serialized kind of the list for this resource.  Defaults to <kind>List.
	ListKind string `json:"listKind"`
	// Categories is a list of grouped resources custom resources belong to (e.g. 'all')
	// +optional
	Categories []string `json:"categories,omitempty"`

	// +required
	OpenAPIV3Schema *apiextensions.JSONSchemaProps `json:"openAPIV3Schema"`

	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	SubResources    []SubResource `json:"subResources,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	ColumnDefinitions    []ColumnDefinition `json:"columnDefinitions,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}
