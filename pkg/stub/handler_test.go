package stub

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	v1 "github.com/openshift/api/samples/v1"
	"github.com/openshift/cluster-samples-operator/pkg/cache"
	operator "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
	"github.com/openshift/cluster-samples-operator/pkg/util"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	restclient "k8s.io/client-go/rest"

	configv1 "github.com/openshift/api/config/v1"
	imagev1 "github.com/openshift/api/image/v1"
	operatorsv1api "github.com/openshift/api/operator/v1"
	templatev1 "github.com/openshift/api/template/v1"

	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
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
	validate(true, err, "", cfg, []v1.ConfigConditionType{}, []corev1.ConditionStatus{}, t)
}

func TestNoArchOrDist(t *testing.T) {
	h, cfg, event := setup()
	err := h.Handle(event)
	// image in progress (4th entry, array index 3) should still be false when there is no content ... a la z or ppc
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	err = h.Handle(event)
	// and on a subsequent event, exists should still be false since we did not create any content previously
	validate(true, err, "", cfg, conditions, statuses, t)
}

func TestWithDist(t *testing.T) {
	h, cfg, event := setup()
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	err = h.Handle(event)
	// image in progress (4th entry, array index 3) should still be false when there is no content ... a la z or ppc
	statuses = []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	mimic(&h, x86OCPContentRootDir)
	err = h.Handle(event)
	// with content present, image im progress should now be true
	statuses = []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
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

	mimic(&h, x86OCPContentRootDir)
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg,
		conditions,
		statuses, t)
	err = h.Handle(event)
	statuses = []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
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
	validateArchOverride(true, err, "", cfg, conditions, []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t, v1.PPCArchitecture)
}

func TestWithBadArch(t *testing.T) {
	h, cfg, event := setup()
	cfg.Spec.Architectures = []string{
		"bad",
	}
	h.Handle(event)
	invalidConfig(t, "architecture bad unsupported", util.Condition(cfg, v1.ConfigurationValid))
}

func TestManagementState(t *testing.T) {
	h, cfg, event := setup()
	iskeys := getISKeys()
	tkeys := getTKeys()
	mimic(&h, x86OCPContentRootDir)
	cfg.Spec.ManagementState = operatorsv1api.Unmanaged

	err := h.Handle(event)
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
	err = h.Handle(event)
	statuses = []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)

	err = h.Handle(event)
	// event after in progress set to true, sets exists to true
	statuses = []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)

	// event after exists is true that should trigger samples upsert
	err = h.Handle(event)
	// event after in progress set to true
	statuses = []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
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
	// RemovePending now true
	statuses[4] = corev1.ConditionTrue
	validate(true, err, "", cfg, conditions, statuses, t)
	if cfg.Status.ManagementState == operatorsv1api.Removed {
		t.Fatalf("cfg status set to removed too early %#v", cfg)
	}

	// verify while we are image in progress no false and the remove on hold setting is still set to true
	cfg.ResourceVersion = "4"
	err = h.Handle(event)
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

	mimic(&h, x86OCPContentRootDir)

	err := h.Handle(event)
	validate(true, err, "", cfg, conditions, []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)

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
	importerror.Reason = "foo "
	importerror.Message = "<imagestream/foo> import failed <imagestream/foo>"
	importerror.Status = corev1.ConditionTrue
	util.ConditionUpdate(cfg, importerror)
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
	importerror = util.Condition(cfg, v1.ImportImageErrorsExist)
	if len(importerror.Reason) > 0 || importerror.Status == corev1.ConditionTrue {
		t.Fatalf("skipped imagestream still reporting error %#v", importerror)
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
	cfg.Spec.SamplesRegistry = "foo.io"
	iskeys := getISKeys()
	tkeys := getTKeys()

	mimic(&h, x86OCPContentRootDir)

	err := h.Handle(event)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)

	err = h.Handle(event)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)
	err = h.Handle(event)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)

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

	faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
	for _, key := range tkeys {
		if _, ok := h.skippedTemplates[key]; ok {
			t.Fatalf("bad skipped processing for %s: %#v", key, h)
		}
		if _, ok := faketclient.upsertkeys[key]; !ok {
			t.Fatalf("t did not reach client %s: %#v", key, h)
		}
	}

	// make sure registries are updated after already updating from the defaults
	cfg.Spec.SamplesRegistry = "bar.io"
	cfg.ResourceVersion = "2"
	// fake out that the samples completed updating
	progressing := util.Condition(cfg, v1.ImageChangesInProgress)
	progressing.Status = corev1.ConditionFalse
	util.ConditionUpdate(cfg, progressing)

	err = h.Handle(event)
	validate(true, err, "", cfg,
		conditions,
		[]corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}, t)
	is, _ = fakeisclient.Get("foo")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}
	is, _ = fakeisclient.Get("bar")
	if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, cfg.Spec.SamplesRegistry) {
		t.Fatalf("stream repo not updated %#v, %#v", is, h)
	}

	// make sure registries are updated when switch back to default
	cfg.ResourceVersion = "3"
	cfg.Spec.SamplesRegistry = ""
	// also make sure processing occurs for disruptive config change even if progressing==true
	progressing = util.Condition(cfg, v1.ImageChangesInProgress)
	// fake out that the samples completed updating
	progressing.Status = corev1.ConditionFalse
	util.ConditionUpdate(cfg, progressing)
	// reset operator image to clear out previous registry image override
	mimic(&h, x86OCPContentRootDir)

	err = h.Handle(event)
	is, _ = fakeisclient.Get("foo")
	if is == nil || strings.HasPrefix(is.Spec.DockerImageRepository, "bar.io") {
		t.Fatalf("foo stream repo still has bar.io")
	}
	is, _ = fakeisclient.Get("bar")
	if is == nil || strings.HasPrefix(is.Spec.DockerImageRepository, "bar.io") {
		t.Fatalf("bar stream repo still has bar.io")
	}

}

