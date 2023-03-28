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

package mutatingwebhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	admissionv1 "k8s.io/api/admission/v1"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/configuration"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/config"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/mutating"
	webhookutil "k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/admission/webhook"
	"github.com/kcp-dev/kcp/pkg/tunnel"
)

const (
	PluginName = "apis.kcp.io/MutatingWebhook"
)

type Plugin struct {
	// Using validating plugin, for the dispatcher to use.
	// This plugins admit function will never be called.
	mutating.Plugin
	*webhook.WebhookDispatcher

	tunneler *tunnel.Tunneler
}

var (
	_ = admission.MutationInterface(&Plugin{})
	_ = admission.InitializationValidator(&Plugin{})
	_ = kcpinitializers.WantsKcpInformers(&Plugin{})
	_ = kcpinitializers.WantsKubeInformers(&Plugin{})
	_ = kcpinitializers.WantsTunneler(&Plugin{})
)

func NewMutatingAdmissionWebhook(configfile io.Reader) (*Plugin, error) {
	p := &Plugin{
		Plugin:            mutating.Plugin{Webhook: &generic.Webhook{}},
		WebhookDispatcher: webhook.NewWebhookDispatcher(),
	}
	p.WebhookDispatcher.Handler = admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update)

	dispatcherFactory := mutating.NewMutatingDispatcher(&p.Plugin)

	// Making our own dispatcher so that we can control the webhook accessors.
	kubeconfigFile, err := config.LoadConfig(configfile)
	if err != nil {
		return nil, err
	}
	cm, err := webhookutil.NewClientManager(
		[]schema.GroupVersion{
			admissionv1beta1.SchemeGroupVersion,
			admissionv1.SchemeGroupVersion,
		},
		admissionv1beta1.AddToScheme,
		admissionv1.AddToScheme,
	)
	if err != nil {
		return nil, err
	}
	authInfoResolver, err := webhookutil.NewDefaultAuthenticationInfoResolver(kubeconfigFile)
	if err != nil {
		return nil, err
	}

	// TODO(davidfestal): let's have these values fixed for now, just to test.
	// This should be replaced by functions that use the cached SyncTarget informer
	// as well as logic similar to what is used in the SubResourceProxyHandler.
	physicalNamespace := "kcp-2nrk8iaz3r87"
	syncTargetLogicalCluster := logicalcluster.Name("2s6kr9ttj9j4s0eu")
	syncTargetName := "west"

	// Set defaults which may be overridden later.
	cm.SetAuthenticationInfoResolver(authInfoResolver)

	// TODO(davidfestal): not sure it is really useful, it only overrides the addr passed to the dialer
	// in the clientManager.
	// However we don't use the address in the tunneler dialer.
	// The host passed to the http request will still be the one hard-coded ("name.namespace.svc").
	cm.SetServiceResolver(&kcpServiceResolver{
		transformNamespace: func(namespace string) (string, error) {
			return physicalNamespace, nil
		},
	})
	cm.SetAuthenticationInfoResolverWrapper(func(delegate webhookutil.AuthenticationInfoResolver) webhookutil.AuthenticationInfoResolver {
		return &webhookutil.AuthenticationInfoResolverDelegator{
			ClientConfigForFunc: delegate.ClientConfigFor,
			ClientConfigForServiceFunc: func(serviceName, serviceNamespace string, servicePort int) (*rest.Config, error) {
				config, err := delegate.ClientConfigForService(serviceName, serviceNamespace, servicePort)
				if err != nil {
					return nil, err
				}
				// Overriding the Dial function here with the dial method of the right tunneler Dialer,
				// means that the tls handshake will in fact be done on the Syncer side.
				// But for this the connection handler on the Syncer side should be able to detect
				// this situation and react accordingly.
				// For this leads to the request being rejected by the http server on the syncer side
				// because it is not valid http (of course, this is the tls handshake, which is not part
				// of the http request content).
				config.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
					dialer := p.tunneler.GetDialer(syncTargetLogicalCluster, syncTargetName)
					if dialer == nil {
						return nil, errors.New("no SyncTarget tunnel available")
					}
					return dialer.Dial(ctx, network, address)
				}
				return config, nil
			},
		}
	})

	p.WebhookDispatcher.SetDispatcher(dispatcherFactory(&cm))
	// Need to do this, to make sure that the underlying objects for the call to ShouldCallHook have the right values
	p.Plugin.Webhook, err = generic.NewWebhook(p.Handler, configfile, configuration.NewMutatingWebhookConfigurationManager, dispatcherFactory)
	if err != nil {
		return nil, err
	}

	// Override the ready func

	p.SetReadyFunc(func() bool {
		if p.WebhookDispatcher.HasSynced() && p.Plugin.WaitForReady() {
			return true
		}
		return false
	})
	return p, nil
}

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewMutatingAdmissionWebhook(configFile)
	})
}

func (p *Plugin) Admit(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	return p.WebhookDispatcher.Dispatch(ctx, attr, o)
}

// SetTunneler implements the WantsTunneler interface.
func (p *Plugin) SetTunneler(tunneler *tunnel.Tunneler) {
	p.tunneler = tunneler
}

// SetExternalKubeInformerFactory implements the WantsExternalKubeInformerFactory interface.
func (p *Plugin) SetExternalKubeInformerFactory(f informers.SharedInformerFactory) {
	p.Plugin.SetExternalKubeInformerFactory(f) // for namespaces
}

func (p *Plugin) SetKubeInformers(local, global kcpkubernetesinformers.SharedInformerFactory) {
	p.WebhookDispatcher.SetHookSource(func(cluster logicalcluster.Name) generic.Source {
		informer := global.Admissionregistration().V1().MutatingWebhookConfigurations().Cluster(cluster)
		return configuration.NewMutatingWebhookConfigurationManagerForInformer(informer)
	}, global.Admissionregistration().V1().MutatingWebhookConfigurations().Informer().HasSynced)
}

type kcpServiceResolver struct {
	transformNamespace func(namespace string) (string, error)
}

func (sr kcpServiceResolver) ResolveEndpoint(namespace, name string, port int32) (*url.URL, error) {
	if len(name) == 0 || len(namespace) == 0 || port == 0 {
		return nil, errors.New("cannot resolve an empty service name or namespace or port")
	}
	namespace, err := sr.transformNamespace(namespace)
	if err != nil {
		return nil, err
	}
	return &url.URL{Scheme: "https", Host: fmt.Sprintf("%s.%s.svc:%d", name, namespace, port)}, nil
}
