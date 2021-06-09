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

	// plural is the plural name of the resource to serve.
	// The custom resources are served under `/apis/<group>/<version>/.../<plural>`.
	// Must match the name of the CustomResourceDefinition (in the form `<names.plural>.<group>`).
	// Must be all lowercase.
	Plural string `json:"plural"`
	// singular is the singular name of the resource. It must be all lowercase. Defaults to lowercased `kind`.
	Singular string `json:"singular"`
	// shortNames are short names for the resource, exposed in API discovery documents,
	// and used by clients to support invocations like `kubectl get <shortname>`.
	// It must be all lowercase.
	// +optional
	ShortNames []string `json:"shortNames,omitempty"`
	// kind is the serialized kind of the resource. It is normally CamelCase and singular.
	// Custom resource instances will use this value as the `kind` attribute in API calls.
	Kind string `json:"kind"`
	// listKind is the serialized kind of the list for this resource. Defaults to "`kind`List".
	ListKind string `json:"listKind"`
	// categories is a list of grouped resources this custom resource belongs to (e.g. 'all').
	// This is published in API discovery documents, and used by clients to support invocations like
	// `kubectl get all`.
	// +optional
	Categories []string `json:"categories,omitempty"`

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
