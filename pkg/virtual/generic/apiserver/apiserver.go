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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/generic/builders"
)

type virtualNamespaceNameKeyType string

const VirtualNamespaceNameKey virtualNamespaceNameKeyType = "VirtualWorkspaceName"

type ExtraConfig struct {
	builders.GroupBuilder
}

type GroupAPIServerConfig struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ExtraConfig   ExtraConfig
}

// GroupAPIServer contains state for a Kubernetes cluster master/api server.
type GroupAPIServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ExtraConfig   *ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *GroupAPIServerConfig) Complete() completedConfig {
	cfg := completedConfig{
		c.GenericConfig.Complete(),
		&c.ExtraConfig,
	}

	return cfg
}

// New returns a new instance of VirtualWorkspaceAPIServer from the given config.
func (c completedConfig) New(virtualWorkspaceName string, delegationTarget genericapiserver.DelegationTarget) (*GroupAPIServer, error) {
	genericServer, err := c.GenericConfig.New(virtualWorkspaceName+"-"+c.ExtraConfig.GroupVersion.Group+"-virtual-workspace-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}

	director := genericServer.Handler.Director
	genericServer.Handler.Director = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if vwName := r.Context().Value(VirtualNamespaceNameKey); vwName != nil {
			if vwNameString, isString := vwName.(string); isString && vwNameString == virtualWorkspaceName {
				director.ServeHTTP(rw, r)
				return
			}
		}
		delegatedHandler := delegationTarget.UnprotectedHandler()
		if delegatedHandler != nil {
			delegatedHandler.ServeHTTP(rw, r)
		}
	})

	s := &GroupAPIServer{
		GenericAPIServer: genericServer,
	}

	scheme := runtime.NewScheme()
	codecs := serializer.NewCodecFactory(scheme)
	if err := c.ExtraConfig.AddToScheme(scheme); err != nil {
		return nil, err
	}

	builders, err := c.ExtraConfig.NewRestStorageBuilders(c.GenericConfig)
	if err != nil {
		return nil, err
	}
	storage := map[string]rest.Storage{}
	for resource, storageBuilder := range builders {
		restStorage, err := storageBuilder(c.GenericConfig)
		if err != nil {
			return nil, err
		}
		storage[resource] = restStorage
	}

	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(c.ExtraConfig.GroupVersion.Group, scheme, metav1.ParameterCodec, codecs)
	apiGroupInfo.VersionedResourcesStorageMap[c.ExtraConfig.GroupVersion.Version] = storage
	if err := s.GenericAPIServer.InstallAPIGroup(&apiGroupInfo); err != nil {
		return nil, err
	}

	return s, nil
}
