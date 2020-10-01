package main

import (
	"flag"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/klog/v2"

	"github.com/openshift/library-go/pkg/operator/watchdog"

	"github.com/openshift/cluster-samples-operator/pkg/metrics"
	"github.com/openshift/cluster-samples-operator/pkg/operator"
	"github.com/openshift/cluster-samples-operator/pkg/signals"
)

func printVersion() {
	klog.Infof("Go Version: %s", runtime.Version())
	klog.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
}

func main() {
	klogFlags := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(klogFlags)
	flag.Parse()
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	if logstderr := klogFlags.Lookup("logtostderr"); logstderr != nil {
		_ = logstderr.Value.Set("true")
	}

	cmd := &cobra.Command{
		Use:   "cluster-samples-operator",
		Short: "OpenShift cluster samples operator",
		Run: func(cmd *cobra.Command, args []string) {
			printVersion()

			// set up signals so we handle the first shutdown signal gracefully
			stopCh := signals.SetupSignalHandler()

			srv := metrics.BuildServer(metrics.MetricsPort)
			go metrics.RunServer(srv, stopCh)

			controller, err := operator.NewController()
			if err != nil {
				klog.Fatal(err)
			}

			if err = controller.Run(stopCh); err != nil {
				klog.Fatal(err)
			}
		},
	}

	cmd.AddCommand(watchdog.NewFileWatcherWatchdog())

	if err := cmd.Execute(); err != nil {
		klog.Errorf("%v", err)
		os.Exit(1)
	}
}
