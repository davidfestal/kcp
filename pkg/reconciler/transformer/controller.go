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
	"time"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	typedapiresource "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/apiresource/v1alpha1"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apiresourcelister "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer"
	"github.com/kcp-dev/kcp/pkg/util/errors"
)

const resyncPeriod = 10 * time.Hour
const clusterNameIndex string = "ClusterName"

func NewController(cfg *rest.Config, kubeconfig clientcmdapi.Config) (*Controller, error) {
	apiresourceClient := typedapiresource.NewForConfigOrDie(cfg)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	stopCh := make(chan struct{}) // TODO: hook this up to SIGTERM/SIGINT

	c := &Controller{
		queue:                            queue,
		apiresourceClient:                apiresourceClient,
		stopCh:                           stopCh,
		kubeconfig:                       kubeconfig,
		syncers: make(map[string]*syncer.Syncer),		
	}

	apiresourceSif := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(cfg), resyncPeriod)
	apiresourceSif.Apiresource().V1alpha1().NegotiatedAPIResources().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})
	c.negotiatedApiResourceIndexer = apiresourceSif.Apiresource().V1alpha1().NegotiatedAPIResources().Informer().GetIndexer()
	if err := c.negotiatedApiResourceIndexer.AddIndexers(map[string]cache.IndexFunc{
		clusterNameIndex: func(obj interface{}) ([]string, error) {
			if negotiatedApiResource, ok := obj.(*apiresourcev1alpha1.NegotiatedAPIResource); ok {
				return []string{ negotiatedApiResource.ClusterName }, nil
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to add indexer for NegotiatedAPIResource: %v", err)
	}	
	c.negotiatedApiResourceLister = apiresourceSif.Apiresource().V1alpha1().NegotiatedAPIResources().Lister()

	apiresourceSif.WaitForCacheSync(stopCh)
	apiresourceSif.Start(stopCh)

	return c, nil
}

type Controller struct {
	queue workqueue.RateLimitingInterface

	apiresourceClient            typedapiresource.ApiresourceV1alpha1Interface
	negotiatedApiResourceIndexer cache.Indexer
	negotiatedApiResourceLister  apiresourcelister.NegotiatedAPIResourceLister

	kubeconfig                   clientcmdapi.Config
	syncers                      map[string]*syncer.Syncer

	stopCh                           chan struct{}
}

func (c *Controller) enqueue(obj interface{}) {
	if obj == nil {
		return
	}

	var negotiatedAPIResource *apiresourcev1alpha1.NegotiatedAPIResource
	switch typedObj := obj.(type) {
	case *apiresourcev1alpha1.NegotiatedAPIResource:
		negotiatedAPIResource = typedObj
	case cache.DeletedFinalStateUnknown:
		deletedImport, ok := typedObj.Obj.(*apiresourcev1alpha1.NegotiatedAPIResource)
		if ok {
			negotiatedAPIResource = deletedImport
		}
	}

	c.queue.AddRateLimited(negotiatedAPIResource.GetClusterName())
}

func (c *Controller) Start() {
	go wait.Until(c.startWorker, time.Second, c.stopCh)
	klog.Info("Starting worker")
}

// Stop stops the controller.
func (c *Controller) Stop() {
	klog.Info("Stopping worker")
	c.queue.ShutDown()
	close(c.stopCh)
}

// Done returns a channel that's closed when the controller is stopped.
func (c *Controller) Done() <-chan struct{} { return c.stopCh }

func (c *Controller) startWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	// Wait until there is a new item in the working queue
	element, quit := c.queue.Get()
	if quit {
		return false
	}
	clusterName := element.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(clusterName)

	err := c.process(clusterName)
	c.handleErr(err, clusterName)
	return true
}

func (c *Controller) handleErr(err error, key string) {
	// Reconcile worked, nothing else to do for this workqueue item.
	if err == nil {
		klog.Info("Successfully reconciled", key)
		c.queue.Forget(key)
		return
	}

	// Re-enqueue up to 5 times.
	num := c.queue.NumRequeues(key)
	if errors.IsRetryable(err) || num < 5 {
		klog.Infof("Error reconciling key %q, retrying... (#%d): %v", key, num, err)
		c.queue.AddRateLimited(key)
		return
	}

	// Give up and report error elsewhere.
	c.queue.Forget(key)
	runtime.HandleError(err)
	klog.Infof("Dropping key %q after failed retries: %v", key, err)
}
