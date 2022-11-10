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
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	// DelayStatusSyncing instructs delaying the update of the PVC status back to KCP
	DelayStatusSyncing = "internal.workload.kcp.dev/delaystatussyncing"
	upSyncDiff         = "internal.workload.kcp.dev/upsyncdiff"
)

func (c *PersistentVolumeClaimController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	logger = logger.WithValues(logging.NamespaceKey, namespace, logging.NameKey, name)

	// Get the downstream PersistentVolumeClaim object for inspection
	downstreamPVCObject, err := c.getDownstreamPersistentVolumeClaim(name, namespace)
	if err != nil {
		// If the downstream PVC is not found, then we can assume that it has been deleted and we do not need to process it
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get downstream PersistentVolumeClaim: %w", err)
	}

	downstreamPVC, ok := downstreamPVCObject.(*corev1.PersistentVolumeClaim)
	if !ok {
		return fmt.Errorf("failed to assert object to a PersistentVolumeClaim: %T", downstreamPVCObject)
	}

	// Copy the current downstream PVC object for comparison later
	old := downstreamPVC
	downstreamPVC = downstreamPVC.DeepCopy()

	logger.V(1).Info("processing downstream PersistentVolumeClaim")

	switch downstreamPVC.Status.Phase {
	case corev1.ClaimBound:
		// Fetch the namespace locator annotation from the downstream PVC, this will be helpful to
		// get the upstream PVC
		nsLocatorJSON, ok := downstreamPVC.GetAnnotations()[shared.NamespaceLocatorAnnotation]
		if !ok || nsLocatorJSON == "" {
			return fmt.Errorf("downstream PersistentVolumeClaim %q does not have the %q annotation", downstreamPVC.Name, shared.NamespaceLocatorAnnotation)
		}

		// Convert the string from the annotation into a NamespaceLocator object
		nsLocator := shared.NamespaceLocator{}
		if err := json.Unmarshal([]byte(nsLocatorJSON), &nsLocator); err != nil {
			return fmt.Errorf("failed to unmarshal namespace locator from downstream PersistentVolumeClaim %q: %w", downstreamPVC.Name, err)
		}

		// Get the PV corresponding to the PVC
		downstreamPersistentVolumeName := downstreamPVC.Spec.VolumeName
		downstreamPVObject, err := c.getDownstreamPersistentVolume(downstreamPersistentVolumeName)
		if err != nil {
			return fmt.Errorf("failed to get downstream PersistentVolume: %w", err)
		}

		downstreamPV, ok := downstreamPVObject.(*corev1.PersistentVolume)
		if !ok {
			return fmt.Errorf("failed to assert object to a PersistentVolume: %T", downstreamPVObject)
		}

		downstreamPV, err = addNsAllocatorToDownstreamPV(nsLocatorJSON, downstreamPV)
		if err != nil {
			return fmt.Errorf("failed to add namespace locator to downstream PersistentVolume: %w", err)
		}

		// Get upstream PVC from downstream PVC
		upstreamPVCObject, err := c.getUpstreamPersistentVolumeClaim(nsLocator.Workspace, downstreamPVC.Name, nsLocator.Namespace)
		if err != nil {
			return fmt.Errorf("failed to get upstream PersistentVolumeClaim: %w", err)
		}

		upstreamPVC, ok := upstreamPVCObject.(*corev1.PersistentVolumeClaim)
		if !ok {
			return fmt.Errorf("failed to assert object to a PersistentVolumeClaim: %T", upstreamPVCObject)
		}

		// Create a Json patch for the PV resource whose content would update the PVC reference to match the upstream PVC reference (namespace, uid, resource version, etc ...)
		upstreamClaimRef := &corev1.ObjectReference{
			Kind:            upstreamPVC.Kind,
			Namespace:       upstreamPVC.Namespace,
			Name:            upstreamPVC.Name,
			UID:             upstreamPVC.UID,
			APIVersion:      upstreamPVC.APIVersion,
			ResourceVersion: upstreamPVC.ResourceVersion,
		}

		if !equality.Semantic.DeepEqual(downstreamPV.Spec.ClaimRef, upstreamClaimRef) {
			claimRefJSONPatchAnnotation, err := claimRefJSONPatchAnnotation(downstreamPV.Spec.ClaimRef, upstreamClaimRef)
			if err != nil {
				return fmt.Errorf("failed to create claimRef JSON patch annotations: %w", err)
			}

			annotations := downstreamPV.GetAnnotations()
			annotations[upSyncDiff] = claimRefJSONPatchAnnotation
			downstreamPV.SetAnnotations(annotations)

		} else {
			logger.V(1).Info("downstream PersistentVolume claim ref is already up-to-date", "PersistentVolume", downstreamPV.Name)
		}

		// Update the PV with the Upsync requestState label for the current SyncTarget to trigger the Upsyncing of the PV to the upstream workspace
		downstreamPV.SetLabels(c.addResourceStateUpsyncLabel(downstreamPV.GetLabels()))

		logger.V(1).Info("updating downstream PersistentVolume with upstream PersistentVolumeClaim claim ref", "PersistentVolume", downstreamPV.Name)
		_, err = c.updateDownstreamPersistentVolume(ctx, downstreamPV)
		if err != nil {
			return fmt.Errorf("failed to update PersistentVolume with %q annotation: %w", upSyncDiff, err)
		}

		logger.V(1).Info("successfully updated PersistentVolume claim ref with upstream PersistentVolumeClaim", "PersistentVolume", downstreamPV.Name, "PersistentVolumeClaim", upstreamPVC.Name)

	case corev1.ClaimPending:
		_, ok = downstreamPVC.GetAnnotations()[DelayStatusSyncing]
		if !ok {
			annotations := downstreamPVC.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[DelayStatusSyncing] = "true"
			downstreamPVC.SetAnnotations(annotations)

			oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
			newResource := &Resource{ObjectMeta: downstreamPVC.ObjectMeta, Spec: &downstreamPVC.Spec, Status: &downstreamPVC.Status}

			// If the object being reconciled changed as a result, update it.
			err := c.commit(ctx, oldResource, newResource, downstreamPVC.Namespace)
			if err != nil {
				return fmt.Errorf("failed to commit PVC: %w", err)
			}

			logger.V(1).Info("downstream PersistentVolumeClaim annotated to delay status syncing")
		}
		return nil

	case corev1.ClaimLost:
		logger.V(6).Info("LostPVC", downstreamPVC.Name)
		return nil
	}

	return nil

}

