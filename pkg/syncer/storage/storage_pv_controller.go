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
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	PersistentVolumeControllerName = "kcp-workload-syncer-storage-pv"
)

type PersistentVolumeController struct {
	queue                                 workqueue.RateLimitingInterface
	ddsifForUpstreamUpsyncer              *ddsif.DiscoveringDynamicSharedInformerFactory
	ddsifForDownstream                    *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer]
	syncTarget                            syncTargetSpec
	updateDownstreamPersistentVolumeClaim func(ctx context.Context, persistentVolumeClaim *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error)
	getDownstreamPersistentVolume         func(ctx context.Context, name string) (runtime.Object, error)
	getDownstreamPersistentVolumeClaim    func(persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error)
	getUpstreamPersistentVolume           func(clusterName logicalcluster.Name, persistentVolumeName string) (runtime.Object, error)
}

// NewPersistentVolumeSyncer returns a new storage persistent volume syncer controller.
func NewPersistentVolumeSyncer(
	syncerLogger logr.Logger,
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	downstreamKubeClient *kubernetes.Clientset,
	ddsifForUpstreamUpsyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	syncedInformers map[schema.GroupVersionResource]cache.SharedIndexInformer,
	syncTargetUID types.UID,
) (*PersistentVolumeController, error) {

	c := &PersistentVolumeController{
		queue:                    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), PersistentVolumeControllerName),
		ddsifForUpstreamUpsyncer: ddsifForUpstreamUpsyncer,
		ddsifForDownstream:       ddsifForDownstream,
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
		getDownstreamPersistentVolume: func(ctx context.Context, name string) (runtime.Object, error) {
			lister, known, synced := ddsifForDownstream.Lister(persistentVolumeSchemeGroupVersion)
			if !known || !synced {
				return nil, errors.New("informer should be up and synced for persistentvolumes in the downstream syncer informer factory")
			}

			return lister.Get(name)
		},
	}

	logger := logging.WithReconciler(syncerLogger, PersistentVolumeControllerName)

	// Add the PV informers to the controller to react to upstream PV events.
	logger.V(2).Info("Setting upstream up informer", persistentVolumeSchemeGroupVersion.String())
	pvInformer, ok := syncedInformers[persistentVolumeSchemeGroupVersion]
	if !ok {
		return nil, fmt.Errorf("informer for %s not found", persistentVolumeClaimSchemeGroupVersion.String())
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

	logger := logging.WithReconciler(klog.FromContext(ctx), PersistentVolumeControllerName)
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
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", PersistentVolumeControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
