package syncer

import (
	"context"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog"
)

func deleteFromDownstream(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	// TODO: get UID of just-deleted object and pass it as a precondition on this delete.
	// This would avoid races where an object is deleted and another object with the same name is created immediately after.

	return c.GetClient(gvr, namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func upsertIntoDownstream(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured, labelsToAdd map[string]string) error {
	client := c.GetClient(gvr, namespace)

	unstrob = unstrob.DeepCopy()

	// Attempt to create the object; if the object already exists, update it.
	unstrob.SetUID("")
	unstrob.SetResourceVersion("")

	ownedByLabel := unstrob.GetLabels()["kcp.dev/owned-by"]
	var ownerReferences []metav1.OwnerReference
	for _, reference := range unstrob.GetOwnerReferences() {
		if reference.Name == ownedByLabel {
			continue
		}
		ownerReferences = append(ownerReferences, reference)
	}
	unstrob.SetOwnerReferences(ownerReferences)
	labels := unstrob.GetLabels()
	for name, value := range labelsToAdd {
		labels[name] = value
	}
	unstrob.SetLabels(labels)

	if _, err := client.Create(ctx, unstrob, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			klog.Errorf("Creating resource %s/%s: %v", namespace, unstrob.GetName(), err)
			return err
		}

		existing, err := client.Get(ctx, unstrob.GetName(), metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Getting resource %s/%s: %v", namespace, unstrob.GetName(), err)
			return err
		}
		klog.Infof("Object %s/%s already exists: update it", gvr.Resource, unstrob.GetName())

		unstrob.SetResourceVersion(existing.GetResourceVersion())
		if _, err := client.Update(ctx, unstrob, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Updating resource %s/%s: %v", namespace, unstrob.GetName(), err)
			return err
		}
		return nil
	}
	klog.Infof("Created object %s/%s", gvr.Resource, unstrob.GetName())
	return nil
}

func updateStatusInUpstream(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) (bool, error) {
	client := c.GetClient(gvr, namespace)

	unstrob = unstrob.DeepCopy()

	// Attempt to create the object; if the object already exists, update it.
	unstrob.SetUID("")
	unstrob.SetResourceVersion("")

	existing, err := client.Get(ctx, unstrob.GetName(), metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		klog.Errorf("Getting resource %s/%s: %v", namespace, unstrob.GetName(), err)
		return false, err
	}

	unstrob.SetResourceVersion(existing.GetResourceVersion())
	if _, err := client.UpdateStatus(ctx, unstrob, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Updating status of resource %s/%s: %v", namespace, unstrob.GetName(), err)
		return false, err
	}

	return false, nil
}

type identitySyncing struct {
	labelsToAdd map[string]string
}
var _ Syncing = identitySyncing{}

func NewIdentitySyncing(labelsToAdd map[string]string) Syncing {
	if labelsToAdd == nil {
		labelsToAdd = make(map[string]string)
	}
	return identitySyncing {
		labelsToAdd: labelsToAdd,
	}
}

func (is identitySyncing) UpsertIntoDownstream() UpsertFunc {
	return upsertIntoDownstream
}

func (is identitySyncing) DeleteFromDownstream() DeleteFunc {
	return deleteFromDownstream
}
func (is identitySyncing) UpdateStatusInUpstream() UpdateStatusFunc {
	return updateStatusInUpstream
}
func (is identitySyncing) LabelsToAdd() map[string]string {
	return is.labelsToAdd
}
