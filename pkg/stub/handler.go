package stub

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"

	restclient "k8s.io/client-go/rest"

	imagev1 "github.com/openshift/api/image/v1"
	templatev1 "github.com/openshift/api/template/v1"

	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	templatev1client "github.com/openshift/client-go/template/clientset/versioned/typed/template/v1"

	operatorsv1api "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	operatorstatus "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"

	sampleclientv1 "github.com/openshift/cluster-samples-operator/pkg/generated/clientset/versioned/typed/samples/v1"
)

const (
	x86OCPContentRootDir   = "/opt/openshift/operator/ocp-x86_64"
	x86OKDContentRootDir   = "/opt/openshift/operator/okd-x86_64"
	ppc64OCPContentRootDir = "/opt/openshift/operator/ocp-ppc64le"
	installtypekey         = "keyForInstallTypeField"
	regkey                 = "keyForSamplesRegistryField"
	skippedstreamskey      = "keyForSkippedImageStreamsField"
	skippedtempskey        = "keyForSkippedTemplatesField"
)

func NewSamplesOperatorHandler(kubeconfig *restclient.Config) (*Handler, error) {
	h := &Handler{}

	h.initter = &defaultInClusterInitter{}
	h.initter.init(h, kubeconfig)

	crdWrapper := &generatedCRDWrapper{}
	client, err := sampleclientv1.NewForConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	crdWrapper.client = client.Configs()

	h.crdwrapper = crdWrapper

	h.Fileimagegetter = &DefaultImageStreamFromFileGetter{}
	h.Filetemplategetter = &DefaultTemplateFromFileGetter{}
	h.Filefinder = &DefaultResourceFileLister{}

	h.imageclientwrapper = &defaultImageStreamClientWrapper{h: h}
	h.templateclientwrapper = &defaultTemplateClientWrapper{h: h}
	h.secretclientwrapper = &defaultSecretClientWrapper{coreclient: h.coreclient}
	h.cvowrapper = operatorstatus.NewClusterOperatorHandler(h.configclient)

	h.namespace = GetNamespace()

	h.skippedImagestreams = make(map[string]bool)
	h.skippedTemplates = make(map[string]bool)

	h.CreateDefaultResourceIfNeeded(nil)

	h.imagestreamFile = make(map[string]string)
	h.templateFile = make(map[string]string)

	return h, nil
}

type Handler struct {
	initter InClusterInitter

	crdwrapper CRDWrapper
	cvowrapper *operatorstatus.ClusterOperatorHandler

	restconfig   *restclient.Config
	tempclient   *templatev1client.TemplateV1Client
	imageclient  *imagev1client.ImageV1Client
	coreclient   *corev1client.CoreV1Client
	configclient *configv1client.ConfigV1Client

	imageclientwrapper    ImageStreamClientWrapper
	templateclientwrapper TemplateClientWrapper
	secretclientwrapper   SecretClientWrapper

	Fileimagegetter    ImageStreamFromFileGetter
	Filetemplategetter TemplateFromFileGetter
	Filefinder         ResourceFileLister

	namespace string

	skippedTemplates    map[string]bool
	skippedImagestreams map[string]bool

	imagestreamFile map[string]string
	templateFile    map[string]string

	creationInProgress bool
	secretRetryCount   int8
}

// prepWatchEvent decides whether an upsert of the sample should be done, as well as data for either doing the upsert or checking the status of a prior upsert;
// the return values:
// - cfg: return this if we want to check the status of a prior upsert
// - filePath: used to the caller to look up the image content for the upsert
// - doUpsert: whether to do the upsert of not ... not doing the upsert optionally triggers the need for checking status of prior upsert
// - err: if a problem occurred getting the Config, we return the error to bubble up and initiate a retry
func (h *Handler) prepWatchEvent(kind, name string, annotations map[string]string, deleted bool) (*v1.Config, string, bool, error) {
	cfg, err := h.crdwrapper.Get(v1.ConfigName)
	if cfg == nil || err != nil {
		logrus.Printf("Received watch event %s but not upserting since not have the Config yet: %#v %#v", kind+"/"+name, err, cfg)
		return nil, "", false, err
	}

	if cfg.ConditionFalse(v1.ImageChangesInProgress) {
		// we do no return the cfg in these cases because we do not want to bother with any progress tracking

		if cfg.DeletionTimestamp != nil {
			logrus.Printf("Received watch event %s but not upserting since deletion of the Config is in progress and image changes are not in progress", kind+"/"+name)
			return nil, "", false, nil
		}
		switch cfg.Spec.ManagementState {
		case operatorsv1api.Removed:
			logrus.Debugf("Not upserting %s/%s event because operator is in removed state and image changes are not in progress", kind, name)
			return nil, "", false, nil
		case operatorsv1api.Unmanaged:
			logrus.Debugf("Not upserting %s/%s event because operator is in unmanaged state and image changes are not in progress", kind, name)
			return nil, "", false, nil
		}
	}

	filePath := ""
	// if by some slim chance sample watch events come in before the first
	// Config event get the file locations and skipped lists
	if len(h.imagestreamFile) == 0 || len(h.templateFile) == 0 {
		for _, arch := range cfg.Spec.Architectures {
			dir := h.GetBaseDir(arch, cfg)
			files, _ := h.Filefinder.List(dir)
			h.processFiles(dir, files, cfg)
		}
	}
	h.buildSkipFilters(cfg)

	inInventory := false
	skipped := false
	switch kind {
	case "imagestream":
		filePath, inInventory = h.imagestreamFile[name]
		_, skipped = h.skippedImagestreams[name]
	case "template":
		filePath, inInventory = h.templateFile[name]
		_, skipped = h.skippedTemplates[name]
	}
	if !inInventory {
		logrus.Printf("watch event %s not part of operators inventory", name)
		return nil, "", false, nil
	}
	if skipped {
		logrus.Printf("watch event %s in skipped list for %s", name, kind)
		return nil, "", false, nil
	}

	if deleted && !h.creationInProgress {
		logrus.Printf("going to recreate deleted managed sample %s/%s", kind, name)
		return cfg, filePath, true, nil
	}

	if annotations != nil {
		gitVersion := v1.GitVersionString()
		isv, ok := annotations[v1.SamplesVersionAnnotation]
		logrus.Debugf("Comparing %s/%s version %s ok %v with git version %s", kind, name, isv, ok, gitVersion)
		if ok && isv == gitVersion {
			logrus.Debugf("Not upserting %s/%s cause operator version matches", kind, name)
			// but return cfg to potentially toggle pending condition
			return cfg, "", false, nil
		}
	}
	return cfg, filePath, true, nil
}

