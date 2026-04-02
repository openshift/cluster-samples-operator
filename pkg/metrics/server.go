package metrics

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	v1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

var (
	tlsCRT = "/etc/secrets/tls.crt"
	tlsKey = "/etc/secrets/tls.key"
)

// MetricsServer controls a http server capable of serving prometheus metrics
// through https connections. A metric server is configured by means of a
// v1.GenericControllerConfig object.
type MetricsServer struct {
	bindAddress string
	tlsConfig   *tls.Config
	httpServer  *http.Server
}

// NewServer returns a new metrics server configured according to the provided
// GenericControllerConfig. If no bind port is configured then the http
// server will listen on MetricsPort constant. If the configuration
// does not provide certificate paths then tlsCRT and tlsKey variables
// are used as default location.
func NewServer(cfg *v1.GenericControllerConfig) (*MetricsServer, error) {
	bindAddress := fmt.Sprintf(":%d", MetricsPort)
	if len(cfg.ServingInfo.BindAddress) != 0 {
		bindAddress = cfg.ServingInfo.BindAddress
	}

	minTLS, err := crypto.TLSVersion(cfg.ServingInfo.MinTLSVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid min tls version: %w", err)
	}

	var suites []uint16
	for _, suiteString := range cfg.ServingInfo.CipherSuites {
		suite, err := crypto.CipherSuite(suiteString)
		if err != nil {
			return nil, fmt.Errorf("invalid cipher suite: %w", err)
		}
		suites = append(suites, suite)
	}

	router := http.NewServeMux()
	router.Handle("/metrics", promhttp.Handler())

	crt, key := tlsCRT, tlsKey
	if len(cfg.ServingInfo.CertFile) != 0 {
		crt = cfg.ServingInfo.CertFile
	}
	if len(cfg.ServingInfo.KeyFile) != 0 {
		key = cfg.ServingInfo.KeyFile
	}

	certificate, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificates: %w", err)
	}

	return &MetricsServer{
		bindAddress: bindAddress,
		httpServer:  &http.Server{Handler: router},
		tlsConfig: &tls.Config{
			CipherSuites: suites,
			MinVersion:   minTLS,
			Certificates: []tls.Certificate{certificate},
		},
	}, nil
}

// Start starts the metrics server on a go routine. Fails during bind stage
// are immediately returned.
func (m *MetricsServer) Start() error {
	listener, err := net.Listen("tcp", m.bindAddress)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	go func() {
		if err := m.httpServer.Serve(
			tls.NewListener(listener, m.tlsConfig),
		); err != nil && err != http.ErrServerClosed {
			logrus.Errorf("failed to serve metrics: %s", err)
		}
	}()

	return nil
}

// Stop stops the underlying http server. This function waits for the server to
// gracefully exit (it might take a while).
func (m *MetricsServer) Stop(ctx context.Context) error {
	if err := m.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown http server: %w", err)
	}
	return nil
}

// Degraded sets the metric that indicates if the operator is in degraded
// mode or not.
func Degraded(deg bool) {
	if deg {
		degradedStat.Set(1)
		return
	}
	degradedStat.Set(0)
}

func ConfigInvalid(inv bool) {
	if inv {
		configInvalidStat.Set(1)
		return
	}
	configInvalidStat.Set(0)
}

func TBRInaccessibleOnBoot(badTBR bool) {
	if badTBR {
		tbrInaccessibleOnBootStat.Set(1)
		return
	}
	tbrInaccessibleOnBootStat.Set(0)
}

func ImageStreamImportRetry(imagestreamName string) {
	importRetryStat.With(prometheus.Labels{"imagestreamname": imagestreamName}).Inc()
}
