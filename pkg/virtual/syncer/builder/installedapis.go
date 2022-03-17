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
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
	apidefs "github.com/kcp-dev/kcp/pkg/virtual/syncer/dynamic/apidefs"
)

type createAPIDefinitionFunc func(workspaceName string, spec *apiresourcev1alpha1.CommonAPIResourceSpec) (apidefs.APIDefinition, error)

type installedAPIs struct {
	createAPIDefinition createAPIDefinitionFunc

	mutex   sync.RWMutex
	apiSets map[string]apidefs.APISet
}

func newInstalledAPIs(createAPIDefinition createAPIDefinitionFunc) *installedAPIs {
	return &installedAPIs{
		createAPIDefinition: createAPIDefinition,
		apiSets:             make(map[string]apidefs.APISet),
	}
}

func (apis *installedAPIs) addLocation(location syncer.WorkloadCluster) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	locationKey := location.Key()
	if _, exists := apis.apiSets[locationKey]; !exists {
		apis.apiSets[locationKey] = make(apidefs.APISet)
	}
}

func (apis *installedAPIs) removeLocation(location syncer.WorkloadCluster) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	locationKey := location.Key()
	delete(apis.apiSets, locationKey)
}

func (apis *installedAPIs) GetAPIs(apiDomainKey string) (apidefs.APISet, bool) {
	apis.mutex.RLock()
	defer apis.mutex.RUnlock()

	apiSet, ok := apis.apiSets[apiDomainKey]
	return apiSet, ok
}

func (apis *installedAPIs) Upsert(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	if locationAPIs, exists := apis.apiSets[api.Key()]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.LocationName, api.WorkspaceName)
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		if apiDefinition, err := apis.createAPIDefinition(api.WorkspaceName, api.Spec); err != nil {
			return err
		} else {
			locationAPIs[gvr] = apiDefinition
		}
	}
	return nil
}

func (apis *installedAPIs) Remove(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	if locationAPIs, exists := apis.apiSets[api.Key()]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.LocationName, api.WorkspaceName)
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		delete(locationAPIs, gvr)
	}
	return nil
}
