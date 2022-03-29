/*
Copyright 2022 The KCP Authors.

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

package builder

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	endpointsopenapi "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/util/openapi"
	builder "k8s.io/kube-openapi/pkg/builder"
	"k8s.io/kube-openapi/pkg/util"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
)

var internalAPIs []*apiresourcev1alpha1.CommonAPIResourceSpec

type internalTypeDef struct {
	names    apiextensionsv1.CustomResourceDefinitionNames
	instance runtime.Object
	scope    apiextensionsv1.ResourceScope
}

func init() {
	defs := []internalTypeDef{
		{
			names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "namespaces",
				Singular: "namespace",
				Kind:     "Namespace",
			},
			instance: &corev1.Namespace{},
			scope:    apiextensionsv1.ClusterScoped,
		},
		{
			names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "configmaps",
				Singular: "configmap",
				Kind:     "ConfigMap",
			},
			instance: &corev1.ConfigMap{},
			scope:    apiextensionsv1.NamespaceScoped,
		},
		{
			names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "secrets",
				Singular: "secrets",
				Kind:     "Secret",
			},
			instance: &corev1.Secret{},
			scope:    apiextensionsv1.NamespaceScoped,
		},
		{
			names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "serviceaccounts",
				Singular: "serviceaccount",
				Kind:     "ServiceAccount",
			},
			instance: &corev1.ServiceAccount{},
			scope:    apiextensionsv1.NamespaceScoped,
		},
	}
	config := genericapiserver.DefaultOpenAPIConfig(generatedopenapi.GetOpenAPIDefinitions, endpointsopenapi.NewDefinitionNamer(legacyscheme.Scheme))
	var canonicalTypeNames []string
	for _, def := range defs {
		canonicalTypeNames = append(canonicalTypeNames, util.GetCanonicalTypeName(def.instance))
	}
	swagger, err := builder.BuildOpenAPIDefinitionsForResources(config, canonicalTypeNames...)
	if err != nil {
		panic(err)
	}
	swagger.Info.Version = "1.0"
	models, err := openapi.ToProtoModels(swagger)
	if err != nil {
		panic(err)
	}

	modelsByGKV, err := endpointsopenapi.GetModelsByGKV(models)
	if err != nil {
		panic(err)
	}

	gv := schema.GroupVersion{Group: "", Version: "v1"}
	for _, def := range defs {
		gvk := gv.WithKind(def.names.Kind)
		var schemaProps apiextensionsv1.JSONSchemaProps
		errs := crdpuller.Convert(modelsByGKV[gvk], &schemaProps)
		if len(errs) > 0 {
			panic(errs)
		}
		spec := &apiresourcev1alpha1.CommonAPIResourceSpec{
			GroupVersion:                  apiresourcev1alpha1.GroupVersion(gvk.GroupVersion()),
			Scope:                         def.scope,
			CustomResourceDefinitionNames: def.names,
		}
		if err := spec.SetSchema(&schemaProps); err != nil {
			panic(err)
		}

		internalAPIs = append(internalAPIs, spec)
	}
}
