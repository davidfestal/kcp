package syncer

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

const resyncPeriod = 10 * time.Hour
const SyncerNamespaceKey = "SYNCER_NAMESPACE"

type UpsertFunc func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured, labelsToAdd map[string]string) error
type UpdateStatusFunc func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error
type DeleteFunc func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error

type Syncing interface {
	UpsertIntoDownstream() UpsertFunc
	DeleteFromDownstream() DeleteFunc
    UpdateStatusInUpstream() UpdateStatusFunc
	LabelsToAdd() map[string]string
}

type Syncer struct {
	specSyncer   *Controller
	statusSyncer *Controller
	Resources    sets.String
}

func (s *Syncer) Stop() {
	s.specSyncer.Stop()
	s.statusSyncer.Stop()
}

func (s *Syncer) WaitUntilDone() {
	<-s.specSyncer.Done()
	<-s.statusSyncer.Done()
}

var EnqueueAllButStatusUpdates HandlersProvider = func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.AddToQueue(gvr, obj) },
		UpdateFunc: func(oldObj, newObj interface{}) {
			if !deepEqualApartFromStatus(oldObj, newObj) {
				c.AddToQueue(gvr, newObj)
			}
		},
		DeleteFunc: func(obj interface{}) { c.AddToQueue(gvr, obj) },
	}
}

var EnqueueStatusUpdatesOnly HandlersProvider = func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			if !deepEqualStatus(oldObj, newObj) {
				c.AddToQueue(gvr, newObj)
			}
		},
	}
}

func StartSyncer(upstream, downstream *rest.Config, fromLabelSelector labels.Selector, syncing Syncing, resources sets.String, numSyncerThreads int) (*Syncer, error) {
	specSyncer, err := NewSyncerController(
		upstream, downstream,
		fromLabelSelector,
		ensuringNamespaceExists(syncing.UpsertIntoDownstream()), 
		syncing.DeleteFromDownstream(),
		syncing.LabelsToAdd(),
		EnqueueAllButStatusUpdates, 
		resources.List())
	if err != nil {
		return nil, err
	}

	statusSyncer, err := NewSyncerController(
		downstream, upstream,
		fromLabelSelector, 
		func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured, labelsToAdd map[string]string) error {
			return syncing.UpdateStatusInUpstream()(c, ctx, gvr, namespace, unstrob)
		}, 
		nil,
		nil,
		EnqueueStatusUpdatesOnly, 
		resources.List())
	if err != nil {
		specSyncer.Stop()
		return nil, err
	}
	specSyncer.Start(numSyncerThreads)
	statusSyncer.Start(numSyncerThreads)

	return &Syncer{
		specSyncer:   specSyncer,
		statusSyncer: statusSyncer,
		Resources:    resources,
	}, nil
}

func deepEqualApartFromStatus(oldObj, newObj interface{}) bool {
	oldUnstrob, isOldObjUnstructured := oldObj.(*unstructured.Unstructured)
	newUnstrob, isNewObjUnstructured := newObj.(*unstructured.Unstructured)
	if !isOldObjUnstructured || !isNewObjUnstructured {
		return false
	}
	if !equality.Semantic.DeepEqual(oldUnstrob.GetAnnotations(), newUnstrob.GetAnnotations()) {
		return false
	}
	if !equality.Semantic.DeepEqual(oldUnstrob.GetLabels(), newUnstrob.GetLabels()) {
		return false
	}

	oldObjKeys := sets.StringKeySet(oldUnstrob.UnstructuredContent())
	newObjKeys := sets.StringKeySet(newUnstrob.UnstructuredContent())
	for _, key := range oldObjKeys.Union(newObjKeys).UnsortedList() {
		if key == "metadata" || key == "status" {
			continue
		}
		if !equality.Semantic.DeepEqual(oldUnstrob.UnstructuredContent()[key], newUnstrob.UnstructuredContent()[key]) {
			return false
		}
	}
	return true
}

func ensuringNamespaceExists(upsert UpsertFunc) UpsertFunc {
	return func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured, labelsToAdd map[string]string) error {
		if upsert == nil {
			return nil
		}
		if err := c.ensureNamespaceExists(namespace); err != nil {
			klog.Error(err)
			return err
		}
		return upsert(c, ctx, gvr, namespace, unstrob, labelsToAdd)
	} 
}

// TODO:
// This function is there as a quick and dirty implementation of namespace creation.
// In fact We should also be getting notifications about namespaces created upstream and be creating downstream equivalents.
func (c *Controller) ensureNamespaceExists(namespace string) error {
	namespaces := c.toClient.Resource(schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	})
	newNamespace := &unstructured.Unstructured{}
	newNamespace.SetAPIVersion("v1")
	newNamespace.SetKind("Namespace")
	newNamespace.SetName(namespace)
	if _, err := namespaces.Create(context.TODO(), newNamespace, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			klog.Infof("Error while creating namespace %s: %v", namespace, err)
			return err
		}
	}
	return nil
}

func deepEqualStatus(oldObj, newObj interface{}) bool {
	oldUnstrob, isOldObjUnstructured := oldObj.(*unstructured.Unstructured)
	newUnstrob, isNewObjUnstructured := newObj.(*unstructured.Unstructured)
	if !isOldObjUnstructured || !isNewObjUnstructured || oldObj == nil || newObj == nil {
		return false
	}

	if newStatus, statusExists := newUnstrob.UnstructuredContent()["status"]; statusExists {
		oldStatus := oldUnstrob.UnstructuredContent()["status"]
		return equality.Semantic.DeepEqual(oldStatus, newStatus)
	}
	return false
}
