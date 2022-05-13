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
	"fmt"
	"sync"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clusters"

	apidefinition "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
)

var _ syncer.WorkloadClusterAPIManager = (*installedAPIs)(nil)
var _ apidefinition.APIDefinitionSetGetter = (*installedAPIs)(nil)

type installedAPIs struct {
	createAPIDefinition apidefinition.CreateAPIDefinitionFunc

	mutex   sync.RWMutex
	apiSets map[string]apidefinition.APIDefinitionSet
}

func newInstalledAPIs(createAPIDefinition apidefinition.CreateAPIDefinitionFunc) *installedAPIs {
	return &installedAPIs{
		createAPIDefinition: createAPIDefinition,
		apiSets:             make(map[string]apidefinition.APIDefinitionSet),
	}
}

func (apis *installedAPIs) addWorkloadCluster(cluster logicalcluster.Name, workloadCluster string) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	workloadClusterKey := clusters.ToClusterAwareKey(cluster, workloadCluster)
	if _, exists := apis.apiSets[workloadClusterKey]; !exists {
		apis.apiSets[workloadClusterKey] = make(apidefinition.APIDefinitionSet)
	}
}

func (apis *installedAPIs) removeWorkloadCluster(cluster logicalcluster.Name, workloadCluster string) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	workloadClusterKey := clusters.ToClusterAwareKey(cluster, workloadCluster)
	delete(apis.apiSets, workloadClusterKey)
}

func (apis *installedAPIs) GetAPIDefinitionSet(apiDomainKey string) (apidefinition.APIDefinitionSet, bool) {
	apis.mutex.RLock()
	defer apis.mutex.RUnlock()

	apiSet, ok := apis.apiSets[apiDomainKey]
	return apiSet, ok
}

func (apis *installedAPIs) Upsert(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	key := clusters.ToClusterAwareKey(api.LogicalClusterName, api.Name)
	if workloadClusterAPIs, exists := apis.apiSets[key]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.Name, api.LogicalClusterName.String())
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		if apiDefinition, err := apis.createAPIDefinition(api.LogicalClusterName, api.Spec); err != nil {
			return err
		} else {
			workloadClusterAPIs[gvr] = apiDefinition
		}
	}
	return nil
}

func (apis *installedAPIs) Remove(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	key := clusters.ToClusterAwareKey(api.LogicalClusterName, api.Name)
	if workloadClusterAPIs, exists := apis.apiSets[key]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.Name, api.LogicalClusterName.String())
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		delete(workloadClusterAPIs, gvr)
	}
	return nil
}
