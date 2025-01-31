package stub

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	restclient "k8s.io/client-go/rest"

	configv1 "github.com/openshift/api/config/v1"
	imagev1 "github.com/openshift/api/image/v1"
	operatorsv1api "github.com/openshift/api/operator/v1"
	v1 "github.com/openshift/api/samples/v1"
	templatev1 "github.com/openshift/api/template/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"

	operator "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
	"github.com/openshift/cluster-samples-operator/pkg/util"
)

const (
	TestVersion = "1.0-test"
)

var (
	conditions = []v1.ConfigConditionType{v1.SamplesExist,
		v1.ImportCredentialsExist,
		v1.ConfigurationValid,
		v1.ImageChangesInProgress,
		v1.RemovePending,
		v1.MigrationInProgress,
		v1.ImportImageErrorsExist}
)

func TestWrongSampleResourceName(t *testing.T) {
	h, cfg, event := setup()
	cfg.Name = "foo"
	cfg.Status.Conditions = nil
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, []v1.ConfigConditionType{}, []corev1.ConditionStatus{}, t)
}

func TestWithDist(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
}

func TestWithArchDist(t *testing.T) {
	h, cfg, event := setup()
	if len(cfg.Spec.Architectures) == 0 {
		t.Errorf("arch not set on bootstrap")
	}
	testValue := runtime.GOARCH
	if testValue == v1.AMDArchitecture {
		testValue = v1.X86Architecture
	}
	if cfg.Spec.Architectures[0] != testValue {
		t.Errorf("arch set to %s instead of %s", cfg.Spec.Architectures[0], runtime.GOARCH)
	}

	mimic(&h, x86ContentRootDir)
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg,
		conditions,
		statuses, t)

}

func TestWithArch(t *testing.T) {
	h, cfg, event := setup()
	// without a mimic call this simulates our current PPC/390 stories of no samples content
	cfg.Spec.Architectures = []string{
		v1.PPCArchitecture,
	}
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validateArchOverride(true, err, "", cfg, conditions, []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t, v1.PPCArchitecture)
}

func TestWithBadArch(t *testing.T) {
	h, cfg, event := setup()
	cfg.Spec.Architectures = []string{
		"bad",
	}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	invalidConfig(t, "architecture bad unsupported", util.Condition(cfg, v1.ConfigurationValid))
}

func TestManagementState(t *testing.T) {
	h, cfg, event := setup()
	iskeys := getISKeys()
	tkeys := getTKeys()
	mimic(&h, x86ContentRootDir)
	cfg.Spec.ManagementState = operatorsv1api.Unmanaged

	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)

	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	for _, key := range iskeys {
		if _, ok := fakeisclient.upsertkeys[key]; ok {
			t.Fatalf("upserted imagestream while unmanaged %#v", cfg)
		}
	}
	faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
	for _, key := range tkeys {
		if _, ok := faketclient.upsertkeys[key]; ok {
			t.Fatalf("upserted template while unmanaged %#v", cfg)
		}
	}

	cfg.ResourceVersion = "2"
	cfg.Spec.ManagementState = operatorsv1api.Managed
	// get status to managed
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if cfg.Status.ManagementState != operatorsv1api.Managed {
		t.Fatalf("status not set to managed")
	}
	// should go to in progress
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses = []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, statuses, t)

	for _, key := range iskeys {
		if _, ok := fakeisclient.upsertkeys[key]; !ok {
			t.Fatalf("did not upsert imagestream while managed %#v", cfg)
		}
	}
	for _, key := range tkeys {
		if _, ok := faketclient.upsertkeys[key]; !ok {
			t.Fatalf("did not upsert template while managed %#v", cfg)
		}
	}

	// mimic a remove attempt ... in progress, index 3, already true
	cfg.ResourceVersion = "3"
	cfg.Spec.ManagementState = operatorsv1api.Removed
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	// RemovePending now true
	statuses[4] = corev1.ConditionTrue
	validate(true, err, "", cfg, conditions, statuses, t)
	if cfg.Status.ManagementState == operatorsv1api.Removed {
		t.Fatalf("cfg status set to removed too early %#v", cfg)
	}

	// verify while we are image in progress no false and the remove on hold setting is still set to true
	cfg.ResourceVersion = "4"
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	// SamplesExists, ImageChangesInProgress, ImportImageErrorsExists all false
	statuses[0] = corev1.ConditionFalse
	statuses[3] = corev1.ConditionFalse
	statuses[6] = corev1.ConditionFalse
	validate(true, err, "", cfg, conditions, statuses, t)
	if cfg.Status.ManagementState != operatorsv1api.Removed {
		t.Fatalf("cfg status should have been set to removed %#v", cfg)
	}

	cfg.ResourceVersion = "5"
	err = h.Handle(event)
	// remove pending should be false
	statuses[4] = corev1.ConditionFalse
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, statuses, t)

}

func TestSkipped(t *testing.T) {
	h, cfg, event := setup()
	iskeys := getISKeys()
	tkeys := getTKeys()
	cfg.Spec.SkippedImagestreams = iskeys
	cfg.Spec.SkippedTemplates = tkeys
	cfg.Status.SkippedImagestreams = iskeys
	cfg.Status.SkippedTemplates = tkeys

	mimic(&h, x86ContentRootDir)

	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)

	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	for _, key := range iskeys {
		if _, ok := h.skippedImagestreams[key]; !ok {
			t.Fatalf("bad skipped processing for %s: %#v", key, h)
		}
		if _, ok := fakeisclient.upsertkeys[key]; ok {
			t.Fatalf("skipped is reached client %s: %#v", key, h)
		}
	}
	faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
	for _, key := range tkeys {
		if _, ok := h.skippedTemplates[key]; !ok {
			t.Fatalf("bad skipped processing for %s: %#v", key, h)
		}
		if _, ok := faketclient.upsertkeys[key]; ok {
			t.Fatalf("skipped t reached client %s: %#v", key, h)
		}
	}

	// also, even with an import error, on an imagestream event, the import error should be cleared out
	importerror := util.Condition(cfg, v1.ImportImageErrorsExist)
	importerror.Status = corev1.ConditionTrue
	util.ConditionUpdate(cfg, importerror)

	fakecmclient := h.configmapclientwrapper.(*fakeConfigMapClientWrapper)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Data: map[string]string{
			"foo": "could not import",
		},
		BinaryData: nil,
	}
	fakecmclient.configMaps["foo"] = cm

	event.Object = &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "openshift",
			Labels:    map[string]string{},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					// no Name field set on purpose, cover more code paths
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
					},
				},
			},
		},
	}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	_, stillHasCM := fakecmclient.configMaps["foo"]
	if stillHasCM {
		t.Fatalf("clean imagestream did not result in configmap getting deleted")
	}
	event.Deleted = true
	event.Object = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
	}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	importerror = util.Condition(cfg, v1.ImportImageErrorsExist)
	if importerror.Status == corev1.ConditionTrue {
		t.Fatalf("skipped imagestream still reporting error %#v", importerror)
	}
}

