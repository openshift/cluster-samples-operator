package operator

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	kubeset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	imagev1 "github.com/openshift/api/image/v1"
	sampopapi "github.com/openshift/api/samples/v1"
	templatev1 "github.com/openshift/api/template/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imageset "github.com/openshift/client-go/image/clientset/versioned"
	imageinformers "github.com/openshift/client-go/image/informers/externalversions"
	sampleclientv1 "github.com/openshift/client-go/samples/clientset/versioned"
	sampopinformers "github.com/openshift/client-go/samples/informers/externalversions"
	templateset "github.com/openshift/client-go/template/clientset/versioned"
	templateinformers "github.com/openshift/client-go/template/informers/externalversions"

	sampcache "github.com/openshift/cluster-samples-operator/pkg/cache"
	sampopclient "github.com/openshift/cluster-samples-operator/pkg/client"
	operatorstatus "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
	"github.com/openshift/cluster-samples-operator/pkg/stub"
	"github.com/openshift/cluster-samples-operator/pkg/util"
)

const (
	defaultResyncDuration = 10 * time.Minute
)

type Controller struct {
	restconfig *restclient.Config
	cvowrapper *operatorstatus.ClusterOperatorHandler

	crWorkqueue workqueue.RateLimitingInterface
	isWorkqueue workqueue.RateLimitingInterface
	tWorkqueue  workqueue.RateLimitingInterface

	ocSecWorkqueue workqueue.RateLimitingInterface

	crInformer cache.SharedIndexInformer
	isInformer cache.SharedIndexInformer
	tInformer  cache.SharedIndexInformer

	ocSecInformer cache.SharedIndexInformer

	kubeOCNSInformerFactory kubeinformers.SharedInformerFactory
	imageInformerFactory    imageinformers.SharedInformerFactory
	templateInformerFactory templateinformers.SharedInformerFactory
	sampopInformerFactory   sampopinformers.SharedInformerFactory

	listers *sampopclient.Listers

	handlerStub *stub.Handler
}