func (h *Handler) processImageStreamWatchEvent(is *imagev1.ImageStream, deleted bool) error {
	// our pattern is the top most caller locks the mutex

	cfg, filePath, doUpsert, err := h.prepWatchEvent("imagestream", is.Name, is.Annotations, deleted)
	logrus.Debugf("prep watch event imgstr %s ok %v", is.Name, doUpsert)
	if !doUpsert {
		if err != nil {
			return err
		}
		if cfg == nil {
			return nil
		}
		processing := cfg.Condition(v1.ImageChangesInProgress)
		// if somebody edited our sample but left the version annotation correct, instead of this
		// operator initiating a change, we could get in a lengthy retry loop if we always return errors here.
		// Conversely, if the first event after an update provides an imagestream whose spec and status generations match,
		// we do not want to ignore it and wait for the relist to clear out the entry in the in progress condition reason field
		// So we are employing a flag in this operator that is set while the upserting is in progress.
		// If the operator is restarted, since ImageChangesInProgress has not yet been set to True during
		// our upsert cycle, it will go through the upsert cycle on its restart anyway, so new imagestream
		// events are coming again, and losing this state has no consequence
		if h.creationInProgress && processing.Status != corev1.ConditionTrue {
			return fmt.Errorf("retry imagestream %s because operator samples creation in progress", is.Name)
		}
		importError := cfg.Condition(v1.ImportImageErrorsExist)

		// so reaching this point means we have a prior upsert in progress, and we just want to track the status

		logrus.Debugf("checking tag spec/status for %s spec len %d status len %d", is.Name, len(is.Spec.Tags), len(is.Status.Tags))
		// won't be updating imagestream this go around, which sets pending==true, so see if we should turn off pending
		pending := false
		anyErrors := false
		if len(is.Spec.Tags) == len(is.Status.Tags) {
			for _, specTag := range is.Spec.Tags {
				matched := false
				for _, statusTag := range is.Status.Tags {
					logrus.Debugf("checking spec tag %s against status tag %s with num items %d", specTag.Name, statusTag.Tag, len(statusTag.Items))
					if specTag.Name == statusTag.Tag {
						// if an error occurred with the latest generation, let's give up as we are no longer "in progress"
						// in that case as well, but mark the import failure
						if statusTag.Conditions != nil && len(statusTag.Conditions) > 0 {
							var latestGeneration int64
							var mostRecentErrorGeneration int64
							reason := ""
							message := ""
							for _, condition := range statusTag.Conditions {
								if condition.Generation > latestGeneration {
									latestGeneration = condition.Generation
								}
								if condition.Status == corev1.ConditionFalse &&
									len(condition.Message) > 0 &&
									len(condition.Reason) > 0 {
									if condition.Generation > mostRecentErrorGeneration {
										mostRecentErrorGeneration = condition.Generation
										reason = condition.Reason
										message = condition.Message
									}
								}
							}
							if mostRecentErrorGeneration >= latestGeneration {
								logrus.Warningf("Image import for imagestream %s tag %s generation %v failed with reason %s and detailed message %s", is.Name, statusTag.Tag, mostRecentErrorGeneration, reason, message)
								matched = true
								anyErrors = true
								// add this imagestream to the Reason field
								if !strings.Contains(importError.Reason, is.Name+" ") {
									importError.Reason = importError.Reason + is.Name + " "
								}
								break
							}
						}
						// if the latest gens have no errors, see if we got gen match
						if statusTag.Items != nil {
							for _, event := range statusTag.Items {
								if specTag.Generation != nil {
									logrus.Debugf("checking status tag %d against spec tag %d", event.Generation, *specTag.Generation)
								}
								if specTag.Generation != nil &&
									*specTag.Generation <= event.Generation {
									logrus.Debugf("got match")
									matched = true
									break
								}
							}
						}
					}
				}
				if !matched {
					pending = true
					break
				}
			}
		} else {
			pending = true
		}

		logrus.Debugf("pending is %v any errors %v for %s", pending, anyErrors, is.Name)

		// we check for processing == true here as well to avoid churn on relists
		if !pending && processing.Status == corev1.ConditionTrue {
			if !anyErrors {
				// remove this imagestream from the error reason field in case it exists there;
				// this call is a no-op if it is not there
				importError.Reason = strings.Replace(importError.Reason, is.Name+" ", "", -1)
			}
			now := kapis.Now()
			// remove this imagestream name, including the space separator
			logrus.Debugf("current reason %s ", processing.Reason)
			processing.Reason = strings.Replace(processing.Reason, is.Name+" ", "", -1)
			logrus.Debugf("processing reason now %s", processing.Reason)
			if len(strings.TrimSpace(processing.Reason)) == 0 {
				logrus.Debugf("reason empty setting to false")
				processing.Status = corev1.ConditionFalse
				processing.Reason = ""
				// also turn off as needed migration flag if no in progress left
				migration := cfg.Condition(v1.MigrationInProgress)
				if migration.Status == corev1.ConditionTrue {
					migration.LastTransitionTime = now
					migration.LastUpdateTime = now
					migration.Status = corev1.ConditionFalse
					cfg.ConditionUpdate(migration)
				}
			}
			processing.LastTransitionTime = now
			processing.LastUpdateTime = now
			cfg.ConditionUpdate(processing)
			// update the import error as needed
			logrus.Debugf("import error reason now %s", importError.Reason)
			if len(strings.TrimSpace(importError.Reason)) == 0 {
				importError.Status = corev1.ConditionFalse
				importError.Reason = ""
			} else {
				importError.Status = corev1.ConditionTrue
			}
			importError.LastTransitionTime = now
			importError.LastUpdateTime = now
			cfg.ConditionUpdate(importError)
			logrus.Debugf("SDKUPDATE no migration / no pending / no error imgstr update")
			return h.crdwrapper.Update(cfg)
		}

		// clear out error for this stream if there were errors previously but no longer are
		// think a scheduled import failing then recovering
		if strings.Contains(importError.Reason, is.Name) && !anyErrors {
			now := kapis.Now()
			importError.Reason = strings.Replace(importError.Reason, is.Name+" ", "", -1)
			if len(strings.TrimSpace(importError.Reason)) == 0 {
				importError.Status = corev1.ConditionFalse
				importError.Reason = ""
			} else {
				importError.Status = corev1.ConditionTrue
			}
			importError.LastTransitionTime = now
			importError.LastUpdateTime = now
			cfg.ConditionUpdate(importError)
			logrus.Debugf("SDKUPDATE no error imgstr update")
		}

		return nil
	}

	// prepWatchEvent has determined we actually need to do the upsert
	if cfg == nil {
		return fmt.Errorf("cannot upsert imagestream %s because could not obtain Config", is.Name)
	}

	imagestream, err := h.Fileimagegetter.Get(filePath)
	if err != nil {
		// still attempt to report error in status
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", filePath)
		logrus.Debugf("SDKUPDATE event img update err bad fs read")
		h.crdwrapper.Update(cfg)
		// if we get this, don't bother retrying
		return nil
	}
	if deleted {
		// set is to nil so upsert will create
		is = nil
	}
	err = h.upsertImageStream(imagestream, is, cfg)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			// it the main loop created, we will let it set the image change reason field
			return nil
		}
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error replacing imagestream %s", imagestream.Name)
		logrus.Debugf("SDKUPDATE event img update err bad api obj update")
		h.crdwrapper.Update(cfg)
		return err
	}
	// refetch cfg to narrow conflict window
	s, err := h.crdwrapper.Get(v1.ConfigName)
	if err == nil {
		cfg = s
	}
	// now update progressing condition
	progressing := cfg.Condition(v1.ImageChangesInProgress)
	now := kapis.Now()
	progressing.LastUpdateTime = now
	progressing.LastTransitionTime = now
	logrus.Debugf("Handle changing processing from false to true for imagestream %s", imagestream.Name)
	progressing.Status = corev1.ConditionTrue
	if !strings.Contains(progressing.Reason, imagestream.Name+" ") {
		progressing.Reason = progressing.Reason + imagestream.Name + " "
	}
	cfg.ConditionUpdate(progressing)
	if cfg.Spec.Version != v1.GitVersionString() {
		cfg.Spec.Version = v1.GitVersionString()
		logrus.Debugf("SDKUPDATE progressing true update for imagestream %s", imagestream.Name)
		return h.crdwrapper.Update(cfg)
	}
	return nil

}

func (h *Handler) processTemplateWatchEvent(t *templatev1.Template, deleted bool) error {
	// our pattern is the top most caller locks the mutex

	cfg, filePath, doUpsert, err := h.prepWatchEvent("template", t.Name, t.Annotations, deleted)
	if err != nil {
		return err
	}
	if !doUpsert {
		return nil
	}

	template, err := h.Filetemplategetter.Get(filePath)
	if err != nil {
		// still attempt to report error in status
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", filePath)
		logrus.Debugf("SDKUPDATE event temp udpate err")
		h.crdwrapper.Update(cfg)
		// if we get this, don't bother retrying
		return nil
	}
	if deleted {
		// set t to nil so upsert will create
		t = nil
	}
	err = h.upsertTemplate(template, t, cfg)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			return nil
		}
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error replacing template %s", template.Name)
		logrus.Debugf("SDKUPDATE event temp update err bad api obj update")
		h.crdwrapper.Update(cfg)
		return err
	}
	if cfg.Spec.Version != v1.GitVersionString() {
		logrus.Debugf("SDKUPDATE update spec version on template event")
		cfg.Spec.Version = v1.GitVersionString()
		return h.crdwrapper.Update(cfg)
	}
	return nil

}

func (h *Handler) VariableConfigChanged(cfg *v1.Config) (bool, error) {
	logrus.Debugf("cfg skipped streams %#v", cfg.Spec.SkippedImagestreams)
	if cfg.Spec.SamplesRegistry != cfg.Status.SamplesRegistry {
		logrus.Printf("SamplesRegistry changed from %s to %s", cfg.Status.SamplesRegistry, cfg.Spec.SamplesRegistry)
		return true, nil
	}

	if len(cfg.Spec.SkippedImagestreams) != len(cfg.Status.SkippedImagestreams) {
		logrus.Printf("SkippedImagestreams changed from %#v to %#v", cfg.Status.SkippedImagestreams, cfg.Spec.SkippedImagestreams)
		return true, nil
	}

	for i, skip := range cfg.Status.SkippedImagestreams {
		if skip != cfg.Spec.SkippedImagestreams[i] {
			logrus.Printf("SkippedImagestreams changed from %#v to %#v", cfg.Status.SkippedImagestreams, cfg.Spec.SkippedImagestreams)
			return true, nil
		}
	}

	if len(cfg.Spec.SkippedTemplates) != len(cfg.Status.SkippedTemplates) {
		logrus.Printf("SkippedTemplates changed from %#v to %#v", cfg.Status.SkippedTemplates, cfg.Spec.SkippedTemplates)
		return true, nil
	}

	for i, skip := range cfg.Status.SkippedTemplates {
		if skip != cfg.Spec.SkippedTemplates[i] {
			logrus.Printf("SkippedTemplates changed from %#v to %#v", cfg.Status.SkippedTemplates, cfg.Spec.SkippedTemplates)
			return true, nil
		}
	}

	logrus.Debugf("Incoming Config unchanged from last processed version")
	return false, nil
}

