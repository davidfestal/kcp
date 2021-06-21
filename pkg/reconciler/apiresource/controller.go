package apiresource

import (
	"context"
	"io/ioutil"
	"log"
	"time"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	typedapiresource "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/apiresource/v1alpha1"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apiresourcelister "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/util/errors"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	typedapiextensions "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	crdlister "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
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
const ClusterNameAndGVRIndexName = "clusterNameAndGVR"

func GetClusterNameAndGVRIndexKey(clusterName string, gvr metav1.GroupVersionResource) string {
	return clusterName + "$" + gvr.String()
}

func NewController(cfg *rest.Config, autoPublishNegociatedAPIResource bool) *Controller {
	apiresourceClient := typedapiresource.NewForConfigOrDie(cfg)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	stopCh := make(chan struct{}) // TODO: hook this up to SIGTERM/SIGINT

	crdClient := typedapiextensions.NewForConfigOrDie(cfg)

	c := &Controller{
		queue:             queue,
		apiresourceClient: apiresourceClient,
		crdClient:         crdClient,
		stopCh:            stopCh,
		AutoPublishNegociatedAPIResource: autoPublishNegociatedAPIResource,
	}

	apiresourceSif := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(cfg), resyncPeriod)
	apiresourceSif.Apiresource().V1alpha1().NegociatedAPIResources().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(Add, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(Update, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(Delete, nil, obj) },
	})
	c.negociatedApiResourceIndexer = apiresourceSif.Apiresource().V1alpha1().NegociatedAPIResources().Informer().GetIndexer()
	c.negociatedApiResourceIndexer.AddIndexers(map[string]cache.IndexFunc {
		ClusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if negociatedApiResource, ok := obj.(*apiresourcev1alpha1.NegociatedAPIResource); ok {
				return []string{ GetClusterNameAndGVRIndexKey(negociatedApiResource.ClusterName, negociatedApiResource.GVR())}, nil
			}
			return []string{}, nil
		},
	})	
	c.negociatedApiResourceLister = apiresourceSif.Apiresource().V1alpha1().NegociatedAPIResources().Lister()
	apiresourceSif.Apiresource().V1alpha1().APIResourceImports().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(Add, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(Update, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(Delete, nil, obj) },
	})
	c.apiResourceImportIndexer = apiresourceSif.Apiresource().V1alpha1().APIResourceImports().Informer().GetIndexer()
	c.apiResourceImportIndexer.AddIndexers(map[string]cache.IndexFunc {
		ClusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{ GetClusterNameAndGVRIndexKey(apiResourceImport.ClusterName, apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
	})	
	c.apiResourceImportLister = apiresourceSif.Apiresource().V1alpha1().APIResourceImports().Lister()

	apiresourceSif.WaitForCacheSync(stopCh)
	apiresourceSif.Start(stopCh)

	crdSif := crdexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsclient.NewForConfigOrDie(cfg), resyncPeriod)
	crdSif.Apiextensions().V1().CustomResourceDefinitions().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(Add, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(Update, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(Delete, nil, obj) },
	})
	c.crdIndexer = crdSif.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer()
	c.crdIndexer.AddIndexers(map[string]cache.IndexFunc {
		ClusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
				return []string{ GetClusterNameAndGVRIndexKey(crd.ClusterName, metav1.GroupVersionResource{
					Group: crd.Spec.Group,
					Resource: crd.Spec.Names.Plural,
				})}, nil
			}
			return []string{}, nil
		},
	})	
	c.crdLister = crdSif.Apiextensions().V1().CustomResourceDefinitions().Lister()
	crdSif.WaitForCacheSync(stopCh)
	crdSif.Start(stopCh)

	return c
}

type Controller struct {
	queue workqueue.RateLimitingInterface

	apiresourceClient            typedapiresource.ApiresourceV1alpha1Interface
	negociatedApiResourceIndexer cache.Indexer
	negociatedApiResourceLister  apiresourcelister.NegociatedAPIResourceLister
	apiResourceImportIndexer     cache.Indexer
	apiResourceImportLister      apiresourcelister.APIResourceImportLister

	crdClient  typedapiextensions.ApiextensionsV1Interface
	crdIndexer cache.Indexer
	crdLister  crdlister.CustomResourceDefinitionLister

	kubeconfig clientcmdapi.Config
	stopCh     chan struct{}
	AutoPublishNegociatedAPIResource bool
}

type QueueElementType string

const (
	CustomResourceDefinitionType QueueElementType = "CustomResourceDefinition"
	NegociatedAPIResourceType    QueueElementType = "NegociatedAPIResource"
	APIResourceImportType        QueueElementType = "APIResourceImport"
)

type QueueElementAction string

const (
	SpecChangedAction             QueueElementAction = "SpecChanged"
	StatusOnlyChangedAction       QueueElementAction = "StatusOnlyChanged"
	AnnotationOrLabelsOnlyChanged QueueElementAction = "AnnotationOrLabelsOnlyChanged"
	Deleted                       QueueElementAction = "Deleted"
	Created                       QueueElementAction = "Created"
)

type ResourceHandlerAction string

const (
	Add    ResourceHandlerAction = "Add"
	Update ResourceHandlerAction = "Update"
	Delete ResourceHandlerAction = "Delete"
)