func NewController() (*Controller, error) {
	kubeconfig, err := sampopclient.GetConfig()
	if err != nil {
		return nil, err
	}
	operatorClient, err := configv1client.NewForConfig(kubeconfig)
	if err != nil {
		return nil, err
	}

	listers := &sampopclient.Listers{}
	c := &Controller{
		restconfig:     kubeconfig,
		cvowrapper:     operatorstatus.NewClusterOperatorHandler(operatorClient),
		crWorkqueue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "samplesconfig-changes"),
		isWorkqueue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "imagestream-changes"),
		tWorkqueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "template-changes"),
		ocSecWorkqueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "openshift-config-namespace-secret-changes"),
		listers:        listers,
	}

	// Initial event to bootstrap CR if it doesn't exist.
	c.crWorkqueue.AddRateLimited(sampopapi.ConfigName)

	kubeClient, err := kubeset.NewForConfig(c.restconfig)
	if err != nil {
		return nil, err
	}

	imageClient, err := imageset.NewForConfig(c.restconfig)
	if err != nil {
		return nil, err
	}

	templateClient, err := templateset.NewForConfig(c.restconfig)
	if err != nil {
		return nil, err
	}

	sampopClient, err := sampleclientv1.NewForConfig(c.restconfig)
	if err != nil {
		return nil, err
	}

	c.kubeOCNSInformerFactory = kubeinformers.NewFilteredSharedInformerFactory(kubeClient, defaultResyncDuration, "openshift-config", nil)
	//TODO - eventually a k8s go-client deps bump will lead to the form below, similar to the image registry operator's kubeinformer initialization,
	// and similar to what is available with the openshift go-client for imagestreams and templates
	//kubeInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, defaultResyncDuration, kubeinformers.WithNamespace("kube-system"))
	c.imageInformerFactory = imageinformers.NewSharedInformerFactoryWithOptions(imageClient, defaultResyncDuration, imageinformers.WithNamespace("openshift"))
	c.templateInformerFactory = templateinformers.NewSharedInformerFactoryWithOptions(templateClient, defaultResyncDuration, templateinformers.WithNamespace("openshift"))
	c.sampopInformerFactory = sampopinformers.NewSharedInformerFactory(sampopClient, defaultResyncDuration)

	// A note on the fact we are listening on secrets in the openshift-config namespace, even though we no longer
	// copy that secret to the openshift namespace for imagestream import
	// 1) we still inspect that secret to make sure it has credentials for registry.redhat.io, unless the samples
	// registry is overriden.  If those credentials don't exist, the imagestream imports will fail.  We capture these
	// results in prometheus metrics/alerts.
	// 2) we employ the lister/sharedinformer/workqueue controller apparatus to get cached versions of the data, so
	// we do not have to hit the API server everytime somebody queries our prometheus stuff
	// 3) however, you have to go all the way with this, including to workqueue, to get the underlying watches so the
	// cache is at least initially populated
	c.ocSecInformer = c.kubeOCNSInformerFactory.Core().V1().Secrets().Informer()
	c.listers.ConfigNamespaceSecrets = c.kubeOCNSInformerFactory.Core().V1().Secrets().Lister().Secrets("openshift-config")

	c.isInformer = c.imageInformerFactory.Image().V1().ImageStreams().Informer()
	c.isInformer.AddEventHandler(c.imagestreamInformerEventHandler())
	c.listers.ImageStreams = c.imageInformerFactory.Image().V1().ImageStreams().Lister().ImageStreams("openshift")

	c.tInformer = c.templateInformerFactory.Template().V1().Templates().Informer()
	c.tInformer.AddEventHandler(c.templateInformerEventHandler())
	c.listers.Templates = c.templateInformerFactory.Template().V1().Templates().Lister().Templates("openshift")

	c.crInformer = c.sampopInformerFactory.Samples().V1().Configs().Informer()
	c.crInformer.AddEventHandler(c.crInformerEventHandler())
	c.listers.Config = c.sampopInformerFactory.Samples().V1().Configs().Lister()

	c.handlerStub, err = stub.NewSamplesOperatorHandler(kubeconfig,
		c.listers)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (c *Controller) Run(stopCh <-chan struct{}) error {
	defer c.crWorkqueue.ShutDown()
	defer c.isWorkqueue.ShutDown()
	defer c.tWorkqueue.ShutDown()
	defer c.ocSecWorkqueue.ShutDown()

	c.imageInformerFactory.Start(stopCh)
	c.templateInformerFactory.Start(stopCh)
	c.sampopInformerFactory.Start(stopCh)
	c.kubeOCNSInformerFactory.Start(stopCh)

	klog.Infof("waiting for informer caches to sync")
	if !cache.WaitForCacheSync(stopCh, c.isInformer.HasSynced, c.tInformer.HasSynced, c.crInformer.HasSynced, c.ocSecInformer.HasSynced) {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	crQueueWorker := queueWorker{
		c:         c,
		workQueue: c.crWorkqueue,
		getter:    &crGetter{},
	}
	go wait.Until(crQueueWorker.workqueueProcessor, time.Second, stopCh)
	isQueueWorker := queueWorker{
		c:         c,
		workQueue: c.isWorkqueue,
		getter:    &isGetter{},
	}
	for i := 0; i < 5; i++ {
		go wait.Until(isQueueWorker.workqueueProcessor, time.Second, stopCh)
	}
	tQueueWorker := queueWorker{
		c:         c,
		workQueue: c.tWorkqueue,
		getter:    &tGetter{},
	}
	for i := 0; i < 5; i++ {
		go wait.Until(tQueueWorker.workqueueProcessor, time.Second, stopCh)
	}
	ocSecQueueWorker := queueWorker{
		c:         c,
		workQueue: c.ocSecWorkqueue,
		getter:    &ocSecretGetter{},
	}
	go wait.Until(ocSecQueueWorker.workqueueProcessor, time.Second, stopCh)

	klog.Infof("started events processor")
	<-stopCh
	klog.Infof("shutting down events processor")

	return nil
}

// LISTER ABSTRACTIONS FOR WORK QUEUE EVENT PROCESSING

type runtimeObjectGetter interface {
	Get(c *Controller, key string) (runtime.Object, error)
}

type crGetter struct{}

func (g *crGetter) Get(c *Controller, key string) (runtime.Object, error) {
	return c.listers.Config.Get(sampopapi.ConfigName)
}

type ocSecretGetter struct{}

func (g *ocSecretGetter) Get(c *Controller, key string) (runtime.Object, error) {
	return c.listers.ConfigNamespaceSecrets.Get(key)
}

type isGetter struct{}

func (g *isGetter) Get(c *Controller, key string) (runtime.Object, error) {
	return c.listers.ImageStreams.Get(key)
}

type tGetter struct{}

func (g *tGetter) Get(c *Controller, key string) (runtime.Object, error) {
	return c.listers.Templates.Get(key)
}

// WORK QUEUE EVENT PROCESSING

func (c *Controller) handleWork(getter runtimeObjectGetter, o interface{}) error {
	klog.Infof("handleWork key %s getter %#v", o, getter)

	event := util.Event{
		Object:  nil,
		Deleted: true,
	}

	// actual objects mean delete, strings mean add/update
	if ro, ok := o.(runtime.Object); ok {
		event.Object = ro.DeepCopyObject()
	} else if key, ok := o.(string); ok {
		event.Deleted = false
		obj, err := getter.Get(c, key)
		if err != nil {
			// see if this is a operator bootstrap scenario
			if kerrors.IsNotFound(err) {
				_, opCR := getter.(*crGetter)
				if opCR && key == sampopapi.ConfigName {
					return c.Bootstrap()
				}
				klog.Infof("handleWork resource %s has since been deleted, ignore update event", key)
				return nil
			}
			return fmt.Errorf("handleWork failed to get %q resource: %s", key, err)
		}
		event.Object = obj.DeepCopyObject()
	}

	if event.Object != nil {
		return c.handlerStub.Handle(event)
	}
	return fmt.Errorf("handleWork expected a runtime object but got %#v", o)
}

// WORK QUEUE KEY RELATED

type queueKeyGen interface {
	Key(o interface{}) string
}

type crQueueKeyGen struct{}

func (c *crQueueKeyGen) Key(o interface{}) string {
	cr := o.(*sampopapi.Config)
	return cr.Name
}

type secretQueueKeyGen struct{}

func (c *secretQueueKeyGen) Key(o interface{}) string {
	secret := o.(*corev1.Secret)
	return secret.Name
}

type imagestreamQueueKeyGen struct{}

func (c *imagestreamQueueKeyGen) Key(o interface{}) string {
	imagestream := o.(*imagev1.ImageStream)
	return imagestream.Name
}

type templateQueueKeyGen struct{}

func (c *templateQueueKeyGen) Key(o interface{}) string {
	template := o.(*templatev1.Template)
	return template.Name
}

// WORK QUEUE LOOP

type queueWorker struct {
	workQueue workqueue.RateLimitingInterface
	c         *Controller
	getter    runtimeObjectGetter
}

func (w *queueWorker) workqueueProcessor() {
	for {
		obj, shutdown := w.workQueue.Get()
		if shutdown {
			return
		}

		klog.Infof("get event from workqueue %#v", obj)
		func() {
			defer w.workQueue.Done(obj)

			dbg := ""
			if ro, ok := obj.(runtime.Object); ok {
				dbg = ro.GetObjectKind().GroupVersionKind().String()
			} else if str, ok := obj.(string); ok {
				dbg = str
			} else {
				w.workQueue.Forget(obj)
				klog.Errorf("expected string in workqueue but got %#v", obj)
				return
			}

			if err := w.c.handleWork(w.getter, obj); err != nil {
				w.workQueue.AddRateLimited(obj)
				klog.Errorf("unable to sync: %s, requeuing", err)
			} else {
				w.workQueue.Forget(obj)
				klog.Infof("event from workqueue successfully processed %s", dbg)
			}
		}()
	}
}

// INFORMER EVENT HANDLER RELATED

func (c *Controller) commonInformerEventHandler(keygen queueKeyGen, wq workqueue.RateLimitingInterface) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(o interface{}) {
			key := keygen.Key(o)
			klog.Infof("add event to workqueue due to %s (add) via %#v", key, keygen)
			// we pass key vs. obj to distinguish from delete
			wq.Add(key)
		},
		UpdateFunc: func(o, n interface{}) {
			key := keygen.Key(n)
			klog.Infof("add event to workqueue due to %s (update) via %#v", key, keygen)
			// we pass key vs. obj to distinguish from delete
			wq.Add(key)
		},
		DeleteFunc: func(o interface{}) {
			object, ok := o.(metaapi.Object)
			if !ok {
				tombstone, ok := o.(cache.DeletedFinalStateUnknown)
				if !ok {
					klog.Errorf("error decoding object, invalid type")
					return
				}
				object, ok = tombstone.Obj.(metaapi.Object)
				if !ok {
					klog.Errorf("error decoding object tombstone, invalid type")
					return
				}
				klog.Infof("recovered deleted object %q from tombstone", object.GetName())
			}
			_, stream := keygen.(*imagestreamQueueKeyGen)
			if stream && sampcache.ImageStreamDeletePartOfMassDelete(object.GetName()) {
				klog.Infof("one time ignoring of delete event for imagestream %s as part of group delete", object.GetName())
				return
			}
			_, tpl := keygen.(*templateQueueKeyGen)
			if tpl && sampcache.TemplateDeletePartOfMassDelete(object.GetName()) {
				klog.Infof("one time ignoring of delete event for template %s as part of a group delete", object.GetName())
				return
			}
			key := keygen.Key(object)
			klog.Infof("add event to workqueue due to %#v (delete) via %#v", key, keygen)
			// but we pass in the actual object on delete so it can be leveraged by the
			// event handling (objs without finalizers won't be accessible via get)
			wq.Add(object)

		},
	}
}

func (c *Controller) crInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&crQueueKeyGen{}, c.crWorkqueue)
}

func (c *Controller) ocSecretInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&secretQueueKeyGen{}, c.ocSecWorkqueue)
}

func (c *Controller) imagestreamInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&imagestreamQueueKeyGen{}, c.isWorkqueue)
}

func (c *Controller) templateInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&templateQueueKeyGen{}, c.tWorkqueue)
}
