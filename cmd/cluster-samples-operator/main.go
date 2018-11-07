package main

import (
	"context"
	"runtime"

	"github.com/openshift/cluster-samples-operator/pkg/apis/samplesoperator/v1alpha1"
	stub "github.com/openshift/cluster-samples-operator/pkg/stub"
	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"
	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"

	imagev1 "github.com/openshift/api/image/v1"
	templatev1 "github.com/openshift/api/template/v1"

	ocfgapi "github.com/openshift/cluster-version-operator/pkg/apis/config.openshift.io/v1"

	"github.com/sirupsen/logrus"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()

	sdk.ExposeMetricsPort()

	resource := v1alpha1.GroupName + "/" + v1alpha1.Version
	kind := "SamplesResource"
	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		logrus.Fatalf("failed to get watch namespace: %v", err)
	}
	resyncPeriod := 10 * 60 // 10 minutes
	logrus.Infof("Watching %s, %s, %s, %d", resource, kind, namespace, resyncPeriod)
	sdk.Watch(resource, kind, "", resyncPeriod)
	logrus.Infof("Watching secrets")
	sdk.Watch("v1", "Secret", namespace, resyncPeriod)
	sdk.Watch("v1", "Secret", "openshift", resyncPeriod)
	k8sutil.AddToSDKScheme(imagev1.AddToScheme)
	k8sutil.AddToSDKScheme(templatev1.AddToScheme)
	k8sutil.AddToSDKScheme(ocfgapi.AddToScheme)
	sdk.Watch(imagev1.SchemeGroupVersion.String(), "ImageStream", "openshift", resyncPeriod)
	sdk.Watch(templatev1.SchemeGroupVersion.String(), "Template", "openshift", resyncPeriod)
	sdk.Handle(stub.NewHandler())
	sdk.Run(context.TODO())
}
