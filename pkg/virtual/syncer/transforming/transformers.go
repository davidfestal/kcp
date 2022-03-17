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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
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