type BlockTestScenario struct {
	Name           string
	RegistryName   string
	ImageConfig    configv1.Image
	ExpectedResult bool
}

func buildBlockTestScenarios() []BlockTestScenario {
	testSecenarios := []BlockTestScenario{
		{
			Name:         "Test AllowRegistriesForImport whitelisted",
			RegistryName: "registry.redhat.io",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "redhat.io",
						},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistriesForImport whitelisted but empty registry name (1)",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "registry.redhat.io",
						},
						{
							DomainName: "quay.io",
						},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistriesForImport whitelisted but empty registry name (2)",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "registry.redhat.io",
						},
						{
							DomainName: "access.redhat.com",
						},
						{
							DomainName: "quay.io",
						},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistriesForImport whitelisted but empty registry name (3)",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "registry.redhat.io",
						},
						{
							DomainName: "registry.access.redhat.com",
						},
						{
							DomainName: "quay.io",
						},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistriesForImport not whitelisted but empty registry name (1)",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "quay.io",
						},
					},
				},
			},
			ExpectedResult: true,
		},
		{
			Name:         "Test AllowRegistriesForImport not whitelisted but empty registry name (2)",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "quay.io",
						},
						{
							DomainName: "access.redhat.com",
						},
					},
				},
			},
			ExpectedResult: true,
		},
		{
			Name:         "Test AllowRegistriesForImport not whitelisted but empty registry name (3)",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "quay.io",
						},
						{
							DomainName: "redhat.io",
						},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistriesForImport not whitelisted",
			RegistryName: "registry.redhat.io",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "quay.io",
						},
					},
				},
			},
			ExpectedResult: true,
		},
		{
			Name:         "Test AllowRegistries whitelisted",
			RegistryName: "registry.redhat.io",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						AllowedRegistries: []string{"registry.redhat.io"},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistries whitelisted but empty registry name",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						AllowedRegistries: []string{
							"registry.redhat.io",
							"registry.access.redhat.com",
							"quay.io",
						},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistriesforimport and allowedregistries whitelisted but empty registry name",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					AllowedRegistriesForImport: []configv1.RegistryLocation{
						{
							DomainName: "registry.redhat.io",
						},
						{
							DomainName: "registry.access.redhat.com",
						},
						{
							DomainName: "quay.io",
						},
					},
					RegistrySources: configv1.RegistrySources{
						AllowedRegistries: []string{
							"registry.redhat.io",
							"registry.access.redhat.com",
							"quay.io",
						},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test AllowRegistries not whitelisted but empty registry name(1)",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						AllowedRegistries: []string{
							"registry.redhat.io",
							"registry.access.redhat.com",
						},
					},
				},
			},
			ExpectedResult: true,
		},
		{
			Name:         "Test AllowRegistries not whitelisted",
			RegistryName: "registry.redhat.io",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						AllowedRegistries: []string{"quay.io"},
					},
				},
			},
			ExpectedResult: true,
		},
		{
			Name:         "Test BlockedRegistries whitelisted",
			RegistryName: "registry.redhat.io",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						BlockedRegistries: []string{"registry.redhat.io"},
					},
				},
			},
			ExpectedResult: true,
		},
		{
			Name:         "Test BlockedRegistries not whitelisted but emtpy registry name ",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						BlockedRegistries: []string{"registry.redhat.io"},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test BlockedRegistries not whitelisted",
			RegistryName: "registry.redhat.io",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						BlockedRegistries: []string{"quay.io"},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test BlockedRegistries not whitelisted",
			RegistryName: "registry.access.redhat.com",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{
					RegistrySources: configv1.RegistrySources{
						BlockedRegistries: []string{"quay.io"},
					},
				},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test None whitelisted",
			RegistryName: "registry.redhat.io",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{},
			},
			ExpectedResult: false,
		},
		{
			Name:         "Test None whitelisted and empty registry name",
			RegistryName: "",
			ImageConfig: configv1.Image{
				Spec: configv1.ImageSpec{},
			},
			ExpectedResult: false,
		},
	}

	return testSecenarios
}

func TestImageConfigBlocksImageStreamCreation(t *testing.T) {
	h, _, _ := setup()
	h.configclient = new(configv1client.ConfigV1Client)
	testSecenarios := buildBlockTestScenarios()
	for _, scenario := range testSecenarios {
		getImageConfig = func(h *Handler) (*configv1.Image, error) {
			return &scenario.ImageConfig, nil
		}
		blocked := h.imageConfigBlocksImageStreamCreation(scenario.RegistryName)
		if blocked != scenario.ExpectedResult {
			t.Errorf("Scenario failed: %s", scenario.Name)
		}
	}
}

func TestTBRInaccessibleBit(t *testing.T) {
	h, cfg, event := setup()
	event.Object = cfg
	cfg.Spec.ManagementState = operatorsv1api.Removed
	h.tbrCheckFailed = true
	// ensure check failed field stays set for all 3 stages
	// of removed processing
	i := 0
	for i < 3 {
		err := h.Handle(event)
		if err != nil {
			t.Fatalf("err %s cfg %#v", err.Error(), cfg)
		}
		i++
	}
	if !h.tbrCheckFailed {
		t.Fatalf("TBR bit get reset")
	}

}

