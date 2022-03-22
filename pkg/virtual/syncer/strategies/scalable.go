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

package strategies

import (
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
)

func ScalableSpreadStrategy(gvr schema.GroupVersionResource) SyncStrategy {
	return SyncStrategy{
		ReadFromKCP: func(locationName string, newKCPResource, existingLocationResource *unstructured.Unstructured, requestedSyncing Syncing) (newLocationResource *unstructured.Unstructured, err error) {
			replicas, exists, err := unstructured.NestedInt64(newKCPResource.UnstructuredContent(), "spec", "replicas")
			if err != nil {
				return nil, err
			}
			if !exists {
				return newKCPResource, nil
			}
			locations := sets.NewString()
			for locationName, locationSyncing := range requestedSyncing {
				if locationSyncing.Active() {
					locations.Insert(locationName)
				}
			}

			if len(locations) > 0 {
				replicasEach := replicas / int64(locations.Len())
				rest := replicas % int64(locations.Len())

				// For now we don't use the detail of the location placement,
				// and spread the replicas evenly.
				//
				// But we could think of using it along the following lines to calculate the replicas for each location:
				// - detail contains the percentage of replicas
				// - no detail == take the rest (after existing percentages are applied)
				// Detail could even be used to defined the behavior in the HPA use-case
				// (additional info for HPA strategy: increase total / diminish others)

				for index, location := range locations.List() {
					if location == locationName {
						replicasToSet := replicasEach
						if index == 0 {
							replicasToSet += rest
						}
						newLocationResource = newKCPResource.DeepCopy()
						_ = unstructured.SetNestedField(newLocationResource.UnstructuredContent(), replicasToSet, "spec", "replicas")

						if err := CarryOnLocationStatus(existingLocationResource, newLocationResource); err != nil {
							return nil, err
						}

						return newLocationResource, nil
					}
				}
			}

			return nil, errors.NewNotFound(gvr.GroupResource(), newKCPResource.GetName())
		},
		UpdateFromLocation: func(locationName string, newLocationresource *unstructured.Unstructured, existingKCPResource *unstructured.Unstructured, existingLocationResources map[string]unstructured.Unstructured, requestedPlacement Syncing) (newKCPResource *unstructured.Unstructured, err error) {
			newKCPResource = existingKCPResource.DeepCopy()

			locations := existingLocationResources
			if newLocationresource == nil {
				delete(locations, locationName)
			} else {
				locations[locationName] = *newLocationresource
			}
			consolidatedStatus := map[string]interface{}{}

			var totalReplicas int64
			var totalUpdatedReplicas int64
			var totalReadyReplicas int64
			var totalAvailableReplicas int64
			var totalUnavailableReplicas int64

			var consolidatedConditions []interface{}

			for _, locationResource := range locations {
				locationResource := locationResource
				status, exists, err := unstructured.NestedFieldCopy(locationResource.UnstructuredContent(), "status")
				if err != nil {
					return nil, err
				}
				if !exists {
					continue
				}
				statusMap, isMap := status.(map[string]interface{})
				if !isMap {
					continue
				}
				if replicas, exists, err := unstructured.NestedInt64(statusMap, "replicas"); exists && err == nil {
					totalReplicas += replicas
				}
				if updatedReplicas, exists, err := unstructured.NestedInt64(statusMap, "updatedReplicas"); exists && err == nil {
					totalUpdatedReplicas += updatedReplicas
				}
				if readyReplicas, exists, err := unstructured.NestedInt64(statusMap, "readyReplicas"); exists && err == nil {
					totalReadyReplicas += readyReplicas
				}
				if availableReplicas, exists, err := unstructured.NestedInt64(statusMap, "availableReplicas"); exists && err == nil {
					totalAvailableReplicas += availableReplicas
				}
				if unavailableReplicas, exists, err := unstructured.NestedInt64(statusMap, "unavailableReplicas"); exists && err == nil {
					totalUnavailableReplicas += unavailableReplicas
				}

				if conditions, exists, err := unstructured.NestedSlice(statusMap, "conditions"); exists && err == nil {
					if consolidatedConditions == nil {
						consolidatedConditions = conditions
					}
				}
			}
			_ = unstructured.SetNestedField(consolidatedStatus, totalReplicas, "replicas")
			_ = unstructured.SetNestedField(consolidatedStatus, totalUpdatedReplicas, "updatedReplicas")
			_ = unstructured.SetNestedField(consolidatedStatus, totalReadyReplicas, "readyReplicas")
			_ = unstructured.SetNestedField(consolidatedStatus, totalAvailableReplicas, "availableReplicas")
			_ = unstructured.SetNestedField(consolidatedStatus, totalUnavailableReplicas, "unavailableReplicas")
			_ = unstructured.SetNestedField(consolidatedStatus, consolidatedConditions, "conditions")

			_ = unstructured.SetNestedField(newKCPResource.UnstructuredContent(), consolidatedStatus, "status")
			return newKCPResource, nil
		},
	}
}
