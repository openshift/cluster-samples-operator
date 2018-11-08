package stub

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/operator-framework/operator-sdk/pkg/sdk"
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

	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	templatev1client "github.com/openshift/client-go/template/clientset/versioned/typed/template/v1"

	operatorsv1alpha1api "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/cluster-samples-operator/pkg/apis/samplesoperator/v1alpha1"
	operatorstatus "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
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

func NewHandler() sdk.Handler {
	h := Handler{}

	h.initter = &defaultInClusterInitter{h: &h}
	h.initter.init()

	h.sdkwrapper = &defaultSDKWrapper{h: &h}

	h.fileimagegetter = &defaultImageStreamFromFileGetter{h: &h}
	h.filetemplategetter = &defaultTemplateFromFileGetter{h: &h}
	h.filefinder = &defaultResourceFileLister{h: &h}

	h.imageclientwrapper = &defaultImageStreamClientWrapper{h: &h}
	h.templateclientwrapper = &defaultTemplateClientWrapper{h: &h}
	h.secretclientwrapper = &defaultSecretClientWrapper{h: &h}
	h.configmapclientwrapper = &defaultConfigMapClientWrapper{h: &h}

	h.namespace = getNamespace()
	h.cvowrapper = operatorstatus.NewCVOOperatorStatusHandler(h.namespace)

	h.skippedImagestreams = make(map[string]bool)
	h.skippedTemplates = make(map[string]bool)

	h.mutex = &sync.Mutex{}
	h.CreateDefaultResourceIfNeeded()

	h.imagestreamFile = make(map[string]string)
	h.templateFile = make(map[string]string)

	return &h
}

type Handler struct {
	initter InClusterInitter

	sdkwrapper SDKWrapper
	cvowrapper *operatorstatus.CVOOperatorStatusHandler

	restconfig  *restclient.Config
	tempclient  *templatev1client.TemplateV1Client
	imageclient *imagev1client.ImageV1Client
	coreclient  *corev1client.CoreV1Client

	imageclientwrapper     ImageStreamClientWrapper
	templateclientwrapper  TemplateClientWrapper
	secretclientwrapper    SecretClientWrapper
	configmapclientwrapper ConfigMapClientWrapper

	fileimagegetter    ImageStreamFromFileGetter
	filetemplategetter TemplateFromFileGetter
	filefinder         ResourceFileLister

	namespace string

	skippedTemplates    map[string]bool
	skippedImagestreams map[string]bool

	imagestreamFile map[string]string
	templateFile    map[string]string

	deleteInProgress bool

	mutex *sync.Mutex
}

func (h *Handler) prepWatchEvent(kind, name string, annotations map[string]string) (*v1alpha1.SamplesResource, string, bool) {
	if h.deleteInProgress {
		return nil, "", false
	}
	srcfg, err := h.sdkwrapper.Get(v1alpha1.SamplesResourceName)
	if srcfg == nil || err != nil {
		logrus.Printf("Received watch event %s but ignoring since not have the SamplesResource yet: %#v %#v", kind+"/"+name, err, srcfg)
		return nil, "", false
	}
	if annotations != nil {
		isv, ok := annotations[v1alpha1.SamplesVersionAnnotation]
		if ok && isv == v1alpha1.CodeLevel {
			logrus.Debugf("ignoring %s/%s cause operator version matches", kind, name)
			// but return srcfg to potentially toggle pending condition
			return srcfg, "", false
		}
	}
	proceed, err := h.ProcessManagementField(srcfg)
	if !proceed || err != nil {
		return nil, "", false
	}

	cm, err := h.configmapclientwrapper.Get(h.namespace, v1alpha1.SamplesResourceName)
	if err != nil {
		logrus.Warningf("Problem accessing config map during image stream watch event processing: %#v", err)
		return nil, "", false
	}
	filePath, ok := cm.Data[kind+"-"+name]
	if !ok {
		logrus.Printf("watch event %s not part of operators inventory", name)
		return nil, "", false
	}

	return srcfg, filePath, true
}

func (h *Handler) processImageStreamWatchEvent(is *imagev1.ImageStream) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	srcfg, filePath, ok := h.prepWatchEvent("imagestream", is.Name, is.Annotations)
	if !ok {
		if srcfg != nil {
			logrus.Debugf("checking tag spec/status for %s spec len %d status len %d", is.Name, len(is.Spec.Tags), len(is.Status.Tags))
			// won't be updating imagestream this go around, which sets pending==true, so see if we should turn off pending
			pending := false
			if len(is.Spec.Tags) == len(is.Status.Tags) {
				for _, specTag := range is.Spec.Tags {
					matched := false
					for _, statusTag := range is.Status.Tags {
						logrus.Debugf("checking spec tag %s against status tag %s with num items %d", specTag.Name, statusTag.Tag, len(statusTag.Items))
						if specTag.Name == statusTag.Tag {
							for _, event := range statusTag.Items {
								logrus.Debugf("checking status tag %d against spec tag %#v", event.Generation, specTag.Generation)
								if specTag.Generation != nil &&
									*specTag.Generation <= event.Generation {
									logrus.Debugf("got match")
									matched = true
									break
								}
							}
						}

					}
					if !matched {
						pending = true
						break
					}
				}
			}

			logrus.Debugf("pending is %v for %s", pending, is.Name)

			if !pending {
				processing := srcfg.Condition(v1alpha1.ImageChangesInProgress)
				now := kapis.Now()
				// remove this imagestream name, including the space separator
				logrus.Debugf("current reason %s ", processing.Reason)
				processing.Reason = strings.Replace(processing.Reason, is.Name+" ", "", -1)
				logrus.Debugf("reason now %s", processing.Reason)
				if len(strings.TrimSpace(processing.Reason)) == 0 {
					logrus.Debugf(" reason empty setting to false")
					processing.Status = corev1.ConditionFalse
					processing.Reason = ""
				}
				processing.LastTransitionTime = now
				processing.LastUpdateTime = now
				srcfg.ConditionUpdate(processing)
				h.sdkwrapper.Update(srcfg)
				return nil
			}
			// return error here so we retry the imagestream event
			return fmt.Errorf("imagestream %s still in progress", is.Name)
		}

		return nil
	}

	imagestream, err := h.fileimagegetter.Get(filePath)
	if err != nil {
		h.processError(srcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", filePath)
		// if we get this, don't bother retrying
		err = nil
	} else {
		err = h.upsertImageStream(imagestream, is, srcfg)
	}
	h.sdkwrapper.Update(srcfg)
	return err

}

