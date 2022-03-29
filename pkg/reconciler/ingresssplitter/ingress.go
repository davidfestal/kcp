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

package ingresssplitter

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/strategies"
)

// reconcile is triggered on every change to an ingress resource, or it's associated services (by tracker).
func (c *Controller) reconcile(ctx context.Context, ingress *networkingv1.Ingress) error {
	klog.InfoS("reconciling Ingress", "ClusterName", ingress.ClusterName, "Namespace", ingress.Namespace, "Name", ingress.Name)

	existingLocations := sets.NewString()
	for locationName, locationSyncing := range strategies.GetSyncing(ingress) {
		if locationSyncing.Active() && locationSyncing.RequiredDeletion() == nil {
			existingLocations.Insert(locationName)
		}
	}

	desiredLocs, err := c.desiredLocations(ctx, ingress)
	if err != nil {
		return err
	}

	if ingress.Labels == nil {
		ingress.Labels = make(map[string]string)
	}
	for _, location := range desiredLocs.List() {
		ingress.Labels[syncer.LocationClusterLabelName(location)] = "Sync"
		delete(ingress.Annotations, strategies.LocationDeletionAnnotationName(location))
	}
	for _, location := range existingLocations.Difference(desiredLocs).List() {
		deletionTimestamp, err := metav1.Now().MarshalText()
		if err != nil {
			return err
		}
		ingress.Annotations[strategies.LocationDeletionAnnotationName(location)] = string(deletionTimestamp)
	}

	if _, err := c.client.Cluster(ingress.ClusterName).NetworkingV1().Ingresses(ingress.Namespace).Update(ctx, ingress, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

// desiredLocations returns a list of leaves (ingresses) to be created based on an ingress.
func (c *Controller) desiredLocations(ctx context.Context, root *networkingv1.Ingress) (sets.String, error) {
	// This will parse the ingresses and extract all the destination services,
	// then create a new ingress leaf for each of them.
	services, err := c.getServices(ctx, root)
	if err != nil {
		return nil, err
	}

	clusterDests := sets.NewString()
	for _, service := range services {
		for locationName, locationSyncing := range strategies.GetSyncing(service) {
			if locationSyncing.Active() && locationSyncing.RequiredDeletion() == nil {
				clusterDests.Insert(locationName)
			}
		}

		// Trigger reconciliation of the root ingress when this service changes.
		c.tracker.add(root, service)
	}

	return clusterDests, nil
}

// getServices will parse the ingress object and return a list of the services.
func (c *Controller) getServices(ctx context.Context, ingress *networkingv1.Ingress) ([]*corev1.Service, error) {
	var services []*corev1.Service
	for _, rule := range ingress.Spec.Rules {
		for _, path := range rule.HTTP.Paths {
			// TODO(jmprusi): Use a service lister
			svc, err := c.client.Cluster(ingress.ClusterName).CoreV1().Services(ingress.Namespace).Get(ctx, path.Backend.Service.Name, metav1.GetOptions{})
			// TODO(jmprusi): If one of the services doesn't exist, we invalidate all the other ones.. review this.
			if err != nil {
				return nil, err
			}
			services = append(services, svc)
		}
	}
	return services, nil
}

func LabelEscapeClusterName(s string) string {
	return strings.ReplaceAll(s, ":", "_")
}

func UnescapeClusterNameLabel(s string) string {
	return strings.ReplaceAll(s, "_", ":")
}