func TestProcessed(t *testing.T) {
	h, cfg, event := setup()
	event.Object = cfg
	mimic(&h, x86ContentRootDir)

	// initial boostrap creation of samples
	h.Handle(event)

	// shortcut completed initial bootstrap, and then change the samples registry setting
	cfg.Spec.SamplesRegistry = "foo.io"
	progress := util.Condition(cfg, v1.ImageChangesInProgress)
	progress.Status = corev1.ConditionFalse
	util.ConditionUpdate(cfg, progress)
	exists := util.Condition(cfg, v1.SamplesExist)
	exists.Status = corev1.ConditionTrue
	util.ConditionUpdate(cfg, exists)
	h.crdwrapper.Update(cfg)
	iskeys := getISKeys()
	tkeys := getTKeys()

	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)

	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	for _, key := range iskeys {
		if _, ok := h.skippedImagestreams[key]; ok {
			t.Fatalf("bad skipped processing for %s: %#v", key, h)
		}
		if _, ok := fakeisclient.upsertkeys[key]; !ok {
			t.Fatalf("is did not reach client %s: %#v", key, h)
		}
	}
	is, _ := fakeisclient.Get("foo")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("bar")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("baz")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}

	faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
	for _, key := range tkeys {
		if _, ok := h.skippedTemplates[key]; ok {
			t.Fatalf("bad skipped processing for %s: %#v", key, h)
		}
		if _, ok := faketclient.upsertkeys[key]; !ok {
			t.Fatalf("t did not reach client %s: %#v", key, h)
		}
	}

	// get status samples registry set to foo.io
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if cfg.Status.SamplesRegistry != "foo.io" {
		t.Fatalf("status did not pick up new samples registry")
	}

	// make sure registries are updated after already updating from the defaults
	cfg.Spec.SamplesRegistry = "bar.io"
	cfg.ResourceVersion = "2"
	// clear out config map tracking
	fakecfgmapclient := h.configmapclientwrapper.(*fakeConfigMapClientWrapper)
	fakecfgmapclient.configMaps = map[string]*corev1.ConfigMap{}
	// lack of copy fix previously masked that complete updating means version updated as well
	cfg.Status.Version = h.version
	h.crdwrapper.Update(cfg)

	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if cfg.Status.SamplesRegistry != "bar.io" {
		t.Fatalf("second update to status registry not in status")
	}
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)
	is, _ = fakeisclient.Get("foo")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("bar")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("baz")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}

	// make sure registries are updated when sampleRegistry is the form of host/path
	cfg.Spec.SamplesRegistry = "foo.io/bar"
	cfg.ResourceVersion = "3"
	// clear out config map tracking
	fakecfgmapclient.configMaps = map[string]*corev1.ConfigMap{}
	h.crdwrapper.Update(cfg)
	// reset operator image to clear out previous registry image override
	mimic(&h, x86ContentRootDir)

	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if cfg.Status.SamplesRegistry != "foo.io/bar" {
		t.Fatalf("third update to samples registry not in status")
	}
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)
	is, _ = fakeisclient.Get("foo")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("bar")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("baz")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}

	// make sure registries are updated when sampleRegistry is the form of host:port/path
	cfg.Spec.SamplesRegistry = "foo.io:1111/bar"
	cfg.ResourceVersion = "4"
	// clear out config map tracking
	fakecfgmapclient.configMaps = map[string]*corev1.ConfigMap{}
	h.crdwrapper.Update(cfg)
	// reset operator image to clear out previous registry image override
	mimic(&h, x86ContentRootDir)

	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if cfg.Status.SamplesRegistry != "foo.io:1111/bar" {
		t.Fatalf("fourth update to samples registry no in status")
	}
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)
	is, _ = fakeisclient.Get("foo")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("bar")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("baz")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	// make sure registries are updated when switch back to default
	cfg.ResourceVersion = "5"
	cfg.Spec.SamplesRegistry = ""
	// clear out config map tracking
	fakecfgmapclient.configMaps = map[string]*corev1.ConfigMap{}
	h.crdwrapper.Update(cfg)
	// reset operator image to clear out previous registry image override
	mimic(&h, x86ContentRootDir)

	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if cfg.Status.SamplesRegistry != "" {
		t.Fatalf("fifth update to samples registry not in status")
	}
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	is, _ = fakeisclient.Get("foo")
	if is == nil || strings.HasPrefix(is.Spec.DockerImageRepository, "bar.io") {
		t.Fatalf("foo stream repo still has bar.io")
	}
	is, _ = fakeisclient.Get("bar")
	if is == nil || strings.HasPrefix(is.Spec.DockerImageRepository, "bar.io") {
		t.Fatalf("bar stream repo still has bar.io")
	}
	is, _ = fakeisclient.Get("baz")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
}

func TestImageStreamEvent(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	// expedite the stream events coming in
	//cache.ClearUpsertsCache()

	tagVersion := int64(1)
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: h.version,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:       "foo",
					Generation: &tagVersion,
				},
			},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "foo",
					Items: []imagev1.TagEvent{
						{
							Generation: tagVersion,
						},
					},
				},
			},
		},
	}

	cfg.Status.Architectures = []string{v1.X86Architecture}
	cfg.Status.SkippedImagestreams = []string{}
	cfg.Status.SkippedTemplates = []string{}
	h.processImageStreamWatchEvent(is, false)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, statuses, t)

	// now make sure when a non standard change event is not ignored and that we update
	// the imagestream ... to mimic, remove annotation
	delete(is.Annotations, v1.SamplesVersionAnnotation)
	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	delete(fakeisclient.upsertkeys, "foo")
	h.processImageStreamWatchEvent(is, false)
	if _, ok := fakeisclient.upsertkeys["foo"]; !ok {
		t.Fatalf("is did not reach client %s: %#v", "foo", h)
	}

	// with the update above, now process both of the imagestreams and see the in progress condition
	// go false
	is.Annotations[v1.SamplesVersionAnnotation] = h.version
	h.processImageStreamWatchEvent(is, false)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, statuses, t)
	is = &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: h.version,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:       "bar",
					Generation: &tagVersion,
				},
			},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "bar",
					Items: []imagev1.TagEvent{
						{
							Generation: tagVersion,
						},
					},
				},
			},
		},
	}
	h.processImageStreamWatchEvent(is, false)
	is = &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "baz",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: h.version,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:       "baz",
					Generation: &tagVersion,
				},
			},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "baz",
					Items: []imagev1.TagEvent{
						{
							Generation: tagVersion,
						},
					},
				},
			},
		},
	}
	h.processImageStreamWatchEvent(is, false)
	h.processImageCondition()
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, statuses, t)
}

func TestImageStreamErrorRetry(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)

	tagVersion := int64(1)
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: h.version,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:       "foo",
					Generation: &tagVersion,
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "foofoo",
					},
				},
				{
					Name:       "bar",
					Generation: &tagVersion,
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "foofoo",
					},
				}},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "foo",
					Conditions: []imagev1.TagEventCondition{
						{
							Generation: tagVersion,
							Status:     corev1.ConditionFalse,
							Message:    "failed import",
						},
					},
				},
				{
					Tag: "bar",
					Conditions: []imagev1.TagEventCondition{
						{
							Generation: tagVersion,
							Status:     corev1.ConditionFalse,
							Message:    "failed import",
						},
					},
				}},
		},
	}

	h.processImageStreamWatchEvent(is, false)

	fakecmclient := h.configmapclientwrapper.(*fakeConfigMapClientWrapper)
	cm, exists := fakecmclient.configMaps[is.Name]
	if !exists {
		t.Fatalf("no associated configmap for %s", is.Name)
	}
	event = util.Event{Object: cm}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)

	if !util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
		t.Fatalf("Import Error Condition not true: %#v", cfg)
	}

	if !h.imageStreamHasErrors(is.Name) {
		t.Fatalf("Import errors not registered in config map for %s", is.Name)
	}

	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	fakeimporter := fakeisclient.ImageStreamImports("foo").(*fakeImageStreamImporter)

	if fakeimporter.count != 2 {
		t.Fatalf("incorrect amount of import calls %d", fakeimporter.count)
	}

	initialImportErrorLastUpdateTime := util.Condition(cfg, v1.ImportImageErrorsExist).LastUpdateTime
	h.processImageStreamWatchEvent(is, false)
	cm, _ = fakecmclient.configMaps[is.Name]
	event = util.Event{Object: cm}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	// refetch to see if updated
	importError := util.Condition(cfg, v1.ImportImageErrorsExist)
	if !importError.LastUpdateTime.Equal(&initialImportErrorLastUpdateTime) {
		t.Fatalf("Import Error Condition updated too soon: old update time %s new update time %s", initialImportErrorLastUpdateTime.String(), importError.LastUpdateTime.String())
	}

	// now let's push back 15 minutes and update
	lastUpdateTime, _ := h.imagestreamRetry[is.Name]
	lastUpdateTime.Time = metav1.Now().Add(-15 * time.Minute)
	h.imagestreamRetry[is.Name] = lastUpdateTime
	util.ConditionUpdate(cfg, importError)
	// save a copy for compare
	fifteenMinutesAgo := lastUpdateTime

	h.processImageStreamWatchEvent(is, false)

	// refetch and make sure it has changed
	lastUpdateTime, _ = h.imagestreamRetry[is.Name]
	if lastUpdateTime.Equal(&fifteenMinutesAgo) {
		t.Fatalf("Import Error Condition should have been updated: old update time %s new update time %s", initialImportErrorLastUpdateTime.String(), util.Condition(cfg, v1.ImportImageErrorsExist).LastUpdateTime.String())
	}

	tagVersion = int64(2)
	for _, tag := range is.Status.Tags {
		tag.Conditions[0] = imagev1.TagEventCondition{}
		tag.Items = []imagev1.TagEvent{
			{
				Generation: tagVersion,
			},
		}
	}

	h.processImageStreamWatchEvent(is, false)

	_, stillHasCM := fakecmclient.configMaps["foo"]
	if stillHasCM {
		t.Fatalf("clean imagestream did not result in configmap getting deleted")
	}
	event.Deleted = true
	event.Object = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
	}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)

	if util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
		t.Fatalf("Import Error Condition still true: %#v", cfg)
	}
}

