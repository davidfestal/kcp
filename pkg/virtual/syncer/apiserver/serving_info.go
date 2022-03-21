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
	"strings"
	"time"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	schemaobjectmeta "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/objectmeta"

	structuralpruning "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/crdserverscheme"
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
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	flowcontrolrequest "k8s.io/apiserver/pkg/util/flowcontrol/request"
	klog "k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	syncer "github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/registry"
)

// servingInfo stores enough information to serve the storage for the apiResourceSpec
type servingInfo struct {
	logicalClusterName string
	apiResourceSpec    *apiresourcev1alpha1.CommonAPIResourceSpec

	storage       rest.Storage
	statusStorage rest.Storage

	requestScope *handlers.RequestScope

	statusRequestScope *handlers.RequestScope
}

var _ syncer.APIDefinition = (*servingInfo)(nil)

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

func CreateServingInfoFor(genericConfig genericapiserver.CompletedConfig, logicalClusterName string, apiResourceSpec *apiresourcev1alpha1.CommonAPIResourceSpec, dynamicClientGetter syncer.ClientGetter) (*servingInfo, error) {
	equivalentResourceRegistry := runtime.NewEquivalentResourceRegistry()

	v1OpenAPISchema, err := apiResourceSpec.GetSchema()
	if err != nil {
		return nil, err
	}
	internalSchema := &apiextensionsinternal.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v1OpenAPISchema, internalSchema, nil); err != nil {
		return nil, fmt.Errorf("failed converting CRD validation to internal version: %v", err)
	}
	structuralSchema, err := structuralschema.NewStructural(internalSchema)
	if err != nil {
		// This should never happen. If it does, it is a programming error.
		utilruntime.HandleError(fmt.Errorf("failed to convert schema to structural: %v", err))
		return nil, fmt.Errorf("the server could not properly serve the CR schema") // validation should avoid this
	}

	// we don't own structuralSchema completely, e.g. defaults are not deep-copied. So better make a copy here.
	structuralSchema = structuralSchema.DeepCopy()

	if err := structuraldefaulting.PruneDefaults(structuralSchema); err != nil {
		// This should never happen. If it does, it is a programming error.
		utilruntime.HandleError(fmt.Errorf("failed to prune defaults: %v", err))
		return nil, fmt.Errorf("the server could not properly serve the CR schema") // validation should avoid this
	}

	/*
		TODO: restore if necessary

		openAPIModels, err := buildOpenAPIModelsForApply(r.staticOpenAPISpec, crd)
		var modelsByGKV openapi.ModelsByGKV

		if err != nil {
			utilruntime.HandleError(fmt.Errorf("error building openapi models for %s: %v", crd.Name, err))
			openAPIModels = nil
		} else {
			modelsByGKV, err = openapi.GetModelsByGKV(openAPIModels)
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("error gathering openapi models by GKV for %s: %v", crd.Name, err))
				modelsByGKV = nil
			}
		}
		var typeConverter fieldmanager.TypeConverter = fieldmanager.DeducedTypeConverter{}
		if openAPIModels != nil {
			typeConverter, err = fieldmanager.NewTypeConverter(openAPIModels, crd.Spec.PreserveUnknownFields)
			if err != nil {
				return nil, err
			}
		}
	*/

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

	resource := schema.GroupVersionResource{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version, Resource: apiResourceSpec.Plural}
	kind := schema.GroupVersionKind{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version, Kind: apiResourceSpec.Kind}
	equivalentResourceRegistry.RegisterKindFor(resource, "", kind)

	typer := NewUnstructuredObjectTyper(parameterScheme)
	creator := UnstructuredCreator{}

	internalValidationSchema := &apiextensionsinternal.CustomResourceValidation{
		OpenAPIV3Schema: internalSchema,
	}
	validator, _, err := apiservervalidation.NewSchemaValidator(internalValidationSchema)
	if err != nil {
		return nil, err
	}

	var statusValidator *validate.SchemaValidator
	subresources := apiResourceSpec.SubResources
	if subresources != nil && subresources.Contains("status") {
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
	}

	table, err := tableconvertor.New(apiResourceSpec.ColumnDefinitions.ToCustomResourceColumnDefinitions())
	if err != nil {
		klog.V(2).Infof("The CRD for %v has an invalid printer specification, falling back to default printing: %v", kind, err)
	}

	storage, statusStorage := registry.NewREST(
		resource,
		kind,
		schema.GroupVersionKind{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version, Kind: apiResourceSpec.ListKind},
		registry.NewStrategy(
			typer,
			apiResourceSpec.Scope == apiextensionsv1.NamespaceScoped,
			kind,
			validator,
			statusValidator,
			structuralSchema,
			subresources.Contains("status"),
		),
		table,
		dynamicClientGetter,
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

	// CRDs explicitly do not support protobuf, but some objects returned by the API server dos[v.Name]
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
		Serializer:          negotiatedSerializer,
		ParameterCodec:      parameterCodec,
		StandardSerializers: standardSerializers,

		Creater:         creator,
		Convertor:       safeConverter,
		Defaulter:       unstructuredDefaulter{parameterScheme, structuralSchema, kind},
		Typer:           typer,
		UnsafeConvertor: unsafeConverter,

		EquivalentResourceMapper: equivalentResourceRegistry,

		Resource: schema.GroupVersionResource{Group: apiResourceSpec.GroupVersion.Group, Version: apiResourceSpec.GroupVersion.Version, Resource: apiResourceSpec.Plural},
		Kind:     kind,

		// a handler for a specific group-version of a custom resource uses that version as the in-memory representation
		HubGroupVersion: kind.GroupVersion(),

		MetaGroupVersion: metav1.SchemeGroupVersion,

		TableConvertor: storage.TableConvertor,

		Authorizer: genericConfig.Authorization.Authorizer,

		MaxRequestBodyBytes: genericConfig.MaxRequestBodyBytes,

		OpenapiModels: nil, // TODO ?
	}

	// TODO: restore the serversideapply stuff here ??

	// shallow copy
	statusScope := *requestScope
	statusScope.Subresource = "status"
	statusScope.Namer = handlers.ContextBasedNaming{
		SelfLinker:         meta.NewAccessor(),
		ClusterScoped:      clusterScoped,
		SelfLinkPathPrefix: selfLinkPrefix,
		SelfLinkPathSuffix: "/status",
	}

	// TODO: restore the serversideapply stuff for status here ??

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

