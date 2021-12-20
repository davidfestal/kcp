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

package rootapiserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericapiserveroptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/kubernetes/pkg/api/legacyscheme"

	virtualapiserver "github.com/kcp-dev/kcp/pkg/virtual/generic/apiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/generic/builders"
)

type InformerStart func(stopCh <-chan struct{})
type InformerStarts []InformerStart

type RootAPIExtraConfig struct {
	// we phrase it like this so we can build the post-start-hook, but no one can take more indirect dependencies on informers
	informerStart func(stopCh <-chan struct{})

	VirtualWorkspaces []builders.VirtualWorkspaceBuilder
}

// Validate helps ensure that we build this config correctly, because there are lots of bits to remember for now
func (c *RootAPIExtraConfig) Validate() error {
	ret := []error{}

	return utilerrors.NewAggregate(ret)
}

type RootAPIConfig struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ExtraConfig   RootAPIExtraConfig
}

// RootAPIServer is only responsible for serving the APIs for the virtual workspace
// at a given root path or root path family
// It does NOT expose oauth, related oauth endpoints, or any kube APIs.
type RootAPIServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ExtraConfig   *RootAPIExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *RootAPIConfig) Complete() completedConfig {
	cfg := completedConfig{
		c.GenericConfig.Complete(),
		&c.ExtraConfig,
	}

	return cfg
}

func (c *completedConfig) withAPIServerForAPIGroup(virtualWorkspaceName string, groupAPIServerBuilder builders.APIGroupAPIServerBuilder) apiServerAppenderFunc {
	return func(delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
		restStorageBuilders, err := groupAPIServerBuilder.Initialize(c.GenericConfig)
		cfg := &virtualapiserver.GroupAPIServerConfig{
			GenericConfig: &genericapiserver.RecommendedConfig{Config: *c.GenericConfig.Config, SharedInformerFactory: c.GenericConfig.SharedInformerFactory},
			ExtraConfig: virtualapiserver.ExtraConfig{
				Codecs:          legacyscheme.Codecs,
				Scheme:          legacyscheme.Scheme,
				GroupVersion:    groupAPIServerBuilder.GroupVersion,
				StorageBuilders: restStorageBuilders,
			},
		}
		cfg.GenericConfig.PostStartHooks = map[string]genericapiserver.PostStartHookConfigEntry{}
		config := cfg.Complete()
		if err != nil {
			return nil, err
		}

		server, err := config.New(virtualWorkspaceName, delegateAPIServer)
		if err != nil {
			return nil, err
		}

		return server.GenericAPIServer, nil
	}
}

func (c *completedConfig) WithOpenAPIAggregationController(delegatedAPIServer *genericapiserver.GenericAPIServer) error {
	return nil
}

type apiServerAppenderFunc func(delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error)

func (c completedConfig) New(delegationTarget genericapiserver.DelegationTarget) (*RootAPIServer, error) {
	delegateAPIServer := delegationTarget

	vwNames := sets.NewString()
	for _, virtualWorkspace := range c.ExtraConfig.VirtualWorkspaces {
		name := virtualWorkspace.Name
		if vwNames.Has(name) {
			return nil, errors.New("Several virtual workspaces with the same name: " + name)
		}
		vwNames.Insert(name)
		for _, groupAPIServerBuilder := range virtualWorkspace.GroupAPIServerBuilders {
			var err error
			delegateAPIServer, err = c.withAPIServerForAPIGroup(name, groupAPIServerBuilder)(delegateAPIServer)
			if err != nil {
				return nil, err
			}
		}
	}

	c.GenericConfig.BuildHandlerChainFunc = c.getRootHandlerChain(delegateAPIServer)
	c.GenericConfig.RequestInfoResolver = c

	genericServer, err := c.GenericConfig.New("virtual-workspaces-root-apiserver", delegateAPIServer)
	if err != nil {
		return nil, err
	}

	s := &RootAPIServer{
		GenericAPIServer: genericServer,
	}

	// register our poststarthooks
	s.GenericAPIServer.AddPostStartHookOrDie("virtual-workspace-startinformers", func(context genericapiserver.PostStartHookContext) error {
		c.ExtraConfig.informerStart(context.StopCh)
		return nil
	})

	return s, nil
}

func (c completedConfig) resolveRootPaths(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	completedContext = requestContext
	for _, virtualWorkspace := range c.ExtraConfig.VirtualWorkspaces {
		if accepted, prefixToStrip, completedContext := virtualWorkspace.RootPathResolver(urlPath, requestContext); accepted {
			return accepted, prefixToStrip, context.WithValue(completedContext, virtualapiserver.VirtualNamespaceNameKey, virtualWorkspace.Name)
		}
	}
	return
}