func TestTemplateEvent(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses[0] = corev1.ConditionTrue
	validate(true, err, "", cfg, conditions, statuses, t)
	// expedite the template events coming in
	//cache.ClearUpsertsCache()

	template := &templatev1.Template{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bo",
		},
	}

	faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
	delete(faketclient.upsertkeys, "bo")
	h.processTemplateWatchEvent(template, false)
	if _, ok := faketclient.upsertkeys["bo"]; !ok {
		t.Fatalf("template did not reach client %s: %#v", "bo", h)
	}

}

func setup() (Handler, *v1.Config, util.Event) {
	h := NewTestHandler()
	cfg, _ := h.CreateDefaultResourceIfNeeded(nil)
	cfg = h.initConditions(cfg)
	cfg.Status.ManagementState = operatorsv1api.Managed
	h.StoreCurrentValidConfig(cfg)
	h.crdwrapper.(*fakeCRDWrapper).cfg = cfg
	//cache.ClearUpsertsCache()
	return h, cfg, util.Event{Object: cfg}
}

func TestTemplateRemovedFromPayload(t *testing.T) {
	h, _, _ := setup()
	mimic(&h, x86ContentRootDir)

	_, filePath, doUpsert, err := h.prepSamplesWatchEvent("template", "no-longer-exists", map[string]string{}, false)
	switch {
	case len(filePath) > 0:
		t.Fatalf("should not have returned a file path")
	case doUpsert:
		t.Fatalf("do upsert should not be true")
	case err != nil:
		t.Fatalf("got unexpected err %s", err.Error())
	}
}

func TestImageStreamRemovedFromPayloadWithProgressingErrors(t *testing.T) {
	h, cfg, _ := setup()
	mimic(&h, x86ContentRootDir)
	errors := util.Condition(cfg, v1.ImportImageErrorsExist)
	util.ConditionUpdate(cfg, errors)
	cm := &corev1.ConfigMap{}
	cm.Name = "foo"
	fakeconfigmapclient := h.configmapclientwrapper.(*fakeConfigMapClientWrapper)
	fakeconfigmapclient.Create(cm)
	cm = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar",
		},
		Data: map[string]string{"bar": "could not import"},
	}
	fakeconfigmapclient.Create(cm)

	fakefile := h.Filefinder.(*fakeResourceFileLister)
	fakefile.files = map[string][]fakeFileInfo{}
	tagVersion := int64(1)
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: h.version,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:       "foo",
					Generation: &tagVersion,
				},
			},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "foo",
					Items: []imagev1.TagEvent{
						{
							Generation: tagVersion,
						},
					},
				},
			},
		},
	}
	err := h.processImageStreamWatchEvent(is, false)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	cm, err = fakeconfigmapclient.Get("foo")
	if cm != nil {
		t.Fatal("still tracking foo after it was no longer in payload")
	}
	is.Name = "bar"
	err = h.processImageStreamWatchEvent(is, false)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	cm, err = fakeconfigmapclient.Get("bar")
	if cm != nil {
		t.Fatal("still tracking bar after it was no longer in payload")
	}
	if util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
		t.Fatal("still tracking import error true after no longer in payload")
	}

}

func TestImageStreamCreateErrorDegradedReason(t *testing.T) {
	err := kerrors.NewServiceUnavailable("BadService")
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	fakeisclient.geterrors = map[string]error{"foo": err}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	_, reason := util.AnyConditionUnknown(cfg)
	if reason != "APIServerServiceUnavailableError" {
		t.Fatalf("unexpected reason %s", reason)
	}

}

func TestImageGetError(t *testing.T) {
	errors := []error{
		fmt.Errorf("getstreamerror"),
		kerrors.NewNotFound(schema.GroupResource{}, "getstreamerror"),
	}
	for _, iserr := range errors {
		h, cfg, event := setup()

		mimic(&h, x86ContentRootDir)

		fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
		fakeisclient.geterrors = map[string]error{"foo": iserr}

		statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
		err := h.Handle(event)
		cfg, _ = h.crdwrapper.Get(cfg.Name)
		if !kerrors.IsNotFound(iserr) {
			statuses[0] = corev1.ConditionUnknown
			statuses[3] = corev1.ConditionFalse
			validate(false, err, "getstreamerror", cfg, conditions, statuses, t)
		} else {
			validate(true, err, "", cfg, conditions, statuses, t)
		}
	}

}

func TestImageUpdateError(t *testing.T) {

	h, cfg, event := setup()

	mimic(&h, x86ContentRootDir)

	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	fakeisclient.upserterrors = map[string]error{"foo": kerrors.NewServiceUnavailable("upsertstreamerror")}

	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses[0] = corev1.ConditionUnknown
	statuses[3] = corev1.ConditionFalse
	validate(false, err, "upsertstreamerror", cfg, conditions, statuses, t)

}