func (h *Handler) processTemplateWatchEvent(t *templatev1.Template) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	srcfg, filePath, ok := h.prepWatchEvent("template", t.Name, t.Annotations)
	if !ok {
		return nil
	}

	template, err := h.filetemplategetter.Get(filePath)
	if err != nil {
		h.processError(srcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", filePath)
		// if we get this, don't bother retrying
		err = nil
	} else {
		err = h.upsertTemplate(template, t, srcfg)
	}
	h.sdkwrapper.Update(srcfg)
	return err

}

func (h *Handler) VariableConfigChanged(srcfg *v1alpha1.SamplesResource, cm *corev1.ConfigMap) (bool, error) {
	samplesRegistry, ok := cm.Data[regkey]
	if !ok {
		err := fmt.Errorf("could not find the registry setting in the config map %#v", cm.Data)
		return false, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "%v")
	}
	if srcfg.Spec.SamplesRegistry != samplesRegistry {
		logrus.Printf("SamplesRegistry changed from %s, processing %v", samplesRegistry, srcfg)
		return true, nil
	}

	skippedStreams, ok := cm.Data[skippedstreamskey]
	if !ok {
		err := fmt.Errorf("could not find the skipped stream setting in the config map %#v", cm.Data)
		return false, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "%v")
	}
	streams := []string{}
	// Split will return an item of len 1 if skippedStreams is empty;
	// verified via testing and godoc of method
	if len(skippedStreams) > 0 {
		// gotta trim any remaining space to get array size comparison correct
		// a string of say 'jenkins ' will create an array of size 2
		skippedStreams = strings.TrimSpace(skippedStreams)
		streams = strings.Split(skippedStreams, " ")
	}

	if len(streams) != len(srcfg.Spec.SkippedImagestreams) {
		logrus.Printf("SkippedImageStreams number of entries changed from %s, processing %v", skippedStreams, srcfg)
		return true, nil
	}

	streamsMap := make(map[string]bool)
	for _, stream := range streams {
		streamsMap[stream] = true
	}
	for _, stream := range srcfg.Spec.SkippedImagestreams {
		if _, ok := streamsMap[stream]; !ok {
			logrus.Printf("SkippedImageStreams list of entries changed from %s, processing %v", skippedStreams, srcfg)
			return true, nil
		}
	}

	skippedTemps, ok := cm.Data[skippedtempskey]
	if !ok {
		err := fmt.Errorf("could not find the skipped template setting in the config map %#v", cm.Data)
		return false, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "%v")
	}
	temps := []string{}
	// Split will return an item of len 1 if skippedStreams is empty;
	// verified via testing and godoc of method
	if len(skippedTemps) > 0 {
		// gotta trim any remaining space to get array size comparison correct
		// a string of say 'jenkins ' will create an array of size 2
		skippedTemps = strings.TrimSpace(skippedTemps)
		temps = strings.Split(skippedTemps, " ")
	}
	if len(temps) != len(srcfg.Spec.SkippedTemplates) {
		logrus.Printf("SkippedTemplates number of entries changed from %s, processing %v", skippedTemps, srcfg)
		return true, nil
	}

	tempsMap := make(map[string]bool)
	for _, temp := range temps {
		tempsMap[temp] = true
	}
	for _, temp := range srcfg.Spec.SkippedTemplates {
		if _, ok := tempsMap[temp]; !ok {
			logrus.Printf("SkippedTemplates list of entries changed from %s, processing %v", skippedTemps, srcfg)
			return true, nil
		}
	}

	logrus.Printf("Incoming samplesresource unchanged from last processed version")
	return false, nil
}

func (h *Handler) StoreCurrentValidConfig(srcfg *v1alpha1.SamplesResource) error {

	cm, err := h.configmapclientwrapper.Get(h.namespace, v1alpha1.SamplesResourceName)
	if err != nil && !kerrors.IsNotFound(err) {
		// just return error to sdk for retry
		return err
	}
	if kerrors.IsNotFound(err) {
		err = fmt.Errorf("Operator in compromised state; Could not find config map even though samplesresource exists")
		h.processError(srcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "%v")
		h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "%v")
		return err
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}

	if len(srcfg.Spec.InstallType) == 0 {
		cm.Data[installtypekey] = string(v1alpha1.CentosSamplesDistribution)
	} else {
		cm.Data[installtypekey] = string(srcfg.Spec.InstallType)
	}
	if len(srcfg.Spec.Architectures) == 0 {
		cm.Data[v1alpha1.X86Architecture] = v1alpha1.X86Architecture
	} else {
		for _, arch := range srcfg.Spec.Architectures {
			switch arch {
			case v1alpha1.X86Architecture:
				cm.Data[v1alpha1.X86Architecture] = v1alpha1.X86Architecture
			case v1alpha1.PPCArchitecture:
				cm.Data[v1alpha1.PPCArchitecture] = v1alpha1.PPCArchitecture
			}
		}
	}

	cm.Data[regkey] = srcfg.Spec.SamplesRegistry
	var value string
	for _, val := range srcfg.Spec.SkippedImagestreams {
		value = value + val + " "
	}
	cm.Data[skippedstreamskey] = value

	value = ""
	for _, val := range srcfg.Spec.SkippedTemplates {
		value = value + val + " "
	}
	cm.Data[skippedtempskey] = value

	for key, path := range h.imagestreamFile {
		cm.Data["imagestream-"+key] = path
	}
	for key, path := range h.templateFile {
		cm.Data["template-"+key] = path
	}

	_, err = h.configmapclientwrapper.Update(h.namespace, cm)
	return err
}

