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
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	listers "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	//	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/metrics"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	syncer "github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/registry"
	//	"k8s.io/apiserver/pkg/features"
	//	utilfeature "k8s.io/apiserver/pkg/util/feature"
)

// resourceHandler serves the `/apis` endpoint.
// This is registered as a filter so that it never collides with any explicitly registered endpoints
type resourceHandler struct {
	apiSetRetriever         syncer.APISetRetriever
	versionDiscoveryHandler *versionDiscoveryHandler
	groupDiscoveryHandler   *groupDiscoveryHandler
	rootDiscoveryHandler    *rootDiscoveryHandler

	crdLister listers.CustomResourceDefinitionLister

	delegate http.Handler

	admission admission.Interface

	// so that we can do create on update.
	authorizer authorizer.Authorizer

	// request timeout we should delay storage teardown for
	requestTimeout time.Duration

	// minRequestTimeout applies to CR's list/watch calls
	minRequestTimeout time.Duration

	// The limit on the request size that would be accepted and decoded in a write request
	// 0 means no limit.
	maxRequestBodyBytes int64
}

func NewResourceHandler(
	apiSetRetriever syncer.APISetRetriever,
	versionDiscoveryHandler *versionDiscoveryHandler,
	groupDiscoveryHandler *groupDiscoveryHandler,
	rootDiscoveryHandler *rootDiscoveryHandler,
	delegate http.Handler,
	admission admission.Interface,
	authorizer authorizer.Authorizer,
	requestTimeout time.Duration,
	minRequestTimeout time.Duration,
	maxRequestBodyBytes int64) (*resourceHandler, error) {
	ret := &resourceHandler{
		apiSetRetriever:         apiSetRetriever,
		versionDiscoveryHandler: versionDiscoveryHandler,
		groupDiscoveryHandler:   groupDiscoveryHandler,
		rootDiscoveryHandler:    rootDiscoveryHandler,
		delegate:                delegate,
		admission:               admission,
		authorizer:              authorizer,
		requestTimeout:          requestTimeout,
		minRequestTimeout:       minRequestTimeout,
		maxRequestBodyBytes:     maxRequestBodyBytes,
	}

	return ret, nil
}

// watches are expected to handle storage disruption gracefully,
// both on the server-side (by terminating the watch connection)
// and on the client side (by restarting the watch)
var longRunningFilter = genericfilters.BasicLongRunningRequestCheck(sets.NewString("watch"), sets.NewString())

// possiblyAcrossAllNamespacesVerbs contains those verbs which can be per-namespace and across all
// namespaces for namespaces resources. I.e. for these an empty namespace in the requestInfo is fine.
var possiblyAcrossAllNamespacesVerbs = sets.NewString("list", "watch")

