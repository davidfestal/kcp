/*
Copyright 2022 The KCP Authors.

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

package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func namespace(name, clusterName string, labels, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			ClusterName: clusterName,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

func deployment(name, namespace, clusterName string, labels, annotations map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			ClusterName: clusterName,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

type deploymentChange func(*appsv1.Deployment)

func changeDeployment(in *appsv1.Deployment, changes ...deploymentChange) *appsv1.Deployment {
	for _, change := range changes {
		change(in)
	}
	return in
}

func addDeploymentStatus(status appsv1.DeploymentStatus) deploymentChange {
	return func(d *appsv1.Deployment) {
		d.Status = status
	}
}

func toJson(t require.TestingT, object runtime.Object) []byte {
	result, err := json.Marshal(object)
	require.NoError(t, err)
	return result
}

func toUnstructured(t require.TestingT, obj metav1.Object) *unstructured.Unstructured {
	var result unstructured.Unstructured
	err := scheme.Convert(obj, &result, nil)
	require.NoError(t, err)

	return &result
}

type unstructuredChange func(d *unstructured.Unstructured)

func changeUnstructured(in *unstructured.Unstructured, changes ...unstructuredChange) *unstructured.Unstructured {
	for _, change := range changes {
		change(in)
	}
	return in
}

func removeNilOrEmptyFields(in *unstructured.Unstructured) {
	if val, exists, _ := unstructured.NestedFieldNoCopy(in.UnstructuredContent(), "metadata", "creationTimestamp"); val == nil && exists {
		unstructured.RemoveNestedField(in.UnstructuredContent(), "metadata", "creationTimestamp")
	}
	if val, exists, _ := unstructured.NestedMap(in.UnstructuredContent(), "spec"); len(val) == 0 && exists {
		delete(in.Object, "spec")
	}
	if val, exists, _ := unstructured.NestedMap(in.UnstructuredContent(), "status"); len(val) == 0 && exists {
		delete(in.Object, "status")
	}
}

func setNestedField(value interface{}, fields ...string) unstructuredChange {
	return func(d *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(d.UnstructuredContent(), value, fields...)
	}
}

func deploymentAction(verb, namespace string, subresources ...string) clienttesting.ActionImpl {
	return clienttesting.ActionImpl{
		Namespace:   namespace,
		Verb:        verb,
		Resource:    schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Subresource: strings.Join(subresources, "/"),
	}
}

func namespaceAction(verb string, subresources ...string) clienttesting.ActionImpl {
	return clienttesting.ActionImpl{
		Namespace:   "",
		Verb:        verb,
		Resource:    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"},
		Subresource: strings.Join(subresources, "/"),
	}
}

func getDeploymentAction(name, namespace string) clienttesting.GetActionImpl {
	return clienttesting.GetActionImpl{
		ActionImpl: deploymentAction("get", namespace),
		Name:       name,
	}
}

func createNamespaceAction(name string, object runtime.Object) clienttesting.CreateActionImpl {
	return clienttesting.CreateActionImpl{
		ActionImpl: namespaceAction("create"),
		Name:       name,
		Object:     object,
	}
}

func updateDeploymentAction(namespace string, object runtime.Object, subresources ...string) clienttesting.UpdateActionImpl {
	return clienttesting.UpdateActionImpl{
		ActionImpl: deploymentAction("update", namespace, subresources...),
		Object:     object,
	}
}

func patchDeploymentAction(name, namespace string, patchType types.PatchType, patch []byte, subresources ...string) clienttesting.PatchActionImpl {
	return clienttesting.PatchActionImpl{
		ActionImpl: deploymentAction("patch", namespace, subresources...),
		Name:       name,
		PatchType:  patchType,
		Patch:      patch,
	}
}

func TestSyncerProcess(t *testing.T) {
	tests := map[string]struct {
		fromNamespace *corev1.Namespace
		gvr           schema.GroupVersionResource
		fromResource  runtime.Object
		toResources   []runtime.Object

		resourceToProcessName               string
		resourceToProcessLogicalClusterName string

		direction              SyncDirection
		upstreamLogicalCluster string
		workloadClusterName    string

		expectError         bool
		expectActionsOnFrom []clienttesting.Action
		expectActionsOnTo   []clienttesting.Action
	}{
		"SpecSyncer": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: deployment("theDeployment", "test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",

			expectError:         false,
			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
							map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							},
							map[string]string{
								"kcp.dev/namespace-locator": "{\"logical-cluster\":\"root:org:ws\",\"namespace\":\"test\"}",
							})),
						removeNilOrEmptyFields,
					),
				),
				patchDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							}, nil)),
							setNestedField(map[string]interface{}{}, "status"),
						),
					),
				),
			},
		},
		"StatusSyncer": {
			direction:              SyncUp,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
				map[string]string{
					"kcp.dev/namespace-locator": "{\"logical-cluster\":\"root:org:ws\",\"namespace\":\"test\"}",
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil),
			},
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",

			expectError:         false,
			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				getDeploymentAction("theDeployment", "test"),
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "", map[string]string{
							"state.internal.workloads.kcp.dev/us-west1": "Sync",
						}, nil),
						addDeploymentStatus(appsv1.DeploymentStatus{
							Replicas: 15,
						}))),
					"status"),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			kcpLogicalCluster := logicalcluster.New(tc.upstreamLogicalCluster)
			var allFromResources []runtime.Object
			allFromResources = append(allFromResources, tc.fromNamespace)
			allFromResources = append(allFromResources, tc.fromResource)
			fromClient := dynamicfake.NewSimpleDynamicClient(scheme, allFromResources...)
			toClient := dynamicfake.NewSimpleDynamicClient(scheme, tc.toResources...)

			setupServersideApplyPatchReactor(toClient)
			namespaceWatcherStarted := setupWatchReactor("namespaces", fromClient)
			resourceWatcherStarted := setupWatchReactor(tc.gvr.Resource, fromClient)

			gvrs := []string{fmt.Sprintf("%s.%s.%s", tc.gvr.Resource, tc.gvr.Version, tc.gvr.Group), "namespaces.v1."}
			controller, err := New(kcpLogicalCluster, tc.workloadClusterName, fromClient, toClient, tc.direction, gvrs, tc.workloadClusterName, nil)
			require.NoError(t, err)
			controller.fromInformers.Start(ctx.Done())
			controller.fromInformers.WaitForCacheSync(ctx.Done())
			<-resourceWatcherStarted
			<-namespaceWatcherStarted
			fromClient.ClearActions()
			toClient.ClearActions()

			err = controller.process(context.Background(), holder{
				gvr: schema.GroupVersionResource{
					Group:    "apps",
					Version:  "v1",
					Resource: "deployments",
				},
				clusterName: logicalcluster.New(tc.resourceToProcessLogicalClusterName),
				namespace:   tc.fromNamespace.Name,
				name:        tc.resourceToProcessName,
			})
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.EqualValues(t, tc.expectActionsOnFrom, fromClient.Actions())
			assert.EqualValues(t, tc.expectActionsOnTo, toClient.Actions())

		})
	}
}

func setupServersideApplyPatchReactor(toClient *dynamicfake.FakeDynamicClient) {
	toClient.PrependReactor("patch", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(clienttesting.PatchAction)
		if patchAction.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		return true, nil, err
	})
}

func setupWatchReactor(resource string, fromClient *dynamicfake.FakeDynamicClient) chan struct{} {
	watcherStarted := make(chan struct{})
	fromClient.PrependWatchReactor(resource, func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		gvr := action.GetResource()
		ns := action.GetNamespace()
		watch, err := fromClient.Tracker().Watch(gvr, ns)
		if err != nil {
			return false, nil, err
		}
		close(watcherStarted)
		return true, watch, nil
	})
	return watcherStarted
}
