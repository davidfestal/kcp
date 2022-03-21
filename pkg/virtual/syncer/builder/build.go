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

package builder

import (
	"context"
	"errors"
	"strings"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/apiserver"
	virtualworkspacesregistry "github.com/kcp-dev/kcp/pkg/virtual/syncer/registry"
)

const SyncerVirtualWorkspaceName string = "syncer"
const DefaultRootPathPrefix string = "/services/syncer"

type clusterAwareClientGetter struct {
	clusterInterface dynamic.ClusterInterface
}

func (g *clusterAwareClientGetter) GetDynamicClient(ctx context.Context) (dynamic.Interface, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, err
	}
	if cluster.Wildcard {
		return g.clusterInterface.Cluster("*"), nil
	} else {
		return g.clusterInterface.Cluster(cluster.Name), nil
	}
}

func BuildVirtualWorkspace(rootPathPrefix string, dynamicClusterInterface dynamic.ClusterInterface, kcpWildcardClient kcpclient.Interface) framework.VirtualWorkspace {

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	var apiSets syncer.APISets = make(syncer.APISets)

	return &SyncerVirtualWorkspace{
		Name: SyncerVirtualWorkspaceName,
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			completedContext = requestContext
			if path := urlPath; strings.HasPrefix(path, rootPathPrefix) {
				withoutRootPathPrefix := strings.TrimPrefix(path, rootPathPrefix)
				parts := strings.SplitN(withoutRootPathPrefix, "/", 2)
				if len(parts) == 0 || parts[0] == "" {
					return
				}
				locationKey := parts[0]
				realPath := "/"
				if len(parts) > 1 {
					realPath += parts[1]
				}

				cluster := genericapirequest.Cluster{Name: "", Wildcard: true}
				if strings.HasPrefix(realPath, "/clusters/") {
					withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
					parts = strings.SplitN(withoutClustersPrefix, "/", 2)
					lclusterName := parts[0]
					realPath = "/"
					if len(parts) > 1 {
						realPath += parts[1]
					}
					cluster = genericapirequest.Cluster{Name: lclusterName}
					if lclusterName == "*" {
						cluster.Wildcard = true
					}
				}
				requestContext = genericapirequest.WithCluster(requestContext, cluster)

				if _, exists := apiSets[locationKey]; !exists {
					return
				}

				prefixToStrip = strings.TrimSuffix(path, realPath)
				return true, prefixToStrip, context.WithValue(requestContext, virtualworkspacesregistry.LocationKeyContextKey, locationKey)
			}
			return
		},
		Ready: func() error {
			// TODO: implement for real
			return errors.New("not implemented")
		},
		BootstrapAPISetManager: func(mainConfig genericapiserver.CompletedConfig) error {

			// TODO: this code is temporary and just for the sake of early manual testing
			// This would be replaced by one or several controllers that would drive additions/removals in the apiSets map
			nars, err := kcpWildcardClient.ApiresourceV1alpha1().NegotiatedAPIResources().List(context.TODO(), v1.ListOptions{})
			if err != nil {
				return err
			}
			for _, nar := range nars.Items {
				cars := nar.Spec.CommonAPIResourceSpec
				serverInfo, err := apiserver.CreateServingInfoFor(mainConfig, nar.ClusterName, &cars, &clusterAwareClientGetter{
					clusterInterface: dynamicClusterInterface,
				})
				if err != nil {
					return err
				}
				if apiSets["cluster1"] == nil {
					apiSets["cluster1"] = make(syncer.APISet)
				}
				apiSets["cluster1"][serverInfo.GetRequestScope().Resource] = serverInfo
			}
			return nil
		},
		apiSets: apiSets,
	}
}
