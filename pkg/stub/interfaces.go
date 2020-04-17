package stub

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"time"

	"k8s.io/client-go/util/flowcontrol"

	imagev1 "github.com/openshift/api/image/v1"
	v1 "github.com/openshift/api/samples/v1"
	templatev1 "github.com/openshift/api/template/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	imagev1lister "github.com/openshift/client-go/image/listers/image/v1"
	sampleclientv1 "github.com/openshift/client-go/samples/clientset/versioned/typed/samples/v1"
	configv1lister "github.com/openshift/client-go/samples/listers/samples/v1"
	templatev1lister "github.com/openshift/client-go/template/listers/template/v1"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	restclient "k8s.io/client-go/rest"
)

type ImageStreamClientWrapper interface {
	Get(name string) (*imagev1.ImageStream, error)
	List(opts metav1.ListOptions) (*imagev1.ImageStreamList, error)
	Create(is *imagev1.ImageStream) (*imagev1.ImageStream, error)
	Update(is *imagev1.ImageStream) (*imagev1.ImageStream, error)
	Delete(name string, opts *metav1.DeleteOptions) error
	Watch() (watch.Interface, error)
	ImageStreamImports(namespace string) imagev1client.ImageStreamImportInterface
}

type defaultImageStreamClientWrapper struct {
	h      *Handler
	lister imagev1lister.ImageStreamNamespaceLister
}

func (g *defaultImageStreamClientWrapper) Get(name string) (*imagev1.ImageStream, error) {
	return g.lister.Get(name)
}

func (g *defaultImageStreamClientWrapper) List(opts metav1.ListOptions) (*imagev1.ImageStreamList, error) {
	return g.h.imageclient.ImageStreams("openshift").List(context.TODO(), opts)
}

func (g *defaultImageStreamClientWrapper) Create(is *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return g.h.imageclient.ImageStreams("openshift").Create(context.TODO(), is, metav1.CreateOptions{})
}

func (g *defaultImageStreamClientWrapper) Update(is *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return g.h.imageclient.ImageStreams("openshift").Update(context.TODO(), is, metav1.UpdateOptions{})
}

func (g *defaultImageStreamClientWrapper) Delete(name string, opts *metav1.DeleteOptions) error {
	return g.h.imageclient.ImageStreams("openshift").Delete(context.TODO(), name, *opts)
}

func (g *defaultImageStreamClientWrapper) Watch() (watch.Interface, error) {
	opts := metav1.ListOptions{}
	return g.h.imageclient.ImageStreams("openshift").Watch(context.TODO(), opts)
}

func (g *defaultImageStreamClientWrapper) ImageStreamImports(namespace string) imagev1client.ImageStreamImportInterface {
	return g.h.imageclient.ImageStreamImports(namespace)
}

type TemplateClientWrapper interface {
	Get(name string) (*templatev1.Template, error)
	List(opts metav1.ListOptions) (*templatev1.TemplateList, error)
	Create(t *templatev1.Template) (*templatev1.Template, error)
	Update(t *templatev1.Template) (*templatev1.Template, error)
	Delete(name string, opts *metav1.DeleteOptions) error
	Watch() (watch.Interface, error)
}

type defaultTemplateClientWrapper struct {
	h      *Handler
	lister templatev1lister.TemplateNamespaceLister
}

func (g *defaultTemplateClientWrapper) Get(name string) (*templatev1.Template, error) {
	return g.lister.Get(name)
}

func (g *defaultTemplateClientWrapper) List(opts metav1.ListOptions) (*templatev1.TemplateList, error) {
	return g.h.tempclient.Templates("openshift").List(context.TODO(), opts)
}

func (g *defaultTemplateClientWrapper) Create(t *templatev1.Template) (*templatev1.Template, error) {
	return g.h.tempclient.Templates("openshift").Create(context.TODO(), t, metav1.CreateOptions{})
}

func (g *defaultTemplateClientWrapper) Update(t *templatev1.Template) (*templatev1.Template, error) {
	return g.h.tempclient.Templates("openshift").Update(context.TODO(), t, metav1.UpdateOptions{})
}

func (g *defaultTemplateClientWrapper) Delete(name string, opts *metav1.DeleteOptions) error {
	return g.h.tempclient.Templates("openshift").Delete(context.TODO(), name, *opts)
}

