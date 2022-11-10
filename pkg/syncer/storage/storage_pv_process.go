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
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

func (c *PersistentVolumeController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	// Note "upstreamNamespace" will be empty since PersistentVolume is cluster-scoped
	clusterName, upstreamNamespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "Invalid key", key)
		return nil
	}

	logger = logger.WithValues(logging.WorkspaceKey, clusterName, logging.NameKey, name)

	upstreamPVObject, err := c.getUpstreamPersistentVolume(clusterName, name)
	if err != nil {
		return fmt.Errorf("failed to get upstream PersistentVolume: %w", err)
	}

	upstreamPV, ok := upstreamPVObject.(*corev1.PersistentVolume)
	if !ok {
		return fmt.Errorf("failed to assert object to a PersistentVolume: %T", upstreamPVObject)
	}

	resourceState, ok := upstreamPV.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+c.syncTarget.key]
	if ok && resourceState == string(workloadv1alpha1.ResourceStateUpsync) {
		desiredNSLocator := shared.NewNamespaceLocator(clusterName, c.syncTarget.workspace, c.syncTarget.uid, c.syncTarget.name, upstreamNamespace)
		downstreamPV, err := c.getDownstreamPersistentVolumeFromNamespaceLocator(desiredNSLocator)
		if apierrors.IsNotFound(err) {
			logger.V(4).Info("downstream persistent volume not found, ignoring key")
			return nil
		} else if err != nil {
			logger.Error(err, "failed to get downstream persistent volume")
			return nil
		}

		if downstreamPV == nil {
			logger.Info("downstream persistent volume is nil, ignoring key")
			return nil
		}

		downstreamPVCObject, err := c.getDownstreamPersistentVolumeClaim(downstreamPV.Spec.ClaimRef.Name, downstreamPV.Spec.ClaimRef.Namespace)
		if err != nil {
			return fmt.Errorf("failed to get downstream PersistentVolumeClaim: %w", err)
		}

		downstreamPVC, ok := downstreamPVCObject.(*corev1.PersistentVolumeClaim)
		if !ok {
			return fmt.Errorf("failed to assert object to a PersistentVolumeClaim: %T", downstreamPVCObject)
		}

		// Remove the internal.workload.kcp.dev/delaystatussyncing annotation
		removeAnnotationFromPersistentVolumeClaim(downstreamPVC)

		_, err = c.updateDownstreamPersistentVolumeClaim(ctx, downstreamPVC)
		if err != nil {
			return fmt.Errorf("failed to update downstream PersistentVolumeClaim: %w", err)
		}

		logger.V(1).Info("Removed", DelayStatusSyncing, "PersistentVolumeClaim", downstreamPVC.Name)
		return nil
	}

	return nil
}

func removeAnnotationFromPersistentVolumeClaim(downstreamPVC *corev1.PersistentVolumeClaim) {
	annotations := downstreamPVC.GetAnnotations()
	if annotations == nil {
		return
	}

	delete(annotations, DelayStatusSyncing)
	downstreamPVC.SetAnnotations(annotations)
}
