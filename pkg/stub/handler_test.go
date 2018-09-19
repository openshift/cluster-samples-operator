package stub

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"testing"

	"github.com/openshift/cluster-samples-operator/pkg/apis/samplesoperator/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	imagev1 "github.com/openshift/api/image/v1"
	templatev1 "github.com/openshift/api/template/v1"

	"github.com/operator-framework/operator-sdk/pkg/sdk"
)

func TestWrongSampleResourceName(t *testing.T) {
	h, sr, event := setup()
	sr.Name = "foo"
	err := h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{}, []corev1.ConditionStatus{}, t)
}

func TestNoArchOrDist(t *testing.T) {
	h, sr, event := setup()
	err := h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)
}

func TestWithDist(t *testing.T) {
	distlist := []v1alpha1.SamplesDistributionType{
		v1alpha1.CentosSamplesDistribution,
		v1alpha1.RHELSamplesDistribution,
	}

	for _, dist := range distlist {
		h, sr, event := setup()
		sr.Spec.InstallType = dist
		err := h.Handle(nil, event)
		validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)
		//sr.Status.Conditions = []v1alpha1.SamplesResourceCondition{}
	}
}

func TestWithArchDist(t *testing.T) {
	distlist := []v1alpha1.SamplesDistributionType{
		v1alpha1.CentosSamplesDistribution,
		v1alpha1.RHELSamplesDistribution,
	}

	for _, dist := range distlist {
		h, sr, event := setup()
		sr.Spec.Architectures = []string{
			x86,
		}
		mimic(&h, dist, x86OKDContentRootDir)
		sr.Spec.InstallType = dist
		err := h.Handle(nil, event)
		validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)
		//sr.Status.Conditions = []v1alpha1.SamplesResourceCondition{}
	}

	h, sr, event := setup()
	mimic(&h, v1alpha1.RHELSamplesDistribution, ppc64OCPContentRootDir)
	sr.Spec.InstallType = v1alpha1.RHELSamplesDistribution
	sr.Spec.Architectures = []string{
		ppc,
	}
	err := h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)

	// verify cannot change arch and distro
	_, sr, _ = setup()
	event.Object = sr
	sr.ResourceVersion = "2"
	sr.Spec.InstallType = v1alpha1.CentosSamplesDistribution
	sr.Spec.Architectures = []string{
		x86,
	}
	mimic(&h, v1alpha1.CentosSamplesDistribution, x86OCPContentRootDir)
	err = h.Handle(nil, event)
	validate(false, err, "cannot change installtype from", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.ConfigurationValid}, []corev1.ConditionStatus{corev1.ConditionFalse}, t)
	sr.Spec.InstallType = v1alpha1.RHELSamplesDistribution
	err = h.Handle(nil, event)
	validate(false, err, "cannot change architectures from", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.ConfigurationValid}, []corev1.ConditionStatus{corev1.ConditionFalse}, t)
}

func TestWithBadDist(t *testing.T) {
	h, sr, event := setup()
	sr.Spec.InstallType = v1alpha1.SamplesDistributionType("foo")
	err := h.Handle(nil, event)
	validate(false, err, "invalid install type", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionUnknown}, t)
}

func TestWithBadDistPPCArch(t *testing.T) {
	h, sr, event := setup()
	sr.Spec.InstallType = v1alpha1.SamplesDistributionType("foo")
	sr.Spec.Architectures = []string{
		ppc,
	}
	err := h.Handle(nil, event)
	validate(false, err, "invalid install type", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionUnknown}, t)
}

func TestWithArch(t *testing.T) {
	h, sr, event := setup()
	sr.Spec.Architectures = []string{
		x86,
	}
	err := h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)
}

func TestWithBadArch(t *testing.T) {
	h, sr, event := setup()
	sr.Spec.Architectures = []string{
		"bad",
	}
	err := h.Handle(nil, event)
	validate(false, err, "architecture bad unsupported", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionUnknown}, t)
}

func TestSkipped(t *testing.T) {
	h, sr, event := setup()
	event.Object = sr
	iskeys := getISKeys()
	tkeys := getTKeys()
	sr.Spec.SkippedImagestreams = iskeys
	sr.Spec.SkippedTemplates = tkeys

	mimic(&h, v1alpha1.CentosSamplesDistribution, x86OCPContentRootDir)

	err := h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)

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

}

