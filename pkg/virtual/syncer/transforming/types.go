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
	BeforeCreate     func(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.CreateOptions, []string, error)
	BeforeUpdate     func(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.UpdateOptions, []string, error)
	BeforeDelete     func(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (context.Context, string, metav1.DeleteOptions, []string, error)
	DeleteCollection func(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (context.Context, metav1.DeleteOptions, metav1.ListOptions, error)
	BeforeGet        func(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (context.Context, string, metav1.GetOptions, []string, error)
	BeforeList       func(ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error)
	BeforeWatch      func(ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error)
}

type TransformingClient struct {
	Transformations Transformers
	Client          dynamic.ResourceInterface
}

func (tc *TransformingClient) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
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
	return tc.Client.Update(ctx, obj, options, subresources...)
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
	return nil, errors.New("not implemented")
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
	return tc.Client.List(ctx, opts)
}
func (tc *TransformingClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeWatch
		ctx, opts, err = action(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	return tc.Client.Watch(ctx, opts)
}
func (tc *TransformingClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
}