func TestImageStreamImportError(t *testing.T) {
	two := int64(2)
	one := int64(1)
	streams := []*imagev1.ImageStream{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
				Annotations: map[string]string{
					v1.SamplesVersionAnnotation: TestVersion,
				},
			},
			Spec: imagev1.ImageStreamSpec{
				Tags: []imagev1.TagReference{
					{
						Name:       "1",
						Generation: &two,
					},
					{
						Name:       "2",
						Generation: &one,
					},
				},
			},
			Status: imagev1.ImageStreamStatus{
				Tags: []imagev1.NamedTagEventList{
					{
						Tag: "1",
						Items: []imagev1.TagEvent{
							{
								Generation: two,
							},
						},
					},
					{
						Tag: "2",
						Conditions: []imagev1.TagEventCondition{
							{
								Generation: two,
								Status:     corev1.ConditionFalse,
								Message:    "Internal error occurred: unknown: Not Found",
								Reason:     "InternalError",
								Type:       imagev1.ImportSuccess,
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
				Annotations: map[string]string{
					v1.SamplesVersionAnnotation: TestVersion,
				},
			},
			Spec: imagev1.ImageStreamSpec{
				Tags: []imagev1.TagReference{
					{
						Name:       "1",
						Generation: &two,
					},
					{
						Name:       "2",
						Generation: &one,
					},
				},
			},
			Status: imagev1.ImageStreamStatus{
				Tags: []imagev1.NamedTagEventList{
					{
						Tag: "1",
						Conditions: []imagev1.TagEventCondition{
							{
								Generation: two,
								Status:     corev1.ConditionFalse,
								Message:    "Internal error occurred: unknown: Not Found",
								Reason:     "InternalError",
								Type:       imagev1.ImportSuccess,
							},
						},
					},
					{
						Tag: "2",
						Conditions: []imagev1.TagEventCondition{
							{
								Generation: two,
								Status:     corev1.ConditionFalse,
								Message:    "Internal error occurred: unknown: Not Found",
								Reason:     "InternalError",
								Type:       imagev1.ImportSuccess,
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
				Annotations: map[string]string{
					v1.SamplesVersionAnnotation: TestVersion,
				},
			},
			Spec: imagev1.ImageStreamSpec{
				Tags: []imagev1.TagReference{
					{
						Name:       "1",
						Generation: &two,
					},
					{
						Name:       "2",
						Generation: &one,
					},
					{
						Name:       "3",
						Generation: &one,
					},
				},
			},
			Status: imagev1.ImageStreamStatus{
				Tags: []imagev1.NamedTagEventList{
					{
						Tag: "1",
						Conditions: []imagev1.TagEventCondition{
							{
								Generation: two,
								Status:     corev1.ConditionFalse,
								Message:    "Internal error occurred: unknown: Not Found",
								Reason:     "InternalError",
								Type:       imagev1.ImportSuccess,
							},
						},
					},
					{
						Tag: "2",
						Conditions: []imagev1.TagEventCondition{
							{
								Generation: two,
								Status:     corev1.ConditionFalse,
								Message:    "Internal error occurred: unknown: Not Found",
								Reason:     "InternalError",
								Type:       imagev1.ImportSuccess,
							},
						},
					},
				},
			},
		},
	}
	for _, is := range streams {
		h, cfg, _ := setup()
		mimic(&h, x86ContentRootDir)
		dir := h.GetBaseDir(v1.X86Architecture, cfg)
		files, _ := h.Filefinder.List(dir)
		h.processFiles(dir, files, cfg)

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: is.Name,
			},
		}
		fakecmclient := h.configmapclientwrapper.(*fakeConfigMapClientWrapper)
		fakecmclient.configMaps[is.Name] = cm

		needCreds := util.Condition(cfg, v1.ImportCredentialsExist)
		needCreds.Status = corev1.ConditionTrue
		util.ConditionUpdate(cfg, needCreds)
		err := h.processImageStreamWatchEvent(is, false)
		if err != nil {
			t.Fatalf("processImageStreamWatchEvent error %#v for stream %#v", err, is)
		}
		event := util.Event{Object: cm}
		h.Handle(event)
		cfg, _ = h.crdwrapper.Get(cfg.Name)
		if util.ConditionFalse(cfg, v1.ImportImageErrorsExist) {
			t.Fatalf("processImageStreamWatchEvent did not set import error to true %#v for stream %#v", cfg, is)
		}

		cm, err = fakecmclient.Get(is.Name)
		if err != nil {
			t.Fatalf("error on cm get %s: %s", is.Name, err.Error())
		}
		if cm == nil {
			t.Fatalf("cm get nil %s", is.Name)
		}
		if cm.Data == nil || len(cm.Data) == 0 {
			t.Fatalf("processImageStreamWatchEvent did not put error for imagestream %s in configmap: %#v", is.Name, cm)
		}
	}
}

func TestImageStreamTagImportErrorRecovery(t *testing.T) {
	two := int64(2)
	one := int64(1)
	stream := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: TestVersion,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:       "1",
					Generation: &two,
				},
				{
					Name:       "2",
					Generation: &one,
				},
			},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "1",
					Items: []imagev1.TagEvent{
						{
							Generation: two,
						},
					},
					Conditions: []imagev1.TagEventCondition{
						{
							Status:     corev1.ConditionFalse,
							Generation: two,
						},
					},
				},
				{
					Tag: "2",
					Items: []imagev1.TagEvent{
						{
							Generation: two,
						},
					},
				},
			},
		},
	}
	h, cfg, _ := setup()
	mimic(&h, x86ContentRootDir)
	dir := h.GetBaseDir(v1.X86Architecture, cfg)
	files, _ := h.Filefinder.List(dir)
	h.processFiles(dir, files, cfg)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	importError := util.Condition(cfg, v1.ImportImageErrorsExist)
	importError.Status = corev1.ConditionTrue
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Data: map[string]string{
			"1": "could not import",
			"2": "could not import",
		},
	}
	fakecmclient := h.configmapclientwrapper.(*fakeConfigMapClientWrapper)
	fakecmclient.configMaps["foo"] = cm
	util.ConditionUpdate(cfg, importError)
	h.crdwrapper.Update(cfg)
	err := h.processImageStreamWatchEvent(stream, false)
	if err != nil {
		t.Fatalf("processImageStreamWatchEvent error %#v", err)
	}
	cm, stillHasCM := fakecmclient.configMaps["foo"]
	if !stillHasCM {
		t.Fatalf("partially clean imagestream did not keep configmap")
	}
	_, stillHasCleanTag := cm.Data["2"]
	if stillHasCleanTag {
		t.Fatalf("partially clean imagestream still has clean tag in configmap")
	}
	_, stillHasBrokenTag := cm.Data["1"]
	if !stillHasBrokenTag {
		t.Fatalf("partially clean imagestream does not have tag that is still broke in configmap")
	}
}

