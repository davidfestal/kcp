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

package builder

import (
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/syncer/apiserver"
)

func (vw *SyncerVirtualWorkspace) Register(rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	// TODO: build the main APIServer based on:
	// - the config here
	// - the `APISetRetriever`s that will be built by the initialization (including the cluster controller, etc, etc)

	if err := vw.BootstrapAPISetManager(rootAPIServerConfig); err != nil {
		return nil, err
	}

	cfg := &apiserver.DynamicAPIServerConfig{
		GenericConfig: &genericapiserver.RecommendedConfig{Config: *rootAPIServerConfig.Config, SharedInformerFactory: rootAPIServerConfig.SharedInformerFactory},
		ExtraConfig: apiserver.DynamicAPIServerExtraConfig{
			APISetRetriever: vw.apiSets,
		},
	}

	// We don't want any poststart hooks at the level of a GroupVersionAPIServer.
	// In the current design, PostStartHooks are only added at the top level RootAPIServer.
	// So let's drop the PostStartHooks from the GroupVersionAPIServerConfig since they are simply copied
	// from the RootAPIServerConfig
	cfg.GenericConfig.PostStartHooks = map[string]genericapiserver.PostStartHookConfigEntry{}
	config := cfg.Complete()

	server, err := config.New(vw.Name, delegateAPIServer)
	if err != nil {
		return nil, err
	}

	delegateAPIServer = server.GenericAPIServer

	return delegateAPIServer, nil
}
