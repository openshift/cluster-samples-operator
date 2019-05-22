package operator

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

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

	imagev1 "github.com/openshift/api/image/v1"
	imageset "github.com/openshift/client-go/image/clientset/versioned"
	imageinformers "github.com/openshift/client-go/image/informers/externalversions"

	templatev1 "github.com/openshift/api/template/v1"
	templateset "github.com/openshift/client-go/template/clientset/versioned"
	templateinformers "github.com/openshift/client-go/template/informers/externalversions"

	sampopapi "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	sampcache "github.com/openshift/cluster-samples-operator/pkg/cache"
	sampopclient "github.com/openshift/cluster-samples-operator/pkg/client"
	sampleclientv1 "github.com/openshift/cluster-samples-operator/pkg/generated/clientset/versioned"
	sampopinformers "github.com/openshift/cluster-samples-operator/pkg/generated/informers/externalversions"

	operatorstatus "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
	"github.com/openshift/cluster-samples-operator/pkg/stub"

	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
)

const (
	defaultResyncDuration = 10 * time.Minute
)

type Controller struct {
	restconfig *restclient.Config
	cvowrapper *operatorstatus.ClusterOperatorHandler
	//generator  *resource.Generator
	crWorkqueue    workqueue.RateLimitingInterface
	osSecWorkqueue workqueue.RateLimitingInterface
	opSecWorkqueue workqueue.RateLimitingInterface
	isWorkqueue    workqueue.RateLimitingInterface
	tWorkqueue     workqueue.RateLimitingInterface

	crInformer    cache.SharedIndexInformer
	osSecInformer cache.SharedIndexInformer
	opSecInformer cache.SharedIndexInformer
	isInformer    cache.SharedIndexInformer
	tInformer     cache.SharedIndexInformer

	kubeOSNSInformerFactory kubeinformers.SharedInformerFactory
	kubeOPNSInformerFactory kubeinformers.SharedInformerFactory
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
		osSecWorkqueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "openshift-secret-changes"),
		opSecWorkqueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "openshift-config-namespace-secret-changes"),
		isWorkqueue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "imagestream-changes"),
		tWorkqueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "template-changes"),
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

	c.kubeOSNSInformerFactory = kubeinformers.NewFilteredSharedInformerFactory(kubeClient, defaultResyncDuration, "openshift", nil)
	c.kubeOPNSInformerFactory = kubeinformers.NewFilteredSharedInformerFactory(kubeClient, defaultResyncDuration, "openshift-config", nil)
	//TODO - eventually a k8s go-client deps bump will lead to the form below, similar to the image registry operator's kubeinformer initialization,
	// and similar to what is available with the openshift go-client for imagestreams and templates
	//kubeInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, defaultResyncDuration, kubeinformers.WithNamespace("kube-system"))
	c.imageInformerFactory = imageinformers.NewSharedInformerFactoryWithOptions(imageClient, defaultResyncDuration, imageinformers.WithNamespace("openshift"))
	c.templateInformerFactory = templateinformers.NewSharedInformerFactoryWithOptions(templateClient, defaultResyncDuration, templateinformers.WithNamespace("openshift"))
	c.sampopInformerFactory = sampopinformers.NewSharedInformerFactory(sampopClient, defaultResyncDuration)

	c.osSecInformer = c.kubeOSNSInformerFactory.Core().V1().Secrets().Informer()
	c.osSecInformer.AddEventHandler(c.osSecretInformerEventHandler())
	c.listers.OpenShiftNamespaceSecrets = c.kubeOSNSInformerFactory.Core().V1().Secrets().Lister().Secrets("openshift")

	c.opSecInformer = c.kubeOPNSInformerFactory.Core().V1().Secrets().Informer()
	c.opSecInformer.AddEventHandler(c.opSecretInformerEventHandler())
	c.listers.OperatorNamespaceSecrets = c.kubeOPNSInformerFactory.Core().V1().Secrets().Lister().Secrets("openshift-config")

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
		c.listers.Config,
		c.listers.ImageStreams,
		c.listers.Templates,
		c.listers.OpenShiftNamespaceSecrets,
		c.listers.OperatorNamespaceSecrets)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (c *Controller) Run(stopCh <-chan struct{}) error {
	defer c.crWorkqueue.ShutDown()
	defer c.osSecWorkqueue.ShutDown()
	defer c.opSecWorkqueue.ShutDown()
	defer c.isWorkqueue.ShutDown()
	defer c.tWorkqueue.ShutDown()

	c.kubeOSNSInformerFactory.Start(stopCh)
	c.kubeOPNSInformerFactory.Start(stopCh)
	c.imageInformerFactory.Start(stopCh)
	c.templateInformerFactory.Start(stopCh)
	c.sampopInformerFactory.Start(stopCh)

	logrus.Println("waiting for informer caches to sync")
	if !cache.WaitForCacheSync(stopCh, c.osSecInformer.HasSynced, c.opSecInformer.HasSynced,
		c.isInformer.HasSynced, c.tInformer.HasSynced, c.crInformer.HasSynced) {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	crQueueWorker := queueWorker{
		c:         c,
		workQueue: c.crWorkqueue,
		getter:    &crGetter{},
	}
	go wait.Until(crQueueWorker.workqueueProcessor, time.Second, stopCh)
	osSecQueueWorker := queueWorker{
		c:         c,
		workQueue: c.osSecWorkqueue,
		getter:    &osSecretGetter{},
	}
	go wait.Until(osSecQueueWorker.workqueueProcessor, time.Second, stopCh)
	opSecQueueWorker := queueWorker{
		c:         c,
		workQueue: c.opSecWorkqueue,
		getter:    &opSecretGetter{},
	}
	go wait.Until(opSecQueueWorker.workqueueProcessor, time.Second, stopCh)
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

	logrus.Println("started events processor")
	<-stopCh
	logrus.Println("shutting down events processor")

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

type osSecretGetter struct{}

func (g *osSecretGetter) Get(c *Controller, key string) (runtime.Object, error) {
	return c.listers.OpenShiftNamespaceSecrets.Get(key)
}

type opSecretGetter struct{}

func (g *opSecretGetter) Get(c *Controller, key string) (runtime.Object, error) {
	return c.listers.OperatorNamespaceSecrets.Get(key)
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
	logrus.Debugf("handleWork key %s getter %#v", o, getter)

	event := sampopapi.Event{
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
				logrus.Printf("handleWork resource %s has since been deleted, ignore update event", key)
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

		logrus.Debugf("get event from workqueue %#v", obj)
		func() {
			defer w.workQueue.Done(obj)

			dbg := ""
			if ro, ok := obj.(runtime.Object); ok {
				dbg = ro.GetObjectKind().GroupVersionKind().String()
			} else if str, ok := obj.(string); ok {
				dbg = str
			} else {
				w.workQueue.Forget(obj)
				logrus.Errorf("expected string in workqueue but got %#v", obj)
				return
			}

			if err := w.c.handleWork(w.getter, obj); err != nil {
				w.workQueue.AddRateLimited(obj)
				logrus.Errorf("unable to sync: %s, requeuing", err)
			} else {
				w.workQueue.Forget(obj)
				logrus.Debugf("event from workqueue successfully processed %s", dbg)
			}
		}()
	}
}

// INFORMER EVENT HANDLER RELATED

func (c *Controller) commonInformerEventHandler(keygen queueKeyGen, wq workqueue.RateLimitingInterface) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(o interface{}) {
			key := keygen.Key(o)
			logrus.Debugf("add event to workqueue due to %s (add) via %#v", key, keygen)
			// we pass key vs. obj to distinguish from delete
			wq.Add(key)
		},
		UpdateFunc: func(o, n interface{}) {
			key := keygen.Key(n)
			logrus.Debugf("add event to workqueue due to %s (update) via %#v", key, keygen)
			// we pass key vs. obj to distinguish from delete
			wq.Add(key)
		},
		DeleteFunc: func(o interface{}) {
			object, ok := o.(metaapi.Object)
			if !ok {
				tombstone, ok := o.(cache.DeletedFinalStateUnknown)
				if !ok {
					logrus.Errorf("error decoding object, invalid type")
					return
				}
				object, ok = tombstone.Obj.(metaapi.Object)
				if !ok {
					logrus.Errorf("error decoding object tombstone, invalid type")
					return
				}
				logrus.Debugf("recovered deleted object %q from tombstone", object.GetName())
			}
			_, stream := keygen.(*imagestreamQueueKeyGen)
			if stream && sampcache.ImageStreamDeletePartOfMassDelete(object.GetName()) {
				logrus.Printf("one time ignoring of delete event for imagestream %s as part of group delete", object.GetName())
				return
			}
			_, tpl := keygen.(*templateQueueKeyGen)
			if tpl && sampcache.TemplateDeletePartOfMassDelete(object.GetName()) {
				logrus.Printf("one time ignoring of delete event for template %s as part of a group delete", object.GetName())
				return
			}
			key := keygen.Key(object)
			logrus.Debugf("add event to workqueue due to %#v (delete) via %#v", key, keygen)
			// but we pass in the actual object on delete so it can be leveraged by the
			// event handling (objs without finalizers won't be accessible via get)
			wq.Add(object)

		},
	}
}

func (c *Controller) crInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&crQueueKeyGen{}, c.crWorkqueue)
}

func (c *Controller) osSecretInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&secretQueueKeyGen{}, c.osSecWorkqueue)
}

func (c *Controller) opSecretInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&secretQueueKeyGen{}, c.opSecWorkqueue)
}

func (c *Controller) imagestreamInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&imagestreamQueueKeyGen{}, c.isWorkqueue)
}

func (c *Controller) templateInformerEventHandler() cache.ResourceEventHandlerFuncs {
	return c.commonInformerEventHandler(&templateQueueKeyGen{}, c.tWorkqueue)
}
