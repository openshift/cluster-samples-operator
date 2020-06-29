package metrics

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	mr "math/rand"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

var portMap = sync.Map{}

func TestMain(m *testing.M) {
	var err error

	mr.Seed(time.Now().UnixNano())

	tlsKey, tlsCRT, err = generateTempCertificates()
	if err != nil {
		panic(err)
	}

	// sets the default http client to skip certificate check.
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	code := m.Run()
	os.Remove(tlsKey)
	os.Remove(tlsCRT)
	os.Exit(code)
}

func generatePort(t *testing.T) int {
	port := 0
	for i := 0; i < 200; i++ {
		port = mr.Intn(4000) + 6000
		_, alreadyLoaded := portMap.LoadOrStore(port, port)
		if alreadyLoaded {
			t.Logf("port %v in use, trying again", port)
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	if port == 0 {
		t.Fatal("could not get unique metrics port")
	}
	return port
}

func generateTempCertificates() (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return "", "", err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, key.Public(), key)
	if err != nil {
		return "", "", err
	}

	cert, err := ioutil.TempFile("", "testcert-")
	if err != nil {
		return "", "", err
	}
	defer cert.Close()
	pem.Encode(cert, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	keyPath, err := ioutil.TempFile("", "testkey-")
	if err != nil {
		return "", "", err
	}
	defer keyPath.Close()
	pem.Encode(keyPath, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	return keyPath.Name(), cert.Name(), nil
}

func TestRun(t *testing.T) {
	ch := make(chan struct{})
	defer close(ch)
	port := generatePort(t)
	srv := BuildServer(port)
	go RunServer(srv, ch)

	resp, err := http.Get(fmt.Sprintf("https://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("error requesting metrics server: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, received %d instead.", resp.StatusCode)
	}
}

func runServer(t *testing.T) (chan struct{}, int) {
	ch := make(chan struct{})
	port := generatePort(t)
	srv := BuildServer(port)
	go RunServer(srv, ch)
	return ch, port
}

func testQueryGaugeMetric(t *testing.T, port, value int, query string) {
	resp, err := http.Get(fmt.Sprintf("https://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("error requesting metrics server: %v", err)
	}
	metrics := findMetricsByCounter(resp.Body, query)
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
	resp, err := http.Get(fmt.Sprintf("https://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("error requesting metrics server: %v", err)
	}
	metrics := findMetricsByCounter(resp.Body, query)
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

func findMetricsByCounter(buf io.ReadCloser, name string) []*io_prometheus_client.Metric {
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

func TestDegradedOn(t *testing.T) {
	ch, port := runServer(t)
	defer close(ch)

	Degraded(true)
	testQueryGaugeMetric(t, port, 1, degradedQuery)
	// makes sure it does not go greater than 1
	Degraded(true)
	testQueryGaugeMetric(t, port, 1, degradedQuery)
}

func TestDegradedOff(t *testing.T) {
	ch, port := runServer(t)
	defer close(ch)

	Degraded(false)
	testQueryGaugeMetric(t, port, 0, degradedQuery)
	// makes sure it does not go less than 0
	Degraded(false)
	testQueryGaugeMetric(t, port, 0, degradedQuery)
}

func TestConfigInvalidOn(t *testing.T) {
	ch, port := runServer(t)
	defer close(ch)

	ConfigInvalid(true)
	testQueryGaugeMetric(t, port, 1, invalidConfigQuery)
	// makes sure it does not go greater than 1
	ConfigInvalid(true)
	testQueryGaugeMetric(t, port, 1, invalidConfigQuery)
}

func TestConfigInvalidOff(t *testing.T) {
	ch, port := runServer(t)
	defer close(ch)

	ConfigInvalid(false)
	testQueryGaugeMetric(t, port, 0, invalidConfigQuery)
	// makes sure it does not go less than 0
	ConfigInvalid(false)
	testQueryGaugeMetric(t, port, 0, invalidConfigQuery)
}

func TestTBRInaccessibleOnBootOn(t *testing.T) {
	ch, port := runServer(t)
	defer close(ch)

	TBRInaccessibleOnBoot(true)
	testQueryGaugeMetric(t, port, 1, tbrInaccessibleOnBootstrapQuery)
	// makes sure it does not go greater than 1
	TBRInaccessibleOnBoot(true)
	testQueryGaugeMetric(t, port, 1, tbrInaccessibleOnBootstrapQuery)
}

func TestTBRInaccessibleOnBootOff(t *testing.T) {
	ch, port := runServer(t)
	defer close(ch)

	TBRInaccessibleOnBoot(false)
	testQueryGaugeMetric(t, port, 0, tbrInaccessibleOnBootstrapQuery)
	// makes sure it does not go less than 0
	TBRInaccessibleOnBoot(false)
	testQueryGaugeMetric(t, port, 0, tbrInaccessibleOnBootstrapQuery)
}

func TestImageStreamImportRetry(t *testing.T) {
	ch, port := runServer(t)
	defer close(ch)
	for i := 1; i < 3; i++ {
		ImageStreamImportRetry()
		testQueryCounterMetric(t, port, i, importRetryQuery)
	}
}
