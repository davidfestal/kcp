/*
Copyright 2017 The Kubernetes Authors.

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

package apiserver

import (
	"fmt"
	"path"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	schemaobjectmeta "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/objectmeta"

	structuralpruning "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource/tableconvertor"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/apimachinery/pkg/runtime/serializer/versioning"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/endpoints/handlers/fieldmanager"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/apiserver/pkg/features"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	utilopenapi "k8s.io/apiserver/pkg/util/openapi"
	klog "k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apidefs "github.com/kcp-dev/kcp/pkg/virtual/syncer/dynamic/apidefs"
)

var _ apidefs.APIDefinition = (*servingInfo)(nil)

// servingInfo stores enough information to serve the storage for the apiResourceSpec
type servingInfo struct {
	logicalClusterName string
	apiResourceSpec    *apiresourcev1alpha1.CommonAPIResourceSpec

	storage       rest.Storage
	statusStorage rest.Storage

	requestScope       *handlers.RequestScope
	statusRequestScope *handlers.RequestScope
}

// Implement APIDefinition interface

func (apiDef *servingInfo) GetAPIResourceSpec() *apiresourcev1alpha1.CommonAPIResourceSpec {
	return apiDef.apiResourceSpec
}
func (apiDef *servingInfo) GetClusterName() string {
	return apiDef.logicalClusterName
}
func (apiDef *servingInfo) GetStorage() rest.Storage {
	return apiDef.storage
}
func (apiDef *servingInfo) GetSubResourceStorage(subresource string) rest.Storage {
	if subresource == "status" {
		return apiDef.statusStorage
	}
	return nil
}
func (apiDef *servingInfo) GetRequestScope() *handlers.RequestScope {
	return apiDef.requestScope
}
func (apiDef *servingInfo) GetSubResourceRequestScope(subresource string) *handlers.RequestScope {
	if subresource == "status" {
		return apiDef.statusRequestScope
	}
	return nil
}

type RestProviderFunc func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator *validate.SchemaValidator, subresourcesSchemaValidator map[string]*validate.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage)

func CreateServingInfoFor(genericConfig genericapiserver.CompletedConfig, logicalClusterName string, apiResourceSpec *apiresourcev1alpha1.CommonAPIResourceSpec, restProvider RestProviderFunc) (apidefs.APIDefinition, error) {
	equivalentResourceRegistry := runtime.NewEquivalentResourceRegistry()

	v1OpenAPISchema, err := apiResourceSpec.GetSchema()
	if err != nil {
		return nil, err
	}
	internalSchema := &apiextensionsinternal.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v1OpenAPISchema, internalSchema, nil); err != nil {
		return nil, fmt.Errorf("failed converting CRD validation to internal version: %w", err)
	}
	structuralSchema, err := structuralschema.NewStructural(internalSchema)
	if err != nil {
		// This should never happen. If it does, it is a programming error.
		utilruntime.HandleError(fmt.Errorf("failed to convert schema to structural: %w", err))
		return nil, fmt.Errorf("the server could not properly serve the CR schema") // validation should avoid this
	}

	// we don't own structuralSchema completely, e.g. defaults are not deep-copied. So better make a copy here.
	structuralSchema = structuralSchema.DeepCopy()

	if err := structuraldefaulting.PruneDefaults(structuralSchema); err != nil {
		// This should never happen. If it does, it is a programming error.
		utilruntime.HandleError(fmt.Errorf("failed to prune defaults: %w", err))
		return nil, fmt.Errorf("the server could not properly serve the CR schema") // validation should avoid this
	}

	resource := schema.GroupVersionResource{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version, Resource: apiResourceSpec.Plural}
	kind := schema.GroupVersionKind{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version, Kind: apiResourceSpec.Kind}
	listKind := schema.GroupVersionKind{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version, Kind: apiResourceSpec.ListKind}

	s, err := BuildOpenAPIV2(apiResourceSpec, builder.Options{V2: true, SkipFilterSchemaForKubectlOpenAPIV2Validation: true, StripValueValidation: true, StripNullable: true, AllowNonStructural: false})
	if err != nil {
		return nil, err
	}
	openAPIModels, err := utilopenapi.ToProtoModels(s)

	var modelsByGKV openapi.ModelsByGKV

	if err != nil {
		utilruntime.HandleError(fmt.Errorf("error building openapi models for %s: %w", kind.String(), err))
		openAPIModels = nil
	} else {
		modelsByGKV, err = openapi.GetModelsByGKV(openAPIModels)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("error gathering openapi models by GKV for %s: %w", kind.String(), err))
			modelsByGKV = nil
		}
	}
	var typeConverter fieldmanager.TypeConverter = fieldmanager.DeducedTypeConverter{}
	if openAPIModels != nil {
		typeConverter, err = fieldmanager.NewTypeConverter(openAPIModels, false)
		if err != nil {
			return nil, err
		}
	}

	safeConverter, unsafeConverter := &nopConverter{}, &nopConverter{}
	if err != nil {
		return nil, err
	}

	// In addition to Unstructured objects (Custom Resources), we also may sometimes need to
	// decode unversioned Options objects, so we delegate to parameterScheme for such types.
	parameterScheme := runtime.NewScheme()
	parameterScheme.AddUnversionedTypes(schema.GroupVersion{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version},
		&metav1.ListOptions{},
		&metav1.GetOptions{},
		&metav1.DeleteOptions{},
	)
	parameterCodec := runtime.NewParameterCodec(parameterScheme)

	equivalentResourceRegistry.RegisterKindFor(resource, "", kind)

	typer := apiextensionsapiserver.NewUnstructuredObjectTyper(parameterScheme)
	creator := apiextensionsapiserver.UnstructuredCreator{}

	internalValidationSchema := &apiextensionsinternal.CustomResourceValidation{
		OpenAPIV3Schema: internalSchema,
	}
	validator, _, err := apiservervalidation.NewSchemaValidator(internalValidationSchema)
	if err != nil {
		return nil, err
	}

	subResourcesValidators := map[string]*validate.SchemaValidator{}

	if subresources := apiResourceSpec.SubResources; subresources != nil && subresources.Contains("status") {
		var statusValidator *validate.SchemaValidator
		equivalentResourceRegistry.RegisterKindFor(resource, "status", kind)
		// for the status subresource, validate only against the status schema
		if internalValidationSchema != nil && internalValidationSchema.OpenAPIV3Schema != nil && internalValidationSchema.OpenAPIV3Schema.Properties != nil {
			if statusSchema, ok := internalValidationSchema.OpenAPIV3Schema.Properties["status"]; ok {
				openapiSchema := &spec.Schema{}
				if err := apiservervalidation.ConvertJSONSchemaPropsWithPostProcess(&statusSchema, openapiSchema, apiservervalidation.StripUnsupportedFormatsPostProcess); err != nil {
					return nil, err
				}
				statusValidator = validate.NewSchemaValidator(openapiSchema, nil, "", strfmt.Default)
			}
		}
		subResourcesValidators["status"] = statusValidator
	}

	table, err := tableconvertor.New(apiResourceSpec.ColumnDefinitions.ToCustomResourceColumnDefinitions())
	if err != nil {
		klog.V(2).Infof("The CRD for %v has an invalid printer specification, falling back to default printing: %v", kind, err)
	}

	storage, subresourceStorages := restProvider(
		resource,
		kind,
		listKind,
		typer,
		table,
		apiResourceSpec.Scope == apiextensionsv1.NamespaceScoped,
		validator,
		subResourcesValidators,
		structuralSchema,
	)

	selfLinkPrefixPrefix := path.Join("apis", apiResourceSpec.GroupVersion.Group, apiResourceSpec.GroupVersion.Version)
	if apiResourceSpec.GroupVersion.Group == "" {
		selfLinkPrefixPrefix = path.Join("api", apiResourceSpec.GroupVersion.Version)
	}
	selfLinkPrefix := ""
	switch apiResourceSpec.Scope {
	case apiextensionsv1.ClusterScoped:
		selfLinkPrefix = "/" + selfLinkPrefixPrefix + "/" + apiResourceSpec.Plural + "/"
	case apiextensionsv1.NamespaceScoped:
		selfLinkPrefix = "/" + selfLinkPrefixPrefix + "/namespaces/"
	}

	clusterScoped := apiResourceSpec.Scope == apiextensionsv1.ClusterScoped

	// CRDs explicitly do not support protobuf, but some objects returned by the API server do
	negotiatedSerializer := unstructuredNegotiatedSerializer{
		typer:                 typer,
		creator:               creator,
		converter:             safeConverter,
		structuralSchema:      structuralSchema,
		structuralSchemaGVK:   kind,
		preserveUnknownFields: false,
	}
	var standardSerializers []runtime.SerializerInfo
	for _, s := range negotiatedSerializer.SupportedMediaTypes() {
		if s.MediaType == runtime.ContentTypeProtobuf {
			continue
		}
		standardSerializers = append(standardSerializers, s)
	}

	requestScope := &handlers.RequestScope{
		Namer: handlers.ContextBasedNaming{
			SelfLinker:         meta.NewAccessor(),
			ClusterScoped:      clusterScoped,
			SelfLinkPathPrefix: selfLinkPrefix,
		},
		Serializer:               negotiatedSerializer,
		ParameterCodec:           parameterCodec,
		StandardSerializers:      standardSerializers,
		Creater:                  creator,
		Convertor:                safeConverter,
		Defaulter:                unstructuredDefaulter{parameterScheme, structuralSchema, kind},
		Typer:                    typer,
		UnsafeConvertor:          unsafeConverter,
		EquivalentResourceMapper: equivalentResourceRegistry,
		Resource:                 resource,
		Kind:                     kind,
		HubGroupVersion:          kind.GroupVersion(),
		MetaGroupVersion:         metav1.SchemeGroupVersion,
		TableConvertor:           table,
		Authorizer:               genericConfig.Authorization.Authorizer,
		MaxRequestBodyBytes:      genericConfig.MaxRequestBodyBytes,
		OpenapiModels:            modelsByGKV,
	}

	if utilfeature.DefaultFeatureGate.Enabled(features.ServerSideApply) {
		if withResetFields, canGetResetFields := storage.(rest.ResetFieldsStrategy); canGetResetFields {
			resetFields := withResetFields.GetResetFields()
			reqScope := *requestScope
			reqScope, err = scopeWithFieldManager(
				typeConverter,
				reqScope,
				resetFields,
				"",
			)
			if err != nil {
				return nil, err
			}
			requestScope = &reqScope
		} else {
			return nil, fmt.Errorf("storage for resource %q should define GetResetFields", kind.String())
		}
	}

	var statusScope handlers.RequestScope
	statusStorage, statusEnabled := subresourceStorages["status"]
	if statusEnabled {
		// shallow copy
		statusScope := *requestScope
		statusScope.Subresource = "status"
		statusScope.Namer = handlers.ContextBasedNaming{
			SelfLinker:         meta.NewAccessor(),
			ClusterScoped:      clusterScoped,
			SelfLinkPathPrefix: selfLinkPrefix,
			SelfLinkPathSuffix: "/status",
		}

		if utilfeature.DefaultFeatureGate.Enabled(features.ServerSideApply) {
			if withResetFields, canGetResetFields := statusStorage.(rest.ResetFieldsStrategy); canGetResetFields {
				resetFields := withResetFields.GetResetFields()
				statusScope, err = scopeWithFieldManager(
					typeConverter,
					statusScope,
					resetFields,
					"status",
				)
				if err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("storage for resource %q status should define GetResetFields", kind.String())
			}
		}
	}

	ret := &servingInfo{
		logicalClusterName: logicalClusterName,
		apiResourceSpec:    apiResourceSpec,
		storage:            storage,
		statusStorage:      statusStorage,
		requestScope:       requestScope,
		statusRequestScope: &statusScope,
	}

	return ret, nil
}

func scopeWithFieldManager(typeConverter fieldmanager.TypeConverter, reqScope handlers.RequestScope, resetFields map[fieldpath.APIVersion]*fieldpath.Set, subresource string) (handlers.RequestScope, error) {
	fieldManager, err := fieldmanager.NewDefaultCRDFieldManager(
		typeConverter,
		reqScope.Convertor,
		reqScope.Defaulter,
		reqScope.Creater,
		reqScope.Kind,
		reqScope.HubGroupVersion,
		subresource,
		resetFields,
	)
	if err != nil {
		return handlers.RequestScope{}, err
	}
	reqScope.FieldManager = fieldManager
	return reqScope, nil
}

var _ runtime.ObjectConvertor = nopConverter{}

type nopConverter struct{}

func (u nopConverter) Convert(in, out, context interface{}) error {
	sv, err := conversion.EnforcePtr(in)
	if err != nil {
		return err
	}
	dv, err := conversion.EnforcePtr(out)
	if err != nil {
		return err
	}
	dv.Set(sv)
	return nil
}
func (u nopConverter) ConvertToVersion(in runtime.Object, gv runtime.GroupVersioner) (out runtime.Object, err error) {
	return in, nil
}
func (u nopConverter) ConvertFieldLabel(gvk schema.GroupVersionKind, label, value string) (string, string, error) {
	return label, value, nil
}

// unstructuredNegotiatedSerializer provides the same logic as the unstructuredNegotiatedSerializer
// in the apiextensions-apiserver/apiserver package, apart from the fact that it is for a single version.
// This could be replaced by calling the upstream one (once made public) with just a single version in the map.
type unstructuredNegotiatedSerializer struct {
	typer     runtime.ObjectTyper
	creator   runtime.ObjectCreater
	converter runtime.ObjectConvertor

	structuralSchema      *structuralschema.Structural
	structuralSchemaGVK   schema.GroupVersionKind
	preserveUnknownFields bool
}

func (s unstructuredNegotiatedSerializer) SupportedMediaTypes() []runtime.SerializerInfo {
	return []runtime.SerializerInfo{
		{
			MediaType:        "application/json",
			MediaTypeType:    "application",
			MediaTypeSubType: "json",
			EncodesAsText:    true,
			Serializer:       json.NewSerializer(json.DefaultMetaFactory, s.creator, s.typer, false),
			PrettySerializer: json.NewSerializer(json.DefaultMetaFactory, s.creator, s.typer, true),
			StrictSerializer: json.NewSerializerWithOptions(json.DefaultMetaFactory, s.creator, s.typer, json.SerializerOptions{
				Strict: true,
			}),
			StreamSerializer: &runtime.StreamSerializerInfo{
				EncodesAsText: true,
				Serializer:    json.NewSerializer(json.DefaultMetaFactory, s.creator, s.typer, false),
				Framer:        json.Framer,
			},
		},
		{
			MediaType:        "application/yaml",
			MediaTypeType:    "application",
			MediaTypeSubType: "yaml",
			EncodesAsText:    true,
			Serializer:       json.NewYAMLSerializer(json.DefaultMetaFactory, s.creator, s.typer),
			StrictSerializer: json.NewSerializerWithOptions(json.DefaultMetaFactory, s.creator, s.typer, json.SerializerOptions{
				Yaml:   true,
				Strict: true,
			}),
		},
		{
			MediaType:        "application/vnd.kubernetes.protobuf",
			MediaTypeType:    "application",
			MediaTypeSubType: "vnd.kubernetes.protobuf",
			Serializer:       protobuf.NewSerializer(s.creator, s.typer),
			StreamSerializer: &runtime.StreamSerializerInfo{
				Serializer: protobuf.NewRawSerializer(s.creator, s.typer),
				Framer:     protobuf.LengthDelimitedFramer,
			},
		},
	}
}
func (s unstructuredNegotiatedSerializer) EncoderForVersion(encoder runtime.Encoder, gv runtime.GroupVersioner) runtime.Encoder {
	return versioning.NewCodec(encoder, nil, s.converter, Scheme, Scheme, Scheme, gv, nil, "crdNegotiatedSerializer")
}
func (s unstructuredNegotiatedSerializer) DecoderToVersion(decoder runtime.Decoder, gv runtime.GroupVersioner) runtime.Decoder {
	returnUnknownFieldPaths := false
	if serializer, ok := decoder.(*json.Serializer); ok {
		returnUnknownFieldPaths = serializer.IsStrict()
	}
	d := schemaCoercingDecoder{delegate: decoder, validator: unstructuredSchemaCoercer{structuralSchema: s.structuralSchema, structuralSchemaGVK: s.structuralSchemaGVK, preserveUnknownFields: s.preserveUnknownFields, returnUnknownFieldPaths: returnUnknownFieldPaths}}
	return versioning.NewCodec(nil, d, runtime.UnsafeObjectConvertor(Scheme), Scheme, Scheme, unstructuredDefaulter{
		delegate:            Scheme,
		structuralSchema:    s.structuralSchema,
		structuralSchemaGVK: s.structuralSchemaGVK,
	}, nil, gv, "unstructuredNegotiatedSerializer")
}

// unstructuredDefaulter provides the same logic as the unstructuredDefaulter
// in the apiextensions-apiserver/apiserver package, apart from the fact that it is for a single version.
// This could be replaced by calling the upstream one (once made public) with just a single version in the map.
type unstructuredDefaulter struct {
	delegate            runtime.ObjectDefaulter
	structuralSchema    *structuralschema.Structural
	structuralSchemaGVK schema.GroupVersionKind
}

func (d unstructuredDefaulter) Default(in runtime.Object) {
	// Delegate for things other than Unstructured, and other GKs
	u, ok := in.(runtime.Unstructured)
	if !ok || u.GetObjectKind().GroupVersionKind() != d.structuralSchemaGVK {
		d.delegate.Default(in)
		return
	}

	structuraldefaulting.Default(u.UnstructuredContent(), d.structuralSchema)
}

// schemaCoercingDecoder is the copy of the schemaCoercingDecoder
// in the apiextensions-apiserver/apiserver package
// This could be replaced by calling the upstream one, once made public
type schemaCoercingDecoder struct {
	delegate  runtime.Decoder
	validator unstructuredSchemaCoercer
}

var _ runtime.Decoder = schemaCoercingDecoder{}

func (d schemaCoercingDecoder) Decode(data []byte, defaults *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	var decodingStrictErrs []error
	obj, gvk, err := d.delegate.Decode(data, defaults, into)
	if err != nil {
		decodeStrictErr, ok := runtime.AsStrictDecodingError(err)
		if !ok || obj == nil {
			return nil, gvk, err
		}
		decodingStrictErrs = decodeStrictErr.Errors()
	}
	var unknownFields []string
	if u, ok := obj.(*unstructured.Unstructured); ok {
		unknownFields, err = d.validator.apply(u)
		if err != nil {
			return nil, gvk, err
		}
	}
	if d.validator.returnUnknownFieldPaths && (len(decodingStrictErrs) > 0 || len(unknownFields) > 0) {
		for _, unknownField := range unknownFields {
			decodingStrictErrs = append(decodingStrictErrs, fmt.Errorf(`unknown field "%s"`, unknownField))
		}
		return obj, gvk, runtime.NewStrictDecodingError(decodingStrictErrs)
	}

	return obj, gvk, nil
}

// unstructuredSchemaCoercer provides the same logic as the unstructuredSchemaCoercer
// in the apiextensions-apiserver/apiserver package, apart from the fact that it is for a single version.
// This could be replaced by calling the upstream one (once made public) with just a single version in the map.
type unstructuredSchemaCoercer struct {
	dropInvalidMetadata bool
	repairGeneration    bool

	structuralSchema        *structuralschema.Structural
	structuralSchemaGVK     schema.GroupVersionKind
	preserveUnknownFields   bool
	returnUnknownFieldPaths bool
}

func (v *unstructuredSchemaCoercer) apply(u *unstructured.Unstructured) (unknownFieldPaths []string, err error) {
	// save implicit meta fields that don't have to be specified in the validation spec
	kind, foundKind, err := unstructured.NestedString(u.UnstructuredContent(), "kind")
	if err != nil {
		return nil, err
	}
	apiVersion, foundApiVersion, err := unstructured.NestedString(u.UnstructuredContent(), "apiVersion")
	if err != nil {
		return nil, err
	}
	objectMeta, foundObjectMeta, err := schemaobjectmeta.GetObjectMeta(u.Object, v.dropInvalidMetadata)
	if err != nil {
		return nil, err
	}

	// compare group and kind because also other object like DeleteCollection options pass through here
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, err
	}

	if gv.Group == v.structuralSchemaGVK.Group && kind == v.structuralSchemaGVK.Kind && gv.Version == v.structuralSchemaGVK.Version {
		if !v.preserveUnknownFields {
			// TODO: switch over pruning and coercing at the root to schemaobjectmeta.Coerce too
			pruneOpts := structuralpruning.PruneOptions{}
			if v.returnUnknownFieldPaths {
				pruneOpts.ReturnPruned = true
			}
			unknownFieldPaths = structuralpruning.PruneWithOptions(u.Object, v.structuralSchema, false, pruneOpts)
			structuraldefaulting.PruneNonNullableNullsWithoutDefaults(u.Object, v.structuralSchema)
		}

		if err := schemaobjectmeta.Coerce(nil, u.Object, v.structuralSchema, false, v.dropInvalidMetadata); err != nil {
			return nil, err
		}
		// fixup missing generation in very old CRs
		if v.repairGeneration && objectMeta.Generation == 0 {
			objectMeta.Generation = 1
		}
	}

	// restore meta fields, starting clean
	if foundKind {
		u.SetKind(kind)
	}
	if foundApiVersion {
		u.SetAPIVersion(apiVersion)
	}
	if foundObjectMeta {
		if err := schemaobjectmeta.SetObjectMeta(u.Object, objectMeta); err != nil {
			return nil, err
		}
	}

	return unknownFieldPaths, nil
}

// BuildOpenAPIV2 builds OpenAPI v2 for the given apiResourceSpec
func BuildOpenAPIV2(apiResourceSpec *apiresourcev1alpha1.CommonAPIResourceSpec, opts builder.Options) (*spec.Swagger, error) {
	version := apiResourceSpec.GroupVersion.Version
	schema, err := apiResourceSpec.GetSchema()
	if err != nil {
		return nil, err
	}
	var subResources apiextensionsv1.CustomResourceSubresources
	for _, subResource := range apiResourceSpec.SubResources {
		if subResource.Name == "scale" {
			subResources.Scale = &apiextensionsv1.CustomResourceSubresourceScale{
				SpecReplicasPath:   ".spec.replicas",
				StatusReplicasPath: ".status.replicas",
			}
		}
		if subResource.Name == "status" {
			subResources.Status = &apiextensionsv1.CustomResourceSubresourceStatus{}
		}
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: apiResourceSpec.GroupVersion.Group,
			Names: apiResourceSpec.CustomResourceDefinitionNames,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name: version,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: schema,
					},
					Subresources: &subResources,
				},
			},
			Scope: apiResourceSpec.Scope,
		},
	}
	return builder.BuildOpenAPIV2(crd, version, opts)
}