func TestImageStreamImportErrorRecovery(t *testing.T) {
	two := int64(2)
	one := int64(1)
	stream := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: TestVersion,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:       "1",
					Generation: &two,
				},
				{
					Name:       "2",
					Generation: &one,
				},
			},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "1",
					Items: []imagev1.TagEvent{
						{
							Generation: two,
						},
					},
				},
				{
					Tag: "2",
					Items: []imagev1.TagEvent{
						{
							Generation: two,
						},
					},
				},
			},
		},
	}
	h, cfg, _ := setup()
	mimic(&h, x86ContentRootDir)
	dir := h.GetBaseDir(v1.X86Architecture, cfg)
	files, _ := h.Filefinder.List(dir)
	h.processFiles(dir, files, cfg)
	importError := util.Condition(cfg, v1.ImportImageErrorsExist)
	importError.Status = corev1.ConditionTrue
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Data: map[string]string{"foo": "could not import"},
	}
	fakecmclient := h.configmapclientwrapper.(*fakeConfigMapClientWrapper)
	fakecmclient.configMaps["foo"] = cm
	util.ConditionUpdate(cfg, importError)
	h.crdwrapper.Update(cfg)
	err := h.processImageStreamWatchEvent(stream, false)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if err != nil {
		t.Fatalf("processImageStreamWatchEvent error %#v", err)
	}
	_, stillHasCM := fakecmclient.configMaps["foo"]
	if stillHasCM {
		t.Fatalf("clean imagestream did not delete configmap")
	}
	event := util.Event{
		Deleted: true,
		Object:  cm,
	}
	h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	if util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
		t.Fatalf("processImageStreamWatchEvent did not set import error to false %#v", cfg)
	}
}

func TestImageImportRequestCreation(t *testing.T) {
	one := int64(1)
	stream := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			Annotations: map[string]string{
				v1.SamplesVersionAnnotation: TestVersion,
			},
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "1",
					From: &corev1.ObjectReference{
						Name: "myreg.myrepo.myname:latest",
						Kind: "DockerImage",
					},
					Generation: &one,
				},
			},
		},
		Status: imagev1.ImageStreamStatus{
			Tags: []imagev1.NamedTagEventList{
				{
					Tag: "1",
					Items: []imagev1.TagEvent{
						{
							Generation: one,
						},
					},
				},
			},
		},
	}
	isi, err := importTag(stream, "1")
	if isi == nil || err != nil {
		t.Fatalf("importTag problem obj %#v err %#v", isi, err)
	}
}

func TestTemplateGetError(t *testing.T) {
	errors := []error{
		fmt.Errorf("gettemplateerror"),
		kerrors.NewNotFound(schema.GroupResource{}, "gettemplateerror"),
	}
	for _, terr := range errors {
		h, cfg, event := setup()

		mimic(&h, x86ContentRootDir)

		faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
		faketclient.geterrors = map[string]error{"bo": terr}

		statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
		err := h.Handle(event)
		cfg, _ = h.crdwrapper.Get(cfg.Name)
		if !kerrors.IsNotFound(terr) {
			statuses[0] = corev1.ConditionUnknown
			statuses[3] = corev1.ConditionFalse
			validate(false, err, "gettemplateerror", cfg, conditions, statuses, t)
		} else {
			validate(true, err, "", cfg, conditions, statuses, t)
		}
	}

}

func TestTemplateUpsertError(t *testing.T) {
	h, cfg, event := setup()

	mimic(&h, x86ContentRootDir)

	faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
	faketclient.upserterrors = map[string]error{"bo": kerrors.NewServiceUnavailable("upsertstreamerror")}

	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses[0] = corev1.ConditionUnknown
	statuses[3] = corev1.ConditionFalse
	validate(false, err, "upsertstreamerror", cfg, conditions, statuses, t)
}

func TestDeletedCR(t *testing.T) {
	h, cfg, event := setup()
	event.Deleted = true
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
}

func TestSameCR(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	cfg.ResourceVersion = "a"

	statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, statuses, t)

	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	validate(true, err, "", cfg, conditions, statuses, t)

	err = h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	// now with content, should see no change in status after duplicate event, where imagestream import status has not changed
	validate(true, err, "", cfg, conditions, statuses, t)

}

func TestBadTopDirList(t *testing.T) {
	h, cfg, event := setup()
	fakefinder := h.Filefinder.(*fakeResourceFileLister)
	fakefinder.errors = map[string]error{x86ContentRootDir: fmt.Errorf("badtopdir")}
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionUnknown, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(false, err, "badtopdir", cfg, conditions, statuses, t)
}

func TestBadSubDirList(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	fakefinder := h.Filefinder.(*fakeResourceFileLister)
	fakefinder.errors = map[string]error{x86ContentRootDir + "/imagestreams": fmt.Errorf("badsubdir")}
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	statuses := []corev1.ConditionStatus{corev1.ConditionUnknown, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(false, err, "badsubdir", cfg, conditions, statuses, t)
	_, reason := util.AnyConditionUnknown(cfg)
	if reason != "FileSystemError" {
		t.Fatalf("incorrect reason %s", reason)
	}
}

func TestBadTopLevelStatus(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86ContentRootDir)
	fakestatus := h.crdwrapper.(*fakeCRDWrapper)
	fakestatus.updateerr = fmt.Errorf("badsdkupdate")
	err := h.Handle(event)
	cfg, _ = h.crdwrapper.Get(cfg.Name)
	// with deferring sdk updates to the very end, the local object will still have valid statuses on it, even though the error
	// error returned by h.Handle indicates etcd was not updated
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(false, err, "badsdkupdate", cfg, conditions, statuses, t)
}

func invalidConfig(t *testing.T, msg string, cfgValid *v1.ConfigCondition) {
	if cfgValid.Status != corev1.ConditionFalse {
		t.Fatalf("config valid condition not false: %v", cfgValid)
	}
	if !strings.Contains(cfgValid.Message, msg) {
		t.Fatalf("wrong config valid message: %s", cfgValid.Message)
	}
}

func getISKeys() []string {
	return []string{"foo", "bar", "baz"}
}

func getTKeys() []string {
	return []string{"bo", "go"}
}

