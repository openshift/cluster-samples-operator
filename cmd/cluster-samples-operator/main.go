package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	v1 "github.com/openshift/api/config/v1"
	_ "github.com/openshift/api/samples/v1/zz_generated.crd-manifests"
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
	cmd.Flags().String("config", "", "Path to the controller config file")
	cmd.AddCommand(watchdog.NewFileWatcherWatchdog())
	if err := cmd.Execute(); err != nil {
		logrus.Errorf("%v", err)
		os.Exit(1)
	}
}

// readAndParseControllerConfig reads and parses the controller configuration file.
// If path is empty, returns default config (for backwards compatibility during
// migration to file-based configuration).
func readAndParseControllerConfig(path string) (*v1.GenericControllerConfig, error) {
	config := &v1.GenericControllerConfig{
		ServingInfo: v1.HTTPServingInfo{
			ServingInfo: v1.ServingInfo{
				BindAddress: fmt.Sprintf(":%d", metrics.MetricsPort),
			},
		},
	}
	if path == "" {
		return config, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := kubeyaml.Unmarshal(content, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config content: %w", err)
	}

	// make sure we always have a bind address present in the config. This
	// is just for the case where the config has an explicitly empty
	// bindAddress value.
	if config.ServingInfo.BindAddress == "" {
		config.ServingInfo.BindAddress = fmt.Sprintf(":%d", metrics.MetricsPort)
	}

	return config, nil
}

func runOperator(cmd *cobra.Command, args []string) {
	printVersion()

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	ctrlConfig, err := cmd.Flags().GetString("config")
	if err != nil {
		logrus.Fatal(err)
	}

	config, err := readAndParseControllerConfig(ctrlConfig)
	if err != nil {
		logrus.Fatal(err)
	}

	for _, arg := range args {
		if arg == "-v" {
			logrus.SetLevel(logrus.DebugLevel)
			break
		}
	}

	srv, err := metrics.NewServer(config)
	if err != nil {
		logrus.Fatal(err)
	}

	if err := srv.Start(); err != nil {
		logrus.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := srv.Stop(ctx); err != nil {
			logrus.Error(err)
		}
		cancel()
	}()

	controller, err := operator.NewController()
	if err != nil {
		logrus.Fatal(err)
	}

	err = controller.Run(stopCh)
	if err != nil {
		logrus.Fatal(err)
	}
}
