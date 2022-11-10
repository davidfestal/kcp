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

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
)

func Test_rmKeyFromJSON(t *testing.T) {
	type args struct {
		jsonBlob string
		key      string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{"rm 'a' key", args{`{"a": 1, "b": 2}`, "a"}, `{"b":2}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rmKeyFromJSON(tt.args.jsonBlob, tt.args.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("rmKeyFromJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("rmKeyFromJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_addNsAllocatorToDownstreamPV(t *testing.T) {
	nsLocator := `{"syncTarget":{"workspace":"test-workspace","name":"test-name","uid":"test-uid"},"workspace":"test-workspace","namespace":"test-namespace"}`
	pv := &corev1.PersistentVolume{}
	var err error
	pv, err = addNsAllocatorToDownstreamPV(nsLocator, pv)
	assert.NoError(t, err)
	assert.NotEmpty(t, pv.GetAnnotations()["kcp.dev/namespace-locator"])
}

func Test_claimRefJSONPatchAnnotation(t *testing.T) {
	type args struct {
		downstreamClaimRef *corev1.ObjectReference
		upstreamClaimRef   *corev1.ObjectReference
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{"claimRef patch", args{&corev1.ObjectReference{UID: "foo"}, &corev1.ObjectReference{UID: "bar"}}, `{"uid":"bar"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := claimRefJSONPatchAnnotation(tt.args.downstreamClaimRef, tt.args.upstreamClaimRef)
			if (err != nil) != tt.wantErr {
				t.Errorf("claimRefJSONPatchAnnotation() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("claimRefJSONPatchAnnotation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestController_addResourceStateUpsyncLabel(t *testing.T) {
	type fields struct {
		syncTarget syncTargetSpec
	}
	type args struct {
		labels map[string]string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{"add label on empty labels", fields{syncTargetSpec{key: "sync-key"}}, args{map[string]string{}}},
		{"add label on non-empty labels", fields{syncTargetSpec{key: "sync-key"}}, args{map[string]string{"foo": "bar"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &PersistentVolumeClaimController{
				syncTarget: tt.fields.syncTarget,
			}

			got := c.addResourceStateUpsyncLabel(tt.args.labels)
			if _, ok := got["state.workload.kcp.dev/sync-key"]; !ok {
				t.Errorf("addResourceStateUpsyncLabel() = %v, want %v", got, tt.args.labels)
			}
		})
	}
}