func TestProcessed(t *testing.T) {
	dists := []v1alpha1.SamplesDistributionType{
		v1alpha1.CentosSamplesDistribution,
		v1alpha1.RHELSamplesDistribution,
	}

	for _, dist := range dists {
		h, sr, event := setup()
		event.Object = sr
		sr.Spec.InstallType = dist
		sr.Spec.SamplesRegistry = "foo.io"
		iskeys := getISKeys()
		tkeys := getTKeys()

		if dist == v1alpha1.CentosSamplesDistribution {
			mimic(&h, dist, x86OKDContentRootDir)
		}
		if dist == v1alpha1.RHELSamplesDistribution {
			mimic(&h, dist, x86OCPContentRootDir)
		}

		err := h.Handle(nil, event)
		validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)

		fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
		for _, key := range iskeys {
			if _, ok := h.skippedImagestreams[key]; ok {
				t.Fatalf("bad skipped processing for %s: %#v", key, h)
			}
			if _, ok := fakeisclient.upsertkeys[key]; !ok {
				t.Fatalf("is did not reach client %s: %#v", key, h)
			}
		}
		is, _ := fakeisclient.Get("", "foo", metav1.GetOptions{})
		if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, sr.Spec.SamplesRegistry) {
			t.Fatalf("stream repo not updated %#v, %#v", is, h)
		}
		is, _ = fakeisclient.Get("", "bar", metav1.GetOptions{})
		if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, sr.Spec.SamplesRegistry) {
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
		_, sr, _ = setup()
		event.Object = sr
		sr.Spec.SamplesRegistry = "bar.io"
		sr.ResourceVersion = "2"
		sr.Spec.InstallType = dist
		err = h.Handle(nil, event)
		validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.ConfigurationValid, v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionTrue}, t)
		is, _ = fakeisclient.Get("", "foo", metav1.GetOptions{})
		if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, sr.Spec.SamplesRegistry) {
			t.Fatalf("stream repo not updated %#v, %#v", is, h)
		}
		is, _ = fakeisclient.Get("", "bar", metav1.GetOptions{})
		if is == nil || !strings.HasPrefix(is.Spec.DockerImageRepository, sr.Spec.SamplesRegistry) {
			t.Fatalf("stream repo not updated %#v, %#v", is, h)
		}

	}

}

func TestCreateDeleteSecretBeforeCR(t *testing.T) {
	h, sr, event := setup()
	event.Object = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            v1alpha1.SamplesRegistryCredentials,
			ResourceVersion: "a",
		},
	}

	err := h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{}, []corev1.ConditionStatus{}, t)

	event.Deleted = true
	err = h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{}, []corev1.ConditionStatus{}, t)

	event.Deleted = false
	event.Object = sr
	err = h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)

}

func TestCreateDeleteSecretAfterCR(t *testing.T) {
	h, sr, event := setup()

	err := h.Handle(nil, event)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue}
	conditions := []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}
	validate(true, err, "", sr, conditions, statuses, t)

	event.Object = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            v1alpha1.SamplesRegistryCredentials,
			ResourceVersion: "a",
		},
	}
	err = h.Handle(nil, event)
	conditions = append(conditions, v1alpha1.ImportCredentialsExist)
	statuses = append(statuses, corev1.ConditionTrue)
	validate(true, err, "", sr, conditions, statuses, t)

	event.Deleted = true
	err = h.Handle(nil, event)
	statuses[1] = corev1.ConditionFalse
	validate(true, err, "", sr, conditions, statuses, t)

}

func setup() (Handler, *v1alpha1.SamplesResource, sdk.Event) {
	sr := &v1alpha1.SamplesResource{}
	sr.Name = v1alpha1.SamplesResourceName
	h := NewTestHandler()
	h.sdkwrapper.(*fakeSDKWrapper).sr = sr
	return h, sr, sdk.Event{Object: sr}
}

func TestIgnoreSameSecret(t *testing.T) {
	h, sr, event := setup()

	err := h.Handle(nil, event)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue}
	conditions := []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}
	validate(true, err, "", sr, conditions, statuses, t)

	event.Object = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            v1alpha1.SamplesRegistryCredentials,
			ResourceVersion: "a",
		},
	}
	err = h.Handle(nil, event)
	statuses = append(statuses, corev1.ConditionTrue)
	conditions = append(conditions, v1alpha1.ImportCredentialsExist)
	validate(true, err, "", sr, conditions, statuses, t)

	err = h.Handle(nil, event)
	// secret should be ignored, no additional conditions added
	validate(true, err, "", sr, conditions, statuses, t)
}