func (h *Handler) SpecValidation(srcfg *v1alpha1.SamplesResource) (*corev1.ConfigMap, error) {
	// the first thing this should do is check that all the config values
	// are "valid" (the architecture name is known, the distribution name is known, etc)
	// if that fails, we should immediately error out and set ConfigValid to false.
	for _, arch := range srcfg.Spec.Architectures {
		switch arch {
		case v1alpha1.X86Architecture:
		case v1alpha1.PPCArchitecture:
			if srcfg.Spec.InstallType == v1alpha1.CentosSamplesDistribution {
				err := fmt.Errorf("do not support centos distribution on ppc64l3")
				return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
			}
		default:
			err := fmt.Errorf("architecture %s unsupported; only support %s and %s", arch, v1alpha1.X86Architecture, v1alpha1.PPCArchitecture)
			return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
		}
	}

	switch srcfg.Spec.InstallType {
	case v1alpha1.RHELSamplesDistribution:
	case v1alpha1.CentosSamplesDistribution:
	default:
		err := fmt.Errorf("invalid install type %s specified, should be rhel or centos", string(srcfg.Spec.InstallType))
		return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
	}

	// only if the values being requested are valid, should we then proceed to check
	// them against the previous values(if we've stored any previous values)

	// if we have not had a valid SamplesResource processed, allow caller to try with
	// the srcfg contents
	if !srcfg.ConditionTrue(v1alpha1.SamplesExist) {
		logrus.Println("Spec is valid because this operator has not processed a config yet")
		return nil, nil
	}
	cm, err := h.configmapclientwrapper.Get(h.namespace, v1alpha1.SamplesResourceName)
	if err != nil && !kerrors.IsNotFound(err) {
		err = fmt.Errorf("error retrieving previous configuration: %v", err)
		return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "%v")
	}
	if kerrors.IsNotFound(err) {
		err = fmt.Errorf("ConfigMap %s does not exist, but it should, so cannot validate config change", v1alpha1.SamplesResourceName)
		return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "%v")
	}

	installtype, ok := cm.Data[installtypekey]
	if !ok {
		err = fmt.Errorf("could not find the installtype in the config map %#v", cm.Data)
		return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "%v")
	}
	switch srcfg.Spec.InstallType {
	case "", v1alpha1.CentosSamplesDistribution:
		if installtype != string(v1alpha1.CentosSamplesDistribution) {
			err = fmt.Errorf("cannot change installtype from %s to %s", installtype, srcfg.Spec.InstallType)
			return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
		}
	case v1alpha1.RHELSamplesDistribution:
		if installtype != string(v1alpha1.RHELSamplesDistribution) {
			err = fmt.Errorf("cannot change installtype from %s to %s", installtype, srcfg.Spec.InstallType)
			return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
		}
	default:
		// above value check catches this
	}

	_, hasx86 := cm.Data[v1alpha1.X86Architecture]
	_, hasppc := cm.Data[v1alpha1.PPCArchitecture]

	wantsx86 := false
	wantsppc := false
	if len(srcfg.Spec.Architectures) == 0 {
		wantsx86 = true
	}
	for _, arch := range srcfg.Spec.Architectures {
		switch arch {
		case v1alpha1.X86Architecture:
			wantsx86 = true
		case v1alpha1.PPCArchitecture:
			wantsppc = true
		default:
			// above value check catches this
		}
	}

	if wantsx86 != hasx86 ||
		wantsppc != hasppc {
		err = fmt.Errorf("cannot change architectures from %#v to %#v", cm.Data, srcfg.Spec.Architectures)
		return nil, h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
	}
	h.GoodConditionUpdate(srcfg, corev1.ConditionTrue, v1alpha1.ConfigurationValid)
	return cm, nil
}

func (h *Handler) AddFinalizer(srcfg *v1alpha1.SamplesResource) {
	hasFinalizer := false
	for _, f := range srcfg.Finalizers {
		if f == v1alpha1.SamplesResourceFinalizer {
			hasFinalizer = true
			break
		}
	}
	if !hasFinalizer {
		srcfg.Finalizers = append(srcfg.Finalizers, v1alpha1.SamplesResourceFinalizer)
	}
}

func (h *Handler) RemoveFinalizer(srcfg *v1alpha1.SamplesResource) {
	newFinalizers := []string{}
	for _, f := range srcfg.Finalizers {
		if f == v1alpha1.SamplesResourceFinalizer {
			continue
		}
		newFinalizers = append(newFinalizers, f)
	}
	srcfg.Finalizers = newFinalizers
}

func (h *Handler) NeedsFinalizing(srcfg *v1alpha1.SamplesResource) bool {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if h.deleteInProgress {
		return false
	}
	h.deleteInProgress = true
	for _, f := range srcfg.Finalizers {
		if f == v1alpha1.SamplesResourceFinalizer {
			return true
		}
	}

	return false
}

func (h *Handler) GoodConditionUpdate(srcfg *v1alpha1.SamplesResource, newStatus corev1.ConditionStatus, conditionType v1alpha1.SamplesResourceConditionType) {
	logrus.Debugf("updating condition %s to %s", conditionType, newStatus)
	condition := srcfg.Condition(conditionType)
	// decision was made to not spam master if
	// duplicate events come it (i.e. status does not
	// change)
	if condition.Status != newStatus {
		now := kapis.Now()
		condition.LastUpdateTime = now
		condition.Status = newStatus
		condition.LastTransitionTime = now
		condition.Message = ""
		srcfg.ConditionUpdate(condition)

		logrus.Println("")
		logrus.Println("")
		logrus.Println("")
		logrus.Println("")
	}
}

