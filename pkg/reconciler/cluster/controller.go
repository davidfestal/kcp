package cluster

import (
	"context"
	"io/ioutil"
	"time"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	versionedclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	typedcluster        "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/cluster/v1alpha1"
	typedapiresource        "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	"github.com/kcp-dev/kcp/pkg/syncer"
	"github.com/kcp-dev/kcp/pkg/util/errors"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serorrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
	"sigs.k8s.io/yaml"
)

const resyncPeriod = 10 * time.Hour

type SyncerMode int

const (
	SyncerModePull SyncerMode = iota
	SyncerModePush
	SyncerModeNone
)

// NewController returns a new Controller which reconciles Cluster resources in the API
// server it reaches using the REST client.
//
// When new Clusters are found, the syncer will be run there using the given image.
func NewController(cfg *rest.Config, syncerImage string, kubeconfig clientcmdapi.Config, resourcesToSync []string, syncerMode SyncerMode) *Controller {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	stopCh := make(chan struct{}) // TODO: hook this up to SIGTERM/SIGINT

	crdClient := apiextensionsv1client.NewForConfigOrDie(cfg)

	c := &Controller{
		queue:           queue,
		clusterClient:          typedcluster.NewForConfigOrDie(cfg),
		apiResourceClient:      typedapiresource.NewForConfigOrDie(cfg),
		crdClient:       crdClient,
		syncerImage:     syncerImage,
		kubeconfig:      kubeconfig,
		stopCh:          stopCh,
		resourcesToSync: resourcesToSync,
		syncerMode:      syncerMode,
		syncers:         map[string]*syncer.Syncer{},
	}

	sif := externalversions.NewSharedInformerFactoryWithOptions(versionedclient.NewForConfigOrDie(cfg), resyncPeriod)
	sif.Cluster().V1alpha1().Clusters().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.deletedCluster(obj) },
	})
	c.clusterIndexer = sif.Cluster().V1alpha1().Clusters().Informer().GetIndexer()

	enqueueRelatedCluster := func(obj interface{}) {
		var apiResourceImport *apiresourcev1alpha1.APIResourceImport
		switch typedObj := obj.(type) {
		case *apiresourcev1alpha1.APIResourceImport:
			apiResourceImport = typedObj
		case cache.DeletedFinalStateUnknown:
			deletedImport, ok := typedObj.Obj.(*apiresourcev1alpha1.APIResourceImport)
			if ok {
				apiResourceImport = deletedImport
			}
		}
		if apiResourceImport != nil {
			c.enqueue(metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{
					Name: apiResourceImport.Name,
					ClusterName: apiResourceImport.ClusterName,
				},
			})
		}
	}

	sif.Apiresource().V1alpha1().APIResourceImports().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) {
			enqueueRelatedCluster(obj)
		},
		DeleteFunc: func(obj interface{}) {
			enqueueRelatedCluster(obj)
		},
	})
	c.apiresourceImportIndexer = sif.Cluster().V1alpha1().Clusters().Informer().GetIndexer()
	c.apiresourceImportIndexer.AddIndexers(map[string]cache.IndexFunc {
		apiresource.ClusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if negociatedApiResource, ok := obj.(*apiresourcev1alpha1.NegociatedAPIResource); ok {
				return []string{ apiresource.GetClusterNameAndGVRIndexKey(negociatedApiResource.ClusterName, negociatedApiResource.GVR()) }, nil
			}
			return []string{}, nil
		},
		"cluster": func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{ apiResourceImport.ClusterName + "/" + apiResourceImport.Spec.Location }, nil
			}
			return []string{}, nil
		},
	})

	sif.WaitForCacheSync(stopCh)
	sif.Start(stopCh)

	return c
}
type Controller struct {
	queue           workqueue.RateLimitingInterface
	clusterClient          typedcluster.ClusterV1alpha1Interface
	apiResourceClient      typedapiresource.ApiresourceV1alpha1Interface
	clusterIndexer         cache.Indexer
	apiresourceImportIndexer         cache.Indexer
	crdClient       apiextensionsv1client.ApiextensionsV1Interface
	syncerImage     string
	kubeconfig      clientcmdapi.Config
	stopCh          chan struct{}
	resourcesToSync []string
	syncerMode      SyncerMode
	syncers         map[string]*syncer.Syncer
	apiImporters    map[string]*APIImporter
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.AddRateLimited(key)
}

func (c *Controller) Start(numThreads int) {
	defer c.queue.ShutDown()
	for i := 0; i < numThreads; i++ {
		go wait.Until(c.startWorker, time.Second, c.stopCh)
	}
	klog.Info("Starting workers")
	<-c.stopCh
	klog.Info("Stopping workers")
}

func (c *Controller) startWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	err := c.process(key)
	c.handleErr(err, key)
	return true
}

func (c *Controller) handleErr(err error, key string) {
	// Reconcile worked, nothing else to do for this workqueue item.
	if err == nil {
		klog.Infof("Successfully reconciled %q", key)
		c.queue.Forget(key)
		return
	}

	// Re-enqueue up to 5 times.
	num := c.queue.NumRequeues(key)
	if errors.IsRetryable(err) || num < 5 {
		klog.Errorf("Error reconciling key %q, retrying... (#%d): %v", key, num, err)
		c.queue.AddRateLimited(key)
		return
	}

	// Give up and report error elsewhere.
	c.queue.Forget(key)
	runtime.HandleError(err)
	klog.Errorf("Dropping key %q after failed retries: %v", key, err)
}

func (c *Controller) process(key string) error {
	obj, exists, err := c.clusterIndexer.GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		klog.Errorf("Object with key %q was deleted", key)
		return nil
	}
	current := obj.(*clusterv1alpha1.Cluster)
	previous := current.DeepCopy()

	ctx := context.TODO()

	if err := c.reconcile(ctx, current); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, current.Status) {
		_, uerr := c.clusterClient.Clusters().UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return uerr
	}

	return nil
}

func (c *Controller) deletedCluster(obj interface{}) {
	castObj, ok := obj.(*clusterv1alpha1.Cluster)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("Couldn't get object from tombstone %#v", obj)
			return
		}
		castObj, ok = tombstone.Obj.(*clusterv1alpha1.Cluster)
		if !ok {
			klog.Errorf("Tombstone contained object that is not expected %#v", obj)
			return
		}
	}
	klog.V(4).Infof("Deleting cluster %q", castObj.Name)
	ctx := context.TODO()
	c.cleanup(ctx, castObj)
}

func RegisterClusterCRD(cfg *rest.Config) error {
	bytes, err := ioutil.ReadFile("config/cluster.example.dev_clusters.yaml")

	crdClient := apiextensionsv1client.NewForConfigOrDie(cfg)

	crd := &apiextensionsv1.CustomResourceDefinition{}
	err = yaml.Unmarshal(bytes, crd)
	if err != nil {
		return err
	}

	_, err = crdClient.CustomResourceDefinitions().Create(context.TODO(), crd, metav1.CreateOptions{})
	if err != nil && !k8serorrs.IsAlreadyExists(err) {
		return err
	}

	return nil
}
