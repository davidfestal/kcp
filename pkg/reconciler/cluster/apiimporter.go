package cluster

import (
	"context"
	"log"
	"time"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type APIImporter struct {
	c                  *Controller
	physicalClusterID  string
	logicalClusterName string
	schemaPuller       crdpuller.SchemaPuller
	done               chan bool
}

func (c *Controller) StartAPIImporter(config *rest.Config, physicalClusterID string, logicalClusterName string, pollInterval time.Duration) (*APIImporter, error) {
	apiImporter := APIImporter{
		c:                  c,
		physicalClusterID:  physicalClusterID,
		logicalClusterName: logicalClusterName,
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	apiImporter.done = make(chan bool)

	var err error
	apiImporter.schemaPuller, err = crdpuller.NewSchemaPuller(config)
	if err != nil {
		return nil, err
	}

	go func() {
		for {
			select {
			case <-apiImporter.done:
				return
			case <-ticker.C:
				apiImporter.ImportAPIs()
			}
		}
	}()

	return &apiImporter, nil
}

func (i *APIImporter) Stop() {
	i.done <- true
}

func (i *APIImporter) ImportAPIs() {
	crds, err := i.schemaPuller.PullCRDs(context.Background(), i.c.resourcesToSync...)
	if err != nil {
		log.Printf("error pulling CRDs: %v", err)
	}

	for resourceName, pulledCrd := range crds {

		gvr := metav1.GroupVersionResource{
			Group:    pulledCrd.Spec.Group,
			Version:  pulledCrd.Spec.Versions[0].Name,
			Resource: pulledCrd.Spec.Names.Plural,
		}

		// TODO: Dans l'index inclure aussi le cluster physique ?

		var apiResourceImport *apiresourcev1alpha1.APIResourceImport
		objs, err := i.c.apiresourceImportIndexer.ByIndex(apiresource.ClusterNameAndGVRIndexName, apiresource.GetClusterNameAndGVRIndexKey(i.logicalClusterName, gvr))
		if err != nil {
			log.Printf("error pulling CRDs: %v", err)
			continue
		}
		for _, obj := range objs {
			typedObj := obj.(*apiresourcev1alpha1.APIResourceImport)
			if typedObj.Spec.Location == i.physicalClusterID {
				apiResourceImport = typedObj
			}
		}

		print(resourceName, apiResourceImport)
		// S'il existe : mettre à jour à partir de la CRD 'en gardant le même nom)
		// Sinon en créer un à partir de la CRD (penser à positionner le (logical) clusterName dans ce cas)
	}

}
