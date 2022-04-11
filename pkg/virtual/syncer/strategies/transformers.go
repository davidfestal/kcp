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
	"context"
	"encoding/json"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/transforming"
)

const (
	locationViewAnnotationPrefix string = "location.diff.syncer.internal.kcp.dev/"
)

func locationViewAnnotation(locationName string) string {
	return locationViewAnnotationPrefix + locationName
}
func locationViewTransformer(gvr schema.GroupVersionResource, syncStrategy SyncStrategy) transforming.Transformer {
	return transforming.TransformsResource(
		"LocationViewTransformer",

		// Update from a syncer location object (status) to the virtual workspace resource
		func(client dynamic.ResourceInterface, ctx context.Context, newLocationObject *unstructured.Unstructured, subresources ...string) (newKCPViewObject *unstructured.Unstructured, err error) {
			workloadCluster, err := syncer.FromContext(ctx)
			if err != nil {
				return nil, err
			}

			numberOfFinalizers := len(newLocationObject.GetFinalizers())
			removeFromLocation := newLocationObject.GetDeletionTimestamp() != nil && numberOfFinalizers == 0

			if bytes, err := yaml.Marshal(newLocationObject.UnstructuredContent()); err == nil {
				klog.V(5).Infof("Before LocationViewTransformer - Location Resource:\n%s", string(bytes))
			}

			newLocationObjectJson, err := json.Marshal(newLocationObject.UnstructuredContent())
			if err != nil {
				return nil, err
			}

			oldKCPViewObject, err := client.Get(ctx, newLocationObject.GetName(), metav1.GetOptions{ResourceVersion: newLocationObject.GetResourceVersion()})
			if err != nil {
				return nil, err
			}

			oldLocations, err := GetLocations(oldKCPViewObject)
			if err != nil {
				return nil, err
			}

			if removeFromLocation {
				newLocationObject = nil
			}

			syncing := GetSyncing(oldKCPViewObject)

			newKCPViewObject, err = syncStrategy.UpdateFromLocation(workloadCluster.LocationName, newLocationObject, oldKCPViewObject, oldLocations, syncing)
			if err != nil {
				return nil, err
			}

			newKCPViewAnnotations := newKCPViewObject.GetAnnotations()
			if newKCPViewAnnotations == nil {
				newKCPViewAnnotations = make(map[string]string)
			}

			newKCPViewObjectJson, err := newKCPViewObject.MarshalJSON()
			if err != nil {
				return nil, err
			}

			if removeFromLocation {
				delete(oldLocations, workloadCluster.LocationName)
			} else {
				oldLocations[workloadCluster.LocationName] = *newLocationObject
			}

			newSyncing := GetSyncing(newKCPViewObject)

			// Update location diff for all location based on the new KCP View object
			for locationName, location := range oldLocations {
				if locationSyncing := newSyncing[locationName]; !locationSyncing.Active() {
					delete(newKCPViewAnnotations, locationViewAnnotation(locationName))
					continue
				}

				var locationObjectJson []byte
				if locationName == workloadCluster.LocationName {
					locationObjectJson = newLocationObjectJson
				} else {
					locationObjectJson, err = location.MarshalJSON()
					if err != nil {
						return nil, err
					}
				}
				locationViewDiff, err := jsonpatch.CreateMergePatch(newKCPViewObjectJson, locationObjectJson)
				if err != nil {
					return nil, err
				}
				newKCPViewAnnotations[locationViewAnnotation(locationName)] = string(locationViewDiff)
			}
			if removeFromLocation {
				delete(newKCPViewAnnotations, locationViewAnnotation(workloadCluster.LocationName))
			}
			newKCPViewObject.SetAnnotations(newKCPViewAnnotations)

			if bytes, err := yaml.Marshal(newKCPViewObject.UnstructuredContent()); err == nil {
				klog.V(5).Infof("Before - New KCP Resource:\n%s", string(bytes))
			}

			return newKCPViewObject, nil
		},
		func(client dynamic.ResourceInterface, ctx context.Context, returnedKCPResource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformedLocationResource *unstructured.Unstructured, err error) {
			workloadCluster, err := syncer.FromContext(ctx)
			if err != nil {
				return nil, err
			}

			syncing := GetSyncing(returnedKCPResource)
			klog.Infof("After LocationViewTransformer - KCP Object Syncing : %#v", syncing)

			if bytes, err := yaml.Marshal(returnedKCPResource.UnstructuredContent()); err == nil {
				klog.V(5).Infof("After - KCP Resource:\n%s", string(bytes))
			}

			kcpObjectJson, err := json.Marshal(returnedKCPResource.UnstructuredContent())
			if err != nil {
				return nil, err
			}

			var oldLocationViewObject *unstructured.Unstructured
			if filteredOldLocations, err := GetLocations(returnedKCPResource, workloadCluster.LocationName); err != nil {
				return nil, err
			} else {
				if location, exists := filteredOldLocations[workloadCluster.LocationName]; exists {
					oldLocationViewObject = &location
				}
			}

			transformedLocationResource, err = syncStrategy.ReadFromKCP(workloadCluster.LocationName, returnedKCPResource, oldLocationViewObject, syncing)
			if err != nil {
				return nil, err
			}

			finalizers := sets.NewString(transformedLocationResource.GetFinalizers()...)
			finalizers.Insert(syncer.SyncerFinalizer)
			transformedLocationResource.SetFinalizers(finalizers.List())

			if deletionTimestamp := syncing[workloadCluster.LocationName].RequiredDeletion(); deletionTimestamp != nil {
				transformedLocationResource.SetDeletionTimestamp(deletionTimestamp)
			}

			// Remove the locationView diff annotation from the locationView resource

			locationResourceAnnotations := transformedLocationResource.GetAnnotations()
			for name := range locationResourceAnnotations {
				if strings.HasPrefix(name, locationViewAnnotationPrefix) {
					delete(locationResourceAnnotations, name)
				}
			}
			transformedLocationResource.SetAnnotations(locationResourceAnnotations)

			// Add the reverted diff annotation to the locationView resource

			transformedJson, err := json.Marshal(transformedLocationResource.UnstructuredContent())
			if err != nil {
				return nil, err
			}

			locationViewDiff, err := jsonpatch.CreateMergePatch(kcpObjectJson, transformedJson)
			if err != nil {
				return nil, err
			}

			kcpAnnotations := returnedKCPResource.GetAnnotations()
			if kcpAnnotations == nil {
				kcpAnnotations = make(map[string]string)
			}

			kcpAnnotations[locationViewAnnotation(workloadCluster.LocationName)] = string(locationViewDiff)
			returnedKCPResource.SetAnnotations(kcpAnnotations)
			locationAnnotations := transformedLocationResource.GetAnnotations()
			for name := range locationAnnotations {
				if strings.HasPrefix(name, locationViewAnnotationPrefix) {
					delete(locationAnnotations, name)
				}
			}
			transformedLocationResource.SetAnnotations(locationAnnotations)

			// Finished adding the reverted diff annotation to the locationView resource

			if bytes, err := yaml.Marshal(transformedLocationResource.UnstructuredContent()); err == nil {
				klog.V(5).Infof("After - Transformed Resource:\n%s", string(bytes))
			}
			return transformedLocationResource, nil
		},
	)
}