func mimic(h *Handler, topdir string) {
	//cache.ClearUpsertsCache()
	registry1 := "registry.access.redhat.com"
	registry2 := "registry.redhat.io"
	fakefile := h.Filefinder.(*fakeResourceFileLister)
	fakefile.files = map[string][]fakeFileInfo{
		topdir: {
			{
				name: "imagestreams",
				dir:  true,
			},
			{
				name: "templates",
				dir:  true,
			},
		},
		topdir + "/" + "imagestreams": {
			{
				name: "foo",
				dir:  false,
			},
			{
				name: "bar",
				dir:  false,
			},
			{
				name: "baz",
				dir:  false,
			},
		},
		topdir + "/" + "templates": {
			{
				name: "bo",
				dir:  false,
			},
			{
				name: "go",
				dir:  false,
			},
		},
	}
	fakeisgetter := h.Fileimagegetter.(*fakeImageStreamFromFileGetter)
	foo := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "foo",
			Labels: map[string]string{},
		},
		Spec: imagev1.ImageStreamSpec{
			DockerImageRepository: registry1,
			Tags: []imagev1.TagReference{
				{
					// no Name field set on purpose, cover more code paths
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
					},
				},
			},
		},
	}
	bar := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "bar",
			Labels: map[string]string{},
		},
		Spec: imagev1.ImageStreamSpec{
			DockerImageRepository: registry2,
			Tags: []imagev1.TagReference{
				{
					From: &corev1.ObjectReference{
						Name: registry2,
						Kind: "DockerImage",
					},
				},
			},
		},
	}
	baz := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "baz",
			Labels: map[string]string{},
		},
		Spec: imagev1.ImageStreamSpec{
			DockerImageRepository: registry2,
			Tags: []imagev1.TagReference{
				{
					From: &corev1.ObjectReference{
						Name: registry2 + "/repo/image:1.0",
						Kind: "DockerImage",
					},
				},
			},
		},
	}
	fakeisgetter.streams = map[string]*imagev1.ImageStream{
		topdir + "/imagestreams/foo": foo,
		topdir + "/imagestreams/bar": bar,
		topdir + "/imagestreams/baz": baz,
		"foo":                        foo,
		"bar":                        bar,
		"baz":                        baz,
	}
	faketempgetter := h.Filetemplategetter.(*fakeTemplateFromFileGetter)
	bo := &templatev1.Template{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "bo",
			Labels: map[string]string{},
		},
	}
	gogo := &templatev1.Template{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "go",
			Labels: map[string]string{},
		},
	}
	faketempgetter.templates = map[string]*templatev1.Template{
		topdir + "/templates/bo": bo,
		topdir + "/templates/go": gogo,
		"bo":                     bo,
		"go":                     gogo,
	}

}

func validateArchOverride(succeed bool, err error, errstr string, cfg *v1.Config, statuses []v1.ConfigConditionType, conditions []corev1.ConditionStatus, t *testing.T, arch string) {
	if succeed && err != nil {
		t.Fatal(err)
	}
	if len(statuses) != len(conditions) {
		t.Fatalf("bad input statuses %#v conditions %#v", statuses, conditions)
	}
	if !succeed {
		if err == nil {
			t.Fatalf("error should have been reported")
		}
		if !strings.Contains(err.Error(), errstr) {
			t.Fatalf("unexpected error: %v, expected: %v", err, errstr)
		}
	}
	if cfg != nil {
		if len(cfg.Status.Conditions) != len(statuses) {
			t.Fatalf("condition arrays different lengths got %v\n expected %v", cfg.Status.Conditions, statuses)
		}
		for i, c := range statuses {
			if cfg.Status.Conditions[i].Type != c {
				t.Fatalf("statuses in wrong order or different types have %#v\n expected %#v", cfg.Status.Conditions, statuses)
			}
			if cfg.Status.Conditions[i].Status != conditions[i] {
				t.Fatalf("unexpected for succeed %v have status condition %#v expected condition %#v and status %#v", succeed, cfg.Status.Conditions[i], c, conditions[i])
			}
		}
		testValue := arch
		if testValue == v1.AMDArchitecture {
			testValue = v1.X86Architecture
		}
		if cfg.Spec.Architectures[0] != testValue {
			t.Fatalf("arch set to %s instead of %s", cfg.Spec.Architectures[0], runtime.GOARCH)
		}
	}

}

func validate(succeed bool, err error, errstr string, cfg *v1.Config, statuses []v1.ConfigConditionType, conditions []corev1.ConditionStatus, t *testing.T) {
	validateArchOverride(succeed, err, errstr, cfg, statuses, conditions, t, runtime.GOARCH)
}

func NewTestHandler() Handler {
	h := Handler{}

	h.initter = &fakeInClusterInitter{}

	h.crdwrapper = &fakeCRDWrapper{}
	cvowrapper := fakeCVOWrapper{}
	cvowrapper.state = &configv1.ClusterOperator{
		TypeMeta: metav1.TypeMeta{
			APIVersion: configv1.SchemeGroupVersion.String(),
			Kind:       "ClusterOperator",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "goo",
			Namespace: "gaa",
		},
		Status: configv1.ClusterOperatorStatus{
			Conditions: []configv1.ClusterOperatorStatusCondition{},
		},
	}

	h.cvowrapper = operator.NewClusterOperatorHandler(nil)
	h.cvowrapper.ClusterOperatorWrapper = &cvowrapper

	h.configmapclientwrapper = &fakeConfigMapClientWrapper{
		configMaps: map[string]*corev1.ConfigMap{},
	}

	h.skippedImagestreams = make(map[string]bool)
	h.skippedTemplates = make(map[string]bool)

	h.Fileimagegetter = &fakeImageStreamFromFileGetter{streams: map[string]*imagev1.ImageStream{}}
	h.Filetemplategetter = &fakeTemplateFromFileGetter{}
	h.Filefinder = &fakeResourceFileLister{files: map[string][]fakeFileInfo{}}

	h.imageclientwrapper = &fakeImageStreamClientWrapper{
		upsertkeys:   map[string]bool{},
		streams:      map[string]*imagev1.ImageStream{},
		geterrors:    map[string]error{},
		listerrors:   map[string]error{},
		upserterrors: map[string]error{},
	}
	h.templateclientwrapper = &fakeTemplateClientWrapper{
		upsertkeys:   map[string]bool{},
		templates:    map[string]*templatev1.Template{},
		geterrors:    map[string]error{},
		listerrors:   map[string]error{},
		upserterrors: map[string]error{},
	}

	h.imagestreamFile = make(map[string]string)
	h.templateFile = make(map[string]string)
	h.imagestreatagToImage = make(map[string]string)
	h.imagestreamRetry = make(map[string]metav1.Time)
	h.version = TestVersion

	return h
}

type fakeFileInfo struct {
	name     string
	size     int64
	mode     os.FileMode
	modeTime time.Time
	sys      interface{}
	dir      bool
}

func (f *fakeFileInfo) Name() string {
	return f.name
}
func (f *fakeFileInfo) Size() int64 {
	return f.size
}
func (f *fakeFileInfo) Mode() os.FileMode {
	return f.mode
}
func (f *fakeFileInfo) ModTime() time.Time {
	return f.modeTime
}
func (f *fakeFileInfo) Sys() interface{} {
	return f.sys
}
func (f *fakeFileInfo) IsDir() bool {
	return f.dir
}

type fakeImageStreamFromFileGetter struct {
	streams map[string]*imagev1.ImageStream
	err     error
}

func (f *fakeImageStreamFromFileGetter) Get(fullFilePath string) (*imagev1.ImageStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	is, _ := f.streams[fullFilePath]
	return is, nil
}

type fakeTemplateFromFileGetter struct {
	templates map[string]*templatev1.Template
	err       error
}

func (f *fakeTemplateFromFileGetter) Get(fullFilePath string) (*templatev1.Template, error) {
	if f.err != nil {
		return nil, f.err
	}
	t, _ := f.templates[fullFilePath]
	return t, nil
}

