/*
Copyright 2021 The KCP Authors.

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
	"net/http"
	"strings"
	"time"

	"github.com/emicklei/go-restful"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	genericapiserver "k8s.io/apiserver/pkg/server"

	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	syncer "github.com/kcp-dev/kcp/pkg/virtual/syncer"
)

var (
	Scheme = runtime.NewScheme()
	Codecs = serializer.NewCodecFactory(Scheme)

	// if you modify this, make sure you update the crEncoder
	unversionedVersion = schema.GroupVersion{Group: "", Version: "v1"}
	unversionedTypes   = []runtime.Object{
		&metav1.Status{},
		&metav1.WatchEvent{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	}
)

func init() {
	// we need to add the options to empty v1
	metav1.AddToGroupVersion(Scheme, schema.GroupVersion{Group: "", Version: "v1"})

	Scheme.AddUnversionedTypes(unversionedVersion, unversionedTypes...)
}

type DynamicAPIServerExtraConfig struct {
	APISetRetriever syncer.APISetRetriever

	// Something that contains all the sort-of crdInfo per location + per GVR ?
}

type DynamicAPIServerConfig struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ExtraConfig   DynamicAPIServerExtraConfig
}

// DynamicAPIServer contains state for a Kubernetes cluster master/api server.
type DynamicAPIServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
	APISetRetriever  syncer.APISetRetriever
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ExtraConfig   *DynamicAPIServerExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *DynamicAPIServerConfig) Complete() completedConfig {
	cfg := completedConfig{
		c.GenericConfig.Complete(),
		&c.ExtraConfig,
	}

	return cfg
}

// New returns a new instance of VirtualWorkspaceAPIServer from the given config.
func (c completedConfig) New(virtualWorkspaceName string, delegationTarget genericapiserver.DelegationTarget) (*DynamicAPIServer, error) {
	genericServer, err := c.GenericConfig.New(virtualWorkspaceName+"-virtual-workspace-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}

	director := genericServer.Handler.Director
	genericServer.Handler.Director = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if vwName := r.Context().Value(virtualcontext.VirtualWorkspaceNameKey); vwName != nil && r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
			if vwNameString, isString := vwName.(string); isString && vwNameString == virtualWorkspaceName {
				director.ServeHTTP(rw, r)
				return
			}
		}
		delegatedHandler := delegationTarget.UnprotectedHandler()
		if delegatedHandler != nil {
			delegatedHandler.ServeHTTP(rw, r)
		} else {
			http.NotFoundHandler().ServeHTTP(rw, r)
		}
	})

	s := &DynamicAPIServer{
		GenericAPIServer: genericServer,
		APISetRetriever:  c.ExtraConfig.APISetRetriever,
	}

	delegateHandler := delegationTarget.UnprotectedHandler()
	if delegateHandler == nil {
		delegateHandler = http.NotFoundHandler()
	}

	versionDiscoveryHandler := &versionDiscoveryHandler{
		apiSetRetriever: s.APISetRetriever,
		delegate:        delegateHandler,
	}

	groupDiscoveryHandler := &groupDiscoveryHandler{
		apiSetRetriever: s.APISetRetriever,
		delegate:        delegateHandler,
	}

	rootDiscoveryHandler := &rootDiscoveryHandler{
		apiSetRetriever: s.APISetRetriever,
		delegate:        delegateHandler,
	}

	crdHandler, err := NewResourceHandler(
		s.APISetRetriever,
		versionDiscoveryHandler,
		groupDiscoveryHandler,
		rootDiscoveryHandler,
		delegateHandler,
		c.GenericConfig.AdmissionControl,
		s.GenericAPIServer.Authorizer,
		c.GenericConfig.RequestTimeout,
		time.Duration(c.GenericConfig.MinRequestTimeout)*time.Second,
		c.GenericConfig.MaxRequestBodyBytes,
	)
	if err != nil {
		return nil, err
	}

	s.GenericAPIServer.Handler.GoRestfulContainer.Filter(func(req *restful.Request, resp *restful.Response, chain *restful.FilterChain) {
		if req.Request.URL.Path != "/apis" && req.Request.URL.Path != "/apis/" {
			chain.ProcessFilter(req, resp)
			return
		}
		rootDiscoveryHandler.ServeHTTP(resp.ResponseWriter, req.Request)
	})
	s.GenericAPIServer.Handler.NonGoRestfulMux.Handle("/apis", crdHandler)
	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/apis/", crdHandler)
	// HACK: Added to allow serving core resources registered through CRDs (for the KCP scenario)
	s.GenericAPIServer.Handler.NonGoRestfulMux.UnlistedHandlePrefix("/api/v1/", crdHandler)

	// TODO: plug OpenAPI when necessary, when this is made generic

	return s, nil
}
