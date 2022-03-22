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
	"strings"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
)

func LocationStrategyAnnotationName(locationName string) string {
	return "transformation.workloads.kcp.dev/" + locationName
}

func LocationDeletionAnnotationName(locationName string) string {
	return "deletion.internal.workloads.kcp.dev/" + locationName
}

func LocationFinalizersAnnotationName(locationName string) string {
	return "finalizers.workloads.kcp.dev/" + locationName
}

func GetSyncing(kcpResource metav1.Object) Syncing {
	locationClusterlabelPrefix := syncer.LocationClusterLabelName("")
	labels := kcpResource.GetLabels()
	syncing := make(Syncing, len(labels))
	annotations := kcpResource.GetAnnotations()
	for labelName, labelValue := range labels {
		if strings.HasPrefix(labelName, locationClusterlabelPrefix) {
			locationName := strings.TrimPrefix(labelName, locationClusterlabelPrefix)
			locationSyncing := WorkloadClusterSyncing{
				RequestedState: RequestedSyncingState(labelValue),
				Detail:         annotations[LocationStrategyAnnotationName(locationName)],
			}
			if deletionAnnotation, exists := annotations[LocationDeletionAnnotationName(locationName)]; exists {
				var deletionTimestamp metav1.Time
				if err := deletionTimestamp.UnmarshalText([]byte(deletionAnnotation)); err != nil {
					klog.Errorf("Parsing of the deletion annotation for location %q failed: %v", locationName, err)
				} else {
					locationSyncing.DeletionTimestamp = &deletionTimestamp
				}
			}
			if finalizersAnnotation, exists := annotations[LocationFinalizersAnnotationName(locationName)]; exists {
				locationSyncing.Finalizers = finalizersAnnotation
			}
			syncing[locationName] = locationSyncing
		}
	}
	return syncing
}

func CarryOnLocationStatus(existingLocationResource, newLocationResource *unstructured.Unstructured) error {
	if existingLocationResource != nil {
		// Set the status to the location view (last set status for this location)
		if status, exists, err := unstructured.NestedFieldCopy(existingLocationResource.UnstructuredContent(), "status"); err != nil {
			return err
		} else if exists {
			if err := unstructured.SetNestedField(newLocationResource.UnstructuredContent(), status, "status"); err != nil {
				return err
			}
		} else {
			unstructured.RemoveNestedField(newLocationResource.UnstructuredContent(), "status")
		}
	}
	return nil
}

func GetLocations(kcpResource *unstructured.Unstructured, locationNames ...string) (map[string]unstructured.Unstructured, error) {
	kcpResource = kcpResource.DeepCopy()

	annotations := kcpResource.GetAnnotations()

	filterLocations := len(locationNames) > 0
	locationsToKeep := sets.NewString(locationNames...)

	locationDiffs := make(map[string]string)
	for name, value := range annotations {
		if strings.HasPrefix(name, locationViewAnnotationPrefix) {
			locationName := strings.TrimPrefix(name, locationViewAnnotationPrefix)
			if filterLocations && !locationsToKeep.Has(locationName) {
				continue
			}
			locationDiffs[locationName] = value
			delete(annotations, name)
		}
	}
	kcpResource.SetAnnotations(annotations)
	kcpResourceJson, err := kcpResource.MarshalJSON()
	if err != nil {
		return nil, err
	}

	locations := make(map[string]unstructured.Unstructured)
	for locationName, locationDiff := range locationDiffs {
		locationResourceJson, err := jsonpatch.MergePatch([]byte(kcpResourceJson), []byte(locationDiff))
		if err != nil {
			return nil, err
		}

		var locationResource unstructured.Unstructured
		if err := locationResource.UnmarshalJSON([]byte(locationResourceJson)); err != nil {
			return nil, err
		}
		locations[locationName] = locationResource
	}

	return locations, nil
}
