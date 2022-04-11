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

package transformers

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/transforming"
)

func RemoveWorkloadClusterLabelSelector() transforming.Transformer {
	return transforming.TransformsSelectors("RemoveWorkloadClusterLabelSelector", func(ctx context.Context, labelSelector labels.Selector, fieldSelector fields.Selector) (labels.Selector, fields.Selector, error) {
		newLabelSelector := labels.NewSelector()
		reqs, _ := labelSelector.Requirements()
		for _, req := range reqs {
			if req.Key() == nscontroller.ClusterLabel && req.Operator() == selection.Equals {
				continue
			}
			newLabelSelector = newLabelSelector.Add(req)
		}
		return newLabelSelector, fieldSelector, nil
	})
}

func AddWorkloadClusterLabelSelector() transforming.Transformer {
	return transforming.TransformsSelectors("AddWorkloadClusterLabelSelector", func(ctx context.Context, labelSelector labels.Selector, fieldSelector fields.Selector) (labels.Selector, fields.Selector, error) {
		workloadCluster, err := syncer.FromContext(ctx)
		if err != nil {
			return nil, nil, err
		}
		notEmpty, _ := labels.NewRequirement(syncer.LocationClusterLabelName(workloadCluster.LocationName), selection.NotEquals, []string{""})
		exists, _ := labels.NewRequirement(syncer.LocationClusterLabelName(workloadCluster.LocationName), selection.Exists, []string{})
		return labelSelector.Add(*exists, *notEmpty), fieldSelector, nil
	})
}

func AddOldWorkloadClusterLabel() transforming.Transformer {
	return transforming.TransformsResource("AddOldWorkloadClusterLabel", nil, func(client dynamic.ResourceInterface, ctx context.Context, resource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformed *unstructured.Unstructured, err error) {
		transformed = resource.DeepCopy()
		workloadCluster, err := syncer.FromContext(ctx)
		if err != nil {
			return nil, err
		}
		labels := resource.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[nscontroller.ClusterLabel] = workloadCluster.LocationName
		transformed.SetLabels(labels)
		return transformed, nil
	})
}