func TestSecretAPIError(t *testing.T) {
	h, sr, event := setup()
	err := h.Handle(nil, event)
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue}
	conditions := []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}
	validate(true, err, "", sr, conditions, statuses, t)

	event.Object = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            v1alpha1.SamplesRegistryCredentials,
			ResourceVersion: "a",
		},
	}
	fakesecretclient := h.secretclientwrapper.(*fakeSecretClientWrapper)
	fakesecretclient.err = fmt.Errorf("problemchangingsecret")
	err = h.Handle(nil, event)
	statuses = append(statuses, corev1.ConditionUnknown)
	conditions = append(conditions, v1alpha1.ImportCredentialsExist)
	validate(false, err, "problemchangingsecret", sr, conditions, statuses, t)
}

func TestImageGetError(t *testing.T) {
	errors := []error{
		fmt.Errorf("getstreamerror"),
		kerrors.NewNotFound(schema.GroupResource{}, "getstreamerror"),
	}
	for _, iserr := range errors {
		h, sr, event := setup()
		event.Object = sr

		mimic(&h, v1alpha1.CentosSamplesDistribution, x86OKDContentRootDir)

		fakeisclient := h.imageclientwrapper.(*fakeImageStreamClientWrapper)
		fakeisclient.geterrors = map[string]error{"foo": iserr}

		err := h.Handle(nil, event)
		if !kerrors.IsNotFound(iserr) {
			validate(false, err, "getstreamerror", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionUnknown}, t)
		} else {
			validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionTrue}, t)
		}
	}

}

func TestTemplateGetEreror(t *testing.T) {
	errors := []error{
		fmt.Errorf("gettemplateerror"),
		kerrors.NewNotFound(schema.GroupResource{}, "gettemplateerror"),
	}
	for _, terr := range errors {
		h, sr, event := setup()
		event.Object = sr

		mimic(&h, v1alpha1.CentosSamplesDistribution, x86OKDContentRootDir)

		faketclient := h.templateclientwrapper.(*fakeTemplateClientWrapper)
		faketclient.geterrors = map[string]error{"bo": terr}

		err := h.Handle(nil, event)
		validate(false, err, "gettemplateerror", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionUnknown}, t)
	}

}

func TestDeletedCR(t *testing.T) {
	h, sr, event := setup()
	event.Deleted = true
	err := h.Handle(nil, event)
	validate(true, err, "", sr, []v1alpha1.SamplesResourceConditionType{}, []corev1.ConditionStatus{}, t)
}

func TestSameCR(t *testing.T) {
	h, sr, event := setup()
	sr.ResourceVersion = "a"
	condtions := []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}
	statuses := []corev1.ConditionStatus{corev1.ConditionTrue}
	err := h.Handle(nil, event)
	validate(true, err, "", sr, condtions, statuses, t)
	err = h.Handle(nil, event)
	// conditions array should not change
	validate(true, err, "", sr, condtions, statuses, t)
}

func TestBadTopDirList(t *testing.T) {
	h, sr, event := setup()
	fakefinder := h.filefinder.(*fakeResourceFileLister)
	fakefinder.errors = map[string]error{x86OKDContentRootDir: fmt.Errorf("badtopdir")}
	err := h.Handle(nil, event)
	validate(false, err, "badtopdir", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionUnknown}, t)
}

func TestBadSubDirList(t *testing.T) {
	h, sr, event := setup()
	mimic(&h, v1alpha1.CentosSamplesDistribution, x86OKDContentRootDir)
	fakefinder := h.filefinder.(*fakeResourceFileLister)
	fakefinder.errors = map[string]error{x86OKDContentRootDir + "/imagestreams": fmt.Errorf("badsubdir")}
	err := h.Handle(nil, event)
	validate(false, err, "badsubdir", sr, []v1alpha1.SamplesResourceConditionType{v1alpha1.SamplesExist}, []corev1.ConditionStatus{corev1.ConditionUnknown}, t)
}

func TestBadTopLevelStatus(t *testing.T) {
	h, sr, event := setup()
	fakestatus := h.sdkwrapper.(*fakeSDKWrapper)
	fakestatus.updateerr = fmt.Errorf("badsdkupdate")
	err := h.Handle(nil, event)
	// note, the local copy will still have a status complete even if the sdk update fails
	conditions := []v1alpha1.SamplesResourceConditionType{
		v1alpha1.SamplesExist,
	}
	statuses := []corev1.ConditionStatus{
		corev1.ConditionUnknown,
	}
	validate(false, err, "badsdkupdate", sr, conditions, statuses, t)
	for _, c := range sr.Status.Conditions {
		if c.Type == v1alpha1.SamplesExist &&
			c.Status != corev1.ConditionUnknown {
			t.Fatalf("sample exists condition should have been unknown: %#v", sr)
		}
	}
}

