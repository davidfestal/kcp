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

package transforming

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

type TypedTransformers map[schema.GroupVersionResource]Transformers
type Transformers []Transformer

type Transformer struct {
	BeforeCreate func(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.CreateOptions, []string, error)
	AfterCreate  func(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error)

	BeforeUpdate func(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.UpdateOptions, []string, error)
	AfterUpdate  func(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error)

	BeforeDelete           func(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (context.Context, string, metav1.DeleteOptions, []string, error)
	BeforeDeleteCollection func(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (context.Context, metav1.DeleteOptions, metav1.ListOptions, error)

	BeforeGet func(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (context.Context, string, metav1.GetOptions, []string, error)
	AfterGet  func(ctx context.Context, name string, options metav1.GetOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error)

	BeforeList func(ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error)
	AfterList  func(ctx context.Context, opts metav1.ListOptions, result *unstructured.UnstructuredList) (*unstructured.UnstructuredList, error)

	BeforeWatch func(ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error)
	AfterWatch  func(ctx context.Context, opts metav1.ListOptions, result watch.Interface) (watch.Interface, error)
}

type TransformingClient struct {
	Transformations Transformers
	Client          dynamic.ResourceInterface
}

func (tc *TransformingClient) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeCreate
		if action == nil {
			continue
		}
		ctx, obj, options, subresources, err = action(ctx, obj, options, subresources...)
		if err != nil {
			return nil, err
		}
	}
	result, err := tc.Client.Create(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterCreate
		if action == nil {
			continue
		}
		result, err = action(ctx, obj, options, subresources, result)
		if err != nil {
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeUpdate
		if action == nil {
			continue
		}
		ctx, obj, options, subresources, err = action(ctx, obj, options, subresources...)
		if err != nil {
			return nil, err
		}
	}
	result, err := tc.Client.Update(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterUpdate
		if action == nil {
			continue
		}
		result, err = action(ctx, obj, options, subresources, result)
		if err != nil {
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
}
func (tc *TransformingClient) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return errors.New("not implemented")
}
func (tc *TransformingClient) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return errors.New("not implemented")
}
func (tc *TransformingClient) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeGet
		if action == nil {
			continue
		}
		ctx, name, options, subresources, err = action(ctx, name, options, subresources...)
		if err != nil {
			return nil, err
		}
	}
	result, err := tc.Client.Get(ctx, name, options, subresources...)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterGet
		if action == nil {
			continue
		}
		result, err = action(ctx, name, options, subresources, result)
		if err != nil {
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeList
		if action == nil {
			continue
		}
		ctx, opts, err = action(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	result, err := tc.Client.List(ctx, opts)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterList
		if action == nil {
			continue
		}
		result, err = action(ctx, opts, result)
		if err != nil {
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeWatch
		if action == nil {
			continue
		}
		ctx, opts, err = action(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	result, err := tc.Client.Watch(ctx, opts)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterWatch
		if action == nil {
			continue
		}
		result, err = action(ctx, opts, result)
		if err != nil {
			return result, err
		}
	}
	return result, err
}

func (tc *TransformingClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
}

type TransformingWatcher struct {
	source           watch.Interface
	transformedCh    chan watch.Event
	eventTransformer EventTransformer
}

type EventTransformer func(event watch.Event) (transformed watch.Event, skipped bool)

func NewTransformingWatcher(watcher watch.Interface, eventTransformer EventTransformer) *TransformingWatcher {
	tw := &TransformingWatcher{
		source:           watcher,
		transformedCh:    make(chan watch.Event),
		eventTransformer: eventTransformer,
	}
	tw.start()
	return tw
}

func (w *TransformingWatcher) start() {
	go func() {
		for {
			if evt, more := <-w.source.ResultChan(); more {
				transformedEvent, skipped := w.eventTransformer(evt)
				if !skipped {
					w.transformedCh <- transformedEvent
				}
			} else {
				close(w.transformedCh)
				return
			}
		}
	}()
}

// Stop implements Interface
func (w *TransformingWatcher) Stop() {
	w.source.Stop()
}

// ResultChan implements Interface
func (w *TransformingWatcher) ResultChan() <-chan watch.Event {
	return w.transformedCh
}
