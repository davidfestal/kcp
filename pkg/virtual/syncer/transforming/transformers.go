/*
Copyright 2021 The KCP Authors.

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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
)

type UpdateSelectorsFunc func(ctx context.Context, labelSelector labels.Selector, fieldSelector fields.Selector) (labels.Selector, fields.Selector, error)

func TransformsSelectors(updateFunc UpdateSelectorsFunc) Transformer {

	updateSelectorString := func(ctx context.Context, labelSelectorStr string, fieldSelectorStr string) (newLabelSelectorStr string, newFieldSelectorStr string, err error) {
		labelSelector, err := labels.Parse(labelSelectorStr)
		if err != nil {
			return "", "", err
		}
		fieldSelector, err := fields.ParseSelector(fieldSelectorStr)
		if err != nil {
			return "", "", err
		}
		newLabelSelector, newFieldSelector, err := updateFunc(ctx, labelSelector, fieldSelector)
		if err != nil {
			return "", "", err
		}
		newLabelSelectorStr = newLabelSelector.String()
		newFieldSelectorStr = newFieldSelector.String()
		return
	}

	return Transformer{
		BeforeList: func(ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error) {
			newLabelSelector, newFieldSelector, err := updateSelectorString(ctx, opts.LabelSelector, opts.FieldSelector)
			if err != nil {
				return ctx, opts, err
			}
			opts.LabelSelector = newLabelSelector
			opts.FieldSelector = newFieldSelector
			return ctx, opts, nil
		},
		BeforeWatch: func(ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error) {
			newLabelSelector, newFieldSelector, err := updateSelectorString(ctx, opts.LabelSelector, opts.FieldSelector)
			if err != nil {
				return ctx, opts, err
			}
			opts.LabelSelector = newLabelSelector
			opts.FieldSelector = newFieldSelector
			return ctx, opts, nil
		},
	}
}

type ResourceTransformer func(ctx context.Context, resource *unstructured.Unstructured, subresources ...string) (transformed *unstructured.Unstructured, err error)

func TransformsResource(before, after ResourceTransformer) Transformer {
	t := Transformer{}

	if before != nil {
		t.BeforeCreate = func(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.CreateOptions, []string, error) {
			if transformedResource, err := before(ctx, obj, subresources...); err != nil {
				return ctx, obj, options, subresources, err
			} else {
				return ctx, transformedResource, options, subresources, nil
			}
		}
		t.BeforeUpdate = func(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.UpdateOptions, []string, error) {
			if transformedResource, err := before(ctx, obj, subresources...); err != nil {
				return ctx, obj, options, subresources, err
			} else {
				return ctx, transformedResource, options, subresources, nil
			}
		}
	}
	if after != nil {
		t.AfterCreate = func(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if transformedResource, err := after(ctx, obj, subresources...); err != nil {
				return obj, err
			} else {
				return transformedResource, nil
			}
		}
		t.AfterUpdate = func(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if transformedResource, err := after(ctx, obj, subresources...); err != nil {
				return obj, err
			} else {
				return transformedResource, nil
			}
		}
		// TODO: implement the delete, but for now we don't need it
		t.AfterGet = func(ctx context.Context, name string, options metav1.GetOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if transformedResource, err := after(ctx, result, subresources...); err != nil {
				return nil, err
			} else {
				return transformedResource, nil
			}
		}
		t.AfterList = func(ctx context.Context, opts metav1.ListOptions, result *unstructured.UnstructuredList) (*unstructured.UnstructuredList, error) {
			transformedResult := result.DeepCopy()
			transformedResult.Items = []unstructured.Unstructured{}
			for _, item := range result.Items {
				item := item
				if transformed, err := after(ctx, &item); err != nil {
					if errors.IsNotFound(err) {
						continue
					}
				} else {
					transformedResult.Items = append(transformedResult.Items, *transformed)
				}
			}
			return transformedResult, nil
		}
		t.AfterWatch = func(ctx context.Context, opts metav1.ListOptions, result watch.Interface) (watch.Interface, error) {
			transformingWatcher := NewTransformingWatcher(result, func(event watch.Event) (transformed watch.Event, skipped bool) {
				transformed = event
				switch event.Type {
				case watch.Added, watch.Modified, watch.Deleted:
					if resource, isUnstructured := event.Object.(*unstructured.Unstructured); !isUnstructured {
						transformed.Type = watch.Error
						transformed.Object = &metav1.Status{
							Status:  "Failure",
							Reason:  metav1.StatusReasonUnknown,
							Message: "Watch expected a resource of type *unstructured.Unstructured",
							Code:    500,
						}
					} else if transformedResource, err := after(ctx, resource); err != nil {
						if errors.IsNotFound(err) {
							skipped = true
						} else {
							transformed.Type = watch.Error
							transformed.Object = &metav1.Status{
								Status:  "Failure",
								Reason:  metav1.StatusReasonUnknown,
								Message: "Watch transformation failed",
								Code:    500,
								Details: &metav1.StatusDetails{
									Name:  resource.GetName(),
									Group: resource.GroupVersionKind().Group,
									Kind:  resource.GroupVersionKind().Kind,
									Causes: []metav1.StatusCause{
										{
											Type:    metav1.CauseTypeUnexpectedServerResponse,
											Message: err.Error(),
										},
									},
								},
							}
						}
					} else {
						transformed.Object = transformedResource
					}
				}
				return transformed, skipped
			})
			return transformingWatcher, nil
		}
	}

	return t
}
