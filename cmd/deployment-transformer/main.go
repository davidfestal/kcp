package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"

	"github.com/kcp-dev/kcp/pkg/syncer"
)

const (
	numThreads   = 2
	clusterLabel = "kcp.dev/cluster"
	ownedByLabel = "kcp.dev/owned-by"
)

var kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig")
var kubecontext = flag.String("context", "", "Context to use in the Kubeconfig file, instead of the current context")

func main() {
	flag.Parse()

	var overrides clientcmd.ConfigOverrides
	if *kubecontext != "" {
		overrides.CurrentContext = *kubecontext
	}

	kubeConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig},
		&overrides).RawConfig()
	if err != nil {
		klog.Fatal(err)
	}

	clusterName := kubeConfig.CurrentContext
	kubeConfigForPrivateLogicalCluster := kubeConfig.DeepCopy()
	noContextInConfig := true
	if logicalClusterContext, exists := kubeConfigForPrivateLogicalCluster.Contexts[clusterName]; exists {
		if _, exists := kubeConfigForPrivateLogicalCluster.Clusters[logicalClusterContext.Cluster]; exists {
			noContextInConfig = false
			server := kubeConfigForPrivateLogicalCluster.Clusters[logicalClusterContext.Cluster].Server
			if !strings.HasSuffix(server, "clusters/"+clusterName) {
				server = server + "/clusters/_admin_"
			} else {
				server = strings.Replace(server, "/clusters/"+clusterName, "/clusters/_"+clusterName+"_", -1)
			}
			kubeConfigForPrivateLogicalCluster.Clusters[logicalClusterContext.Cluster].Server = server
		}
	}
	if noContextInConfig {
		err := fmt.Errorf("no context with the name of the expected cluster: %s", clusterName)
		klog.Fatalf("error installing transformer: %v", err)
	}

	upstream, err := clientcmd.NewNonInteractiveClientConfig(kubeConfig, clusterName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		klog.Fatalf("error installing transformer: %v", err)
	}

	downstream, err := clientcmd.NewNonInteractiveClientConfig(*kubeConfigForPrivateLogicalCluster, clusterName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		klog.Fatalf("error installing transformer: %v", err)
	}

	splitterSyncing := syncer.NewDelegateSyncing(syncer.NewIdentitySyncing(nil))
	splitterSyncing.Delegate(appsv1.SchemeGroupVersion.WithResource("deployments"), deploymentSplitterSyncing{})

	syncerBuilder, err := syncer.BuildCustomSyncerToPrivateLogicalCluster("deployment-transformer", splitterSyncing)
	if err != nil {
		klog.Fatal(err)
	}
	syncer, err := syncerBuilder(upstream, downstream, sets.NewString("deployments"), numThreads)
	if err != nil {
		klog.Fatal(err)
	}
	klog.Infoln("Starting workers")

	syncer.WaitUntilDone()
	klog.Infoln("Stopping workers")
}

type deploymentSplitterSyncing struct {
}

var _ syncer.Syncing = deploymentSplitterSyncing{}

func (deploymentSplitterSyncing) UpsertIntoDownstream() syncer.UpsertFunc {
	return func(c *syncer.Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured, labelsToAdd map[string]string) error {
		var deployment appsv1.Deployment
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstrob.UnstructuredContent(), &deployment)
		if err != nil {
			return err
		}

		toClient := c.GetClient(gvr, deployment.GetNamespace())

		assignedLocationsAnnot, exists := deployment.GetAnnotations()["kcp.dev/assigned-locations"]
		if !exists {
			return nil
		}
		var assignedLocations []string
		for _, location := range strings.Split(assignedLocationsAnnot, ",") {
			location = strings.TrimSpace(location)
			if location != "" {
				assignedLocations = append(assignedLocations, location)
			}
		}

		replicasEach := *deployment.Spec.Replicas / int32(len(assignedLocations))
		rest := *deployment.Spec.Replicas % int32(len(assignedLocations))
		for index, location := range assignedLocations {
			vd := deployment.DeepCopy()

			// TODO: munge cluster name
			vd.Name = fmt.Sprintf("%s--%s", deployment.Name, location)

			if vd.Labels == nil {
				vd.Labels = map[string]string{}
			}
			vd.Labels[clusterLabel] = location
			vd.Labels[ownedByLabel] = deployment.Name

			replicasToSet := replicasEach
			if index == 0 {
				replicasToSet += rest
			}
			vd.Spec.Replicas = &replicasToSet

			vd.SetResourceVersion("")
			vd.SetUID("")
			vdUnstr := unstructured.Unstructured{}
			unstrContent, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&vd)
			if err != nil {
				return err
			}
			vdUnstr.SetUnstructuredContent(unstrContent)

			if _, err := toClient.Create(ctx, &vdUnstr, metav1.CreateOptions{}); err != nil {
				if !errors.IsAlreadyExists(err) {
					return err
				}
				var existing *unstructured.Unstructured
				existing, err = toClient.Get(ctx, vdUnstr.GetName(), metav1.GetOptions{})
				if err != nil {
					return err
				}
				vdUnstr.SetResourceVersion(existing.GetResourceVersion())
				vdUnstr.SetUID(existing.GetUID())
				if _, err := toClient.Update(ctx, &vdUnstr, metav1.UpdateOptions{}); err != nil {
					return err
				}
			}
			klog.Infof("created child deployment %q", vd.Name)
		}

		return nil
	}
}