func getISKeys() []string {
	return []string{"foo", "bar"}
}

func getTKeys() []string {
	return []string{"bo", "go"}
}

func mimic(h *Handler, dist v1alpha1.SamplesDistributionType, topdir string) {
	registry1 := "docker.io"
	registry2 := "docker.io"
	if dist == v1alpha1.RHELSamplesDistribution {
		registry1 = "registry.access.redhat.com"
		registry2 = "registry.redhat.io"
	}
	fakefile := h.filefinder.(*fakeResourceFileLister)
	fakefile.files = map[string][]fakeFileInfo{
		topdir: []fakeFileInfo{
			fakeFileInfo{
				name: "imagestreams",
				dir:  true,
			},
			fakeFileInfo{
				name: "templates",
				dir:  true,
			},
		},
		topdir + "/" + "imagestreams": []fakeFileInfo{
			fakeFileInfo{
				name: "foo",
				dir:  false,
			},
			fakeFileInfo{
				name: "bar",
				dir:  false,
			},
		},
		topdir + "/" + "templates": []fakeFileInfo{
			fakeFileInfo{
				name: "bo",
				dir:  false,
			},
			fakeFileInfo{
				name: "go",
				dir:  false,
			},
		},
	}
	fakeisgetter := h.fileimagegetter.(*fakeImageStreamFromFileGetter)
	fakeisgetter.streams = map[string]*imagev1.ImageStream{
		topdir + "/imagestreams/foo": &imagev1.ImageStream{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
			},
			Spec: imagev1.ImageStreamSpec{
				DockerImageRepository: registry1,
				Tags: []imagev1.TagReference{
					imagev1.TagReference{
						// no Name field set on purpose, cover more code paths
						From: &corev1.ObjectReference{
							Kind: "DockerImage",
						},
					},
				},
			},
		},
		topdir + "/imagestreams/bar": &imagev1.ImageStream{
			ObjectMeta: metav1.ObjectMeta{
				Name: "bar",
			},
			Spec: imagev1.ImageStreamSpec{
				DockerImageRepository: registry2,
				Tags: []imagev1.TagReference{
					imagev1.TagReference{
						From: &corev1.ObjectReference{
							Name: registry2,
							Kind: "DockerImage",
						},
					},
				},
			},
		},
	}
	faketempgetter := h.filetemplategetter.(*fakeTemplateFromFileGetter)
	faketempgetter.templates = map[string]*templatev1.Template{
		topdir + "/templates/bo": &templatev1.Template{
			ObjectMeta: metav1.ObjectMeta{
				Name: "bo",
			},
		},
		topdir + "/templates/go": &templatev1.Template{
			ObjectMeta: metav1.ObjectMeta{
				Name: "go",
			},
		},
	}

}

