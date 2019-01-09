package client

import (
	corelisters "k8s.io/client-go/listers/core/v1"

	imagelisters "github.com/openshift/client-go/image/listers/image/v1"
	templatelisters "github.com/openshift/client-go/template/listers/template/v1"

	sampoplisters "github.com/openshift/cluster-samples-operator/pkg/generated/listers/samples/v1"
)

type Listers struct {
	OpenShiftNamespaceSecrets corelisters.SecretNamespaceLister
	OperatorNamespaceSecrets  corelisters.SecretNamespaceLister
	ImageStreams              imagelisters.ImageStreamNamespaceLister
	Templates                 templatelisters.TemplateNamespaceLister
	Config                    sampoplisters.ConfigLister
}