type queueElement struct {
	theAction QueueElementAction
	theType   QueueElementType
	theKey    string
	gvr       metav1.GroupVersionResource
	clusterName string
	deletedObject interface{}
}

func toQueueElementType(oldObj, obj interface{}) (theType QueueElementType, gvr metav1.GroupVersionResource, oldMeta, newMeta metav1.Object, oldStatus, newStatus interface{}) {
	switch typedObj := obj.(type) {
	case *apiextensionsv1.CustomResourceDefinition:
		theType = CustomResourceDefinitionType
		newMeta = typedObj
		newStatus = typedObj.Status
		if oldObj != nil {
			typedOldObj := oldObj.(*apiextensionsv1.CustomResourceDefinition)
			oldStatus = typedOldObj.Status
			oldMeta = typedOldObj
		}
		gvr = metav1.GroupVersionResource{
			Group:    typedObj.Spec.Group,
			Resource: typedObj.Spec.Names.Plural,
		}
	case *apiresourcev1alpha1.APIResourceImport:
		theType = APIResourceImportType
		newMeta = typedObj
		newStatus = typedObj.Status
		if oldObj != nil {
			typedOldObj := oldObj.(*apiresourcev1alpha1.APIResourceImport)
			oldStatus = typedOldObj.Status
			oldMeta = typedOldObj
		}
		gvr = metav1.GroupVersionResource{
			Group:    typedObj.Spec.Group,
			Version:  typedObj.Spec.Version,
			Resource: typedObj.Spec.Plural,
		}
	case *apiresourcev1alpha1.NegociatedAPIResource:
		theType = NegociatedAPIResourceType
		newMeta = typedObj
		newStatus = typedObj.Status
		if oldObj != nil {
			typedOldObj := oldObj.(*apiresourcev1alpha1.NegociatedAPIResource)
			oldStatus = typedOldObj.Status
			oldMeta = typedOldObj
		}
		gvr = metav1.GroupVersionResource{
			Group:    typedObj.Spec.Group,
			Version:  typedObj.Spec.Version,
			Resource: typedObj.Spec.Plural,
		}
	case cache.DeletedFinalStateUnknown:
		tombstone := typedObj
		theType, gvr, oldMeta, newMeta, oldStatus, newStatus = toQueueElementType(nil, tombstone.Obj)
		if theType == "" {
			klog.Errorf("Tombstone contained object that is not expected %#v", obj)
		}
	}
	return
}

func (c *Controller) enqueue(action ResourceHandlerAction, oldObj, obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if obj == nil {
		return
	}

	theType, gvr, oldMeta, newMeta, oldStatus, newStatus := toQueueElementType(oldObj, obj)
	var theAction QueueElementAction
	var deletedObject interface{}

	switch action {
	case "Add":
		theAction = Created
	case "Update":
		if oldMeta == nil {
			theAction = Created
			break
		}

		if oldMeta.GetResourceVersion() == newMeta.GetResourceVersion() {
			return
		}

		if oldMeta.GetGeneration() != newMeta.GetGeneration() {
			theAction = SpecChangedAction
			break
		}

		if !equality.Semantic.DeepEqual(oldStatus, newStatus) {
			theAction = StatusOnlyChangedAction
			break
		}

		if !equality.Semantic.DeepEqual(oldMeta.GetAnnotations(), newMeta.GetAnnotations()) ||
			equality.Semantic.DeepEqual(oldMeta.GetLabels(), newMeta.GetLabels()) {
			theAction = AnnotationOrLabelsOnlyChanged
			break
		}
		// Nothing significant changed. Ignore the event.
		return
	case "Delete":
		theAction = Deleted
		deletedObject = obj
	}

	c.queue.AddRateLimited(queueElement{
		theAction: theAction,
		theType:   theType,
		theKey:    key,
		gvr:       gvr,
		clusterName: newMeta.GetClusterName(),
		deletedObject: deletedObject,
	})
}

func (c *Controller) Start(numThreads int) {
	for i := 0; i < numThreads; i++ {
		go wait.Until(c.startWorker, time.Second, c.stopCh)
	}
	log.Println("Starting workers")
}

// Stop stops the controller.
func (c *Controller) Stop() {
	log.Println("Stopping workers")
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
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(queueElement)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	err := c.process(key)
	c.handleErr(err, key)
	return true
}

func (c *Controller) handleErr(err error, key queueElement) {
	// Reconcile worked, nothing else to do for this workqueue item.
	if err == nil {
		log.Println("Successfully reconciled", key)
		c.queue.Forget(key)
		return
	}

	// Re-enqueue up to 5 times.
	num := c.queue.NumRequeues(key)
	if errors.IsRetryable(err) || num < 5 {
		log.Printf("Error reconciling key %q, retrying... (#%d): %v", key, num, err)
		c.queue.AddRateLimited(key)
		return
	}

	// Give up and report error elsewhere.
	c.queue.Forget(key)
	runtime.HandleError(err)
	log.Printf("Dropping key %q after failed retries: %v", key, err)
}

func RegisterClusterCRD(cfg *rest.Config) error {
	bytes, err := ioutil.ReadFile("config/cluster.example.dev_clusters.yaml")

	crdClient := typedapiextensions.NewForConfigOrDie(cfg)

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
