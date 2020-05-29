package main

import (
	"os"
	"runtime"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"github.com/openshift/library-go/pkg/operator/watchdog"

	"github.com/openshift/cluster-samples-operator/pkg/metrics"
	"github.com/openshift/cluster-samples-operator/pkg/operator"
	"github.com/openshift/cluster-samples-operator/pkg/signals"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
}

func main() {
	cmd := &cobra.Command{
		Use:   "cluster-samples-operator",
		Short: "OpenShift cluster samples operator",
		Run:   runOperator,
	}
	cmd.AddCommand(watchdog.NewFileWatcherWatchdog())
	if err := cmd.Execute(); err != nil {
		logrus.Errorf("%v", err)
		os.Exit(1)
	}
}

func runOperator(cmd *cobra.Command, args []string) {
	printVersion()

	for _, arg := range args {
		if arg == "-v" {
			logrus.SetLevel(logrus.DebugLevel)
			break
		}
	}

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	srv := metrics.BuildServer(metrics.MetricsPort)
	go metrics.RunServer(srv, stopCh)

	controller, err := operator.NewController()
	if err != nil {
		logrus.Fatal(err)
	}

	err = controller.Run(stopCh)
	if err != nil {
		logrus.Fatal(err)
	}
}
