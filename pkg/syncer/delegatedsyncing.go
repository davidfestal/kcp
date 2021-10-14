package syncer

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type DelegateSyncing interface {
	Syncing
	Delegate(gvr schema.GroupVersionResource, delegateTo Syncing)
}

type delegatedSyncing struct {
	delegates map[schema.GroupVersionResource]Syncing
	defaultSyncing Syncing
}

func NewDelegateSyncing(defaultSyncing Syncing) DelegateSyncing {
	return delegatedSyncing{
		delegates: make(map[schema.GroupVersionResource]Syncing),
		defaultSyncing: defaultSyncing,
	}
}

var _ DelegateSyncing = delegatedSyncing{}

func (ds delegatedSyncing) pickSyncing(gvr schema.GroupVersionResource) Syncing {
	if delegate, exists := ds.delegates[gvr]; exists {
		return delegate
	}
	return ds.defaultSyncing
}

func (ds delegatedSyncing) UpsertIntoDownstream() UpsertFunc {
	return func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured, labelsToAdd map[string]string) error {		
		return ds.pickSyncing(gvr).UpsertIntoDownstream()(c, ctx, gvr, namespace, unstrob, labelsToAdd)
	}
}

func (ds delegatedSyncing) DeleteFromDownstream() DeleteFunc {
	return func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {		
		return ds.pickSyncing(gvr).DeleteFromDownstream()(c, ctx, gvr, namespace, name)
	}
}
func (ds delegatedSyncing) UpdateStatusInUpstream() UpdateStatusFunc {
	return func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) (notFound bool, err error) {		
		return ds.pickSyncing(gvr).UpdateStatusInUpstream()(c, ctx, gvr, namespace, unstrob)
	}
}
func (ds delegatedSyncing) LabelsToAdd() map[string]string {
	return ds.defaultSyncing.LabelsToAdd()
}
func (ds delegatedSyncing) Delegate(gvr schema.GroupVersionResource, delegateTo Syncing) {
	ds.delegates[gvr] = delegateTo
}