func (deploymentSplitterSyncing) DeleteFromDownstream() syncer.DeleteFunc {
	return func(c *syncer.Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
		toClient := c.GetClient(gvr, namespace)
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, name))
		if err != nil {
			return err
		}
		if err := toClient.DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
			LabelSelector: sel.String(),
		}); err != nil {
			return err
		}
		return nil
	}
}

func (deploymentSplitterSyncing) UpdateStatusInUpstream() syncer.UpdateStatusFunc {
	return func(c *syncer.Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) (notFound bool, err error) {
		var deployment appsv1.Deployment
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstrob.UnstructuredContent(), &deployment)
		if err != nil {
			return false, err
		}

		toClient := c.GetClient(gvr, deployment.GetNamespace())

		rootDeploymentName := deployment.Labels[ownedByLabel]
		// A leaf deployment was updated; get others and aggregate status.
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, rootDeploymentName))
		if err != nil {
			return false, err
		}
		others, err := c.GetFromLister(gvr, namespace).List(sel)
		if err != nil {
			return false, err
		}

		rootDeploymentUnstr, err := toClient.Get(ctx, rootDeploymentName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			klog.Errorf("Getting resource %s/%s: %v", namespace, rootDeploymentName, err)
			return false, err
		}
		var rootDeployment appsv1.Deployment
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(rootDeploymentUnstr.UnstructuredContent(), &rootDeployment)
		if err != nil {
			return false, err
		}

		// Aggregate .status from all leafs.

		rootDeployment.Status.Replicas = 0
		rootDeployment.Status.UpdatedReplicas = 0
		rootDeployment.Status.ReadyReplicas = 0
		rootDeployment.Status.AvailableReplicas = 0
		rootDeployment.Status.UnavailableReplicas = 0
		for i, o := range others {
			var child *appsv1.Deployment
			switch typed := o.(type) {
			case *unstructured.Unstructured:
				child = &appsv1.Deployment{}
				err = runtime.DefaultUnstructuredConverter.FromUnstructured(typed.UnstructuredContent(), child)
				if err != nil {
					return false, err
				}
			case *appsv1.Deployment:
				child = typed
			default:
				return false, fmt.Errorf("ouch !!!!!!!!!!!!!!!!!: %v", o)
			}
			rootDeployment.Status.Replicas += child.Status.Replicas
			rootDeployment.Status.UpdatedReplicas += child.Status.UpdatedReplicas
			rootDeployment.Status.ReadyReplicas += child.Status.ReadyReplicas
			rootDeployment.Status.AvailableReplicas += child.Status.AvailableReplicas
			rootDeployment.Status.UnavailableReplicas += child.Status.UnavailableReplicas
			if i == 0 {
				rootDeployment.Status.Conditions = child.Status.Conditions
			}
		}

		unstrContent, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&rootDeployment)
		if err != nil {
			return false, err
		}

		rootDeploymentUnstr.SetUnstructuredContent(unstrContent)

		if _, err := toClient.UpdateStatus(ctx, rootDeploymentUnstr, metav1.UpdateOptions{}); err != nil {
			return false, err
		}

		return false, nil
	}
}

func (deploymentSplitterSyncing) LabelsToAdd() map[string]string {
	return nil
}
