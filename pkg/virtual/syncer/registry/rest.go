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

package registry

import (
	"context"
	"errors"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"

	syncer "github.com/kcp-dev/kcp/pkg/virtual/syncer"
)

const (
	OrganizationScope string = "organization"
	PersonalScope     string = "personal"
	PrettyNameLabel   string = "workspaces.kcp.dev/pretty-name"
	InternalNameLabel string = "workspaces.kcp.dev/internal-name"
	PrettyNameIndex   string = "workspace-pretty-name"
	InternalNameIndex string = "workspace-internal-name"
)

type LocationKeyContextKeyType string

const LocationKeyContextKey LocationKeyContextKeyType = "VirtualWorkspaceSyncerLocationKey"

type REST struct {
	// createStrategy allows extended behavior during creation, required
	createStrategy rest.RESTCreateStrategy
	// updateStrategy allows extended behavior during updates, required
	updateStrategy rest.RESTUpdateStrategy
	// resetFieldsStrategy provides the fields reset by the strategy that
	// should not be modified by the user.
	resetFieldsStrategy rest.ResetFieldsStrategy

	rest.TableConvertor

	resource schema.GroupVersionResource
	kind     schema.GroupVersionKind
	listKind schema.GroupVersionKind

	clientGetter syncer.ClientGetter
	subResources []string
}

var _ rest.Lister = &REST{}
var _ rest.Watcher = &REST{}
var _ rest.Getter = &REST{}
var _ rest.Updater = &REST{}

// NewREST returns a RESTStorage object that will work against Workspace resources
func NewREST(resource schema.GroupVersionResource, kind, listKind schema.GroupVersionKind, strategy strategy, tableConvertor rest.TableConvertor, dynamicClientGetter syncer.ClientGetter) (*REST, *StatusREST) {
	mainREST := &REST{
		createStrategy:      strategy,
		updateStrategy:      strategy,
		resetFieldsStrategy: strategy,

		TableConvertor: tableConvertor,

		resource: resource,
		kind:     kind,
		listKind: listKind,

		clientGetter: dynamicClientGetter,
	}
	statusMainREST := *mainREST
	statusMainREST.subResources = []string{"status"}
	statusMainREST.updateStrategy = NewStatusStrategy(strategy)
	return mainREST,
		&StatusREST{
			mainREST: &statusMainREST,
		}
}

func (s *REST) GetClientResource(ctx context.Context) (dynamic.ResourceInterface, error) {
	client, err := s.clientGetter.GetDynamicClient(ctx)
	if err != nil {
		return nil, err
	}

	if s.createStrategy.NamespaceScoped() {
		if namespace, ok := genericapirequest.NamespaceFrom(ctx); ok {
			return client.Resource(s.resource).Namespace(namespace), nil
		} else {
			return nil, errors.New("Strange: there should be a Namespace context")
		}
	} else {
		return client.Resource(s.resource), nil
	}
}

// New returns a new Workspace
func (s *REST) New() runtime.Object {
	// set the expected group/version/kind in the new object as a signal to the versioning decoder
	ret := &unstructured.Unstructured{}
	ret.SetGroupVersionKind(s.kind)
	return ret
}

// NewList returns a new WorkspaceList
func (s *REST) NewList() runtime.Object {
	// lists are never stored, only manufactured, so stomp in the right kind
	ret := &unstructured.UnstructuredList{}
	ret.SetGroupVersionKind(s.listKind)
	return ret
}

// List retrieves a list of Workspaces that match label.
func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	var v1ListOptions metav1.ListOptions
	if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}
	delegate, err := s.GetClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.List(ctx, v1ListOptions)
}

func (s *REST) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	var v1ListOptions metav1.ListOptions
	if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}
	delegate, err := s.GetClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.Watch(ctx, v1ListOptions)
}

// Get retrieves a Workspace by name
func (s *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	delegate, err := s.GetClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.Get(ctx, name, *options, s.subResources...)
}

func (s *REST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	delegate, err := s.GetClientResource(ctx)
	if err != nil {
		return nil, false, err
	}

	// TODO add the todo
	oldObj, err := s.Get(ctx, name, &metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}

	obj, err := objInfo.UpdatedObject(ctx, oldObj)
	if err != nil {
		return nil, false, err
	}

	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, false, fmt.Errorf("not an Unstructured: %#v", obj)
	}

	s.updateStrategy.PrepareForUpdate(ctx, obj, oldObj)
	if errs := s.updateStrategy.ValidateUpdate(ctx, obj, oldObj); len(errs) > 0 {
		return nil, false, kerrors.NewInvalid(unstructuredObj.GroupVersionKind().GroupKind(), unstructuredObj.GetName(), errs)
	}
	if err := updateValidation(ctx, obj.DeepCopyObject(), oldObj.DeepCopyObject()); err != nil {
		return nil, false, err
	}

	result, err := delegate.Update(ctx, unstructuredObj, *options, s.subResources...)
	return result, false, err
}

// GetResetFields implements rest.ResetFieldsStrategy
func (s *REST) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	if s.resetFieldsStrategy == nil {
		return nil
	}
	return s.resetFieldsStrategy.GetResetFields()
}

func shallowCopyObjectMeta(u runtime.Unstructured) {
	obj := shallowMapDeepCopy(u.UnstructuredContent())
	if metadata, ok := obj["metadata"]; ok {
		if metadata, ok := metadata.(map[string]interface{}); ok {
			obj["metadata"] = shallowMapDeepCopy(metadata)
			u.SetUnstructuredContent(obj)
		}
	}
}

func shallowMapDeepCopy(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}

	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}

	return out
}

// StatusREST implements the REST endpoint for changing the status of a CustomResource
type StatusREST struct {
	mainREST *REST
}

var _ = rest.Patcher(&StatusREST{})

func (r *StatusREST) New() runtime.Object {
	return r.mainREST.New()
}

// Get retrieves the object from the storage. It is required to support Patch.
func (r *StatusREST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	o, err := r.mainREST.Get(ctx, name, options)
	if err != nil {
		return nil, err
	}
	if u, ok := o.(*unstructured.Unstructured); ok {
		shallowCopyObjectMeta(u)
	}
	return o, nil
}

// Update alters the status subset of an object.
func (r *StatusREST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	// We are explicitly setting forceAllowCreate to false in the call to the underlying storage because
	// subresources should never allow create on update.
	return r.mainREST.Update(ctx, name, objInfo, createValidation, updateValidation, false, options)
}

// GetResetFields implements rest.ResetFieldsStrategy
func (r *StatusREST) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return r.mainREST.GetResetFields()
}