func (h *Handler) StoreCurrentValidConfig(cfg *v1.Config) { //error {
	cfg.Status.SamplesRegistry = cfg.Spec.SamplesRegistry
	cfg.Status.InstallType = cfg.Spec.InstallType
	cfg.Status.Architectures = cfg.Spec.Architectures
	cfg.Status.SkippedImagestreams = cfg.Spec.SkippedImagestreams
	cfg.Status.SkippedTemplates = cfg.Spec.SkippedTemplates
}

func (h *Handler) SpecValidation(cfg *v1.Config) error { //(*corev1.ConfigMap, error) {
	// the first thing this should do is check that all the config values
	// are "valid" (the architecture name is known, the distribution name is known, etc)
	// if that fails, we should immediately error out and set ConfigValid to false.
	for _, arch := range cfg.Spec.Architectures {
		switch arch {
		case v1.X86Architecture:
		case v1.PPCArchitecture:
			if cfg.Spec.InstallType == v1.CentosSamplesDistribution {
				err := fmt.Errorf("do not support centos distribution on ppc64le")
				return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
			}
		default:
			err := fmt.Errorf("architecture %s unsupported; only support %s and %s", arch, v1.X86Architecture, v1.PPCArchitecture)
			return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
		}
	}

	switch cfg.Spec.InstallType {
	case v1.RHELSamplesDistribution:
	case v1.CentosSamplesDistribution:
	default:
		err := fmt.Errorf("invalid install type %s specified, should be rhel or centos", string(cfg.Spec.InstallType))
		return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
	}

	// only if the values being requested are valid, should we then proceed to check
	// them against the previous values(if we've stored any previous values)

	// if we have not had a valid Config processed, allow caller to try with
	// the cfg contents
	if !cfg.ConditionTrue(v1.SamplesExist) && !cfg.ConditionTrue(v1.ImageChangesInProgress) {
		logrus.Println("Spec is valid because this operator has not processed a config yet")
		return nil
	}
	if len(cfg.Status.InstallType) > 0 && cfg.Spec.InstallType != cfg.Status.InstallType {
		err := fmt.Errorf("cannot change installtype from %s to %s", cfg.Status.InstallType, cfg.Spec.InstallType)
		return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
	}

	if len(cfg.Status.Architectures) > 0 {
		if len(cfg.Status.Architectures) != len(cfg.Spec.Architectures) {
			err := fmt.Errorf("cannot change architectures from %#v to %#v", cfg.Status.Architectures, cfg.Spec.Architectures)
			return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
		}
		for i, arch := range cfg.Status.Architectures {
			// make 'em keep the order consistent ;-/
			if arch != cfg.Spec.Architectures[i] {
				err := fmt.Errorf("cannot change architectures from %#v to %#v", cfg.Status.Architectures, cfg.Spec.Architectures)
				return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
			}
		}
	}
	h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.ConfigurationValid)
	return nil
}

func (h *Handler) AddFinalizer(cfg *v1.Config) {
	hasFinalizer := false
	for _, f := range cfg.Finalizers {
		if f == v1.ConfigFinalizer {
			hasFinalizer = true
			break
		}
	}
	if !hasFinalizer {
		cfg.Finalizers = append(cfg.Finalizers, v1.ConfigFinalizer)
	}
}

func (h *Handler) RemoveFinalizer(cfg *v1.Config) {
	newFinalizers := []string{}
	for _, f := range cfg.Finalizers {
		if f == v1.ConfigFinalizer {
			continue
		}
		newFinalizers = append(newFinalizers, f)
	}
	cfg.Finalizers = newFinalizers
}

func (h *Handler) NeedsFinalizing(cfg *v1.Config) bool {
	if cfg.ConditionFalse(v1.SamplesExist) {
		return false
	}

	for _, f := range cfg.Finalizers {
		if f == v1.ConfigFinalizer {
			return true
		}
	}

	return false
}

func (h *Handler) GoodConditionUpdate(cfg *v1.Config, newStatus corev1.ConditionStatus, conditionType v1.ConfigConditionType) {
	logrus.Debugf("updating condition %s to %s", conditionType, newStatus)
	condition := cfg.Condition(conditionType)
	// decision was made to not spam master if
	// duplicate events come it (i.e. status does not
	// change)
	if condition.Status != newStatus {
		now := kapis.Now()
		condition.LastUpdateTime = now
		condition.Status = newStatus
		condition.LastTransitionTime = now
		condition.Message = ""
		condition.Reason = ""
		cfg.ConditionUpdate(condition)

		logrus.Println("")
		logrus.Println("")
		logrus.Println("")
		logrus.Println("")
	}
}

// copied from k8s.io/kubernetes/test/utils/
func IsRetryableAPIError(err error) bool {
	if err == nil {
		return false
	}
	// These errors may indicate a transient error that we can retry.
	if kerrors.IsInternalError(err) || kerrors.IsTimeout(err) || kerrors.IsServerTimeout(err) ||
		kerrors.IsTooManyRequests(err) || utilnet.IsProbableEOF(err) || utilnet.IsConnectionReset(err) {
		return true
	}
	// If the error sends the Retry-After header, we respect it as an explicit confirmation we should retry.
	if _, shouldRetry := kerrors.SuggestsClientDelay(err); shouldRetry {
		return true
	}
	return false
}

func (h *Handler) CreateDefaultResourceIfNeeded(cfg *v1.Config) (*v1.Config, error) {
	// assume the caller has call lock on the mutex .. out pattern is to have that as
	// high up the stack as possible ... loc because need to
	// coordinate with event handler processing
	// when it completely updates all imagestreams/templates/statuses

	deleteInProgress := cfg != nil && cfg.DeletionTimestamp != nil

	var err error
	if deleteInProgress {
		cfg = &v1.Config{}
		cfg.Name = v1.ConfigName
		cfg.Kind = "Config"
		cfg.APIVersion = v1.GroupName + "/" + v1.Version
		err = wait.PollImmediate(3*time.Second, 30*time.Second, func() (bool, error) {
			s, e := h.crdwrapper.Get(v1.ConfigName)
			if kerrors.IsNotFound(e) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			// based on 4.0 testing, we've been seeing empty resources returned
			// in the not found case, but just in case ...
			if s == nil {
				return true, nil
			}
			// means still found ... will return wait.ErrWaitTimeout if this continues
			return false, nil
		})
		if err != nil {
			return nil, h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "issues waiting for delete to complete: %v")
		}
		cfg = nil
		logrus.Println("delete of Config recognized")
	}

	if cfg == nil || kerrors.IsNotFound(err) {
		// "4a" in the "startup" workflow, just create default
		// resource and set up that way
		cfg = &v1.Config{}
		cfg.Spec.SkippedTemplates = []string{}
		cfg.Spec.SkippedImagestreams = []string{}
		cfg.Status.SkippedImagestreams = []string{}
		cfg.Status.SkippedTemplates = []string{}
		cfg.Name = v1.ConfigName
		cfg.Kind = "Config"
		cfg.APIVersion = v1.GroupName + "/" + v1.Version
		cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.X86Architecture)
		cfg.Spec.InstallType = v1.CentosSamplesDistribution
		cfg.Spec.ManagementState = operatorsv1api.Managed
		now := kapis.Now()
		exist := cfg.Condition(v1.SamplesExist)
		exist.Status = corev1.ConditionFalse
		exist.LastUpdateTime = now
		exist.LastTransitionTime = now
		cfg.ConditionUpdate(exist)
		cred := cfg.Condition(v1.ImportCredentialsExist)
		cred.Status = corev1.ConditionFalse
		cred.LastUpdateTime = now
		cred.LastTransitionTime = now
		cfg.ConditionUpdate(cred)
		valid := cfg.Condition(v1.ConfigurationValid)
		valid.Status = corev1.ConditionTrue
		valid.LastUpdateTime = now
		valid.LastTransitionTime = now
		cfg.ConditionUpdate(valid)
		inProgress := cfg.Condition(v1.ImageChangesInProgress)
		inProgress.Status = corev1.ConditionFalse
		inProgress.LastUpdateTime = now
		inProgress.LastTransitionTime = now
		cfg.ConditionUpdate(inProgress)
		onHold := cfg.Condition(v1.RemovedManagementStateOnHold)
		onHold.Status = corev1.ConditionFalse
		onHold.LastUpdateTime = now
		onHold.LastTransitionTime = now
		cfg.ConditionUpdate(onHold)
		migration := cfg.Condition(v1.MigrationInProgress)
		migration.Status = corev1.ConditionFalse
		migration.LastUpdateTime = now
		migration.LastTransitionTime = now
		cfg.ConditionUpdate(migration)
		importErrors := cfg.Condition(v1.ImportImageErrorsExist)
		importErrors.Status = corev1.ConditionFalse
		importErrors.LastUpdateTime = now
		importErrors.LastTransitionTime = now
		cfg.ConditionUpdate(importErrors)
		h.AddFinalizer(cfg)
		logrus.Println("creating default Config")
		err = h.crdwrapper.Create(cfg)
		if err != nil {
			if !kerrors.IsAlreadyExists(err) {
				return nil, err
			}
			// in case there is some race condition
			logrus.Println("got already exists error on create default")
		}

	} else {
		logrus.Printf("Config %#v found during operator startup", cfg)
	}

	return cfg, nil
}