func (g *defaultTemplateClientWrapper) Watch() (watch.Interface, error) {
	opts := metav1.ListOptions{}
	return g.h.tempclient.Templates("openshift").Watch(context.TODO(), opts)
}

type ImageStreamFromFileGetter interface {
	Get(fullFilePath string) (is *imagev1.ImageStream, err error)
}

type DefaultImageStreamFromFileGetter struct {
}

func (g *DefaultImageStreamFromFileGetter) Get(fullFilePath string) (is *imagev1.ImageStream, err error) {
	isjsonfile, err := ioutil.ReadFile(fullFilePath)
	if err != nil {
		return nil, err
	}

	imagestream := &imagev1.ImageStream{}
	err = json.Unmarshal(isjsonfile, imagestream)
	if err != nil {
		return nil, err
	}

	return imagestream, nil
}

type TemplateFromFileGetter interface {
	Get(fullFilePath string) (t *templatev1.Template, err error)
}

type DefaultTemplateFromFileGetter struct {
}

func (g *DefaultTemplateFromFileGetter) Get(fullFilePath string) (t *templatev1.Template, err error) {
	tjsonfile, err := ioutil.ReadFile(fullFilePath)
	if err != nil {
		return nil, err
	}
	template := &templatev1.Template{}
	err = json.Unmarshal(tjsonfile, template)
	if err != nil {
		return nil, err
	}

	return template, nil
}

type ResourceFileLister interface {
	List(dir string) (files []os.FileInfo, err error)
}

type DefaultResourceFileLister struct {
}

func (g *DefaultResourceFileLister) List(dir string) (files []os.FileInfo, err error) {
	files, err = ioutil.ReadDir(dir)
	return files, err

}

type InClusterInitter interface {
	init(h *Handler, restconfig *restclient.Config)
}

type defaultInClusterInitter struct {
}

func (g *defaultInClusterInitter) init(h *Handler, restconfig *restclient.Config) {
	if restconfig.RateLimiter == nil {
		restconfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(50.0, 50)
	}
	h.restconfig = restconfig
	tempclient, err := getTemplateClient(restconfig)
	if err != nil {
		logrus.Errorf("failed to get template client : %v", err)
		panic(err)
	}
	h.tempclient = tempclient
	logrus.Printf("template client %#v", tempclient)
	imageclient, err := getImageClient(restconfig)
	if err != nil {
		logrus.Errorf("failed to get image client : %v", err)
		panic(err)
	}
	h.imageclient = imageclient
	logrus.Printf("image client %#v", imageclient)
	coreclient, err := corev1client.NewForConfig(restconfig)
	if err != nil {
		logrus.Errorf("failed to get core client : %v", err)
		panic(err)
	}
	h.coreclient = coreclient
	configclient, err := configv1client.NewForConfig(restconfig)
	if err != nil {
		logrus.Errorf("failed to get config client : %v", err)
		panic(err)
	}
	h.configclient = configclient
}

type CRDWrapper interface {
	Update(*v1.Config) (err error)
	UpdateStatus(Config *v1.Config, dbg string) (err error)
	Create(Config *v1.Config) (err error)
	Get(name string) (*v1.Config, error)
}

type generatedCRDWrapper struct {
	client sampleclientv1.ConfigInterface
	lister configv1lister.ConfigLister
}

func (g *generatedCRDWrapper) UpdateStatus(sr *v1.Config, dbg string) error {
	return wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		_, err := g.client.UpdateStatus(context.TODO(), sr, metav1.UpdateOptions{})
		if err == nil {
			return true, nil
		}
		if !IsRetryableAPIError(err) {
			if len(dbg) > 0 {
				logrus.Printf("CRDERROR %s", dbg)
			}
			return false, err
		}
		return false, nil
	})

}

func (g *generatedCRDWrapper) Update(sr *v1.Config) error {
	return wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		_, err := g.client.Update(context.TODO(), sr, metav1.UpdateOptions{})
		if err == nil {
			return true, nil
		}
		if !IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})

}

func (g *generatedCRDWrapper) Create(sr *v1.Config) error {
	return wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		_, err := g.client.Create(context.TODO(), sr, metav1.CreateOptions{})
		if err == nil {
			return true, nil
		}
		if !IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})
}

func (g *generatedCRDWrapper) Get(name string) (*v1.Config, error) {
	return g.lister.Get(name)
}