// copied from k8s.io/kubernetes/test/utils/
func (h *Handler) IsRetryableAPIError(err error) bool {
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

func (h *Handler) CreateDefaultResourceIfNeeded() (*v1alpha1.SamplesResource, error) {
	// coordinate with event handler processing
	// where it will set h.sampleResource
	// when it completely updates all imagestreams/templates/statuses
	h.mutex.Lock()
	defer h.mutex.Unlock()

	deleteInProgress := h.deleteInProgress

	var err error
	if deleteInProgress {
		srcfg := &v1alpha1.SamplesResource{}
		srcfg.Name = v1alpha1.SamplesResourceName
		srcfg.Kind = "SamplesResource"
		srcfg.APIVersion = v1alpha1.GroupName + "/" + v1alpha1.Version
		err = wait.PollImmediate(3*time.Second, 30*time.Second, func() (bool, error) {
			srcfg, e := h.sdkwrapper.Get(v1alpha1.SamplesResourceName)
			if kerrors.IsNotFound(e) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			// based on 4.0 testing, we've been seeing empty resources returned
			// in the not found case, but just in case ...
			if srcfg == nil {
				return true, nil
			}
			// means still found ... will return wait.ErrWaitTimeout if this continues
			return false, nil
		})
		if err != nil {
			return nil, h.processError(srcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "issues waiting for delete to complete: %v")
		}
	}

	srcfg, err := h.sdkwrapper.Get(v1alpha1.SamplesResourceName)
	if srcfg == nil || kerrors.IsNotFound(err) {
		h.CreateConfigMapIfNeeded()
		// "4a" in the "startup" workflow, just create default
		// resource and set up that way
		srcfg = &v1alpha1.SamplesResource{}
		srcfg.Name = v1alpha1.SamplesResourceName
		srcfg.Kind = "SamplesResource"
		srcfg.APIVersion = v1alpha1.GroupName + "/" + v1alpha1.Version
		srcfg.Spec.Architectures = append(srcfg.Spec.Architectures, v1alpha1.X86Architecture)
		srcfg.Spec.InstallType = v1alpha1.CentosSamplesDistribution
		now := kapis.Now()
		exist := srcfg.Condition(v1alpha1.SamplesExist)
		exist.Status = corev1.ConditionFalse
		exist.LastUpdateTime = now
		exist.LastTransitionTime = now
		srcfg.ConditionUpdate(exist)
		cred := srcfg.Condition(v1alpha1.ImportCredentialsExist)
		cred.Status = corev1.ConditionFalse
		cred.LastUpdateTime = now
		cred.LastTransitionTime = now
		srcfg.ConditionUpdate(cred)
		valid := srcfg.Condition(v1alpha1.ConfigurationValid)
		valid.Status = corev1.ConditionTrue
		valid.LastUpdateTime = now
		valid.LastTransitionTime = now
		srcfg.ConditionUpdate(valid)
		logrus.Println("creating default SamplesResource")
		err = h.sdkwrapper.Create(srcfg)
		if err != nil {
			if !kerrors.IsAlreadyExists(err) {
				return nil, err
			}
			// in case there is some race condition
			logrus.Println("got already exists error on create default")
		}

	} else {
		logrus.Printf("SamplesResource %#v found during operator startup", srcfg)
	}

	h.deleteInProgress = false
	return srcfg, nil
}

func (h *Handler) CreateConfigMapIfNeeded() error {
	cm, err := h.configmapclientwrapper.Get(h.namespace, v1alpha1.SamplesResourceName)
	if cm == nil || kerrors.IsNotFound(err) {
		logrus.Println("creating config map")
		cm = &corev1.ConfigMap{}
		cm.Name = v1alpha1.SamplesResourceName
		cm.Data = map[string]string{}
		_, err = h.configmapclientwrapper.Create(h.namespace, cm)
		if err != nil {
			if !kerrors.IsAlreadyExists(err) {
				return err
			}
			// in case there is some race condition
			logrus.Println("got already exists error on create config map")
			return nil
		}
	} else {
		if err == nil {
			logrus.Printf("ConfigMap for SamplesResource %#v found during operator startup", cm)
		} else {
			logrus.Printf("Could not ascertain presence of ConfigMap: %v", err)
		}
	}
	return err
}

func (h *Handler) WaitingForCredential(srcfg *v1alpha1.SamplesResource) error {
	if srcfg.ConditionTrue(v1alpha1.ImportCredentialsExist) {
		return nil
	}

	// if trying to do rhel to the default registry.redhat.io registry requires the secret
	// be in place since registry.redhat.io requires auth to pull; since it is not ready
	// log error state
	if srcfg.Spec.InstallType == v1alpha1.RHELSamplesDistribution &&
		(srcfg.Spec.SamplesRegistry == "" || srcfg.Spec.SamplesRegistry == "registry.redhat.io") {
		// currently do not attempt to avoid multiple settings of this conditions/message prior to an event
		// where the situation is addressed
		err := fmt.Errorf("Cannot create rhel imagestreams to registry.redhat.io without the credentials being available: %#v", srcfg)
		h.processError(srcfg, v1alpha1.ImportCredentialsExist, corev1.ConditionFalse, err, "%v")
		return err
	}
	return nil
}

func (h *Handler) manageDockerCfgSecret(deleted bool, samplesResource *v1alpha1.SamplesResource, s *corev1.Secret) error {
	secret := s
	var err error
	if secret == nil {
		secret, err = h.secretclientwrapper.Get(h.namespace, v1alpha1.SamplesRegistryCredentials)
		if err != nil && kerrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if secret.Name != v1alpha1.SamplesRegistryCredentials {
		return nil
	}

	var newStatus corev1.ConditionStatus
	if deleted {
		err := h.secretclientwrapper.Delete("openshift", secret.Name, &metav1.DeleteOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return h.processError(samplesResource, v1alpha1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to delete before create dockerconfig secret the openshift namespace: %v")
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
		secretToCreate.Annotations[v1alpha1.SamplesVersionAnnotation] = v1alpha1.CodeLevel

		s, err := h.secretclientwrapper.Get("openshift", secret.Name)
		if err != nil && !kerrors.IsNotFound(err) {
			return h.processError(samplesResource, v1alpha1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to get registry dockerconfig secret in openshift namespace : %v")
		}
		if err != nil {
			s = nil
		}
		if s != nil {
			logrus.Printf("updating dockerconfig secret %s in openshift namespace", v1alpha1.SamplesRegistryCredentials)
			_, err = h.secretclientwrapper.Update("openshift", &secretToCreate)
		} else {
			logrus.Printf("creating dockerconfig secret %s in openshift namespace", v1alpha1.SamplesRegistryCredentials)
			_, err = h.secretclientwrapper.Create("openshift", &secretToCreate)
		}
		if err != nil {
			return h.processError(samplesResource, v1alpha1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to create/update registry dockerconfig secret in openshif namespace : %v")
		}
		newStatus = corev1.ConditionTrue
	}

	h.GoodConditionUpdate(samplesResource, newStatus, v1alpha1.ImportCredentialsExist)

	return nil
}

func (h *Handler) CleanUpOpenshiftNamespaceOnDelete(srcfg *v1alpha1.SamplesResource) error {
	h.buildSkipFilters(srcfg)

	iopts := metav1.ListOptions{LabelSelector: v1alpha1.SamplesManagedLabel + "=true"}

	streamList, err := h.imageclientwrapper.List("openshift", iopts)
	if err != nil {
		logrus.Warnf("Problem listing openshift imagestreams on SamplesResource delete: %#v", err)
	} else {
		for _, stream := range streamList.Items {
			if _, ok := h.skippedImagestreams[stream.Name]; ok {
				continue
			}
			err = h.imageclientwrapper.Delete("openshift", stream.Name, &metav1.DeleteOptions{})
			if err != nil && !kerrors.IsNotFound(err) {
				logrus.Warnf("Problem deleteing openshift imagestream %s on SamplesResource delete: %#v", stream.Name, err)
			}
		}
	}

	tempList, err := h.templateclientwrapper.List("openshift", iopts)
	if err != nil {
		logrus.Warnf("Problem listing openshift imagestreams on SamplesResource delete: %#v", err)
	} else {
		for _, temp := range tempList.Items {
			if _, ok := h.skippedTemplates[temp.Name]; ok {
				continue
			}
			err = h.templateclientwrapper.Delete("openshift", temp.Name, &metav1.DeleteOptions{})
			if err != nil && !kerrors.IsNotFound(err) {
				logrus.Warnf("Problem deleting openshift template %s on SamplesResource delete: %#v", temp.Name, err)
			}
		}
	}

	err = h.secretclientwrapper.Delete("openshift", v1alpha1.SamplesRegistryCredentials, &metav1.DeleteOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		logrus.Warnf("Problem deleting openshift secret %s on SamplesResource delete: %#v", v1alpha1.SamplesRegistryCredentials, err)
	}

	err = h.configmapclientwrapper.Delete(h.namespace, v1alpha1.SamplesResourceName)
	if err != nil && !kerrors.IsNotFound(err) {
		logrus.Warnf("Problem deleting openshift configmap %s on SamplesResource delete: %#v", v1alpha1.SamplesResourceName, err)
	}
	return nil
}

// ProcessManagementField returns true if the operator should handle the SampleResource event
// and false if it should not, as well as an err in case we want to bubble that up to
// the controller level logic for retry
func (h *Handler) ProcessManagementField(srcfg *v1alpha1.SamplesResource) (bool, error) {
	switch srcfg.Spec.ManagementState {
	case operatorsv1alpha1api.Removed:
		// if samples exists already is false, that means the samples related artifacts are gone,
		// and we can ignore, unless this is the rhel scenario where we have not installed the
		// rhel samples because the secret is missing, but then they manage to add the secret AND
		// mark this resource as Removed before we manage to install the samples ... if so, let's
		// at least delete the secret ... won't distinguish between centos and rhel though in case
		// they have also overriden the registry to pull from, and have loaded the centos content there
		if srcfg.ConditionFalse(v1alpha1.SamplesExist) &&
			srcfg.ConditionFalse(v1alpha1.ImportCredentialsExist) {
			logrus.Debugf("ignoring subequent samplesresource with managed==removed after remove is processed")
			return false, nil
		}

		logrus.Println("management state set to removed so deleting samples and configmap")
		err := h.CleanUpOpenshiftNamespaceOnDelete(srcfg)
		if err != nil {
			return false, h.processError(srcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "The error %v during openshift namespace cleanup has left the samples in an unknown state")
		}
		// explicitly reset samples exist to false since the samplesresource has not
		// actually been deleted
		now := kapis.Now()
		condition := srcfg.Condition(v1alpha1.SamplesExist)
		condition.LastTransitionTime = now
		condition.LastUpdateTime = now
		condition.Status = corev1.ConditionFalse
		srcfg.ConditionUpdate(condition)
		return false, nil
	case operatorsv1alpha1api.Managed:
		// in case configmap was removed via dealing or removed mgmt state
		// ensure config map is created
		if !srcfg.ConditionTrue(v1alpha1.SamplesExist) {
			logrus.Println("management state set to managed")
			h.CreateConfigMapIfNeeded()
		}
		return true, nil
	case operatorsv1alpha1api.Unmanaged:
		logrus.Println("management state set to unmanaged")
		return false, nil
	default:
		return true, nil
	}
}

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	switch event.Object.(type) {
	case *imagev1.ImageStream:
		is, _ := event.Object.(*imagev1.ImageStream)
		if is.Namespace != "openshift" {
			return nil
		}
		err := h.processImageStreamWatchEvent(is)
		return err

	case *templatev1.Template:
		t, _ := event.Object.(*templatev1.Template)
		if t.Namespace != "openshift" {
			return nil
		}
		err := h.processTemplateWatchEvent(t)
		return err

	case *corev1.Secret:
		dockercfgSecret, _ := event.Object.(*corev1.Secret)
		if dockercfgSecret.Name != v1alpha1.SamplesRegistryCredentials {
			return nil
		}
		h.mutex.Lock()
		defer h.mutex.Unlock()

		srcfg, _ := h.sdkwrapper.Get(v1alpha1.SamplesResourceName)
		if srcfg != nil {
			doit, err := h.ProcessManagementField(srcfg)
			if !doit || err != nil {
				return err
			}
			err = h.manageDockerCfgSecret(event.Deleted, srcfg, dockercfgSecret)
			if err != nil {
				return err
			}
			// flush the status changes generated by the processing
			return h.sdkwrapper.Update(srcfg)
		} else {
			return fmt.Errorf("Received secret %s but do not have the SamplesResource yet, requeuing", dockercfgSecret.Name)
		}

	case *v1alpha1.SamplesResource:
		newStatus := corev1.ConditionTrue
		srcfg, _ := event.Object.(*v1alpha1.SamplesResource)

		if srcfg.Name != v1alpha1.SamplesResourceName || srcfg.Namespace != "" {
			return nil
		}

		// Every time we see a change to the SamplesResource object, update the ClusterOperator status
		// based on the current conditions of the SamplesResource.
		err := h.cvowrapper.UpdateOperatorStatus(srcfg)
		if err != nil {
			logrus.Errorf("error updating cluster operator status: %v", err)
			return err
		}

		// pattern is 1) come in with delete timestamp, event delete flag false
		// 2) then after we remove finalizer, comes in with delete timestamp
		// and event delete flag true
		if event.Deleted {
			// possibly tell poller to stop
			logrus.Info("A previous delete attempt has been successfully completed")
			return nil
		}
		if srcfg.DeletionTimestamp != nil {
			if h.NeedsFinalizing(srcfg) {
				logrus.Println("Initiating finalizer processing for a SampleResource delete attempt")
				h.RemoveFinalizer(srcfg)
				h.CleanUpOpenshiftNamespaceOnDelete(srcfg)
				h.GoodConditionUpdate(srcfg, corev1.ConditionFalse, v1alpha1.SamplesExist)
				err := h.sdkwrapper.Update(srcfg)
				go func() {
					h.CreateDefaultResourceIfNeeded()
				}()
				return err
			}
			return nil
		}

		doit, err := h.ProcessManagementField(srcfg)
		if !doit || err != nil {
			// flush status update
			h.sdkwrapper.Update(srcfg)
			return err
		}

		// coordinate with timer's check on creating
		// default resource ... looks at h.sampleResource,
		// which is not set until this whole case is completed
		h.mutex.Lock()
		defer h.mutex.Unlock()

		cm, err := h.SpecValidation(srcfg)
		if err != nil {
			// flush status update
			h.sdkwrapper.Update(srcfg)
			return err
		}

		if cm != nil {
			changed, err := h.VariableConfigChanged(srcfg, cm)
			if err != nil {
				// flush status update
				h.sdkwrapper.Update(srcfg)
				return err
			}
			if !changed {
				return nil
			}
		}

		// if trying to do rhel to the default registry.redhat.io registry requires the secret
		// be in place since registry.redhat.io requires auth to pull; if it is not ready
		// error state will be logged by WaitingForCredential, so return error and requeue
		err = h.WaitingForCredential(srcfg)
		if err != nil {
			// flush status update
			h.sdkwrapper.Update(srcfg)
			// abort processing, but not returning error to initiate requeue
			return nil
		}

		h.buildSkipFilters(srcfg)

		if len(srcfg.Spec.Architectures) == 0 {
			srcfg.Spec.Architectures = append(srcfg.Spec.Architectures, v1alpha1.X86Architecture)
		}

		if len(srcfg.Spec.InstallType) == 0 {
			srcfg.Spec.InstallType = v1alpha1.CentosSamplesDistribution
		}

		for _, arch := range srcfg.Spec.Architectures {
			dir := h.GetBaseDir(arch, srcfg)
			files, err := h.filefinder.List(dir)
			if err != nil {
				err = h.processError(srcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "error reading in content : %v")
				h.sdkwrapper.Update(srcfg)
				return err
			}
			err = h.processFiles(dir, files, srcfg)
			if err != nil {
				err = h.processError(srcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "error processing content : %v")
				h.sdkwrapper.Update(srcfg)
				return err
			}

		}

		h.AddFinalizer(srcfg)

		if !h.deleteInProgress {
			err = h.StoreCurrentValidConfig(srcfg)
			if err != nil {
				err = h.processError(srcfg, v1alpha1.ConfigurationValid, corev1.ConditionUnknown, err, "error %v updating configmap, subsequent config validations untenable")
				return err
			}
		}

		// this also atomically updates the ConfigValid condition to True
		h.GoodConditionUpdate(srcfg, newStatus, v1alpha1.SamplesExist)
		// flush updates from processing
		return h.sdkwrapper.Update(srcfg)
	}
	return nil
}

func (h *Handler) buildSkipFilters(opcfg *v1alpha1.SamplesResource) {
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

func (h *Handler) processError(opcfg *v1alpha1.SamplesResource, ctype v1alpha1.SamplesResourceConditionType, cstatus corev1.ConditionStatus, err error, msg string, args ...interface{}) error {
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

func (h *Handler) upsertImageStream(imagestreamInOperatorImage, imagestreamInCluster *imagev1.ImageStream, opcfg *v1alpha1.SamplesResource) error {
	if _, isok := h.skippedImagestreams[imagestreamInOperatorImage.Name]; isok {
		if imagestreamInCluster != nil {
			if imagestreamInCluster.Labels == nil {
				imagestreamInCluster.Labels = make(map[string]string)
			}
			imagestreamInCluster.Labels[v1alpha1.SamplesManagedLabel] = "false"
			h.imageclientwrapper.Update("openshift", imagestreamInCluster)
			// if we get an error, we'll just try to remove the label next
			// time; and we'll examine the skipped lists on delete
		}
		return nil
	}

	progress := opcfg.Condition(v1alpha1.ImageChangesInProgress)
	progress.Status = corev1.ConditionTrue
	now := kapis.Now()
	progress.LastUpdateTime = now
	progress.LastTransitionTime = now
	if !strings.Contains(progress.Reason, imagestreamInOperatorImage.Name) {
		progress.Reason = progress.Reason + imagestreamInOperatorImage.Name + " "
	}
	opcfg.ConditionUpdate(progress)

	h.updateDockerPullSpec([]string{"docker.io", "registry.redhat.io", "registry.access.redhat.com", "quay.io"}, imagestreamInOperatorImage, opcfg)

	if imagestreamInOperatorImage.Labels == nil {
		imagestreamInOperatorImage.Labels = make(map[string]string)
	}
	if imagestreamInOperatorImage.Annotations == nil {
		imagestreamInOperatorImage.Annotations = make(map[string]string)
	}
	imagestreamInOperatorImage.Labels[v1alpha1.SamplesManagedLabel] = "true"
	imagestreamInOperatorImage.Annotations[v1alpha1.SamplesVersionAnnotation] = v1alpha1.CodeLevel

	if imagestreamInCluster == nil {
		_, err := h.imageclientwrapper.Create("openshift", imagestreamInOperatorImage)
		if err != nil {
			return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "imagestream create error: %v")
		}
		logrus.Printf("created imagestream %s", imagestreamInOperatorImage.Name)
		return nil
	}

	imagestreamInOperatorImage.ResourceVersion = imagestreamInCluster.ResourceVersion
	_, err := h.imageclientwrapper.Update("openshift", imagestreamInOperatorImage)
	if err != nil {
		return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "imagestream update error: %v")
	}
	logrus.Printf("updated imagestream %s", imagestreamInCluster.Name)
	return nil
}

func (h *Handler) upsertTemplate(templateInOperatorImage, templateInCluster *templatev1.Template, opcfg *v1alpha1.SamplesResource) error {
	if _, tok := h.skippedTemplates[templateInOperatorImage.Name]; tok {
		if templateInCluster != nil {
			if templateInCluster.Labels == nil {
				templateInCluster.Labels = make(map[string]string)
			}
			templateInCluster.Labels[v1alpha1.SamplesManagedLabel] = "false"
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
	templateInOperatorImage.Labels[v1alpha1.SamplesManagedLabel] = "true"
	templateInOperatorImage.Annotations[v1alpha1.SamplesVersionAnnotation] = v1alpha1.CodeLevel

	if templateInCluster == nil {
		_, err := h.templateclientwrapper.Create("openshift", templateInOperatorImage)
		if err != nil {
			return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "template create error: %v")
		}
		logrus.Printf("created template %s", templateInOperatorImage.Name)
		return nil
	}

	templateInOperatorImage.ResourceVersion = templateInCluster.ResourceVersion
	_, err := h.templateclientwrapper.Update("openshift", templateInOperatorImage)
	if err != nil {
		return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "template update error: %v")
	}
	logrus.Printf("updated template %s", templateInCluster.Name)
	return nil
}

func (h *Handler) processFiles(dir string, files []os.FileInfo, opcfg *v1alpha1.SamplesResource) error {
	for _, file := range files {
		if file.IsDir() {
			logrus.Printf("processing subdir %s from dir %s", file.Name(), dir)
			subfiles, err := h.filefinder.List(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "error reading in content: %v")
			}
			err = h.processFiles(dir+"/"+file.Name(), subfiles, opcfg)
			if err != nil {
				return err
			}
		}
		logrus.Printf("processing file %s from dir %s", file.Name(), dir)

		if strings.HasSuffix(dir, "imagestreams") {
			imagestream, err := h.fileimagegetter.Get(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", dir+"/"+file.Name())
			}

			h.imagestreamFile[imagestream.Name] = dir + "/" + file.Name()

			is, err := h.imageclientwrapper.Get("openshift", imagestream.Name, metav1.GetOptions{})
			if err != nil && !kerrors.IsNotFound(err) {
				return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "unexpected imagestream get error: %v")
			}

			if kerrors.IsNotFound(err) {
				// testing showed that we get an empty is vs. nil in this case
				is = nil
			}

			err = h.upsertImageStream(imagestream, is, opcfg)
			if err != nil {
				return err
			}
		}

		if strings.HasSuffix(dir, "templates") {
			template, err := h.filetemplategetter.Get(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", dir+"/"+file.Name())
			}

			h.templateFile[template.Name] = dir + "/" + file.Name()

			t, err := h.templateclientwrapper.Get("openshift", template.Name, metav1.GetOptions{})
			if err != nil && !kerrors.IsNotFound(err) {
				return h.processError(opcfg, v1alpha1.SamplesExist, corev1.ConditionUnknown, err, "unexpected template get error: %v")
			}

			if kerrors.IsNotFound(err) {
				// testing showed that we get an empty is vs. nil in this case
				t = nil
			}

			err = h.upsertTemplate(template, t, opcfg)
			if err != nil {
				return err
			}
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
	if hasRegistry {
		for _, old := range oldies {
			if strings.HasPrefix(oldreg, old) {
				oldreg = strings.Replace(oldreg, old, newreg, 1)
			} else {
				// the content from openshift/library has something odd in in ... replace the registry piece
				parts := strings.Split(oldreg, "/")
				oldreg = newreg + "/" + parts[1] + "/" + parts[2]
			}
		}
	} else {
		oldreg = newreg + "/" + oldreg
	}

	return oldreg
}

func (h *Handler) updateDockerPullSpec(oldies []string, imagestream *imagev1.ImageStream, opcfg *v1alpha1.SamplesResource) {
	if len(opcfg.Spec.SamplesRegistry) > 0 {
		if !strings.HasPrefix(imagestream.Spec.DockerImageRepository, opcfg.Spec.SamplesRegistry) {
			// if not one of our 4 defaults ...
			imagestream.Spec.DockerImageRepository = h.coreUpdateDockerPullSpec(imagestream.Spec.DockerImageRepository,
				opcfg.Spec.SamplesRegistry,
				oldies)
		}

		for _, tagref := range imagestream.Spec.Tags {
			if !strings.HasPrefix(tagref.From.Name, opcfg.Spec.SamplesRegistry) {
				tagref.From.Name = h.coreUpdateDockerPullSpec(tagref.From.Name,
					opcfg.Spec.SamplesRegistry,
					oldies)
			}
		}
	}

}

func (h *Handler) GetBaseDir(arch string, opcfg *v1alpha1.SamplesResource) (dir string) {
	// invalid settings have already been sorted out by SpecValidation
	switch arch {
	case v1alpha1.X86Architecture:
		switch opcfg.Spec.InstallType {
		case v1alpha1.RHELSamplesDistribution:
			dir = x86OCPContentRootDir
		case v1alpha1.CentosSamplesDistribution:
			dir = x86OKDContentRootDir
		default:
		}
	case v1alpha1.PPCArchitecture:
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

func getRestConfig() (*restclient.Config, error) {
	// Build a rest.Config from configuration injected into the Pod by
	// Kubernetes.  Clients will use the Pod's ServiceAccount principal.
	return restclient.InClusterConfig()
}

func getNamespace() string {
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

type ConfigMapClientWrapper interface {
	Get(namespace, name string) (*corev1.ConfigMap, error)
	Create(namespace string, s *corev1.ConfigMap) (*corev1.ConfigMap, error)
	Update(namespace string, s *corev1.ConfigMap) (*corev1.ConfigMap, error)
	Delete(namespace, name string) error
}

type defaultConfigMapClientWrapper struct {
	h *Handler
}

func (g *defaultConfigMapClientWrapper) Get(namespace, name string) (*corev1.ConfigMap, error) {
	return g.h.coreclient.ConfigMaps(namespace).Get(name, metav1.GetOptions{})
}

func (g *defaultConfigMapClientWrapper) Create(namespace string, s *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	return g.h.coreclient.ConfigMaps(namespace).Create(s)
}

func (g *defaultConfigMapClientWrapper) Update(namespace string, s *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	return g.h.coreclient.ConfigMaps(namespace).Update(s)
}

func (g *defaultConfigMapClientWrapper) Delete(namespace, name string) error {
	return g.h.coreclient.ConfigMaps(namespace).Delete(name, &metav1.DeleteOptions{})
}

type SecretClientWrapper interface {
	Get(namespace, name string) (*corev1.Secret, error)
	Create(namespace string, s *corev1.Secret) (*corev1.Secret, error)
	Update(namespace string, s *corev1.Secret) (*corev1.Secret, error)
	Delete(namespace, name string, opts *metav1.DeleteOptions) error
}

type defaultSecretClientWrapper struct {
	h *Handler
}

func (g *defaultSecretClientWrapper) Get(namespace, name string) (*corev1.Secret, error) {
	return g.h.coreclient.Secrets(namespace).Get(name, metav1.GetOptions{})
}

func (g *defaultSecretClientWrapper) Create(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	return g.h.coreclient.Secrets(namespace).Create(s)
}

func (g *defaultSecretClientWrapper) Update(namespace string, s *corev1.Secret) (*corev1.Secret, error) {
	return g.h.coreclient.Secrets(namespace).Update(s)
}

func (g *defaultSecretClientWrapper) Delete(namespace, name string, opts *metav1.DeleteOptions) error {
	return g.h.coreclient.Secrets(namespace).Delete(name, opts)
}

type ImageStreamFromFileGetter interface {
	Get(fullFilePath string) (is *imagev1.ImageStream, err error)
}

type defaultImageStreamFromFileGetter struct {
	h *Handler
}

func (g *defaultImageStreamFromFileGetter) Get(fullFilePath string) (is *imagev1.ImageStream, err error) {
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

type defaultTemplateFromFileGetter struct {
	h *Handler
}

func (g *defaultTemplateFromFileGetter) Get(fullFilePath string) (t *templatev1.Template, err error) {
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

type defaultResourceFileLister struct {
	h *Handler
}

func (g *defaultResourceFileLister) List(dir string) (files []os.FileInfo, err error) {
	files, err = ioutil.ReadDir(dir)
	return files, err

}

type InClusterInitter interface {
	init()
}

type defaultInClusterInitter struct {
	h *Handler
}

func (g *defaultInClusterInitter) init() {
	restconfig, err := getRestConfig()
	if err != nil {
		logrus.Errorf("failed to get rest config : %v", err)
		panic(err)
	}
	g.h.restconfig = restconfig
	tempclient, err := getTemplateClient(restconfig)
	if err != nil {
		logrus.Errorf("failed to get template client : %v", err)
		panic(err)
	}
	g.h.tempclient = tempclient
	logrus.Printf("template client %#v", tempclient)
	imageclient, err := getImageClient(restconfig)
	if err != nil {
		logrus.Errorf("failed to get image client : %v", err)
		panic(err)
	}
	g.h.imageclient = imageclient
	logrus.Printf("image client %#v", imageclient)
	coreclient, err := corev1client.NewForConfig(restconfig)
	if err != nil {
		logrus.Errorf("failed to get core client : %v", err)
		panic(err)
	}
	g.h.coreclient = coreclient
}

type SDKWrapper interface {
	Update(samplesResource *v1alpha1.SamplesResource) (err error)
	Create(samplesResource *v1alpha1.SamplesResource) (err error)
	Get(name string) (*v1alpha1.SamplesResource, error)
}

type defaultSDKWrapper struct {
	h *Handler
}

func (g *defaultSDKWrapper) Update(samplesResource *v1alpha1.SamplesResource) error {
	return wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		err := sdk.Update(samplesResource)
		if err == nil {
			return true, nil
		}
		if !g.h.IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})
}

func (g *defaultSDKWrapper) Create(samplesResource *v1alpha1.SamplesResource) error {
	return wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		err := sdk.Create(samplesResource)
		if err == nil {
			return true, nil
		}
		if !g.h.IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})
}

func (g *defaultSDKWrapper) Get(name string) (*v1alpha1.SamplesResource, error) {
	sr := v1alpha1.SamplesResource{}
	sr.Kind = "SamplesResource"
	sr.APIVersion = "samplesoperator.config.openshift.io/v1alpha1"
	sr.Name = name
	err := wait.Poll(3*time.Second, 30*time.Second, func() (bool, error) {
		err := sdk.Get(&sr, sdk.WithGetOptions(&metav1.GetOptions{}))
		if err == nil {
			return true, nil
		}
		if !g.h.IsRetryableAPIError(err) {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return &sr, nil
}