var _ runtime.ObjectConvertor = nopConverter{}

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

type UnstructuredObjectTyper struct {
	Delegate          runtime.ObjectTyper
	UnstructuredTyper runtime.ObjectTyper
}

func NewUnstructuredObjectTyper(Delegate runtime.ObjectTyper) UnstructuredObjectTyper {
	return UnstructuredObjectTyper{
		Delegate:          Delegate,
		UnstructuredTyper: crdserverscheme.NewUnstructuredObjectTyper(),
	}
}

func (t UnstructuredObjectTyper) ObjectKinds(obj runtime.Object) ([]schema.GroupVersionKind, bool, error) {
	// Delegate for things other than Unstructured.
	if _, ok := obj.(runtime.Unstructured); !ok {
		return t.Delegate.ObjectKinds(obj)
	}
	return t.UnstructuredTyper.ObjectKinds(obj)
}

func (t UnstructuredObjectTyper) Recognizes(gvk schema.GroupVersionKind) bool {
	return t.Delegate.Recognizes(gvk) || t.UnstructuredTyper.Recognizes(gvk)
}

type UnstructuredCreator struct{}

func (c UnstructuredCreator) New(kind schema.GroupVersionKind) (runtime.Object, error) {
	var ret schema.ObjectKind
	if strings.HasSuffix(kind.Kind, "List") {
		ret = &unstructured.UnstructuredList{}
	} else {
		ret = &unstructured.Unstructured{}
	}
	ret.SetGroupVersionKind(kind)
	return ret.(runtime.Object), nil
}

type unstructuredDefaulter struct {
	delegate            runtime.ObjectDefaulter
	structuralSchema    *structuralschema.Structural // by version
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

type CRDRESTOptionsGetter struct {
	StorageConfig             storagebackend.Config
	StoragePrefix             string
	EnableWatchCache          bool
	DefaultWatchCacheSize     int
	EnableGarbageCollection   bool
	DeleteCollectionWorkers   int
	CountMetricPollPeriod     time.Duration
	StorageObjectCountTracker flowcontrolrequest.StorageObjectCountTracker
}

func (t CRDRESTOptionsGetter) GetRESTOptions(resource schema.GroupResource) (generic.RESTOptions, error) {
	ret := generic.RESTOptions{
		StorageConfig:             t.StorageConfig.ForResource(resource),
		Decorator:                 generic.UndecoratedStorage,
		EnableGarbageCollection:   t.EnableGarbageCollection,
		DeleteCollectionWorkers:   t.DeleteCollectionWorkers,
		ResourcePrefix:            resource.Group + "/" + resource.Resource,
		CountMetricPollPeriod:     t.CountMetricPollPeriod,
		StorageObjectCountTracker: t.StorageObjectCountTracker,
	}
	if t.EnableWatchCache {
		ret.Decorator = genericregistry.StorageWithCacher()
	}
	return ret, nil
}

// schemaCoercingDecoder calls the delegate decoder, and then applies the Unstructured schema validator
// to coerce the schema.
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

// schemaCoercingConverter calls the delegate converter and applies the Unstructured validator to
// coerce the schema.
type schemaCoercingConverter struct {
	delegate  runtime.ObjectConvertor
	validator unstructuredSchemaCoercer
}

var _ runtime.ObjectConvertor = schemaCoercingConverter{}

func (v schemaCoercingConverter) Convert(in, out, context interface{}) error {
	if err := v.delegate.Convert(in, out, context); err != nil {
		return err
	}

	if u, ok := out.(*unstructured.Unstructured); ok {
		if _, err := v.validator.apply(u); err != nil {
			return err
		}
	}

	return nil
}

func (v schemaCoercingConverter) ConvertToVersion(in runtime.Object, gv runtime.GroupVersioner) (runtime.Object, error) {
	out, err := v.delegate.ConvertToVersion(in, gv)
	if err != nil {
		return nil, err
	}

	if u, ok := out.(*unstructured.Unstructured); ok {
		if _, err := v.validator.apply(u); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (v schemaCoercingConverter) ConvertFieldLabel(gvk schema.GroupVersionKind, label, value string) (string, string, error) {
	return v.delegate.ConvertFieldLabel(gvk, label, value)
}

// unstructuredSchemaCoercer adds to unstructured unmarshalling what json.Unmarshal does
// in addition for native types when decoding into Golang structs:
//
// - validating and pruning ObjectMeta
// - generic pruning of unknown fields following a structural schema
// - removal of non-defaulted non-nullable null map values.
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
