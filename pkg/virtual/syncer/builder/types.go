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
	"context"

	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
)

var _ framework.VirtualWorkspace = (*SyncerVirtualWorkspace)(nil)

type SyncerVirtualWorkspace struct {
	Name                   string
	RootPathResolver       framework.RootPathResolverFunc
	Ready                  framework.ReadyFunc
	BootstrapAPISetManager func(mainConfig genericapiserver.CompletedConfig) error
	apiSets                syncer.APISetRetriever
}

func (vw *SyncerVirtualWorkspace) GetName() string {
	return vw.Name
}

func (vw *SyncerVirtualWorkspace) IsReady() error {
	return vw.Ready()
}

func (vw *SyncerVirtualWorkspace) ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	return vw.RootPathResolver(urlPath, context)
}