// WaitingForCredential determines whether we should proceed with processing the sample resource event,
// where we should *NOT* proceed if we are RHEL and using the default redhat registry;  The return from
// this method is in 2 flavors:  1) if the first boolean is true, tell the caller to just return nil to the sdk;
// 2) the second boolean being true means we've updated the Config with cred exists == false and the caller should call
// the sdk to update the object
func (h *Handler) WaitingForCredential(cfg *v1.Config) (bool, bool) {
	if cfg.ConditionTrue(v1.ImportCredentialsExist) {
		return false, false
	}

	// if trying to do rhel to the default registry.redhat.io registry requires the secret
	// be in place since registry.redhat.io requires auth to pull; since it is not ready
	// log error state
	if cfg.Spec.InstallType == v1.RHELSamplesDistribution &&
		(cfg.Spec.SamplesRegistry == "" || cfg.Spec.SamplesRegistry == "registry.redhat.io") {
		cred := cfg.Condition(v1.ImportCredentialsExist)
		// - if import cred is false, and the message is empty, that means we have NOT registered the error, and need to do so
		// - if cred is false, and the message is there, we can just return nil to the sdk, which "true" for the boolean return value indicates;
		// not returning the same error multiple times to the sdk avoids additional churn; once the secret comes in, it will update the Config
		// with cred == true, and then we'll get another Config event that will trigger config processing
		if len(cred.Message) > 0 {
			return true, false
		}
		err := fmt.Errorf("Cannot create rhel imagestreams to registry.redhat.io without the credentials being available")
		h.processError(cfg, v1.ImportCredentialsExist, corev1.ConditionFalse, err, "%v")
		return true, true
	}
	// this is either centos, or the cluster admin is using their own registry for rhel content, so we do not
	// enforce the need for the credential
	return false, false
}

