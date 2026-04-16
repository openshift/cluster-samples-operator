package metrics

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	v1 "github.com/openshift/api/config/v1"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"k8s.io/client-go/util/cert"
)

func writeTempFile(t *testing.T, pattern string, data []byte) (string, func()) {
	t.Helper()
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		t.Fatalf("failed to close temp file: %v", err)
	}
	return path, func() { os.Remove(path) }
}

func generateTempCertificates(t *testing.T) (string, string, func()) {
	t.Helper()
	certBytes, keyBytes, err := cert.GenerateSelfSignedCertKey(
		"localhost", []net.IP{net.ParseIP("127.0.0.1")}, nil,
	)
	if err != nil {
		t.Fatalf("failed to generate self-signed cert: %v", err)
	}

	certPath, cleanupCert := writeTempFile(t, "testcert-*.crt", certBytes)
	keyPath, cleanupKey := writeTempFile(t, "testkey-*.key", keyBytes)

	return keyPath, certPath, func() {
		cleanupKey()
		cleanupCert()
	}
}

func buildTestConfig(port int, certPath, keyPath string) *v1.GenericControllerConfig {
	cfg := &v1.GenericControllerConfig{
		ServingInfo: v1.HTTPServingInfo{
			ServingInfo: v1.ServingInfo{
				CertInfo: v1.CertInfo{
					CertFile: certPath,
					KeyFile:  keyPath,
				},
			},
		},
	}
	if port > 0 {
		cfg.ServingInfo.BindAddress = fmt.Sprintf(":%d", port)
	}
	return cfg
}

func newInsecureClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
}

func waitForServer(t *testing.T, port int) {
	t.Helper()
	if port == 0 {
		port = MetricsPort
	}

	timeout := 5 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	for {
		testURL := fmt.Sprintf("https://localhost:%d", port)
		resp, err := newInsecureClient().Get(testURL)
		if err == nil {
			resp.Body.Close()
			break
		}

		if ctx.Err() != nil {
			t.Fatalf("server did not start within %s, last error: %v", timeout, err)
		}

		time.Sleep(100 * time.Millisecond)
		t.Logf("waiting for server to start: %v", err)
	}
}

func startTestServer(t *testing.T, port int) (*MetricsServer, func()) {
	t.Helper()
	keyPath, certPath, cleanupCerts := generateTempCertificates(t)
	cfg := buildTestConfig(port, certPath, keyPath)

	srv, err := NewServer(cfg)
	if err != nil {
		cleanupCerts()
		t.Fatalf("failed to create metrics server: %v", err)
	}

	if err := srv.Start(); err != nil {
		cleanupCerts()
		t.Fatalf("failed to start metrics server: %v", err)
	}

	// wait for server to start
	waitForServer(t, port)

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Stop(ctx); err != nil {
			t.Errorf("failed to stop server: %v", err)
		}
		cleanupCerts()
	}

	return srv, cleanup
}

func findMetricsByName(buf io.ReadCloser, name string) []*io_prometheus_client.Metric {
	defer buf.Close()
	mf := io_prometheus_client.MetricFamily{}
	decoder := expfmt.NewDecoder(buf, "text/plain")
	for err := decoder.Decode(&mf); err == nil; err = decoder.Decode(&mf) {
		if *mf.Name == name {
			return mf.Metric
		}
	}
	return nil
}

func testQueryGaugeMetric(t *testing.T, port, value int, query string) {
	t.Helper()
	client := newInsecureClient()
	resp, err := client.Get(fmt.Sprintf("https://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("error requesting metrics server: %v", err)
	}
	metrics := findMetricsByName(resp.Body, query)
	if len(metrics) == 0 {
		t.Fatalf("unable to locate metric %s", query)
	}
	if metrics[0].Gauge.Value == nil {
		t.Fatalf("metric did not have value %s", query)
	}
	if *metrics[0].Gauge.Value != float64(value) {
		t.Fatalf("incorrect metric value %v for query %s", *metrics[0].Gauge.Value, query)
	}
}

func testQueryCounterMetric(t *testing.T, port, value int, query string) {
	t.Helper()
	client := newInsecureClient()
	resp, err := client.Get(fmt.Sprintf("https://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("error requesting metrics server: %v", err)
	}
	metrics := findMetricsByName(resp.Body, query)
	if len(metrics) == 0 {
		t.Fatalf("unable to locate metric %s", query)
	}
	if metrics[0].Counter.Value == nil {
		t.Fatalf("metric did not have value %s", query)
	}
	if *metrics[0].Counter.Value != float64(value) {
		t.Fatalf("incorrect metric value %v for query %s", *metrics[0].Counter.Value, query)
	}
}

func TestRun(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	client := newInsecureClient()
	resp, err := client.Get(fmt.Sprintf("https://localhost:%d/metrics", MetricsPort))
	if err != nil {
		t.Fatalf("error requesting metrics server: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, received %d instead.", resp.StatusCode)
	}
}

func TestRunWithCustomPort(t *testing.T) {
	port := 17001
	_, cleanup := startTestServer(t, port)
	defer cleanup()

	client := newInsecureClient()
	resp, err := client.Get(fmt.Sprintf("https://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("error requesting metrics server: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, received %d instead.", resp.StatusCode)
	}
}

func TestDegradedOn(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	Degraded(true)
	testQueryGaugeMetric(t, MetricsPort, 1, degradedQuery)
	// makes sure it does not go greater than 1
	Degraded(true)
	testQueryGaugeMetric(t, MetricsPort, 1, degradedQuery)
}

func TestDegradedOff(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	Degraded(false)
	testQueryGaugeMetric(t, MetricsPort, 0, degradedQuery)
	// makes sure it does not go less than 0
	Degraded(false)
	testQueryGaugeMetric(t, MetricsPort, 0, degradedQuery)
}

func TestConfigInvalidOn(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	ConfigInvalid(true)
	testQueryGaugeMetric(t, MetricsPort, 1, invalidConfigQuery)
	// makes sure it does not go greater than 1
	ConfigInvalid(true)
	testQueryGaugeMetric(t, MetricsPort, 1, invalidConfigQuery)
}

func TestConfigInvalidOff(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	ConfigInvalid(false)
	testQueryGaugeMetric(t, MetricsPort, 0, invalidConfigQuery)
	// makes sure it does not go less than 0
	ConfigInvalid(false)
	testQueryGaugeMetric(t, MetricsPort, 0, invalidConfigQuery)
}

func TestTBRInaccessibleOnBootOn(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	TBRInaccessibleOnBoot(true)
	testQueryGaugeMetric(t, MetricsPort, 1, tbrInaccessibleOnBootstrapQuery)
	// makes sure it does not go greater than 1
	TBRInaccessibleOnBoot(true)
	testQueryGaugeMetric(t, MetricsPort, 1, tbrInaccessibleOnBootstrapQuery)
}

func TestTBRInaccessibleOnBootOff(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	TBRInaccessibleOnBoot(false)
	testQueryGaugeMetric(t, MetricsPort, 0, tbrInaccessibleOnBootstrapQuery)
	// makes sure it does not go less than 0
	TBRInaccessibleOnBoot(false)
	testQueryGaugeMetric(t, MetricsPort, 0, tbrInaccessibleOnBootstrapQuery)
}

func TestImageStreamImportRetry(t *testing.T) {
	_, cleanup := startTestServer(t, 0)
	defer cleanup()

	for i := 1; i < 3; i++ {
		ImageStreamImportRetry("foo")
		testQueryCounterMetric(t, MetricsPort, i, importRetryQuery)
	}
}
