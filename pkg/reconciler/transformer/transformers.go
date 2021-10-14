/*
Copyright 2021 The Authors

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

package transformer

import (
	"fmt"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer"
)

const (
	numSyncerThreads = 2
)

func (c *Controller) process(clusterName string) error {
	logicalClusterSyncer := c.syncers[clusterName]
	var syncerResources sets.String
	if logicalClusterSyncer == nil {
		syncerResources = sets.NewString()
	} else {
		syncerResources = logicalClusterSyncer.Resources
	} 

	logicalClusterResources := sets.NewString()
	objects, err := c.negotiatedApiResourceIndexer.ByIndex(clusterNameIndex, clusterName)
	if err != nil {
		return err
	}
	for _,obj := range objects {
		nar, isNAR := obj.(*apiresourcev1alpha1.NegotiatedAPIResource)
		if !isNAR {
			continue
		}
		if !nar.IsConditionTrue(apiresourcev1alpha1.Published) {
			continue
		}
		logicalClusterResources.Insert(schema.GroupResource {
			Group: nar.Spec.GroupVersion.Group,
			Resource: nar.Spec.Plural,
		}.String())
	}

	if !logicalClusterResources.Equal(syncerResources) {
		if logicalClusterResources.Len() > 0 {
			kubeConfig := c.kubeconfig.DeepCopy()
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
				klog.Errorf("error installing transformer: %v", err)
				return err
			}
			kubeConfig.CurrentContext = clusterName
	
			upstream, err := clientcmd.NewNonInteractiveClientConfig(*kubeConfig, clusterName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			if err != nil {
				klog.Errorf("error installing transformer: %v", err)
				return err
			}
	
			downstream, err := clientcmd.NewNonInteractiveClientConfig(*kubeConfigForPrivateLogicalCluster, clusterName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			if err != nil {
				klog.Errorf("error installing transformer: %v", err)
				return err
			}
	
			syncer, err := syncer.BuildDefaultSyncerToPrivateLogicalCluster()
			if err != nil {
				klog.Errorf("error building transformer syncer: %v", err)
				return err
			}
			newSyncer, err := syncer(upstream, downstream, logicalClusterResources, numSyncerThreads)
			if err != nil {
				klog.Errorf("error starting transformer syncer: %v", err)
				return err
			}
	
			c.syncers[clusterName] = newSyncer
		} else {
			delete(c.syncers, clusterName)
		} 

		if logicalClusterSyncer != nil {
			logicalClusterSyncer.Stop()
		}
	}

	return nil
}