func (r *resourceHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	requestInfo, ok := apirequest.RequestInfoFrom(ctx)
	if !ok {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("no RequestInfo found in the context")),
			Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		return
	}
	if !requestInfo.IsResourceRequest {
		pathParts := splitPath(requestInfo.Path)
		// only match /apis/<group>/<version>
		// only registered under /apis
		if len(pathParts) == 3 {
			r.versionDiscoveryHandler.ServeHTTP(w, req)
			return
		}
		// only match /apis/<group>
		if len(pathParts) == 2 {
			r.groupDiscoveryHandler.ServeHTTP(w, req)
			return
		}
		// only match /apis
		if len(pathParts) == 1 {
			r.rootDiscoveryHandler.ServeHTTP(w, req)
			return
		}

		r.delegate.ServeHTTP(w, req)
		return
	}

	locationKey := ctx.Value(registry.LocationKeyContextKey).(string)

	apiDefs := r.apiSetRetriever.GetAPIs(locationKey)
	apiDef, exists := apiDefs[schema.GroupVersionResource{
		Group:    requestInfo.APIGroup,
		Version:  requestInfo.APIVersion,
		Resource: requestInfo.Resource,
	}]

	if !exists {
		r.delegate.ServeHTTP(w, req)
		return
	}

	apiResourceSpec := apiDef.GetAPIResourceSpec()
	//	apiClusterName := apiDef.GetClusterName()

	verb := strings.ToUpper(requestInfo.Verb)
	resource := requestInfo.Resource
	subresource := requestInfo.Subresource
	scope := metrics.CleanScope(requestInfo)
	supportedTypes := []string{
		//		string(types.JSONPatchType),
		//		string(types.MergePatchType),
	}

	/*

		// HACK: Support resources of the client-go scheme the way existing clients expect it:
		//   - Support Strategic Merge Patch (used by default on these resources by kubectl)
		//   - Support the Protobuf content type on Create / Update resources
		//     (by simply converting the request to the json content type),
		//     since protobuf content type is expected to be supported in a number of client
		//     contexts (like controller-runtime for example)
		if clientgoscheme.Scheme.IsGroupRegistered(requestInfo.APIGroup) {
			supportedTypes = append(supportedTypes, string(types.StrategicMergePatchType))
			req, err := convertProtobufRequestsToJson(verb, req, schema.GroupVersionKind{
				Group:   requestInfo.APIGroup,
				Version: requestInfo.APIVersion,
				Kind:    apiResourceSpec.Kind,
			})
			if err != nil {
				responsewriters.ErrorNegotiated(
					apierrors.NewInternalError(err),
					Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
				)
				return
			}
		}

		if utilfeature.DefaultFeatureGate.Enabled(features.ServerSideApply) {
			supportedTypes = append(supportedTypes, string(types.ApplyPatchType))
		}
	*/
	var handlerFunc http.HandlerFunc
	subresources := apiResourceSpec.SubResources
	switch {
	case subresource == "status" && subresources != nil && subresources.Contains("status"):
		handlerFunc = r.serveStatus(w, req, requestInfo, apiDef, supportedTypes)
	// TODO when we make a generic thing from this, possibly add scale here.
	case len(subresource) == 0:
		handlerFunc = r.serveResource(w, req, requestInfo, apiDef, supportedTypes)
	default:
		responsewriters.ErrorNegotiated(
			apierrors.NewNotFound(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Name),
			Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
	}

	if handlerFunc != nil {
		handlerFunc = metrics.InstrumentHandlerFunc(verb, requestInfo.APIGroup, requestInfo.APIVersion, resource, subresource, scope, metrics.APIServerComponent, false, "", handlerFunc)
		//		handler := genericfilters.WithWaitGroup(handlerFunc, longRunningFilter, crdInfo.waitGroup)
		handlerFunc.ServeHTTP(w, req)
		return
	}
}

// HACK: In some contexts, like the controller-runtime library used by the Operator SDK, all the resources of the
// client-go scheme are created / updated using the protobuf content type.
// However when these resources are in fact added as CRDs, in the KCP minimal API server scenario, these resources cannot
// be created / updated since the protobuf (de)serialization is not supported for CRDs.
// So in this case we just convert the protobuf request to a Json one (using the `client-go` scheme decoder/encoder),
// before letting the CRD handler serve it.
//
// A real, long-term and non-hacky, fix for this problem would be as follows:
// When a request for an unsupported serialization is returned, the server should reject it with a 406
// and provide a list of supported content types.
// client-go should then examine whether it can satisfy such a request by encoding the object with a different scheme.
// This would require a KEP but is in keeping with content negotiation on GET / WATCH in Kube
func convertProtobufRequestsToJson(verb string, req *http.Request, gvk schema.GroupVersionKind) (*http.Request, error) {
	if (verb == "CREATE" || verb == "UPDATE") &&
		req.Header.Get("Content-Type") == runtime.ContentTypeProtobuf {
		resource, err := clientgoscheme.Scheme.New(gvk)
		if err != nil {
			utilruntime.HandleError(err)
			return nil, fmt.Errorf("Error when converting a protobuf request to a json request on a client-go resource added as a CRD")
		}
		reader, err := req.Body, nil
		if err != nil {
			utilruntime.HandleError(err)
			return nil, fmt.Errorf("Error when converting a protobuf request to a json request on a client-go resource added as a CRD")
		}
		defer reader.Close()
		buf := new(bytes.Buffer)
		_, err = buf.ReadFrom(reader)
		if err != nil {
			utilruntime.HandleError(err)
			return nil, fmt.Errorf("Error when converting a protobuf request to a json request on a client-go resource added as a CRD")
		}

		// get bytes through IO operations
		protobuf.NewSerializer(clientgoscheme.Scheme, clientgoscheme.Scheme).Decode(buf.Bytes(), &gvk, resource)
		buf = new(bytes.Buffer)
		json.NewSerializerWithOptions(json.DefaultMetaFactory, clientgoscheme.Scheme, clientgoscheme.Scheme, json.SerializerOptions{Yaml: false, Pretty: false, Strict: true}).
			Encode(resource, buf)
		req.Body = ioutil.NopCloser(buf)
		req.ContentLength = int64(buf.Len())
		req.Header.Set("Content-Type", runtime.ContentTypeJSON)
	}
	return req, nil
}

