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

package apiDefs

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/registry/rest"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
)

type APIDomainKeyContextKeyType string

const APIDomainKeyContextKey APIDomainKeyContextKeyType = "VirtualWorkspaceAPIDomainLocationKey"

type APIDefinition interface {
	GetAPIResourceSpec() *apiresourcev1alpha1.CommonAPIResourceSpec
	GetClusterName() string
	GetStorage() rest.Storage
	GetSubResourceStorage(subresource string) rest.Storage
	GetRequestScope() *handlers.RequestScope
	GetSubResourceRequestScope(subresource string) *handlers.RequestScope
}

type APISet map[schema.GroupVersionResource]APIDefinition

type APISetRetriever interface {
	GetAPIs(apiDomainKey string) (apis APISet, apisExists bool)
}
