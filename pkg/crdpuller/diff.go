package crdpuller

import (
	"encoding/json"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/tufin/oasdiff/diff"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func DiffSchemas(existing, new *apiextensionsv1.JSONSchemaProps) (*diff.SchemaDiff, error) {
	existingBytes, err := json.Marshal(existing)
	if err != nil {
		return nil, err
	}
	newBytes, err := json.Marshal(new)
	if err != nil {
		return nil, err
	}

	existingSchema := openapi3.NewSchema()
	if err = existingSchema.UnmarshalJSON(existingBytes); err != nil {
		return nil, err
	}
	newSchema := openapi3.NewSchema()
	if err = newSchema.UnmarshalJSON(newBytes); err != nil {
		return nil, err
	}
	diff, err := diff.Get(&diff.Config{
		ExcludeExamples: true,
		ExcludeDescription: true,
		IncludeExtensions: diff.StringSet {
			"x-kubernetes-preserve-unknown-fields": {},
			"x-kubernetes-embedded-resource": {},
			"x-kubernetes-int-or-string": {},
			"x-kubernetes-list-map-keys": {},
			"x-kubernetes-list-type": {},
			"x-kubernetes-map-type": {},
		},
	}, &openapi3.T{
		Info: &openapi3.Info{},
		Components: openapi3.Components{
			Schemas: openapi3.Schemas{
				"default": &openapi3.SchemaRef{
					Ref: "default",
					Value: existingSchema,
				},
			},
		},
	}, &openapi3.T{
		Info: &openapi3.Info{},
		Components: openapi3.Components{
			Schemas: openapi3.Schemas{
				"default": &openapi3.SchemaRef{
					Ref: "default",
					Value: newSchema,
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return diff.ComponentsDiff.SchemasDiff.Modified["default"] , nil
}
