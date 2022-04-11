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

package syncer

import (
	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
)

const SyncerFinalizer string = "workloads.kcp.dev/syncer"

func LocationClusterLabelName(locationName string) string {
	return "cluster.internal.workloads.kcp.dev/" + locationName
}

type WorkloadCluster struct {
	WorkspaceName string
	LocationName  string
}

func (c WorkloadCluster) Key() string {
	return c.WorkspaceName + "~~" + c.LocationName
}

type WorkloadClusterAPI struct {
	WorkloadCluster
	Spec *apiresourcev1alpha1.CommonAPIResourceSpec
}

type WorkloadClusterAPIWatcher interface {
	Upsert(api WorkloadClusterAPI) error
	Remove(api WorkloadClusterAPI) error
}
