package main

import (
	//"context"
	"os"
	"runtime"

	"github.com/openshift/cluster-samples-operator/pkg/operator"
	"github.com/openshift/cluster-samples-operator/pkg/signals"

	"github.com/sirupsen/logrus"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
}

func main() {
	printVersion()

	if os.Args != nil && len(os.Args) > 0 {
		for _, arg := range os.Args {
			if arg == "-v" {
				logrus.SetLevel(logrus.DebugLevel)
				break
			}
		}
	}

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	controller, err := operator.NewController()
	if err != nil {
		logrus.Fatal(err)
	}

	err = controller.Run(stopCh)
	if err != nil {
		logrus.Fatal(err)
	}
}
