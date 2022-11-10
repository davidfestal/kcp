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

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	persistentVolumeControllerName = "kcp-workload-syncer-storage-pv"
	byNamespaceLocatorIndexName    = "syncer-persistent-volume-ByNamespaceLocator"
)

type PersistentVolumeController struct {
	queue                                             workqueue.RateLimitingInterface
	ddsifForUpstreamUpsyncer                          *ddsif.DiscoveringDynamicSharedInformerFactory
	ddsifForDownstream                                *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer]
	upstreamClient                                    kcpdynamic.ClusterInterface
	downstreamClient                                  dynamic.Interface
	syncTarget                                        syncTargetSpec
	updateDownstreamPersistentVolumeClaim             func(ctx context.Context, persistentVolumeClaim *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error)
	getDownstreamPersistentVolumeFromNamespaceLocator func(namespaceLocator shared.NamespaceLocator) (*corev1.PersistentVolume, error)
	getDownstreamPersistentVolumeClaim                func(persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error)
	getUpstreamPersistentVolume                       func(clusterName logicalcluster.Name, persistentVolumeName string) (runtime.Object, error)
}

// NewPersistentVolumeSyncer returns a new storage persistent volume syncer controller.
func NewPersistentVolumeSyncer(
	syncerLogger logr.Logger,
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	upstreamClient kcpdynamic.ClusterInterface,
	downstreamClient dynamic.Interface,
	downstreamKubeClient *kubernetes.Clientset,
	ddsifForUpstreamUpsyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	syncedInformers map[schema.GroupVersionResource]cache.SharedIndexInformer,
	syncTargetUID types.UID,
) (*PersistentVolumeController, error) {

	c := &PersistentVolumeController{
		queue:                    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), persistentVolumeControllerName),
		downstreamClient:         downstreamClient,
		ddsifForUpstreamUpsyncer: ddsifForUpstreamUpsyncer,
		ddsifForDownstream:       ddsifForDownstream,
		upstreamClient:           upstreamClient,
		syncTarget: syncTargetSpec{
			name:      syncTargetName,
			workspace: syncTargetWorkspace,
			uid:       syncTargetUID,
			key:       syncTargetKey,
		},
		updateDownstreamPersistentVolumeClaim: func(ctx context.Context, persistentVolumeClaim *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
			return downstreamKubeClient.CoreV1().PersistentVolumeClaims(persistentVolumeClaim.Namespace).Update(ctx, persistentVolumeClaim, metav1.UpdateOptions{})
		},
		getDownstreamPersistentVolumeClaim: func(persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error) {
			lister, known, synced := ddsifForDownstream.Lister(persistentVolumeClaimSchemeGroupVersion)
			if !known || !synced {
				return nil, errors.New("informer should be up and synced for persistentvolumeclaims in the downstream syncer informer factory")
			}
			return lister.ByNamespace(persistentVolumeClaimNamespace).Get(persistentVolumeClaimName)
		},
		getUpstreamPersistentVolume: func(clusterName logicalcluster.Name, persistentVolumeName string) (runtime.Object, error) {
			lister, known, synced := ddsifForUpstreamUpsyncer.Lister(persistentVolumeSchemeGroupVersion)
			if !known || !synced {
				return nil, errors.New("informer should be up and synced for persistentvolumes in the upstream upsyncer informer factory")
			}
			return lister.ByCluster(clusterName).Get(persistentVolumeName)
		},
		getDownstreamPersistentVolumeFromNamespaceLocator: func(namespaceLocator shared.NamespaceLocator) (*corev1.PersistentVolume, error) {
			namespaceLocatorJSONBytes, err := json.Marshal(namespaceLocator)
			if err != nil {
				return nil, err
			}

			informer, known, synced := ddsifForDownstream.Informer(persistentVolumeSchemeGroupVersion)
			if !known || !synced {
				return nil, errors.New("informer should be up and synced for persistentvolumes in the downstream syncer informer factory")
			}

			indexer := informer.GetIndexer()
			persistentVolumes, err := indexers.ByIndex[*corev1.PersistentVolume](indexer, byNamespaceLocatorIndexName, string(namespaceLocatorJSONBytes))
			if err != nil {
				return nil, err
			}

			if len(persistentVolumes) == 0 {
				return nil, fmt.Errorf("no persistent volume found for namespace locator %v", namespaceLocator)
			}

			if len(persistentVolumes) > 1 {
				return nil, fmt.Errorf("found multiple persistent volumes with namespace locator %v", namespaceLocator)
			}

			return persistentVolumes[0], nil
		},
	}

	logger := logging.WithReconciler(syncerLogger, persistentVolumeControllerName)

	// Add the PV informers to the controller to react to upstream PV events.
	logger.V(2).Info("Setting upstream up informer", persistentVolumeSchemeGroupVersion.String())
	pvInformer, ok := syncedInformers[persistentVolumeSchemeGroupVersion]
	if !ok {
		return nil, errors.New("persistentvolumes informer should be available")
	}

	pvInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.AddToQueue(newObj, logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
	})

	return c, nil
}

func (c *PersistentVolumeController) AddToQueue(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing", "key", key)
	c.queue.Add(key)
}

// Start starts N worker processes processing work items.
func (c *PersistentVolumeController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), persistentVolumeControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting syncer workers")
	defer logger.Info("Stopping syncer workers")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *PersistentVolumeController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *PersistentVolumeController) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	qk := key.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), qk)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing", qk)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", persistentVolumeControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