func rmKeyFromJSON(jsonBlob, key string) (string, error) {
	blobToByte := []byte(jsonBlob)
	var p map[string]interface{}

	err := json.Unmarshal(blobToByte, &p)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal blob %q: %w", jsonBlob, err)
	}
	delete(p, key)

	blobToByte, err = json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("failed to marshal blob %q: %w", jsonBlob, err)
	}

	return string(blobToByte), nil
}

func addNsAllocatorToDownstreamPV(nsLocatorJSON string, downstreamPV *corev1.PersistentVolume) (*corev1.PersistentVolume, error) {
	// Remove the "namespace" key from the namespace locator JSON
	var err error
	nsLocatorJSON, err = rmKeyFromJSON(nsLocatorJSON, "namespace")
	if err != nil {
		return nil, fmt.Errorf("failed to remove namespace key from namespace locator: %w", err)
	}

	// Update the PV with the namespace locator annotation
	annotations := downstreamPV.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[shared.NamespaceLocatorAnnotation] = nsLocatorJSON
	downstreamPV.SetAnnotations(annotations)

	return downstreamPV, nil
}

func claimRefJSONPatchAnnotation(downstreamClaimRef, upstreamClaimRef *corev1.ObjectReference) (string, error) {
	oldClaimRef, err := json.Marshal(downstreamClaimRef)
	if err != nil {
		return "", fmt.Errorf("failed to marshal old claim ref for downstream pv: %w", err)
	}

	newClaimRef, err := json.Marshal(upstreamClaimRef)
	if err != nil {
		return "", fmt.Errorf("failed to marshal new claim ref for downstream pv: %w", err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldClaimRef, newClaimRef)
	if err != nil {
		return "", fmt.Errorf("failed to create json patch for downstream pv: %w", err)
	}

	return string(patchBytes), nil
}

func (c *PersistentVolumeClaimController) addResourceStateUpsyncLabel(labels map[string]string) map[string]string {
	if labels == nil {
		labels = map[string]string{}
	}
	labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+c.syncTarget.key] = string(workloadv1alpha1.ResourceStateUpsync)
	return labels
}
