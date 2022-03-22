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

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/syncer/dynamic"
	apidefs "github.com/kcp-dev/kcp/pkg/virtual/syncer/dynamic/apidefs"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/dynamic/apiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/transforming"
)

const SyncerVirtualWorkspaceName string = "syncer"

func BuildVirtualWorkspace(rootPathPrefix string, dynamicClusterClient dynamic.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface, wildcardKcpInformers kcpinformer.SharedInformerFactory) framework.VirtualWorkspace {

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	var installedAPIs *installedAPIs
	ready := false

	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		Name: SyncerVirtualWorkspaceName,
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			if installedAPIs == nil {
				return
			}
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

				if _, exists := installedAPIs.GetAPIs(locationKey); !exists {
					return
				}

				prefixToStrip = strings.TrimSuffix(path, realPath)
				return true, prefixToStrip, context.WithValue(requestContext, apidefs.APIDomainKeyContextKey, locationKey)
			}
			return
		},
		Ready: func() error {
			if !ready {
				return errors.New("syncer virtual workspace controllers are not started")
			}
			return nil
		},
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefs.APISetRetriever, error) {

			clusterInformer := wildcardKcpInformers.Workload().V1alpha1().WorkloadClusters().Informer()
			apiResourceImportInformer := wildcardKcpInformers.Apiresource().V1alpha1().APIResourceImports()
			negotiatedAPIResourceInformer := wildcardKcpInformers.Apiresource().V1alpha1().NegotiatedAPIResources()

			genericTransformers := transforming.Transformers{
			}

			installedAPIs = newInstalledAPIs(func(workspaceName string, spec *apiresourcev1alpha1.CommonAPIResourceSpec) (apidefs.APIDefinition, error) {
				return apiserver.CreateServingInfoFor(mainConfig, workspaceName, spec, provideForwardingRestStorage(dynamicClusterClient, genericTransformers))
			})

			// This should be replaced by a real controller when the URLs should be added to the WorkloadCluster object
			clusterInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					if location, ok := obj.(*workloadv1alpha1.WorkloadCluster); ok {
						workloadCluster := syncer.WorkloadCluster{
							WorkspaceName: location.ClusterName,
							LocationName:  location.Name,
						}
						installedAPIs.addLocation(workloadCluster)
						for _, api := range internalAPIs {
							_ = installedAPIs.Upsert(syncer.WorkloadClusterAPI{
								WorkloadCluster: workloadCluster,
								Spec:            api,
							})
						}
					}
				},
				DeleteFunc: func(obj interface{}) {
					if location, ok := obj.(*workloadv1alpha1.WorkloadCluster); ok {
						installedAPIs.removeLocation(syncer.WorkloadCluster{
							WorkspaceName: location.ClusterName,
							LocationName:  location.Name,
						})
					}
				},
			})

			apiReconciler, err := controllers.NewAPIReconciler(installedAPIs, kcpClusterClient, apiResourceImportInformer, negotiatedAPIResourceInformer)
			if err != nil {
				return nil, err
			}

			if err := mainConfig.AddPostStartHook("apiresourceimports.kcp.dev-api-reconciler", func(hookContext genericapiserver.PostStartHookContext) error {
				for name, informer := range map[string]cache.SharedIndexInformer{
					"apiresourceimports":     apiResourceImportInformer.Informer(),
					"negotiatedapiresources": negotiatedAPIResourceInformer.Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						return errors.New("informer not synced")
					}
				}

				ready = true
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					<-hookContext.StopCh
					cancel()
				}()
				go apiReconciler.Start(ctx)
				return nil
			}); err != nil {
				return nil, err
			}

			return installedAPIs, nil
		},
	}
}