func (h *Handler) manageDockerCfgSecret(deleted bool, Config *v1.Config, s *corev1.Secret) error {
	secret := s
	var err error
	if secret == nil {
		secret, err = h.secretclientwrapper.Get(h.namespace, v1.SamplesRegistryCredentials)
		if err != nil && kerrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if secret.Name != v1.SamplesRegistryCredentials {
		return nil
	}

	var newStatus corev1.ConditionStatus
	if deleted {
		err := h.secretclientwrapper.Delete("openshift", secret.Name, &metav1.DeleteOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return h.processError(Config, v1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to delete before create dockerconfig secret the openshift namespace: %v")
		}
		logrus.Printf("registry dockerconfig secret %s was deleted", secret.Name)
		newStatus = corev1.ConditionFalse
	} else {
		secretToCreate := corev1.Secret{}
		secret.DeepCopyInto(&secretToCreate)
		secretToCreate.Namespace = ""
		secretToCreate.ResourceVersion = ""
		secretToCreate.UID = ""
		secretToCreate.Annotations = make(map[string]string)
		secretToCreate.Annotations[v1.SamplesVersionAnnotation] = v1.GitVersionString()

		s, err := h.secretclientwrapper.Get("openshift", secret.Name)
		if err != nil && !kerrors.IsNotFound(err) {
			return h.processError(Config, v1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to get registry dockerconfig secret in openshift namespace : %v")
		}
		if err != nil {
			s = nil
		}
		if s != nil {
			logrus.Printf("updating dockerconfig secret %s in openshift namespace", v1.SamplesRegistryCredentials)
			_, err = h.secretclientwrapper.Update("openshift", &secretToCreate)
		} else {
			logrus.Printf("creating dockerconfig secret %s in openshift namespace", v1.SamplesRegistryCredentials)
			_, err = h.secretclientwrapper.Create("openshift", &secretToCreate)
		}
		if err != nil {
			return h.processError(Config, v1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to create/update registry dockerconfig secret in openshif namespace : %v")
		}
		newStatus = corev1.ConditionTrue
	}

	h.GoodConditionUpdate(Config, newStatus, v1.ImportCredentialsExist)

	return nil
}

func (h *Handler) CleanUpOpenshiftNamespaceOnDelete(cfg *v1.Config) error {
	h.buildSkipFilters(cfg)

	h.imagestreamFile = map[string]string{}
	h.templateFile = map[string]string{}

	iopts := metav1.ListOptions{LabelSelector: v1.SamplesManagedLabel + "=true"}

	streamList, err := h.imageclientwrapper.List("openshift", iopts)
	if err != nil && !kerrors.IsNotFound(err) {
		logrus.Warnf("Problem listing openshift imagestreams on Config delete: %#v", err)
		return err
	} else {
		if streamList.Items != nil {
			for _, stream := range streamList.Items {
				// this should filter both skipped imagestreams and imagestreams we
				// do not manage
				manage, ok := stream.Labels[v1.SamplesManagedLabel]
				if !ok || strings.TrimSpace(manage) != "true" {
					continue
				}
				err = h.imageclientwrapper.Delete("openshift", stream.Name, &metav1.DeleteOptions{})
				if err != nil && !kerrors.IsNotFound(err) {
					logrus.Warnf("Problem deleting openshift imagestream %s on Config delete: %#v", stream.Name, err)
					return err
				}
			}
		}
	}

	tempList, err := h.templateclientwrapper.List("openshift", iopts)
	if err != nil && !kerrors.IsNotFound(err) {
		logrus.Warnf("Problem listing openshift templates on Config delete: %#v", err)
		return err
	} else {
		if tempList.Items != nil {
			for _, temp := range tempList.Items {
				// this should filter both skipped templates and templates we
				// do not manage
				manage, ok := temp.Labels[v1.SamplesManagedLabel]
				if !ok || strings.TrimSpace(manage) != "true" {
					continue
				}
				err = h.templateclientwrapper.Delete("openshift", temp.Name, &metav1.DeleteOptions{})
				if err != nil && !kerrors.IsNotFound(err) {
					logrus.Warnf("Problem deleting openshift template %s on Config delete: %#v", temp.Name, err)
					return err
				}
			}
		}
	}

	if cfg.ConditionTrue(v1.ImportCredentialsExist) {
		logrus.Println("Operator is deleting the credential in the openshift namespace that was previously created.")
		logrus.Println("If you are Removing content as part of switching from 'centos' to 'rhel', the credential will be recreated in the openshift namespace when you move back to 'Managed' state.")

	}
	err = h.secretclientwrapper.Delete("openshift", v1.SamplesRegistryCredentials, &metav1.DeleteOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		logrus.Warnf("Problem deleting openshift secret %s on Config delete: %#v", v1.SamplesRegistryCredentials, err)
		return err
	}

	//TODO when we start copying secrets from kubesystem to act as the default secret for pulling rhel content
	// we'll want to delete that one too ... we'll need to put a marking on the secret to indicate we created it
	// vs. the admin creating it

	return nil
}

// ProcessManagementField returns true if the operator should handle the SampleResource event
// and false if it should not, as well as an err in case we want to bubble that up to
// the controller level logic for retry
// the returns are
// first bool - whether to process this event
// second bool - whether to update the samples resources with the new conditions
// err - any errors that occurred interacting with the api server during cleanup
func (h *Handler) ProcessManagementField(cfg *v1.Config) (bool, bool, error) {
	switch cfg.Spec.ManagementState {
	case operatorsv1api.Removed:
		// first, we will not process a Removed setting if a prior create/update cycle is still in progress;
		// if still creating/updating, set the remove on hold condition and we'll try the remove once that
		// is false
		if cfg.ConditionTrue(v1.ImageChangesInProgress) && cfg.ConditionTrue(v1.RemovedManagementStateOnHold) {
			return false, false, nil
		}

		if cfg.ConditionTrue(v1.ImageChangesInProgress) && !cfg.ConditionTrue(v1.RemovedManagementStateOnHold) {
			now := kapis.Now()
			condition := cfg.Condition(v1.RemovedManagementStateOnHold)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionTrue
			cfg.ConditionUpdate(condition)
			return false, true, nil
		}

		// turn off on hold if need be
		if cfg.ConditionTrue(v1.RemovedManagementStateOnHold) && cfg.ConditionFalse(v1.ImageChangesInProgress) {
			now := kapis.Now()
			condition := cfg.Condition(v1.RemovedManagementStateOnHold)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionFalse
			cfg.ConditionUpdate(condition)
			return false, true, nil
		}

		// now actually process removed state
		if cfg.Spec.ManagementState != cfg.Status.ManagementState ||
			cfg.ConditionTrue(v1.SamplesExist) {
			logrus.Println("management state set to removed so deleting samples")
			err := h.CleanUpOpenshiftNamespaceOnDelete(cfg)
			if err != nil {
				return false, true, h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "The error %v during openshift namespace cleanup has left the samples in an unknown state")
			}
			// explicitly reset samples exist and import cred to false since the Config has not
			// actually been deleted; secret watch ignores events when samples resource is in removed state
			now := kapis.Now()
			condition := cfg.Condition(v1.SamplesExist)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionFalse
			cfg.ConditionUpdate(condition)
			cred := cfg.Condition(v1.ImportCredentialsExist)
			cred.LastTransitionTime = now
			cred.LastUpdateTime = now
			cred.Status = corev1.ConditionFalse
			cfg.ConditionUpdate(cred)
			cfg.Status.ManagementState = operatorsv1api.Removed
			cfg.Status.Version = ""
			return false, true, nil
		}
		return false, false, nil
	case operatorsv1api.Managed:
		if cfg.Spec.ManagementState != cfg.Status.ManagementState {
			logrus.Println("management state set to managed")
			if cfg.Spec.InstallType == v1.RHELSamplesDistribution &&
				cfg.ConditionFalse(v1.ImportCredentialsExist) {
				secret, err := h.secretclientwrapper.Get(h.namespace, v1.SamplesRegistryCredentials)
				if err == nil && secret != nil {
					// as part of going from centos to rhel, if the secret was created *BEFORE* we went to
					// removed state, it got removed in the openshift namespace as part of going to remove;
					// so we want to get it back into the openshift namespace;  to do so, we
					// initiate a secret event vs. doing the copy ourselves here
					logrus.Println("updating operator namespace credential to initiate creation of credential in openshift namespace")
					if secret.Annotations == nil {
						secret.Annotations = map[string]string{}
					}
					// testing showed that we needed to change something for the update to actually go through
					secret.Annotations[v1.SamplesRecreateCredentialAnnotation] = kapis.Now().String()
					h.secretclientwrapper.Update(h.namespace, secret)
				}
			}
		}
		// will set status state to managed at top level caller
		// to deal with config change processing
		return true, false, nil
	case operatorsv1api.Unmanaged:
		if cfg.Spec.ManagementState != cfg.Status.ManagementState {
			logrus.Println("management state set to unmanaged")
			cfg.Status.ManagementState = operatorsv1api.Unmanaged
			return false, true, nil
		}
		return false, false, nil
	default:
		// force it to Managed if they passed in something funky, including the empty string
		logrus.Warningf("Unknown management state %s specified; switch to Managed", cfg.Spec.ManagementState)
		cfgvalid := cfg.Condition(v1.ConfigurationValid)
		cfgvalid.Message = fmt.Sprintf("Unexpected management state %v received, switching to %v", cfg.Spec.ManagementState, operatorsv1api.Managed)
		now := kapis.Now()
		cfgvalid.LastTransitionTime = now
		cfgvalid.LastUpdateTime = now
		cfg.ConditionUpdate(cfgvalid)
		return true, false, nil
	}
}

func (h *Handler) Handle(event v1.Event) error {
	switch event.Object.(type) {
	case *imagev1.ImageStream:
		is, _ := event.Object.(*imagev1.ImageStream)
		if is.Namespace != "openshift" {
			return nil
		}
		err := h.processImageStreamWatchEvent(is, event.Deleted)
		return err

	case *templatev1.Template:
		t, _ := event.Object.(*templatev1.Template)
		if t.Namespace != "openshift" {
			return nil
		}
		err := h.processTemplateWatchEvent(t, event.Deleted)
		return err

	case *corev1.Secret:
		dockercfgSecret, _ := event.Object.(*corev1.Secret)
		if dockercfgSecret.Name != v1.SamplesRegistryCredentials {
			return nil
		}

		//TODO what do we do about possible missed delete events since we
		// cannot add a finalizer in our namespace secret

		cfg, _ := h.crdwrapper.Get(v1.ConfigName)
		if cfg != nil {
			switch cfg.Spec.ManagementState {
			case operatorsv1api.Removed:
				// so our current recipe to switch to rhel is to
				// - mark mgmt state removed
				// - after that complete, edit again, mark install type to rhel and mgmt state to managed
				// but what about the secret needed for rhel ... do we force the user to create the secret
				// while still in managed/centos state?  Even with that, the "removed" action removes the
				// secret the operator creates in the openshift namespace since it was owned/created by
				// the operator
				// So we allow the processing of the secret event while in removed state to
				// facilitate the switch from centos to rhel, as necessitating use of removed as the means for
				// changing from centos to rhel since  we allow changing the distribution once the samples have initially been created
				logrus.Printf("processing secret watch event while in Removed state; deletion event: %v", event.Deleted)
			case operatorsv1api.Unmanaged:
				logrus.Debugln("Ignoring secret event because samples resource is in unmanaged state")
				return nil
			case operatorsv1api.Managed:
				logrus.Printf("processing secret watch event while in Managed state; deletion event: %v", event.Deleted)
			default:
				logrus.Printf("processing secret watch event like we are in Managed state, even though it is set to %v; deletion event %v", cfg.Spec.ManagementState, event.Deleted)
			}
			deleted := event.Deleted
			if dockercfgSecret.Namespace == "openshift" {
				if !deleted {
					if dockercfgSecret.Annotations != nil {
						_, ok := dockercfgSecret.Annotations[v1.SamplesVersionAnnotation]
						if ok {
							// this is just a notification from our prior create
							logrus.Println("creation of credential in openshift namespace recognized")
							return nil
						}
					}
					// not foolproof protection of course, but the lack of the annotation
					// means somebody tried to create our credential in the openshift namespace
					// on there own ... we are not allowing that
					err := fmt.Errorf("the samples credential was created in the openshift namespace without the version annotation")
					return h.processError(cfg, v1.ImportCredentialsExist, corev1.ConditionUnknown, err, "%v")
				}
				// if deleted, but import credential == true, that means somebody deleted the credential in the openshift
				// namespace while leaving the secret in the operator namespace alone; we don't like that either, and will
				// recreate; but we have to account for the fact that on a valid delete/remove, the secret deletion occurs
				// before the updating of the samples resource, so we employ a short term retry
				if cfg.ConditionTrue(v1.ImportCredentialsExist) {
					if h.secretRetryCount < 3 {
						err := fmt.Errorf("retry on credential deletion in the openshift namespace to make sure the operator deleted it")
						h.secretRetryCount++
						return err
					}
					logrus.Println("credential in openshift namespace deleted while it still exists in operator namespace so recreating")
					s, err := h.secretclientwrapper.Get(h.namespace, v1.SamplesRegistryCredentials)
					if err != nil {
						if !kerrors.IsNotFound(err) {
							// retry
							return err
						}
						// if the credential is missing there too, move on and make sure import cred is false
					} else {
						// reset deleted flag, dockercfgSecret so manageDockerCfgSecret will recreate
						deleted = false
						dockercfgSecret = s
					}
				}
				logrus.Println("deletion of credential in openshift namespace after deletion of credential in operator namespace recognized")
				return nil
			}
			h.secretRetryCount = 0
			err := h.manageDockerCfgSecret(deleted, cfg, dockercfgSecret)
			if err != nil {
				return err
			}
			// flush the status changes generated by the processing
			logrus.Debugf("SDKUPDATE event secret update")
			return h.crdwrapper.Update(cfg)
		} else {
			return fmt.Errorf("Received secret %s but do not have the Config yet, requeuing", dockercfgSecret.Name)
		}

	case *v1.Config:
		cfg, _ := event.Object.(*v1.Config)

		if cfg.Name != v1.ConfigName || cfg.Namespace != "" {
			return nil
		}

		// pattern is 1) come in with delete timestamp, event delete flag false
		// 2) then after we remove finalizer, comes in with delete timestamp
		// and event delete flag true
		if event.Deleted {
			logrus.Info("A previous delete attempt has been successfully completed")
			return nil
		}
		if cfg.DeletionTimestamp != nil {
			// before we kick off the delete cycle though, we make sure a prior creation
			// cycle is not still in progress, because we don't want the create adding back
			// in things we just deleted ... if an upsert is still in progress, return nil
			if cfg.ConditionTrue(v1.ImageChangesInProgress) {
				return nil
			}

			if h.NeedsFinalizing(cfg) {
				// so we initiate the delete and set exists to false first, where if we get
				// conflicts because of start up imagestream events, the retry should work
				// cause the finalizer is still there; also, needs finalizing sets the deleteInProgress
				// flag (which imagestream event processing checks)
				//
				// when we come back in with the deleteInProgress already true, with a delete timestamp
				// we then remove the finalizer and create the new Config
				//
				// note, as part of resetting the delete flag during error retries, we still need
				// a way to tell the imagestream event processing to not bother with pending updates,
				// so we have an additional flag for that special case
				logrus.Println("Initiating samples delete and marking exists false")
				err := h.CleanUpOpenshiftNamespaceOnDelete(cfg)
				if err != nil {
					return err
				}
				h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.SamplesExist)
				logrus.Debugf("SDKUPDATE exist false update")
				err = h.crdwrapper.Update(cfg)
				if err != nil {
					logrus.Printf("error on Config update after setting exists condition to false (returning error to retry): %v", err)
					return err
				}
			} else {
				logrus.Println("Initiating finalizer processing for a SampleResource delete attempt")
				h.RemoveFinalizer(cfg)
				logrus.Debugf("SDKUPDATE remove finalizer update")
				err := h.crdwrapper.Update(cfg)
				if err != nil {
					logrus.Printf("error removing Config finalizer during delete (hopefully retry on return of error works): %v", err)
					return err
				}
				go func() {
					h.CreateDefaultResourceIfNeeded(cfg)
				}()
			}
			return nil
		}

		// Every time we see a change to the Config object, update the ClusterOperator status
		// based on the current conditions of the Config.
		err := h.cvowrapper.UpdateOperatorStatus(cfg)
		if err != nil {
			logrus.Errorf("error updating cluster operator status: %v", err)
			return err
		}

		doit, cfgUpdate, err := h.ProcessManagementField(cfg)
		if !doit || err != nil {
			if err != nil || cfgUpdate {
				// flush status update
				logrus.Debugf("SDKUPDATE process mgmt update")
				h.crdwrapper.Update(cfg)
			}
			return err
		}

		existingValidStatus := cfg.Condition(v1.ConfigurationValid).Status
		err = h.SpecValidation(cfg)
		if err != nil {
			// flush status update
			logrus.Debugf("SDKUPDATE bad spec validation update")
			// only retry on error updating the Config; do not return
			// the error from SpecValidation which denotes a bad config
			return h.crdwrapper.Update(cfg)
		}
		// if a bad config was corrected, update and return
		if existingValidStatus != cfg.Condition(v1.ConfigurationValid).Status {
			logrus.Debugf("SDKUPDATE spec corrected")
			return h.crdwrapper.Update(cfg)
		}

		configChanged := false
		if cfg.Spec.ManagementState == cfg.Status.ManagementState {
			configChanged, err = h.VariableConfigChanged(cfg)
			if err != nil {
				// flush status update
				logrus.Debugf("SDKUPDATE var cfg chg err update")
				h.crdwrapper.Update(cfg)
				return err
			}
			logrus.Debugf("config changed %v exists %v progressing %v spec version %s status version %s",
				configChanged,
				cfg.ConditionTrue(v1.SamplesExist),
				cfg.ConditionFalse(v1.ImageChangesInProgress),
				cfg.Spec.Version,
				cfg.Status.Version)
			// so ignore if config does not change and the samples exist and
			// we are not in progress and at the right level
			if !configChanged &&
				cfg.ConditionTrue(v1.SamplesExist) &&
				cfg.ConditionFalse(v1.ImageChangesInProgress) &&
				cfg.Spec.Version == cfg.Status.Version {
				logrus.Debugf("Handle ignoring because config the same and exists is true, in progress false, and version correct")
				return nil
			}
			// if config changed, but a prior config action is still in progress,
			// reset in progress to false and return; the next event should drive the actual
			// processing of the config change and replace whatever was previously
			// in progress
			if configChanged && cfg.ConditionTrue(v1.ImageChangesInProgress) {
				h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
				logrus.Debugf("SDKUPDATE change in progress from true to false for config change")
				return h.crdwrapper.Update(cfg)
			}
		}

		cfg.Status.ManagementState = operatorsv1api.Managed

		// if trying to do rhel to the default registry.redhat.io registry requires the secret
		// be in place since registry.redhat.io requires auth to pull; if it is not ready
		// error state will be logged by WaitingForCredential
		stillWaitingForSecret, callSDKToUpdate := h.WaitingForCredential(cfg)
		if callSDKToUpdate {
			// flush status update ... the only error generated by WaitingForCredential, not
			// by api obj access
			logrus.Println("Config update ignored since need the RHEL credential")
			// if update to set import cred condition to false fails, return that error
			// to requeue
			return h.crdwrapper.Update(cfg)
		}
		if stillWaitingForSecret {
			// means we previously udpated cfg but nothing has changed wrt the secret's presence
			return nil
		}

		if cfg.ConditionFalse(v1.MigrationInProgress) &&
			len(cfg.Status.Version) > 0 &&
			cfg.Spec.Version != cfg.Status.Version {
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.MigrationInProgress)
			logrus.Debugf("SDKUPDATE migration on")
			return h.crdwrapper.Update(cfg)
		}

		if !configChanged &&
			cfg.ConditionTrue(v1.SamplesExist) &&
			cfg.ConditionFalse(v1.ImageChangesInProgress) &&
			cfg.ConditionFalse(v1.MigrationInProgress) &&
			cfg.Spec.Version != cfg.Status.Version {
			cfg.Status.Version = cfg.Spec.Version
			logrus.Debugf("SDKUPDATE upd status version")
			logrus.Printf("The samples are now at version %s", cfg.Status.Version)
			return h.crdwrapper.Update(cfg)
		}

		// lastly, if we are in samples exists and progressing both true, we can forgo cycling
		// through the image content

		h.buildSkipFilters(cfg)

		if len(cfg.Spec.Architectures) == 0 {
			cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.X86Architecture)
		}

		if len(cfg.Spec.InstallType) == 0 {
			cfg.Spec.InstallType = v1.CentosSamplesDistribution
		}

		h.StoreCurrentValidConfig(cfg)

		if len(h.imagestreamFile) == 0 || len(h.templateFile) == 0 {
			for _, arch := range cfg.Spec.Architectures {
				dir := h.GetBaseDir(arch, cfg)
				files, err := h.Filefinder.List(dir)
				if err != nil {
					err = h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error reading in content : %v")
					logrus.Debugf("SDKUPDATE file list err update")
					h.crdwrapper.Update(cfg)
					return err
				}
				err = h.processFiles(dir, files, cfg)
				if err != nil {
					err = h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error processing content : %v")
					logrus.Debugf("SDKUPDATE proc file err update")
					h.crdwrapper.Update(cfg)
					return err
				}
			}
		}

		if !cfg.ConditionTrue(v1.ImageChangesInProgress) {
			h.creationInProgress = true
			defer func() { h.creationInProgress = false }()
			err = h.createSamples(cfg)
			if err != nil {
				h.processError(cfg, v1.ImageChangesInProgress, corev1.ConditionUnknown, err, "error creating samples: %v")
				e := h.crdwrapper.Update(cfg)
				if e != nil {
					return e
				}
				return err
			}
			now := kapis.Now()
			progressing := cfg.Condition(v1.ImageChangesInProgress)
			progressing.LastUpdateTime = now
			progressing.LastTransitionTime = now
			logrus.Debugf("Handle changing processing from false to true")
			progressing.Status = corev1.ConditionTrue
			for isName := range h.imagestreamFile {
				_, skipped := h.skippedImagestreams[isName]
				if !strings.Contains(progressing.Reason, isName+" ") && !skipped {
					progressing.Reason = progressing.Reason + isName + " "
				}
			}
			logrus.Debugf("Handle Reason field set to %s", progressing.Reason)
			cfg.ConditionUpdate(progressing)
			cfg.Spec.Version = v1.GitVersionString()
			logrus.Debugf("SDKUPDATE progressing true update")
			err = h.crdwrapper.Update(cfg)
			if err != nil {
				return err
			}
			return nil
		}

		if !cfg.ConditionTrue(v1.SamplesExist) {
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.SamplesExist)
			// flush updates from processing
			logrus.Debugf("SDKUPDATE good cond update")
			return h.crdwrapper.Update(cfg)
		}

	}
	return nil
}

