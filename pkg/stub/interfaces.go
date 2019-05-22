package stub

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"time"

	"k8s.io/client-go/util/flowcontrol"

	imagev1 "github.com/openshift/api/image/v1"
	templatev1 "github.com/openshift/api/template/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imagev1lister "github.com/openshift/client-go/image/listers/image/v1"
	templatev1lister "github.com/openshift/client-go/template/listers/template/v1"
	v1 "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	sampleclientv1 "github.com/openshift/cluster-samples-operator/pkg/generated/clientset/versioned/typed/samples/v1"
	configv1lister "github.com/openshift/cluster-samples-operator/pkg/generated/listers/samples/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	restclient "k8s.io/client-go/rest"
)

type ImageStreamClientWrapper interface {
	Get(name string) (*imagev1.ImageStream, error)
	List(opts metav1.ListOptions) (*imagev1.ImageStreamList, error)
	Create(is *imagev1.ImageStream) (*imagev1.ImageStream, error)
	Update(is *imagev1.ImageStream) (*imagev1.ImageStream, error)
	Delete(name string, opts *metav1.DeleteOptions) error
	Watch() (watch.Interface, error)
}

type defaultImageStreamClientWrapper struct {
	h      *Handler
	lister imagev1lister.ImageStreamNamespaceLister
}

func (g *defaultImageStreamClientWrapper) Get(name string) (*imagev1.ImageStream, error) {
	return g.lister.Get(name)
}

func (g *defaultImageStreamClientWrapper) List(opts metav1.ListOptions) (*imagev1.ImageStreamList, error) {
	return g.h.imageclient.ImageStreams("openshift").List(opts)
}

func (g *defaultImageStreamClientWrapper) Create(is *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return g.h.imageclient.ImageStreams("openshift").Create(is)
}

func (g *defaultImageStreamClientWrapper) Update(is *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return g.h.imageclient.ImageStreams("openshift").Update(is)
}

func (g *defaultImageStreamClientWrapper) Delete(name string, opts *metav1.DeleteOptions) error {
	return g.h.imageclient.ImageStreams("openshift").Delete(name, opts)
}

func (g *defaultImageStreamClientWrapper) Watch() (watch.Interface, error) {
	opts := metav1.ListOptions{}
	return g.h.imageclient.ImageStreams("openshift").Watch(opts)
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
	return g.h.tempclient.Templates("openshift").List(opts)
}

func (g *defaultTemplateClientWrapper) Create(t *templatev1.Template) (*templatev1.Template, error) {
	return g.h.tempclient.Templates("openshift").Create(t)
}

func (g *defaultTemplateClientWrapper) Update(t *templatev1.Template) (*templatev1.Template, error) {
	return g.h.tempclient.Templates("openshift").Update(t)
}

func (g *defaultTemplateClientWrapper) Delete(name string, opts *metav1.DeleteOptions) error {
	return g.h.tempclient.Templates("openshift").Delete(name, opts)
}

func (g *defaultTemplateClientWrapper) Watch() (watch.Interface, error) {
	opts := metav1.ListOptions{}
	return g.h.tempclient.Templates("openshift").Watch(opts)
}

type SecretClientWrapper interface {
	Get(namespace, name string) (*corev1.Secret, error)
	Create(namespace string, s *corev1.Secret) (*corev1.Secret, error)
	Update(namespace string, s *corev1.Secret) (*corev1.Secret, error)
	Delete(namespace, name string, opts *metav1.DeleteOptions) error
}

type defaultSecretClientWrapper struct {
	coreclient    *corev1client.CoreV1Client
	opnshftlister corev1lister.SecretNamespaceLister
	cfglister     corev1lister.SecretNamespaceLister
}

func (g *defaultSecretClientWrapper) Get(namespace, name string) (*corev1.Secret, error) {
	switch namespace {
	case "openshift-config":
		return g.cfglister.Get(name)
	case "openshift":
		return g.opnshftlister.Get(name)
	}
	return g.coreclient.Secrets(namespace).Get(name, metav1.GetOptions{})
}

func (g *defaultSecretClientWrapper) Create(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	return g.coreclient.Secrets(namespace).Create(s)
}

func (g *defaultSecretClientWrapper) Update(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	return g.coreclient.Secrets(namespace).Update(s)
}

func (g *defaultSecretClientWrapper) Delete(namespace, name string, opts *metav1.DeleteOptions) error {
	return g.coreclient.Secrets(namespace).Delete(name, opts)
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
		_, err := g.client.UpdateStatus(sr)
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
		_, err := g.client.Update(sr)
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
		_, err := g.client.Create(sr)
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
