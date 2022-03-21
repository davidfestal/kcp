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

// Package apiserver provides an apiserver that can dynamically serve resources based on an
// API definition(CommonAPIResourceSpec) source and a Rest storage provider.
//
// Part of this package are highly insired from kcp-dev/kubernetes/staging/src/k8s.io/apiextensions-apiserver/pkg/apiserver
// https://github.com/kcp-dev/kubernetes/tree/feature-logical-clusters-1.23/staging/src/k8s.io/apiextensions-apiserver/pkg/apiserver
package apiserver
