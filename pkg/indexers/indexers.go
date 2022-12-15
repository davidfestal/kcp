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

package indexers

import (
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	syncershared "github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	// BySyncerFinalizerKey is the name for the index that indexes by syncer finalizer label keys.
	BySyncerFinalizerKey = "bySyncerFinalizerKey"
	// APIBindingByClusterAndAcceptedClaimedGroupResources is the name for the index that indexes an APIBinding by its
	// cluster name and accepted claimed group resources.
	APIBindingByClusterAndAcceptedClaimedGroupResources = "byClusterAndAcceptedClaimedGroupResources"
	// ByClusterResourceStateLabelKey indexes resources based on the cluster state label key.
	ByClusterResourceStateLabelKey = "ByClusterResourceStateLabelKey"
	// NamespaceLocatorIndexName is the name of the index that allows you to filter by namespace locator
	NamespaceLocatorIndexName = "ns-locator"
)

// IndexBySyncerFinalizerKey indexes by syncer finalizer label keys.
func IndexBySyncerFinalizerKey(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	syncerFinalizers := []string{}
	for _, f := range metaObj.GetFinalizers() {
		if strings.HasPrefix(f, syncershared.SyncerFinalizerNamePrefix) {
			syncerFinalizers = append(syncerFinalizers, f)
		}
	}

	return syncerFinalizers, nil
}

// IndexByClusterResourceStateLabelKey indexes resources based on the cluster state key label.
func IndexByClusterResourceStateLabelKey(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	ClusterResourceStateLabelKeys := []string{}
	for k := range metaObj.GetLabels() {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			ClusterResourceStateLabelKeys = append(ClusterResourceStateLabelKeys, k)
		}
	}
	return ClusterResourceStateLabelKeys, nil
}

// ByIndex returns all instances of T that match indexValue in indexName in indexer.
func ByIndex[T runtime.Object](indexer cache.Indexer, indexName, indexValue string) ([]T, error) {
	list, err := indexer.ByIndex(indexName, indexValue)
	if err != nil {
		return nil, err
	}

	var ret []T
	for _, o := range list {
		ret = append(ret, o.(T))
	}

	return ret, nil
}

// IndexByNamespaceLocator is a cache.IndexFunc that indexes namespaces by the namespaceLocator annotation.
func IndexByNamespaceLocator(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	if loc, found, err := shared.LocatorFromAnnotations(metaObj.GetAnnotations()); err != nil {
		return nil, fmt.Errorf("failed to get locator from annotations: %w", err)
	} else if !found {
		return nil, nil
	} else {
		nsLocByte, err := json.Marshal(loc)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal locator %#v: %w", loc, err)
		}
		return []string{NamespaceLocatorIndexKey(nsLocByte)}, nil
	}
}

// NamespaceLocatorIndexKey formats the index key for a namespace locator
func NamespaceLocatorIndexKey(namespaceLocator []byte) string {
	return string(namespaceLocator)
}
