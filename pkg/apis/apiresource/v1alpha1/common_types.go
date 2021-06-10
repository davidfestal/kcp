package v1alpha1

import (
	"encoding/json"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type ColumnDefinition struct {
	metav1.TableColumnDefinition `json:",inline"`

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
	metav1.GroupVersion `json:",inline"`

	Scope apiextensionsv1.ResourceScope `json:"scope"`

	apiextensionsv1.CustomResourceDefinitionNames `json:",inline"`

	// +required
	// +kubebuilder:pruning:PreserveUnknownFields
	OpenAPIV3Schema runtime.RawExtension `json:"openAPIV3Schema"`

	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	SubResources    []SubResource `json:"subResources,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	ColumnDefinitions    []ColumnDefinition `json:"columnDefinitions,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

func (spec *CommonAPIResourceSpec) GetSchema() (*apiextensionsv1.JSONSchemaProps, error) {
	s := &apiextensionsv1.JSONSchemaProps{}
	if err := json.Unmarshal(spec.OpenAPIV3Schema.Raw, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (spec *CommonAPIResourceSpec) SetSchema(s *apiextensionsv1.JSONSchemaProps) error {
	bytes, err := json.Marshal(s)
	if err != nil {
		return err
	}
	spec.OpenAPIV3Schema.Raw = bytes
	return nil
}