type fakeResourceFileLister struct {
	files  map[string][]fakeFileInfo
	errors map[string]error
}

func (f *fakeResourceFileLister) List(dir string) ([]os.FileInfo, error) {
	err, _ := f.errors[dir]
	if err != nil {
		return nil, err
	}
	files, ok := f.files[dir]
	if !ok {
		return []os.FileInfo{}, nil
	}
	// jump through some hoops for method has pointer receiver compile errors
	fis := make([]os.FileInfo, 0, len(files))
	for _, fi := range files {
		tfi := fakeFileInfo{
			name:     fi.name,
			size:     fi.size,
			mode:     fi.mode,
			modeTime: fi.modeTime,
			sys:      fi.sys,
			dir:      fi.dir,
		}
		fis = append(fis, &tfi)
	}
	return fis, nil
}

type fakeImageStreamClientWrapper struct {
	streams                 map[string]*imagev1.ImageStream
	upsertkeys              map[string]bool
	geterrors               map[string]error
	upserterrors            map[string]error
	listerrors              map[string]error
	fakeImageStreamImporter *fakeImageStreamImporter
}

func (f *fakeImageStreamClientWrapper) Get(name string) (*imagev1.ImageStream, error) {
	err, _ := f.geterrors[name]
	if err != nil {
		return nil, err
	}
	is, _ := f.streams[name]
	return is, nil
}

func (f *fakeImageStreamClientWrapper) List(opts metav1.ListOptions) (*imagev1.ImageStreamList, error) {
	err, _ := f.geterrors["openshift"]
	if err != nil {
		return nil, err
	}
	list := &imagev1.ImageStreamList{}
	for _, is := range f.streams {
		list.Items = append(list.Items, *is)
	}
	return list, nil
}

func (f *fakeImageStreamClientWrapper) Create(stream *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	if stream == nil {
		return nil, nil
	}
	f.upsertkeys[stream.Name] = true
	err, _ := f.upserterrors[stream.Name]
	if err != nil {
		return nil, err
	}
	f.streams[stream.Name] = stream.DeepCopy()
	return stream, nil
}

func (f *fakeImageStreamClientWrapper) Update(stream *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return f.Create(stream)
}

func (f *fakeImageStreamClientWrapper) Delete(name string, opts *metav1.DeleteOptions) error {
	return nil
}

func (f *fakeImageStreamClientWrapper) Watch() (watch.Interface, error) {
	return nil, nil
}

func (f *fakeImageStreamClientWrapper) ImageStreamImports(namespace string) imagev1client.ImageStreamImportInterface {
	if f.fakeImageStreamImporter == nil {
		f.fakeImageStreamImporter = &fakeImageStreamImporter{}
	}
	return f.fakeImageStreamImporter
}

type fakeImageStreamImporter struct {
	count int
}

func (f *fakeImageStreamImporter) Create(ctx context.Context, isi *imagev1.ImageStreamImport, opts metav1.CreateOptions) (*imagev1.ImageStreamImport, error) {
	f.count++
	return &imagev1.ImageStreamImport{}, nil
}

type fakeConfigMapClientWrapper struct {
	configMaps map[string]*corev1.ConfigMap
}

func (g *fakeConfigMapClientWrapper) Get(name string) (*corev1.ConfigMap, error) {
	cm, ok := g.configMaps[name]
	if !ok {
		return nil, kerrors.NewNotFound(corev1.Resource("configmap"), name)
	}
	return cm, nil
}

func (g *fakeConfigMapClientWrapper) List() ([]*corev1.ConfigMap, error) {
	cmList := []*corev1.ConfigMap{}
	for _, cm := range g.configMaps {
		cmList = append(cmList, cm)
	}
	return cmList, nil
}

func (g *fakeConfigMapClientWrapper) Create(cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	g.configMaps[cm.Name] = cm
	return cm, nil
}

func (g *fakeConfigMapClientWrapper) Update(cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	return g.Create(cm)
}

func (g *fakeConfigMapClientWrapper) Delete(name string) error {
	delete(g.configMaps, name)
	return nil
}

type fakeTemplateClientWrapper struct {
	templates    map[string]*templatev1.Template
	upsertkeys   map[string]bool
	geterrors    map[string]error
	upserterrors map[string]error
	listerrors   map[string]error
}

func (f *fakeTemplateClientWrapper) Get(name string) (*templatev1.Template, error) {
	err, _ := f.geterrors[name]
	if err != nil {
		return nil, err
	}
	is, _ := f.templates[name]
	return is, nil
}

func (f *fakeTemplateClientWrapper) List(opts metav1.ListOptions) (*templatev1.TemplateList, error) {
	err, _ := f.geterrors["openshift"]
	if err != nil {
		return nil, err
	}
	list := &templatev1.TemplateList{}
	for _, is := range f.templates {
		list.Items = append(list.Items, *is)
	}
	return list, nil
}

func (f *fakeTemplateClientWrapper) Create(t *templatev1.Template) (*templatev1.Template, error) {
	if t == nil {
		return nil, nil
	}
	f.upsertkeys[t.Name] = true
	err, _ := f.upserterrors[t.Name]
	if err != nil {
		return nil, err
	}
	f.templates[t.Name] = t.DeepCopy()
	return t, nil
}

func (f *fakeTemplateClientWrapper) Update(t *templatev1.Template) (*templatev1.Template, error) {
	return f.Create(t)
}

func (f *fakeTemplateClientWrapper) Delete(name string, opts *metav1.DeleteOptions) error {
	return nil
}

func (f *fakeTemplateClientWrapper) Watch() (watch.Interface, error) {
	return nil, nil
}

type fakeInClusterInitter struct{}

func (f *fakeInClusterInitter) init(h *Handler, restconfig *restclient.Config) {}

type fakeCRDWrapper struct {
	updateerr error
	createerr error
	geterr    error
	cfg       *v1.Config
}

func (f *fakeCRDWrapper) UpdateStatus(opcfg *v1.Config, dbg string) error {
	f.cfg = opcfg
	return f.updateerr
}

func (f *fakeCRDWrapper) Update(opcfg *v1.Config) error {
	f.cfg = opcfg
	return f.updateerr
}

func (f *fakeCRDWrapper) Create(opcfg *v1.Config) error { return f.createerr }

func (f *fakeCRDWrapper) Get(name string) (*v1.Config, error) {
	return f.cfg, f.geterr
}

type fakeCVOWrapper struct {
	updateerr error
	createerr error
	geterr    error
	state     *configv1.ClusterOperator
}

func (f *fakeCVOWrapper) UpdateStatus(state *configv1.ClusterOperator) error { return f.updateerr }

func (f *fakeCVOWrapper) Create(state *configv1.ClusterOperator) error { return f.createerr }

func (f *fakeCVOWrapper) Get(name string) (*configv1.ClusterOperator, error) {
	return f.state, f.geterr
}