func (h *Handler) buildSkipFilters(opcfg *v1.Config) {
	newStreamMap := make(map[string]bool)
	newTempMap := make(map[string]bool)
	for _, st := range opcfg.Spec.SkippedTemplates {
		newTempMap[st] = true
	}
	for _, si := range opcfg.Spec.SkippedImagestreams {
		newStreamMap[si] = true
	}
	h.skippedImagestreams = newStreamMap
	h.skippedTemplates = newTempMap
}

func (h *Handler) processError(opcfg *v1.Config, ctype v1.ConfigConditionType, cstatus corev1.ConditionStatus, err error, msg string, args ...interface{}) error {
	log := ""
	if args == nil {
		log = fmt.Sprintf(msg, err)
	} else {
		log = fmt.Sprintf(msg, err, args)
	}
	logrus.Println(log)
	status := opcfg.Condition(ctype)
	// decision was made to not spam master if
	// duplicate events come it (i.e. status does not
	// change)
	if status.Status != cstatus || status.Message != log {
		now := kapis.Now()
		status.LastUpdateTime = now
		status.Status = cstatus
		status.LastTransitionTime = now
		status.Message = log
		opcfg.ConditionUpdate(status)
	}

	// return original error
	return err
}

func (h *Handler) upsertImageStream(imagestreamInOperatorImage, imagestreamInCluster *imagev1.ImageStream, opcfg *v1.Config) error {
	if _, isok := h.skippedImagestreams[imagestreamInOperatorImage.Name]; isok {
		if imagestreamInCluster != nil {
			if imagestreamInCluster.Labels == nil {
				imagestreamInCluster.Labels = make(map[string]string)
			}
			imagestreamInCluster.Labels[v1.SamplesManagedLabel] = "false"
			h.imageclientwrapper.Update("openshift", imagestreamInCluster)
			// if we get an error, we'll just try to remove the label next
			// time; and we'll examine the skipped lists on delete
		}
		return nil
	}

	h.updateDockerPullSpec([]string{"docker.io", "registry.redhat.io", "registry.access.redhat.com", "quay.io"}, imagestreamInOperatorImage, opcfg)

	if imagestreamInOperatorImage.Labels == nil {
		imagestreamInOperatorImage.Labels = make(map[string]string)
	}
	if imagestreamInOperatorImage.Annotations == nil {
		imagestreamInOperatorImage.Annotations = make(map[string]string)
	}
	imagestreamInOperatorImage.Labels[v1.SamplesManagedLabel] = "true"
	imagestreamInOperatorImage.Annotations[v1.SamplesVersionAnnotation] = v1.GitVersionString()

	if imagestreamInCluster == nil {
		_, err := h.imageclientwrapper.Create("openshift", imagestreamInOperatorImage)
		if err != nil {
			if kerrors.IsAlreadyExists(err) {
				logrus.Printf("imagestream %s recreated since delete event", imagestreamInOperatorImage.Name)
				// return the error so the caller can decide what to do
				return err
			}
			return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "imagestream create error: %v")
		}
		logrus.Printf("created imagestream %s", imagestreamInOperatorImage.Name)
		return nil
	}

	imagestreamInOperatorImage.ResourceVersion = imagestreamInCluster.ResourceVersion
	_, err := h.imageclientwrapper.Update("openshift", imagestreamInOperatorImage)
	if err != nil {
		return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "imagestream update error: %v")
	}
	logrus.Printf("updated imagestream %s", imagestreamInCluster.Name)
	return nil
}

