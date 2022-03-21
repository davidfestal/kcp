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
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
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
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/apiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers"
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

type createAPIDefinitionFunc func(workspaceName string, spec *apiresourcev1alpha1.CommonAPIResourceSpec) (syncer.APIDefinition, error)
type installedAPIs struct {
	createAPIDefinition createAPIDefinitionFunc

	mutex   sync.RWMutex
	apiSets map[string]syncer.APISet
}

func newInstalledAPIs(createAPIDefinition createAPIDefinitionFunc) *installedAPIs {
	return &installedAPIs{
		createAPIDefinition: createAPIDefinition,
		apiSets:             make(map[string]syncer.APISet),
	}
}

func (apis *installedAPIs) addLocation(locationName string) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	if _, exists := apis.apiSets[locationName]; !exists {
		apis.apiSets[locationName] = make(syncer.APISet)
	}
}

func (apis *installedAPIs) removeLocation(locationName string) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	delete(apis.apiSets, locationName)
}

func (apis *installedAPIs) GetAPIs(locationKey string) (syncer.APISet, bool) {
	apis.mutex.RLock()
	defer apis.mutex.RUnlock()

	apiSet, ok := apis.apiSets[locationKey]
	return apiSet, ok
}

func (apis *installedAPIs) Upsert(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	if locationAPIs, exists := apis.apiSets[api.LocationName]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.LocationName, api.WorkspaceName)
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		if apiDefinition, err := apis.createAPIDefinition(api.WorkspaceName, api.Spec); err != nil {
			return err
		} else {
			locationAPIs[gvr] = apiDefinition
		}
	}
	return nil
}

func (apis *installedAPIs) Remove(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	if locationAPIs, exists := apis.apiSets[api.LocationName]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.LocationName, api.WorkspaceName)
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		delete(locationAPIs, gvr)
	}
	return nil
}

type delegatingAPISetRetriever struct {
	delegate syncer.APISetRetriever
}

func (d *delegatingAPISetRetriever) GetAPIs(locationKey string) (apis syncer.APISet, locationExists bool) {
	if d.delegate == nil {
		return nil, false
	} else {
		return d.delegate.GetAPIs(locationKey)
	}
}

func BuildVirtualWorkspace(rootPathPrefix string, dynamicClusterClient dynamic.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface, wildcardKcpInformers kcpinformer.SharedInformerFactory) framework.VirtualWorkspace {

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	apiSetRetriever := &delegatingAPISetRetriever{}

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

				if _, exists := apiSetRetriever.GetAPIs(locationKey); !exists {
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

			installedAPIs := newInstalledAPIs(func(workspaceName string, spec *apiresourcev1alpha1.CommonAPIResourceSpec) (syncer.APIDefinition, error) {
				return apiserver.CreateServingInfoFor(mainConfig, workspaceName, spec, &clusterAwareClientGetter{
					clusterInterface: dynamicClusterClient,
				})

			})
			apiSetRetriever.delegate = installedAPIs

			clusterInformer := wildcardKcpInformers.Workload().V1alpha1().WorkloadClusters().Informer()

			// This should be replace by a real controller when the URLs should be added to the WorkloadCluster object
			clusterInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					if location, ok := obj.(*workloadv1alpha1.WorkloadCluster); ok {
						installedAPIs.addLocation(location.Name)
					}
				},
				DeleteFunc: func(obj interface{}) {
					if location, ok := obj.(*workloadv1alpha1.WorkloadCluster); ok {
						installedAPIs.removeLocation(location.Name)
					}
				},
			})

			apiResourceImportInformer := wildcardKcpInformers.Apiresource().V1alpha1().APIResourceImports()
			negotiatedAPIResourceInformer := wildcardKcpInformers.Apiresource().V1alpha1().NegotiatedAPIResources()

			apiReconciler, err := controllers.NewAPIReconciler(installedAPIs, kcpClusterClient, apiResourceImportInformer, negotiatedAPIResourceInformer)
			if err != nil {
				return err
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

				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					<-hookContext.StopCh
					cancel()
				}()
				go apiReconciler.Start(ctx)
				return nil
			}); err != nil {
				return err
			}

			return nil
		},
		apiSets: apiSetRetriever,
	}
}
