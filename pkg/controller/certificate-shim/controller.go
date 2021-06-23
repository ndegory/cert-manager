/*
Copyright 2020 The cert-manager Authors.

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

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1alpha1"
	gatewayclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gatewayinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"

	cmapi "github.com/jetstack/cert-manager/pkg/apis/certmanager/v1"
	clientset "github.com/jetstack/cert-manager/pkg/client/clientset/versioned"
	cmlisters "github.com/jetstack/cert-manager/pkg/client/listers/certmanager/v1"
	controllerpkg "github.com/jetstack/cert-manager/pkg/controller"
	"github.com/jetstack/cert-manager/pkg/issuer"
	logf "github.com/jetstack/cert-manager/pkg/logs"
)

const (
	// IngressShimControllerName is the name of the ingress-shim controller.
	IngressShimControllerName = "ingress-shim"

	// GatewayShimControllerName is the name of the gateway-shim controller.
	GatewayShimControllerName = "gateway-shim"
)

type defaults struct {
	autoCertificateAnnotations          []string
	issuerName, issuerKind, issuerGroup string
}

type ingressShim struct {
	controller
}

func (i *ingressShim) Register(ctx *controllerpkg.Context) (workqueue.RateLimitingInterface, []cache.InformerSynced, error) {
	// construct a new named logger to be reused throughout the controller
	i.log = logf.FromContext(ctx.RootContext, IngressShimControllerName)

	// create a queue used to queue up items to be processed
	i.queue = workqueue.NewNamedRateLimitingQueue(controllerpkg.DefaultItemBasedRateLimiter(), IngressShimControllerName)

	ingressInformer := ctx.KubeSharedInformerFactory.Networking().V1beta1().Ingresses()
	i.objectInformer = ingressInformer.Informer()

	i.objectLister = &internalIngressLister{ingressInformer.Lister()}

	return i.sharedRegister(ctx)
}

type gatewayShim struct {
	controller
}

func (g *gatewayShim) Register(ctx *controllerpkg.Context) (workqueue.RateLimitingInterface, []cache.InformerSynced, error) {
	// construct a new named logger to be reused throughout the controller
	g.log = logf.FromContext(ctx.RootContext, GatewayShimControllerName)

	g.queue = workqueue.NewNamedRateLimitingQueue(controllerpkg.DefaultItemBasedRateLimiter(), GatewayShimControllerName)

	// Discover if the gateway api is available
	d, err := discovery.NewDiscoveryClientForConfig(ctx.RESTConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: couldn't construct discovery client: %w", GatewayShimControllerName, err)
	}
	resources, err := d.ServerResourcesForGroupVersion(gatewayapi.GroupVersion.String())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: couldn't discover gateway API resources: %w", GatewayShimControllerName, err)
	}
	if len(resources.APIResources) == 0 {
		return nil, nil, fmt.Errorf("%s: no gateway API resources were discovered (are the Gateway API CRDS installed?)", GatewayShimControllerName)
	}

	// 10 hours is default: https://github.com/kubernetes-sigs/controller-runtime/pull/88#issuecomment-408500629
	gatewayInformerFactory := gatewayinformers.NewSharedInformerFactory(gatewayclient.NewForConfigOrDie(ctx.RESTConfig), 10*time.Hour)
	gatewayInformerLister := gatewayInformerFactory.Networking().V1alpha1().Gateways()
	g.objectInformer = gatewayInformerLister.Informer()
	gatewayInformerFactory.Start(ctx.StopCh)

	g.objectLister = &internalGatewayLister{gatewayInformerLister.Lister()}

	return g.sharedRegister(ctx)
}

type controller struct {
	// maintain a reference to the workqueue for this controller
	// so the handleOwnedResource method can enqueue resources
	queue workqueue.RateLimitingInterface

	// logger to be used by this controller
	log logr.Logger

	kClient  kubernetes.Interface
	cmClient clientset.Interface
	recorder record.EventRecorder

	objectLister        objectLister
	objectInformer      cache.SharedIndexInformer
	certificateLister   cmlisters.CertificateLister
	issuerLister        cmlisters.IssuerLister
	clusterIssuerLister cmlisters.ClusterIssuerLister

	helper   issuer.Helper
	defaults defaults
}

// Register registers and constructs the controller using the provided context.
// It returns the workqueue to be used to enqueue items, a list of
// InformerSynced functions that must be synced, or an error.
func (c *controller) sharedRegister(ctx *controllerpkg.Context) (workqueue.RateLimitingInterface, []cache.InformerSynced, error) {
	certificatesInformer := ctx.SharedInformerFactory.Certmanager().V1().Certificates()
	issuerInformer := ctx.SharedInformerFactory.Certmanager().V1().Issuers()
	// build a list of InformerSynced functions that will be returned by the Register method.
	// the controller will only begin processing items once all of these informers have synced.
	mustSync := []cache.InformerSynced{
		c.objectInformer.HasSynced,
		certificatesInformer.Informer().HasSynced,
		issuerInformer.Informer().HasSynced,
	}

	// set all the references to the listers for used by the Sync function
	c.certificateLister = certificatesInformer.Lister()
	c.issuerLister = issuerInformer.Lister()

	// if scoped to a single namespace
	// if we are running in non-namespaced mode (i.e. --namespace=""), we also
	// register event handlers and obtain a lister for clusterissuers.
	if ctx.Namespace == "" {
		clusterIssuerInformer := ctx.SharedInformerFactory.Certmanager().V1().ClusterIssuers()
		mustSync = append(mustSync, clusterIssuerInformer.Informer().HasSynced)
		c.clusterIssuerLister = clusterIssuerInformer.Lister()
	}

	// register handler functions
	c.objectInformer.AddEventHandler(&controllerpkg.QueuingEventHandler{Queue: c.queue})
	certificatesInformer.Informer().AddEventHandler(&controllerpkg.BlockingEventHandler{WorkFunc: c.certificateDeleted})

	c.helper = issuer.NewHelper(c.issuerLister, c.clusterIssuerLister)
	c.kClient = ctx.Client
	c.cmClient = ctx.CMClient
	c.recorder = ctx.Recorder
	c.defaults = defaults{
		ctx.DefaultAutoCertificateAnnotations,
		ctx.DefaultIssuerName,
		ctx.DefaultIssuerKind,
		ctx.DefaultIssuerGroup,
	}

	return c.queue, mustSync, nil
}

func (c *controller) certificateDeleted(obj interface{}) {
	crt, ok := obj.(*cmapi.Certificate)
	if !ok {
		runtime.HandleError(fmt.Errorf("Object is not a certificate object %#v", obj))
		return
	}
	objs, err := c.objectsForCertificate(crt)
	if err != nil {
		runtime.HandleError(fmt.Errorf("Error looking up ingress observing certificate: %s/%s", crt.Namespace, crt.Name))
		return
	}
	for _, o := range objs {
		key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(o)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		c.queue.Add(key)
	}
}

func (c *controller) ProcessItem(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	obj, err := c.objectLister.Objects(namespace).Get(name)

	if err != nil {
		if k8sErrors.IsNotFound(err) {
			runtime.HandleError(fmt.Errorf("object '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	return c.Sync(ctx, obj)
}

func init() {
	controllerpkg.Register(IngressShimControllerName, func(ctx *controllerpkg.Context) (controllerpkg.Interface, error) {
		return controllerpkg.NewBuilder(ctx, IngressShimControllerName).
			For(&ingressShim{}).
			Complete()
	})
	controllerpkg.Register(GatewayShimControllerName, func(ctx *controllerpkg.Context) (controllerpkg.Interface, error) {
		return controllerpkg.NewBuilder(ctx, GatewayShimControllerName).
			For(&gatewayShim{}).
			Complete()
	})
}