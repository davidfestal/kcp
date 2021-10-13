package syncer

import (
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
)

type SyncerBuilder func(upstream, downstream *rest.Config, resources sets.String, numSyncerThreads int)  (*Syncer, error)

func BuildSyncerToPhysicalLocation(locationName string) SyncerBuilder {
	assignedToTheLocation := labels.SelectorFromSet(map[string]string {"kcp.dev/cluster": locationName})
	return func(upstream, downstream *rest.Config, resources sets.String, numSyncerThreads int)  (*Syncer, error) {
		return StartSyncer(upstream, downstream, assignedToTheLocation, NewIdentitySyncing(nil), resources, numSyncerThreads)
	}
}

func BuildCustomSyncerToPrivateLogicalCluster(transformerClass string, syncing Syncing) (SyncerBuilder, error) {
	syncing.LabelsToAdd()["kcp.dev/private"] = ""

	assignedToALocation, err := labels.NewRequirement("kcp.dev/cluster", selection.Exists, []string{})
	if err != nil {
		return nil, err
	}
	var tranformerReq *labels.Requirement
	if transformerClass == "" {
		tranformerReq, err = labels.NewRequirement("kcp.dev/transformer", selection.DoesNotExist, []string{})
		if err != nil {
			return nil, err
		}	
	} else {
		tranformerReq, err = labels.NewRequirement("kcp.dev/transformer", selection.Equals, []string{ transformerClass })
		if err != nil {
			return nil, err
		}
	}

	return func(upstream, downstream *rest.Config, resources sets.String, numSyncerThreads int)  (*Syncer, error) {
		return StartSyncer(upstream, downstream, labels.NewSelector().Add(*assignedToALocation, *tranformerReq), syncing, resources, numSyncerThreads)
	}, nil
}

func BuildDefaultSyncerToPrivateLogicalCluster() (SyncerBuilder, error) {
	return BuildCustomSyncerToPrivateLogicalCluster("", NewIdentitySyncing(nil))
}
