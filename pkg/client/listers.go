package client

import (
	imagelisters "github.com/openshift/client-go/image/listers/image/v1"
	templatelisters "github.com/openshift/client-go/template/listers/template/v1"
	corelisters "k8s.io/client-go/listers/core/v1"

	sampoplisters "github.com/openshift/client-go/samples/listers/samples/v1"
)

type Listers struct {
	ConfigNamespaceSecrets    corelisters.SecretNamespaceLister
	ImageStreams              imagelisters.ImageStreamNamespaceLister
	Templates                 templatelisters.TemplateNamespaceLister
	Config                    sampoplisters.ConfigLister
}
