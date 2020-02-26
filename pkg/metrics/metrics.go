package metrics

import (
	"encoding/json"
	"strings"

	corelisters "k8s.io/client-go/listers/core/v1"

	"github.com/prometheus/client_golang/prometheus"

	operatorv1 "github.com/openshift/api/operator/v1"

	configv1 "github.com/openshift/api/samples/v1"
	"github.com/openshift/cluster-samples-operator/pkg/client"
	"github.com/openshift/cluster-samples-operator/pkg/util"
	sampoplisters "github.com/openshift/client-go/samples/listers/samples/v1"

	"github.com/sirupsen/logrus"
)

const (
	// per https://prometheus.io/docs/practices/naming/#metric-names feel our metrics here fall under the _info category of classification

	// since we want to index by imagestream name, where each imagestream is either failing or not failing, this is why
	// we went with the _info suffix vs. the _total suffix
	failedImportsQuery = "openshift_samples_failed_imagestream_import_info"

	degradedQuery = "openshift_samples_degraded_info"

	invalidConfigQuery = "openshift_samples_invalidconfig_info"

	invalidSecretQuery = "openshift_samples_invalidsecret_info"

	missingSecret        = "missing_secret"
	missingTBRCredential = "missing_tbr_credential"

	tbrInaccessibleOnBootstrapQuery = "openshift_samples_tbr_inaccessible_info"

	// MetricsPort is the IP port supplied to the HTTP server used for prometheus,
	// and matches what is specified in the corresponding Service and ServiceMonitor
	MetricsPort = 60000
)

var (
	importsFailedDesc = prometheus.NewDesc(failedImportsQuery,
		"Indicates by name whether a samples imagestream import has failed or not (1 == failure, 0 == succes).",
		[]string{"name"},
		nil)
	invalidSecretDesc = prometheus.NewDesc(invalidSecretQuery,
		"Indicates if the pull secret is functional in the openshift namespace.  If not functional, the reason supplied either points to it missing in the openshift namesapce, or if it is missing credentials for registry.redhat.io when they should exist (1 == not functional, 0 == functional).",
		[]string{"reason"},
		nil)
	degradedStat = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: degradedQuery,
		Help: "Indicates if the samples operator is degraded.",
	})
	configInvalidStat = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: invalidConfigQuery,
		Help: "Indicates the configuration for the samples operator has an invalid configuration setting.",
	})
	tbrInaccessibleOnBootStat = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: tbrInaccessibleOnBootstrapQuery,
		Help: "Indicates that during its initial installation the samples operator could not access registry.redhat.io and it boostrapped as removed.",
	})

	sc         = samplesCollector{}
	registered = false

	// streams is the list of streams from the install payload that is maintained by the Handler
	streams = []string{}
)

func ClearStreams() {
	streams = []string{}
}

func StreamsEmpty() bool {
	return len(streams) == 0
}

func AddStream(stream string) {
	streams = append(streams, stream)
}

type dockerConfigJSON struct {
	Auths map[string]map[string]string `json:"auths"`
}

type samplesCollector struct {
	secrets corelisters.SecretNamespaceLister
	config  sampoplisters.ConfigLister
}

func (sc *samplesCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- importsFailedDesc
	ch <- invalidSecretDesc
}

func (sc *samplesCollector) Collect(ch chan<- prometheus.Metric) {
	skips := map[string]bool{}
	cfg, err := sc.config.Get(configv1.ConfigName)
	if err != nil {
		logrus.Infof("metrics sample config retrieval failed with: %s", err.Error())
		return
	}

	if cfg.Spec.ManagementState == operatorv1.Removed ||
		cfg.Spec.ManagementState == operatorv1.Unmanaged {
		return
	}

	for _, skip := range cfg.Spec.SkippedImagestreams {
		skips[skip] = true
	}

	importFailuresExist := util.ConditionTrue(cfg, configv1.ImportImageErrorsExist)
	importFailures := util.Condition(cfg, configv1.ImportImageErrorsExist)
	importFailuresReason := importFailures.Reason
	for _, stream := range streams {
		_, skipped := skips[stream]

		if skipped {
			// skipping-an-imagestream means we consider the metric as 0 since we don't care if it failed import
			addCountGauge(ch, importsFailedDesc, stream, float64(0))
			continue
		}
		// if there are import failures, and this stream is in the list maintained in the Reason field, set to 1
		if importFailuresExist && (strings.HasPrefix(importFailuresReason, stream+" ") || strings.Contains(importFailuresReason, " "+stream+" ") || strings.HasSuffix(importFailuresReason, " "+stream)) {
			addCountGauge(ch, importsFailedDesc, stream, float64(1))
			continue
		}
		addCountGauge(ch, importsFailedDesc, stream, float64(0))
	}

	if !util.ConditionTrue(cfg, configv1.ConfigurationValid) {
		configInvalidStat.Set(1)
	} else {
		configInvalidStat.Set(0)
	}

	if !util.ConditionTrue(cfg, configv1.ImportCredentialsExist) {
		addCountGauge(ch, invalidSecretDesc, missingSecret, float64(1))
	} else {
		addCountGauge(ch, invalidSecretDesc, missingSecret, float64(0))
	}
	if len(cfg.Spec.SamplesRegistry) > 0 {
		// we do not flag missing TBR credentials if they have overriden the
		// samples registry
		addCountGauge(ch, invalidSecretDesc, missingTBRCredential, float64(0))
		return
	}
	secret, err := sc.secrets.Get("pull-secret")
	if err != nil {
		logrus.Infof("metrics pull secret retrieval failed with: %s", err.Error())
		addCountGauge(ch, invalidSecretDesc, missingTBRCredential, float64(1))
		return
	}
	dockerConfigJSON := dockerConfigJSON{}
	buf, ok := secret.Data[".dockerconfigjson"]
	if !ok {
		logrus.Infof(".dockerconfigjson is missing from pull secret")
		addCountGauge(ch, invalidSecretDesc, missingTBRCredential, float64(1))
		return
	}
	err = json.Unmarshal(buf, &dockerConfigJSON)
	if err != nil {
		logrus.Infof("error unmarshalling .dockerconfigjson: %s", err.Error())
		addCountGauge(ch, invalidSecretDesc, missingTBRCredential, float64(1))
		return
	}
	_, hasTBRCreds := dockerConfigJSON.Auths["registry.redhat.io"]
	if hasTBRCreds {
		addCountGauge(ch, invalidSecretDesc, missingTBRCredential, float64(0))
		return
	}
	logrus.Printf(".dockerconfigjson is missing an entry for registry.redhat.io")
	addCountGauge(ch, invalidSecretDesc, missingTBRCredential, float64(1))
	return

}

func addCountGauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, name string, v float64) {
	lv := []string{name}
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, lv...)
}

func init() {
	prometheus.MustRegister(degradedStat, configInvalidStat, tbrInaccessibleOnBootStat)
}

func InitializeMetricsCollector(listers *client.Listers) {
	sc.secrets = listers.ConfigNamespaceSecrets
	sc.config = listers.Config

	if !registered {
		prometheus.MustRegister(&sc)
	}
}
