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

func Test_removeAnnotationFromPersistentVolumeClaim(t *testing.T) {
	pvc := pvc("foo",
		"ns",
		"cluster1",
		nil,
		map[string]string{
			"kcp.dev/namespace-locator":                    `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
			"internal.workload.kcp.dev/delaystatussyncing": "true",
		},
		corev1.ClaimBound)
	removeAnnotationFromPersistentVolumeClaim(pvc)
	assert.Empty(t, pvc.GetAnnotations()["internal.workload.kcp.dev/delaystatussyncing"])
}