func TestImageStreamEvent(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86OCPContentRootDir)
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	// expedite the stream events coming in
	cache.ClearUpsertsCache()

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

	// mimic img change condition event that sets exists to true
	err = h.Handle(event)
	statuses[0] = corev1.ConditionTrue
	validate(true, err, "", cfg, conditions, statuses, t)

	// with the update above, now process both of the imagestreams and see the in progress condition
	// go false
	is.Annotations[v1.SamplesVersionAnnotation] = h.version
	h.processImageStreamWatchEvent(is, false)
	validate(true, err, "", cfg, conditions, statuses, t)
	statuses[3] = corev1.ConditionFalse
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
	validate(true, err, "", cfg, conditions, statuses, t)
}

func TestImageStreamErrorRetry(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86OCPContentRootDir)
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	// expedite the stream events coming in
	cache.ClearUpsertsCache()

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

	if !util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
		t.Fatalf("Import Error Condition not true: %#v", cfg)
	}

	fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
	fakeimporter := fakeisclient.ImageStreamImports("foo").(*fakeImageStreamImporter)

	if fakeimporter.count != 2 {
		t.Fatalf("incorrect amount of import calls %d", fakeimporter.count)
	}

	initialImportErrorLastUpdateTime := util.Condition(cfg, v1.ImportImageErrorsExist).LastUpdateTime
	h.processImageStreamWatchEvent(is, false)
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

	if util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
		t.Fatalf("Import Error Condition not true: %#v", cfg)
	}
}

func TestTemplateEvent(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86OCPContentRootDir)
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
	err = h.Handle(event)
	statuses[0] = corev1.ConditionTrue
	validate(true, err, "", cfg, conditions, statuses, t)
	// expedite the template events coming in
	cache.ClearUpsertsCache()

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
	h.crdwrapper.(*fakeCRDWrapper).cfg = cfg
	cache.ClearUpsertsCache()
	return h, cfg, util.Event{Object: cfg}
}

