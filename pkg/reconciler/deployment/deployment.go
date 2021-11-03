package deployment

import (
	"context"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
	"k8s.io/kube-openapi/pkg/util/sets"
)

const (
	clusterLabel = "kcp.dev/cluster"
)

func (c *Controller) reconcile(ctx context.Context, deployment *appsv1.Deployment) error {
	klog.Infof("reconciling deployment %q", deployment.Name)

	clusters, err := c.clusterLister.List(labels.Everything())
	if err != nil {
		return err
	}

	var clustersToAssign []string
	for _, cluster := range clusters {
		clusterSyncedResources := sets.NewString(cluster.Status.SyncedResources...)
		if clusterSyncedResources.HasAny("deployments", "deployments.apps") {
			clustersToAssign = append(clustersToAssign, cluster.Name)
		}
	}

	if deployment.Annotations == nil {
		deployment.Annotations = make(map[string]string)
	}
	deployment.Annotations["kcp.dev/assigned-locations"] = strings.Join(clustersToAssign, ",")

	if deployment.Labels == nil {
		deployment.Labels = make(map[string]string)
	}
	deployment.Labels[clusterLabel] = ""
	deployment.Labels["kcp.dev/transformer"] = "deployment-transformer"

	klog.Infof("Assigning deployment %q to %q", deployment.Name, deployment.Annotations["assigned-locations"])
	return nil
}
