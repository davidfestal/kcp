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

type ColumnDefinitions []ColumnDefinition

func (cd *ColumnDefinitions) ImportFromCRDVersion(crdVersion *apiextensionsv1.CustomResourceDefinitionVersion) *ColumnDefinitions {	
	alreadyExists := func(name string) bool {
		for _, colDef := range *cd {
			if colDef.Name == name {
				return true
			}
		}
		return false
	}

	for _, apc := range crdVersion.AdditionalPrinterColumns {
		if ! alreadyExists(apc.Name){
			jsonPath := apc.JSONPath
			*cd = append(*cd, ColumnDefinition{
				TableColumnDefinition: metav1.TableColumnDefinition{
					Name: apc.Name,
					Type: apc.Type,
					Format: apc.Format,
					Description: apc.Description,
					Priority: apc.Priority,
				},
				JSONPath: &jsonPath,
			})
		}
	}
	return cd 
}

type SubResource struct {
	Name string `json:"name"`
}

const (
	ScaleSubResourceName string = "scale"
	StatusSubResourceName string = "status"
)

type SubResources []SubResource

func (sr *SubResources) ImportFromCRDVersion(crdVersion *apiextensionsv1.CustomResourceDefinitionVersion) *SubResources {	
	alreadyExists := func(name string) bool {
		for _, subResource := range *sr {
			if subResource.Name == name {
				return true
			}
		}
		return false
	}

	if crdVersion.Subresources.Scale != nil {
		if ! alreadyExists(ScaleSubResourceName){
			*sr = append(*sr, SubResource{
				Name: ScaleSubResourceName,
			})
		}
	}
	if crdVersion.Subresources.Status != nil {
		if ! alreadyExists(StatusSubResourceName){
			*sr = append(*sr, SubResource{
				Name: StatusSubResourceName,
			})
		}
	}
	return sr 
}

type GroupVersion struct {
	Group   string `json:"group"`
	Version string `json:"version"`
}

func (v GroupVersion) String() string {
	group := v.Group
	if group == "core" {
		group = ""
	}
	return metav1.GroupVersion {
		Group: v.Group,
		Version: v.Version,
	}.String()
}

// CommonAPIResourceSpec holds the common content of both NegociatedAPIResourceSpec
// and APIResourceImportSpec.
type CommonAPIResourceSpec struct {
	GroupVersion GroupVersion `json:"groupVersion"`

	Scope apiextensionsv1.ResourceScope `json:"scope"`

	apiextensionsv1.CustomResourceDefinitionNames `json:",inline"`

	// +required
	// +kubebuilder:pruning:PreserveUnknownFields
	OpenAPIV3Schema runtime.RawExtension `json:"openAPIV3Schema"`

	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	SubResources    SubResources `json:"subResources,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	ColumnDefinitions    ColumnDefinitions `json:"columnDefinitions,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
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