func TestImageStreamRemovedFromPayloadWithProgressingErrors(t *testing.T) {
	h, cfg, _ := setup()
	mimic(&h, x86OCPContentRootDir)
	progressing := util.Condition(cfg, v1.ImageChangesInProgress)
	progressing.Reason = "foo "
	errors := util.Condition(cfg, v1.ImportImageErrorsExist)
	errors.Reason = "bar "
	util.ConditionUpdate(cfg, progressing)
	util.ConditionUpdate(cfg, errors)
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
	if err != nil {
		t.Fatal(err)
	}
	progressing = util.Condition(cfg, v1.ImageChangesInProgress)
	if strings.Contains(progressing.Reason, "foo") {
		t.Fatal("progressing still has foo after it was no longer in payload")
	}
	is.Name = "bar"
	err = h.processImageStreamWatchEvent(is, false)
	errors = util.Condition(cfg, v1.ImportImageErrorsExist)
	if strings.Contains(errors.Reason, "bar") {
		t.Fatal("import errors still has bar after it was no longer in payload")
	}

}

func TestImageGetError(t *testing.T) {
	errors := []error{
		fmt.Errorf("getstreamerror"),
		kerrors.NewNotFound(schema.GroupResource{}, "getstreamerror"),
	}
	for _, iserr := range errors {
		h, cfg, event := setup()

		mimic(&h, x86OCPContentRootDir)

		fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
		fakeisclient.geterrors = map[string]error{"foo": iserr}

		statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
		err := h.Handle(event)
		if !kerrors.IsNotFound(iserr) {
			statuses[3] = corev1.ConditionUnknown
			validate(false, err, "getstreamerror", cfg, conditions, statuses, t)
		} else {
			validate(true, err, "", cfg, conditions, statuses, t)
		}
	}

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
	turnBackThreeHours := true
	for _, is := range streams {
		h, cfg, _ := setup()
		mimic(&h, x86OCPContentRootDir)
		dir := h.GetBaseDir(v1.X86Architecture, cfg)
		files, _ := h.Filefinder.List(dir)
		h.processFiles(dir, files, cfg)
		progressing := util.Condition(cfg, v1.ImageChangesInProgress)
		progressing.Status = corev1.ConditionTrue
		progressing.Reason = is.Name + " "
		util.ConditionUpdate(cfg, progressing)
		needCreds := util.Condition(cfg, v1.ImportCredentialsExist)
		needCreds.Status = corev1.ConditionTrue
		util.ConditionUpdate(cfg, needCreds)
		err := h.processImageStreamWatchEvent(is, false)
		if err != nil {
			t.Fatalf("processImageStreamWatchEvent error %#v for stream %#v", err, is)
		}
		if util.ConditionFalse(cfg, v1.ImportImageErrorsExist) {
			t.Fatalf("processImageStreamWatchEvent did not set import error to true %#v for stream %#v", cfg, is)
		}

		importErr := util.Condition(cfg, v1.ImportImageErrorsExist)
		if len(importErr.Reason) == 0 || !util.NameInReason(cfg, importErr.Reason, is.Name) {
			t.Fatalf("processImageStreamWatchEvent did not set import error reason field %#v for stream %#v", cfg, is)
		}
		if turnBackThreeHours {
			importErr.LastTransitionTime.Time = metav1.Now().Add(-3 * time.Hour)
		} else {
			importErr.LastTransitionTime = metav1.Now()
		}
		util.ConditionUpdate(cfg, importErr)
		status, reason, detail := util.ClusterOperatorStatusDegradedCondition(cfg)
		if (status != configv1.ConditionTrue && turnBackThreeHours) || (status == configv1.ConditionTrue && !turnBackThreeHours) {
			t.Fatalf("image import error from %#v not reflected in cluster status %#v", is, cfg)
		}
		if status == configv1.ConditionFalse && turnBackThreeHours && (len(reason) == 0 || len(detail) == 0) {
			t.Fatalf("image import error from is %#v with cfg %#v had reason %s and detail %s", is, cfg, reason, detail)
		}
		turnBackThreeHours = !turnBackThreeHours
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
	mimic(&h, x86OCPContentRootDir)
	dir := h.GetBaseDir(v1.X86Architecture, cfg)
	files, _ := h.Filefinder.List(dir)
	h.processFiles(dir, files, cfg)
	importError := util.Condition(cfg, v1.ImportImageErrorsExist)
	importError.Status = corev1.ConditionTrue
	importError.Reason = "foo "
	importError.Message = "<imagestream/foo> import failed <imagestream/foo>"
	util.ConditionUpdate(cfg, importError)
	err := h.processImageStreamWatchEvent(stream, false)
	if err != nil {
		t.Fatalf("processImageStreamWatchEvent error %#v", err)
	}
	if util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
		t.Fatalf("processImageStreamWatchEvent did not set import error to false %#v", cfg)
	}
	importErr := util.Condition(cfg, v1.ImportImageErrorsExist)
	if len(importErr.Reason) > 0 && util.NameInReason(cfg, importErr.Reason, stream.Name) {
		t.Fatalf("processImageStreamWatchEvent did not set import error reason field %#v", cfg)
	}
	if len(importErr.Message) > 0 {
		t.Fatalf("processImageStreamWatchEvent did not set import error message field %#v", cfg)
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

func TestTemplateGetEreror(t *testing.T) {
	errors := []error{
		fmt.Errorf("gettemplateerror"),
		kerrors.NewNotFound(schema.GroupResource{}, "gettemplateerror"),
	}
	for _, terr := range errors {
		h, cfg, event := setup()

		mimic(&h, x86OCPContentRootDir)

		faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
		faketclient.geterrors = map[string]error{"bo": terr}

		statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
		err := h.Handle(event)
		if !kerrors.IsNotFound(terr) {
			statuses[3] = corev1.ConditionUnknown
			validate(false, err, "gettemplateerror", cfg, conditions, statuses, t)
		} else {
			validate(true, err, "", cfg, conditions, statuses, t)
		}
	}

}

func TestDeletedCR(t *testing.T) {
	h, cfg, event := setup()
	event.Deleted = true
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(true, err, "", cfg, conditions, statuses, t)
}

func TestSameCR(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86OCPContentRootDir)
	cfg.ResourceVersion = "a"

	// first pass on the resource creates the samples, exists (first entry, index 0) is still false
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	err := h.Handle(event)
	validate(true, err, "", cfg, conditions, statuses, t)

	err = h.Handle(event)
	// next pass is when we expect exists to be true
	statuses[0] = corev1.ConditionTrue
	validate(true, err, "", cfg, conditions, statuses, t)

	err = h.Handle(event)
	// now with content, should see no change in status after duplicate event, where imagestream import status has not changed
	validate(true, err, "", cfg, conditions, statuses, t)

}

func TestBadTopDirList(t *testing.T) {
	h, cfg, event := setup()
	fakefinder := h.Filefinder.(*fakeResourceFileLister)
	fakefinder.errors = map[string]error{x86OCPContentRootDir: fmt.Errorf("badtopdir")}
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionUnknown, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(false, err, "badtopdir", cfg, conditions, statuses, t)
}

func TestBadSubDirList(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86OCPContentRootDir)
	fakefinder := h.Filefinder.(*fakeResourceFileLister)
	fakefinder.errors = map[string]error{x86OCPContentRootDir + "/imagestreams": fmt.Errorf("badsubdir")}
	err := h.Handle(event)
	statuses := []corev1.ConditionStatus{corev1.ConditionUnknown, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
	validate(false, err, "badsubdir", cfg, conditions, statuses, t)
}

func TestBadTopLevelStatus(t *testing.T) {
	h, cfg, event := setup()
	mimic(&h, x86OCPContentRootDir)
	fakestatus := h.crdwrapper.(*fakeCRDWrapper)
	fakestatus.updateerr = fmt.Errorf("badsdkupdate")
	err := h.Handle(event)
	// with deferring sdk updates to the very end, the local object will still have valid statuses on it, even though the error
	// error returned by h.Handle indicates etcd was not updated
	statuses := []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionFalse, corev1.ConditionFalse}
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
	return []string{"foo", "bar"}
}

func getTKeys() []string {
	return []string{"bo", "go"}
}

func mimic(h *Handler, topdir string) {
	cache.ClearUpsertsCache()
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
	fakeisgetter.streams = map[string]*imagev1.ImageStream{
		topdir + "/imagestreams/foo": foo,
		topdir + "/imagestreams/bar": bar,
		"foo":                        foo,
		"bar":                        bar,
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

func (f *fakeCRDWrapper) UpdateStatus(opcfg *v1.Config, dbg string) error { return f.updateerr }

func (f *fakeCRDWrapper) Update(opcfg *v1.Config) error { return f.updateerr }

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