func (r *resourceHandler) serveResource(w http.ResponseWriter, req *http.Request, requestInfo *apirequest.RequestInfo, apiDef syncer.APIDefinition, supportedTypes []string) http.HandlerFunc {
	requestScope := apiDef.GetRequestScope()
	storage := apiDef.GetStorage()

	switch requestInfo.Verb {
	case "get":
		if storage, isAble := storage.(rest.Getter); isAble {
			return handlers.GetResource(storage, requestScope)
		}
	case "list":
		if listerStorage, isAble := storage.(rest.Lister); isAble {
			if watcherStorage, isAble := storage.(rest.Watcher); isAble {
				forceWatch := false
				return handlers.ListResource(listerStorage, watcherStorage, requestScope, forceWatch, r.minRequestTimeout)
			}
		}
	case "watch":
		if listerStorage, isAble := storage.(rest.Lister); isAble {
			if watcherStorage, isAble := storage.(rest.Watcher); isAble {
				forceWatch := true
				return handlers.ListResource(listerStorage, watcherStorage, requestScope, forceWatch, r.minRequestTimeout)
			}
		}
	case "create":
		if storage, isAble := storage.(rest.Creater); isAble {
			return handlers.CreateResource(storage, requestScope, r.admission)
		}
	case "update":
		if storage, isAble := storage.(rest.Updater); isAble {
			return handlers.UpdateResource(storage, requestScope, r.admission)
		}
	case "patch":
		if storage, isAble := storage.(rest.Patcher); isAble {
			return handlers.PatchResource(storage, requestScope, r.admission, supportedTypes)
		}
	case "delete":
		if storage, isAble := storage.(rest.GracefulDeleter); isAble {
			allowsOptions := true
			return handlers.DeleteResource(storage, allowsOptions, requestScope, r.admission)
		}
	case "deletecollection":
		if storage, isAble := storage.(rest.CollectionDeleter); isAble {
			checkBody := true
			return handlers.DeleteCollection(storage, checkBody, requestScope, r.admission)
		}
	}
	responsewriters.ErrorNegotiated(
		apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
		Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
	)
	return nil
}

func (r *resourceHandler) serveStatus(w http.ResponseWriter, req *http.Request, requestInfo *apirequest.RequestInfo, apiDef syncer.APIDefinition, supportedTypes []string) http.HandlerFunc {
	requestScope := apiDef.GetSubResourceRequestScope("status")
	storage := apiDef.GetSubResourceStorage("status")

	switch requestInfo.Verb {
	case "get":
		if storage, isAble := storage.(rest.Getter); isAble {
			return handlers.GetResource(storage, requestScope)
		}
	case "update":
		if storage, isAble := storage.(rest.Updater); isAble {
			return handlers.UpdateResource(storage, requestScope, r.admission)
		}
	case "patch":
		if storage, isAble := storage.(rest.Patcher); isAble {
			return handlers.PatchResource(storage, requestScope, r.admission, supportedTypes)
		}
	}
	responsewriters.ErrorNegotiated(
		apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
		Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
	)
	return nil
}

func (r *resourceHandler) serveScale(w http.ResponseWriter, req *http.Request, requestInfo *apirequest.RequestInfo, apiDef syncer.APIDefinition, supportedTypes []string) http.HandlerFunc {
	requestScope := apiDef.GetSubResourceRequestScope("scale")
	storage := apiDef.GetSubResourceStorage("scale")

	switch requestInfo.Verb {
	case "get":
		if storage, isGetter := storage.(rest.Getter); isGetter {
			return handlers.GetResource(storage, requestScope)
		}
	case "update":
		if storage, isUpdater := storage.(rest.Updater); isUpdater {
			return handlers.UpdateResource(storage, requestScope, r.admission)
		}
	case "patch":
		if storage, isPatcher := storage.(rest.Patcher); isPatcher {
			return handlers.PatchResource(storage, requestScope, r.admission, supportedTypes)
		}
	}
	responsewriters.ErrorNegotiated(
		apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
		Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
	)
	return nil
}