func validate(succeed bool, err error, errstr string, sr *v1alpha1.SamplesResource, conditions []v1alpha1.SamplesResourceConditionType, statuses []corev1.ConditionStatus, t *testing.T) {
	if succeed && err != nil {
		t.Fatal(err)
	}
	if len(conditions) != len(statuses) {
		t.Fatalf("bad input conditions %#v statuses %#v", conditions, statuses)
	}
	if !succeed {
		if err == nil {
			t.Fatalf("error should have been reported")
		}
		if !strings.Contains(err.Error(), errstr) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if sr != nil {
		if len(sr.Status.Conditions) != len(conditions) {
			t.Fatalf("condition arrays different lengths got %v\n expected %v", sr, conditions)
		}
		for i, c := range conditions {
			if sr.Status.Conditions[i].Type != c {
				t.Fatalf("conditions in wrong order or different types have %#v\n expected %#v", sr.Status.Conditions, conditions)
			}
			if sr.Status.Conditions[i].Status != statuses[i] {
				t.Fatalf("unexpected for succeed %v have status condition %#v expected condition %#v and status %#v", succeed, sr.Status.Conditions[i], c, statuses[i])
			}
		}
	}
}

func NewTestHandler() Handler {
	h := Handler{}

	h.initter = &fakeInClusterInitter{}

	h.sdkwrapper = &fakeSDKWrapper{}

	h.namespace = "foo"

	h.mutex = &sync.Mutex{}

	h.skippedImagestreams = make(map[string]bool)
	h.skippedTemplates = make(map[string]bool)

	h.fileimagegetter = &fakeImageStreamFromFileGetter{streams: map[string]*imagev1.ImageStream{}}
	h.filetemplategetter = &fakeTemplateFromFileGetter{}
	h.filefinder = &fakeResourceFileLister{files: map[string][]fakeFileInfo{}}

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
	h.secretclientwrapper = &fakeSecretClientWrapper{}
	h.configmapclientwrapper = &fakeConfigMapClientWrapper{maps: map[string]*corev1.ConfigMap{}}

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
	streams      map[string]*imagev1.ImageStream
	upsertkeys   map[string]bool
	geterrors    map[string]error
	upserterrors map[string]error
	listerrors   map[string]error
}

func (f *fakeImageStreamClientWrapper) Get(namespace, name string, opts metav1.GetOptions) (*imagev1.ImageStream, error) {
	err, _ := f.geterrors[name]
	if err != nil {
		return nil, err
	}
	is, _ := f.streams[name]
	return is, nil
}

func (f *fakeImageStreamClientWrapper) List(namespace string, opts metav1.ListOptions) (*imagev1.ImageStreamList, error) {
	err, _ := f.geterrors[namespace]
	if err != nil {
		return nil, err
	}
	list := &imagev1.ImageStreamList{}
	for _, is := range f.streams {
		list.Items = append(list.Items, *is)
	}
	return list, nil
}

func (f *fakeImageStreamClientWrapper) Create(namespace string, stream *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	if stream == nil {
		return nil, nil
	}
	f.upsertkeys[stream.Name] = true
	err, _ := f.upserterrors[stream.Name]
	if err != nil {
		return nil, err
	}
	f.streams[stream.Name] = stream
	return stream, nil
}

func (f *fakeImageStreamClientWrapper) Update(namespace string, stream *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return f.Create(namespace, stream)
}

type fakeTemplateClientWrapper struct {
	templates    map[string]*templatev1.Template
	upsertkeys   map[string]bool
	geterrors    map[string]error
	upserterrors map[string]error
	listerrors   map[string]error
}

func (f *fakeTemplateClientWrapper) Get(namespace, name string, opts metav1.GetOptions) (*templatev1.Template, error) {
	err, _ := f.geterrors[name]
	if err != nil {
		return nil, err
	}
	is, _ := f.templates[name]
	return is, nil
}

func (f *fakeTemplateClientWrapper) List(namespace string, opts metav1.ListOptions) (*templatev1.TemplateList, error) {
	err, _ := f.geterrors[namespace]
	if err != nil {
		return nil, err
	}
	list := &templatev1.TemplateList{}
	for _, is := range f.templates {
		list.Items = append(list.Items, *is)
	}
	return list, nil
}

func (f *fakeTemplateClientWrapper) Create(namespace string, t *templatev1.Template) (*templatev1.Template, error) {
	if t == nil {
		return nil, nil
	}
	f.upsertkeys[t.Name] = true
	err, _ := f.geterrors[t.Name]
	if err != nil {
		return nil, err
	}
	f.templates[t.Name] = t
	return t, nil
}

func (f *fakeTemplateClientWrapper) Update(namespace string, t *templatev1.Template) (*templatev1.Template, error) {
	return f.Create(namespace, t)
}

type fakeConfigMapClientWrapper struct {
	maps map[string]*corev1.ConfigMap
	err  error
}

func (f *fakeConfigMapClientWrapper) Create(namespace string, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.maps[cm.Name] = cm
	return cm, nil
}

func (f *fakeConfigMapClientWrapper) Get(namespace, name string) (*corev1.ConfigMap, error) {
	if f.err != nil {
		return nil, f.err
	}
	cm, _ := f.maps[name]
	return cm, nil
}

type fakeSecretClientWrapper struct {
	err error
}

func (f *fakeSecretClientWrapper) Create(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	if f.err != nil {
		return nil, f.err
	}
	return s, nil
}

func (f *fakeSecretClientWrapper) Update(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	if f.err != nil {
		return nil, f.err
	}
	return s, nil
}

func (f *fakeSecretClientWrapper) Delete(namespace, name string, opts *metav1.DeleteOptions) error {
	return f.err
}

func (f *fakeSecretClientWrapper) Get(namespace, name string) (*corev1.Secret, error) {
	return nil, f.err
}

type fakeInClusterInitter struct{}

func (f *fakeInClusterInitter) init() {}

type fakeSDKWrapper struct {
	updateerr error
	createerr error
	geterr    error
	sr        *v1alpha1.SamplesResource
}

func (f *fakeSDKWrapper) Update(opcfg *v1alpha1.SamplesResource) error { return f.updateerr }

func (f *fakeSDKWrapper) Create(opcfg *v1alpha1.SamplesResource) error { return f.createerr }

func (f *fakeSDKWrapper) Get(name, namespace string) (*v1alpha1.SamplesResource, error) {
	return f.sr, f.geterr
}