func cleanupLocation(gvr schema.GroupVersionResource) transforming.Transformer {
	return transforming.Transformer{
		Name: "CleanupLocation",
		AfterUpdate: func(client dynamic.ResourceInterface, ctx context.Context, updatedKCPObject *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			workloadCluster, err := syncer.FromContext(ctx)
			if err != nil {
				return nil, err
			}

			locations, err := GetLocations(updatedKCPObject)
			if err != nil {
				return nil, err
			}
			if _, exists := locations[workloadCluster.LocationName]; !exists {
				klog.Infof("Updating syncer annotations and labels for resource %q (%q) after removal from workload cluster %q", updatedKCPObject.GetName(), gvr.String(), workloadCluster.LocationName)
				if updatedKCPViewAnnotations := updatedKCPObject.GetAnnotations(); updatedKCPViewAnnotations != nil {
					delete(updatedKCPViewAnnotations, LocationStrategyAnnotationName(workloadCluster.LocationName))
					delete(updatedKCPViewAnnotations, LocationDeletionAnnotationName(workloadCluster.LocationName))
					updatedKCPObject.SetAnnotations(updatedKCPViewAnnotations)
				}
				if labels := updatedKCPObject.GetLabels(); labels != nil {
					delete(labels, syncer.LocationClusterLabelName(workloadCluster.LocationName))
					updatedKCPObject.SetLabels(labels)
				}
				if updated, err := client.Update(ctx, updatedKCPObject, metav1.UpdateOptions{}); err != nil {
					return nil, err
				} else {
					return updated, nil
				}
			}
			return updatedKCPObject, nil
		},
	}
}

func StrategyTransformers(gvr schema.GroupVersionResource, syncStrategy SyncStrategy) []transforming.Transformer {
	return []transforming.Transformer{
		cleanupLocation(gvr), // cleanup location-related labels and annotations after the UpdateStatus when the location has been deleted on the syncer side
		locationViewTransformer(gvr, syncStrategy),
	}
}