func (h *Handler) upsertTemplate(templateInOperatorImage, templateInCluster *templatev1.Template, opcfg *v1.Config) error {
	if _, tok := h.skippedTemplates[templateInOperatorImage.Name]; tok {
		if templateInCluster != nil {
			if templateInCluster.Labels == nil {
				templateInCluster.Labels = make(map[string]string)
			}
			templateInCluster.Labels[v1.SamplesManagedLabel] = "false"
			h.templateclientwrapper.Update("openshift", templateInCluster)
			// if we get an error, we'll just try to remove the label next
			// time; and we'll examine the skipped lists on delete
		}
		return nil
	}

	if templateInOperatorImage.Labels == nil {
		templateInOperatorImage.Labels = map[string]string{}
	}
	if templateInOperatorImage.Annotations == nil {
		templateInOperatorImage.Annotations = map[string]string{}
	}
	templateInOperatorImage.Labels[v1.SamplesManagedLabel] = "true"
	templateInOperatorImage.Annotations[v1.SamplesVersionAnnotation] = v1.GitVersionString()

	if templateInCluster == nil {
		_, err := h.templateclientwrapper.Create("openshift", templateInOperatorImage)
		if err != nil {
			if kerrors.IsAlreadyExists(err) {
				logrus.Printf("template %s recreated since delete event", templateInOperatorImage.Name)
				// return the error so the caller can decide what to do
				return err
			}
			return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "template create error: %v")
		}
		logrus.Printf("created template %s", templateInOperatorImage.Name)
		return nil
	}

	templateInOperatorImage.ResourceVersion = templateInCluster.ResourceVersion
	_, err := h.templateclientwrapper.Update("openshift", templateInOperatorImage)
	if err != nil {
		return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "template update error: %v")
	}
	logrus.Printf("updated template %s", templateInCluster.Name)
	return nil
}