func (c completedConfig) getRootHandlerChain(delegateAPIServer genericapiserver.DelegationTarget) func(http.Handler, *genericapiserver.Config) http.Handler {
	return func(apiHandler http.Handler, genericConfig *genericapiserver.Config) http.Handler {
		return genericapiserver.DefaultBuildHandlerChain(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if accepted, prefixToStrip, context := c.resolveRootPaths(req.URL.Path, req.Context()); accepted {
				req.URL.Path = strings.TrimPrefix(req.URL.Path, prefixToStrip)
				req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, prefixToStrip)
				req = req.WithContext(genericapirequest.WithCluster(context, genericapirequest.Cluster{Name: "virtual"}))
				delegatedHandler := delegateAPIServer.UnprotectedHandler()
				if delegatedHandler != nil {
					delegatedHandler.ServeHTTP(w, req)
				}
				return
			}
			http.NotFoundHandler().ServeHTTP(w, req)
		}), c.GenericConfig.Config)
	}
}

var _ genericapirequest.RequestInfoResolver = (*completedConfig)(nil)

func (c completedConfig) NewRequestInfo(req *http.Request) (*genericapirequest.RequestInfo, error) {
	defaultResolver := genericapiserver.NewRequestInfoResolver(c.GenericConfig.Config)
	if accepted, prefixToStrip, _ := c.resolveRootPaths(req.URL.Path, req.Context()); accepted {
		p := strings.TrimPrefix(req.URL.Path, prefixToStrip)
		rp := strings.TrimPrefix(req.URL.RawPath, prefixToStrip)
		r2 := new(http.Request)
		*r2 = *req
		r2.URL = new(url.URL)
		*r2.URL = *req.URL
		r2.URL.Path = p
		r2.URL.RawPath = rp
		return defaultResolver.NewRequestInfo(r2)
	}
	return defaultResolver.NewRequestInfo(req)
}

func NewRootAPIConfig(secureServing *genericapiserveroptions.SecureServingOptionsWithLoopback, authenticationOptions *genericapiserveroptions.DelegatingAuthenticationOptions, informerStarts InformerStarts, rootAPIServerBuilders ...builders.VirtualWorkspaceBuilder) (*RootAPIConfig, error) {
	genericConfig := genericapiserver.NewRecommendedConfig(legacyscheme.Codecs)
	// Current default values
	//Serializer:                   codecs,
	//ReadWritePort:                443,
	//BuildHandlerChainFunc:        DefaultBuildHandlerChain,
	//HandlerChainWaitGroup:        new(utilwaitgroup.SafeWaitGroup),
	//LegacyAPIGroupPrefixes:       sets.NewString(DefaultLegacyAPIPrefix),
	//DisabledPostStartHooks:       sets.NewString(),
	//HealthzChecks:                []healthz.HealthzChecker{healthz.PingHealthz, healthz.LogHealthz},
	//EnableIndex:                  true,
	//EnableDiscovery:              true,
	//EnableProfiling:              true,
	//EnableMetrics:                true,
	//MaxRequestsInFlight:          400,
	//MaxMutatingRequestsInFlight:  200,
	//RequestTimeout:               time.Duration(60) * time.Second,
	//MinRequestTimeout:            1800,
	//EnableAPIResponseCompression: utilfeature.DefaultFeatureGate.Enabled(features.APIResponseCompression),
	//LongRunningFunc: genericfilters.BasicLongRunningRequestCheck(sets.NewString("watch"), sets.NewString()),

	genericapiserveroptions.NewCoreAPIOptions().ApplyTo(genericConfig)

	// these are set via options
	//SecureServing *SecureServingInfo
	//Authentication AuthenticationInfo
	//Authorization AuthorizationInfo
	//LoopbackClientConfig *restclient.Config
	// this is set after the options are overlayed to get the authorizer we need.
	//AdmissionControl      admission.Interface
	//ReadWritePort int
	//PublicAddress net.IP

	// these are defaulted sanely during complete
	//DiscoveryAddresses discovery.Addresses

	// TODO: genericConfig.ExternalAddress = ... allow a command line flag or it to be overridden by a top-level multiroot apiServer

	/*
		// previously overwritten.  I don't know why
		genericConfig.RequestTimeout = time.Duration(60) * time.Second
		genericConfig.MinRequestTimeout = int((time.Duration(60) * time.Minute).Seconds())
		genericConfig.MaxRequestsInFlight = -1 // TODO: allow configuring
		genericConfig.MaxMutatingRequestsInFlight = -1 // TODO configuring
		genericConfig.LongRunningFunc = apiserverconfig.IsLongRunningRequest
	*/

	if err := secureServing.ApplyTo(&genericConfig.Config.SecureServing, &genericConfig.Config.LoopbackClientConfig); err != nil {
		return nil, err
	}

	if err := authenticationOptions.ApplyTo(&genericConfig.Authentication, genericConfig.SecureServing, genericConfig.OpenAPIConfig); err != nil {
		return nil, err
	}

	// TODO: in the future it would probably be a mix between a delegated authorizer (delegating to some KCP instance)
	// and a specific authorizer whose rules would be defined by each prefix-based virtual workspace.
	genericConfig.Authorization.Authorizer = authorizerfactory.NewAlwaysAllowAuthorizer()

	ret := &RootAPIConfig{
		GenericConfig: genericConfig,
		ExtraConfig: RootAPIExtraConfig{
			informerStart: func(stopCh <-chan struct{}) {
				for _, informerStart := range informerStarts {
					informerStart(stopCh)
				}
			},
			VirtualWorkspaces: rootAPIServerBuilders,
		},
	}

	return ret, ret.ExtraConfig.Validate()
}