func (h *Handler) createSamples(cfg *v1.Config) error {
	for _, fileName := range h.imagestreamFile {
		imagestream, err := h.Fileimagegetter.Get(fileName)
		if err != nil {
			return err
		}
		is, err := h.imageclientwrapper.Get("openshift", imagestream.Name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return err
		}

		if kerrors.IsNotFound(err) {
			// testing showed that we get an empty is vs. nil in this case
			is = nil
		}

		err = h.upsertImageStream(imagestream, is, cfg)
		if err != nil {
			return err
		}
	}
	for _, fileName := range h.templateFile {
		template, err := h.Filetemplategetter.Get(fileName)
		if err != nil {
			return err
		}

		t, err := h.templateclientwrapper.Get("openshift", template.Name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return err
		}

		if kerrors.IsNotFound(err) {
			// testing showed that we get an empty is vs. nil in this case
			t = nil
		}

		err = h.upsertTemplate(template, t, cfg)
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) processFiles(dir string, files []os.FileInfo, opcfg *v1.Config) error {

	for _, file := range files {
		if file.IsDir() {
			logrus.Printf("processing subdir %s from dir %s", file.Name(), dir)
			subfiles, err := h.Filefinder.List(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error reading in content: %v")
			}
			err = h.processFiles(dir+"/"+file.Name(), subfiles, opcfg)
			if err != nil {
				return err
			}

			continue
		}
		logrus.Printf("processing file %s from dir %s", file.Name(), dir)

		if strings.HasSuffix(dir, "imagestreams") {
			imagestream, err := h.Fileimagegetter.Get(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", dir+"/"+file.Name())
			}
			h.imagestreamFile[imagestream.Name] = dir + "/" + file.Name()
			continue
		}

		if strings.HasSuffix(dir, "templates") {
			template, err := h.Filetemplategetter.Get(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", dir+"/"+file.Name())
			}

			h.templateFile[template.Name] = dir + "/" + file.Name()

		}
	}
	return nil
}

func (h *Handler) coreUpdateDockerPullSpec(oldreg, newreg string, oldies []string) string {
	// see if the imagestream on file (i.e. the openshift/library content) is
	// of the form "reg/repo/img" or "repo/img"
	hasRegistry := false
	if strings.Count(oldreg, "/") == 2 {
		hasRegistry = true
	}
	logrus.Debugf("coreUpdatePull hasRegistry %v", hasRegistry)
	if hasRegistry {
		for _, old := range oldies {
			if strings.HasPrefix(oldreg, old) {
				oldreg = strings.Replace(oldreg, old, newreg, 1)
				logrus.Debugf("coreUpdatePull hasReg1 reg now %s", oldreg)
			} else {
				// the content from openshift/library has something odd in in ... replace the registry piece
				parts := strings.Split(oldreg, "/")
				oldreg = newreg + "/" + parts[1] + "/" + parts[2]
				logrus.Debugf("coreUpdatePull hasReg2 reg now %s", oldreg)
			}
		}
	} else {
		oldreg = newreg + "/" + oldreg
		logrus.Debugf("coreUpdatePull no hasReg reg now %s", oldreg)
	}

	return oldreg
}

func (h *Handler) updateDockerPullSpec(oldies []string, imagestream *imagev1.ImageStream, opcfg *v1.Config) {
	if len(opcfg.Spec.SamplesRegistry) > 0 {
		logrus.Debugf("updateDockerPullSpec stream %s has repo %s", imagestream.Name, imagestream.Spec.DockerImageRepository)
		// don't mess with deprecated field unless it is actually set with something
		if len(imagestream.Spec.DockerImageRepository) > 0 &&
			!strings.HasPrefix(imagestream.Spec.DockerImageRepository, opcfg.Spec.SamplesRegistry) {
			// if not one of our 4 defaults ...
			imagestream.Spec.DockerImageRepository = h.coreUpdateDockerPullSpec(imagestream.Spec.DockerImageRepository,
				opcfg.Spec.SamplesRegistry,
				oldies)
		}

		for _, tagref := range imagestream.Spec.Tags {
			logrus.Debugf("updateDockerPullSpec stream %s and tag %s has from %#v", imagestream.Name, tagref.Name, tagref.From)
			if tagref.From != nil {
				switch tagref.From.Kind {
				// ImageStreamTag and ImageStreamImage will ultimately point to a DockerImage From object reference
				// we are only updating the actual registry pull specs
				case "DockerImage":
					if !strings.HasPrefix(tagref.From.Name, opcfg.Spec.SamplesRegistry) {
						tagref.From.Name = h.coreUpdateDockerPullSpec(tagref.From.Name,
							opcfg.Spec.SamplesRegistry,
							oldies)
					}
				}
			}
		}
	}

}

func (h *Handler) GetBaseDir(arch string, opcfg *v1.Config) (dir string) {
	// invalid settings have already been sorted out by SpecValidation
	switch arch {
	case v1.X86Architecture:
		switch opcfg.Spec.InstallType {
		case v1.RHELSamplesDistribution:
			dir = x86OCPContentRootDir
		case v1.CentosSamplesDistribution:
			dir = x86OKDContentRootDir
		default:
		}
	case v1.PPCArchitecture:
		dir = ppc64OCPContentRootDir
	default:
	}
	return dir
}

func getTemplateClient(restconfig *restclient.Config) (*templatev1client.TemplateV1Client, error) {
	return templatev1client.NewForConfig(restconfig)
}

func getImageClient(restconfig *restclient.Config) (*imagev1client.ImageV1Client, error) {
	return imagev1client.NewForConfig(restconfig)
}

func GetNamespace() string {
	b, _ := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/" + corev1.ServiceAccountNamespaceKey)
	return string(b)
}

type ImageStreamClientWrapper interface {
	Get(namespace, name string, opts metav1.GetOptions) (*imagev1.ImageStream, error)
	List(namespace string, opts metav1.ListOptions) (*imagev1.ImageStreamList, error)
	Create(namespace string, is *imagev1.ImageStream) (*imagev1.ImageStream, error)
	Update(namespace string, is *imagev1.ImageStream) (*imagev1.ImageStream, error)
	Delete(namespace, name string, opts *metav1.DeleteOptions) error
	Watch(namespace string) (watch.Interface, error)
}

type defaultImageStreamClientWrapper struct {
	h *Handler
}

func (g *defaultImageStreamClientWrapper) Get(namespace, name string, opts metav1.GetOptions) (*imagev1.ImageStream, error) {
	return g.h.imageclient.ImageStreams(namespace).Get(name, opts)
}

func (g *defaultImageStreamClientWrapper) List(namespace string, opts metav1.ListOptions) (*imagev1.ImageStreamList, error) {
	return g.h.imageclient.ImageStreams(namespace).List(opts)
}

func (g *defaultImageStreamClientWrapper) Create(namespace string, is *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return g.h.imageclient.ImageStreams(namespace).Create(is)
}

func (g *defaultImageStreamClientWrapper) Update(namespace string, is *imagev1.ImageStream) (*imagev1.ImageStream, error) {
	return g.h.imageclient.ImageStreams(namespace).Update(is)
}

func (g *defaultImageStreamClientWrapper) Delete(namespace, name string, opts *metav1.DeleteOptions) error {
	return g.h.imageclient.ImageStreams(namespace).Delete(name, opts)
}

func (g *defaultImageStreamClientWrapper) Watch(namespace string) (watch.Interface, error) {
	opts := metav1.ListOptions{}
	return g.h.imageclient.ImageStreams(namespace).Watch(opts)
}

type TemplateClientWrapper interface {
	Get(namespace, name string, opts metav1.GetOptions) (*templatev1.Template, error)
	List(namespace string, opts metav1.ListOptions) (*templatev1.TemplateList, error)
	Create(namespace string, t *templatev1.Template) (*templatev1.Template, error)
	Update(namespace string, t *templatev1.Template) (*templatev1.Template, error)
	Delete(namespace, name string, opts *metav1.DeleteOptions) error
	Watch(namespace string) (watch.Interface, error)
}

type defaultTemplateClientWrapper struct {
	h *Handler
}

func (g *defaultTemplateClientWrapper) Get(namespace, name string, opts metav1.GetOptions) (*templatev1.Template, error) {
	return g.h.tempclient.Templates(namespace).Get(name, opts)
}

func (g *defaultTemplateClientWrapper) List(namespace string, opts metav1.ListOptions) (*templatev1.TemplateList, error) {
	return g.h.tempclient.Templates(namespace).List(opts)
}

func (g *defaultTemplateClientWrapper) Create(namespace string, t *templatev1.Template) (*templatev1.Template, error) {
	return g.h.tempclient.Templates(namespace).Create(t)
}

func (g *defaultTemplateClientWrapper) Update(namespace string, t *templatev1.Template) (*templatev1.Template, error) {
	return g.h.tempclient.Templates(namespace).Update(t)
}

func (g *defaultTemplateClientWrapper) Delete(namespace, name string, opts *metav1.DeleteOptions) error {
	return g.h.tempclient.Templates(namespace).Delete(name, opts)
}

func (g *defaultTemplateClientWrapper) Watch(namespace string) (watch.Interface, error) {
	opts := metav1.ListOptions{}
	return g.h.tempclient.Templates(namespace).Watch(opts)
}

type SecretClientWrapper interface {
	Get(namespace, name string) (*corev1.Secret, error)
	Create(namespace string, s *corev1.Secret) (*corev1.Secret, error)
	Update(namespace string, s *corev1.Secret) (*corev1.Secret, error)
	Delete(namespace, name string, opts *metav1.DeleteOptions) error
}

type defaultSecretClientWrapper struct {
	coreclient *corev1client.CoreV1Client
}

func (g *defaultSecretClientWrapper) Get(namespace, name string) (*corev1.Secret, error) {
	return g.coreclient.Secrets(namespace).Get(name, metav1.GetOptions{})
}

func (g *defaultSecretClientWrapper) Create(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	return g.coreclient.Secrets(namespace).Create(s)
}

func (g *defaultSecretClientWrapper) Update(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	return g.coreclient.Secrets(namespace).Update(s)
}

func (g *defaultSecretClientWrapper) Delete(namespace, name string, opts *metav1.DeleteOptions) error {
	return g.coreclient.Secrets(namespace).Delete(name, opts)
}

type ImageStreamFromFileGetter interface {
	Get(fullFilePath string) (is *imagev1.ImageStream, err error)
}

type DefaultImageStreamFromFileGetter struct {
}

func (g *DefaultImageStreamFromFileGetter) Get(fullFilePath string) (is *imagev1.ImageStream, err error) {
	isjsonfile, err := ioutil.ReadFile(fullFilePath)
	if err != nil {
		return nil, err
	}

	imagestream := &imagev1.ImageStream{}
	err = json.Unmarshal(isjsonfile, imagestream)
	if err != nil {
		return nil, err
	}

	return imagestream, nil
}

type TemplateFromFileGetter interface {
	Get(fullFilePath string) (t *templatev1.Template, err error)
}

type DefaultTemplateFromFileGetter struct {
}

func (g *DefaultTemplateFromFileGetter) Get(fullFilePath string) (t *templatev1.Template, err error) {
	tjsonfile, err := ioutil.ReadFile(fullFilePath)
	if err != nil {
		return nil, err
	}
	template := &templatev1.Template{}
	err = json.Unmarshal(tjsonfile, template)
	if err != nil {
		return nil, err
	}

	return template, nil
}

type ResourceFileLister interface {
	List(dir string) (files []os.FileInfo, err error)
}

type DefaultResourceFileLister struct {
}

func (g *DefaultResourceFileLister) List(dir string) (files []os.FileInfo, err error) {
	files, err = ioutil.ReadDir(dir)
	return files, err

}

type InClusterInitter interface {
	init(h *Handler, restconfig *restclient.Config)
}

type defaultInClusterInitter struct {
}

func (g *defaultInClusterInitter) init(h *Handler, restconfig *restclient.Config) {
	h.restconfig = restconfig
	tempclient, err := getTemplateClient(restconfig)
	if err != nil {
		logrus.Errorf("failed to get template client : %v", err)
		panic(err)
	}
	h.tempclient = tempclient
	logrus.Printf("template client %#v", tempclient)
	imageclient, err := getImageClient(restconfig)
	if err != nil {
		logrus.Errorf("failed to get image client : %v", err)
		panic(err)
	}
	h.imageclient = imageclient
	logrus.Printf("image client %#v", imageclient)
	coreclient, err := corev1client.NewForConfig(restconfig)
	if err != nil {
		logrus.Errorf("failed to get core client : %v", err)
		panic(err)
	}
	h.coreclient = coreclient
	configclient, err := configv1client.NewForConfig(restconfig)
	if err != nil {
		logrus.Errorf("failed to get config client : %v", err)
		panic(err)
	}
	h.configclient = configclient
}

type CRDWrapper interface {
	Update(Config *v1.Config) (err error)
	Create(Config *v1.Config) (err error)
	Get(name string) (*v1.Config, error)
}

type generatedCRDWrapper struct {
	client sampleclientv1.ConfigInterface
}

func (g *generatedCRDWrapper) Update(sr *v1.Config) error {
	return wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		_, err := g.client.Update(sr)
		if err == nil {
			return true, nil
		}
		if !IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})

}

func (g *generatedCRDWrapper) Create(sr *v1.Config) error {
	return wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		_, err := g.client.Create(sr)
		if err == nil {
			return true, nil
		}
		if !IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})
}

func (g *generatedCRDWrapper) Get(name string) (*v1.Config, error) {
	sr := &v1.Config{}
	var err error
	err = wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		sr, err = g.client.Get(name, metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		if !IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return sr, nil

}
