package stub

import (
	"fmt"
	"io/ioutil"
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

// prepSamplesWatchEvent decides whether an upsert of the sample should be done, as well as data for either doing the upsert or checking the status of a prior upsert;
// the return values:
// - cfg: return this if we want to check the status of a prior upsert
// - filePath: used to the caller to look up the image content for the upsert
// - doUpsert: whether to do the upsert of not ... not doing the upsert optionally triggers the need for checking status of prior upsert
// - err: if a problem occurred getting the Config, we return the error to bubble up and initiate a retry
func (h *Handler) prepSamplesWatchEvent(kind, name string, annotations map[string]string, deleted bool) (*v1.Config, string, bool, error) {
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
	// Config event return error for retry
	if len(h.imagestreamFile) == 0 || len(h.templateFile) == 0 {
		return nil, "", false, fmt.Errorf("samples file locations not built yet when event for %s/%s came in", kind, name)
	}

	// make sure skip filter list is ready
	if len(cfg.Spec.SkippedImagestreams) != len(h.skippedImagestreams) ||
		len(cfg.Spec.SkippedTemplates) != len(h.skippedTemplates) {
		h.buildSkipFilters(cfg)
	}

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
		// but return cfg to potentially toggle pending/import error condition
		return cfg, "", false, nil
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

	if h.creationInProgress {
		return cfg, "", false, fmt.Errorf("samples creation in progress when event %s/%s came in", kind, name)
	}

	return cfg, filePath, true, nil
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
		cfg.Spec.InstallType = v1.RHELSamplesDistribution
		//TODO force use of registry.access.redhat.com until we sort out TBR/registry.redhat.io creds
		cfg.Spec.SamplesRegistry = "registry.access.redhat.com"
		cfg.Spec.ManagementState = operatorsv1api.Managed
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

func (h *Handler) initConditions(cfg *v1.Config) *v1.Config {
	now := kapis.Now()
	cfg.Condition(v1.SamplesExist)
	cfg.Condition(v1.ImportCredentialsExist)
	valid := cfg.Condition(v1.ConfigurationValid)
	// our default config is valid; since Condition sets new conditions to false
	// if we get false here this is the first pass through; invalid configs
	// are caught above
	if valid.Status != corev1.ConditionTrue {
		valid.Status = corev1.ConditionTrue
		valid.LastUpdateTime = now
		valid.LastTransitionTime = now
		cfg.ConditionUpdate(valid)
	}
	cfg.Condition(v1.ImageChangesInProgress)
	cfg.Condition(v1.RemovedManagementStateOnHold)
	cfg.Condition(v1.MigrationInProgress)
	cfg.Condition(v1.ImportImageErrorsExist)
	return cfg
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
			// if the secret event gets through while we are creating samples, it will
			// lead to a conflict when updating in progress to true in the initial create
			// loop, which can lead to an extra cycle of creates as we'll return an error there and retry;
			// so we check on local flag for creations in progress, and force a retry of the secret
			// event; similar to what we do in the imagestream/template watches
			if h.creationInProgress {
				return fmt.Errorf("retry secret event because in the middle of an sample upsert cycle")
			}

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
			logrus.Printf("CRDUPDATE event secret update")
			return h.crdwrapper.UpdateStatus(cfg)
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
				logrus.Printf("CRDUPDATE exist false update")
				err = h.crdwrapper.UpdateStatus(cfg)
				if err != nil {
					logrus.Printf("error on Config update after setting exists condition to false (returning error to retry): %v", err)
					return err
				}
			} else {
				logrus.Println("Initiating finalizer processing for a SampleResource delete attempt")
				h.RemoveFinalizer(cfg)
				logrus.Printf("CRDUPDATE remove finalizer update")
				// not updating the status, but the metadata annotation
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
				logrus.Printf("CRDUPDATE process mgmt update")
				h.crdwrapper.UpdateStatus(cfg)
			}
			return err
		}

		existingValidStatus := cfg.Condition(v1.ConfigurationValid).Status
		err = h.SpecValidation(cfg)
		if err != nil {
			// flush status update
			logrus.Printf("CRDUPDATE bad spec validation update")
			// only retry on error updating the Config; do not return
			// the error from SpecValidation which denotes a bad config
			return h.crdwrapper.UpdateStatus(cfg)
		}
		// if a bad config was corrected, update and return
		if existingValidStatus != cfg.Condition(v1.ConfigurationValid).Status {
			logrus.Printf("CRDUPDATE spec corrected")
			return h.crdwrapper.UpdateStatus(cfg)
		}

		h.buildSkipFilters(cfg)
		configChanged := false
		configChangeRequiresUpsert := false
		configChangeRequiresImportErrorUpdate := false
		if cfg.Spec.ManagementState == cfg.Status.ManagementState {
			configChanged, configChangeRequiresUpsert, configChangeRequiresImportErrorUpdate = h.VariableConfigChanged(cfg)
			logrus.Debugf("config changed %v upsert needed %v import error upd needed %v exists/true %v progressing/false %v op version %s status version %s",
				configChanged,
				configChangeRequiresUpsert,
				configChangeRequiresImportErrorUpdate,
				cfg.ConditionTrue(v1.SamplesExist),
				cfg.ConditionFalse(v1.ImageChangesInProgress),
				v1.GitVersionString(),
				cfg.Status.Version)
			// so ignore if config does not change and the samples exist and
			// we are not in progress and at the right level
			if !configChanged &&
				cfg.ConditionTrue(v1.SamplesExist) &&
				cfg.ConditionFalse(v1.ImageChangesInProgress) &&
				v1.GitVersionString() == cfg.Status.Version {
				logrus.Debugf("Handle ignoring because config the same and exists is true, in progress false, and version correct")
				return nil
			}
			// if config changed requiring an upsert, but a prior config action is still in progress,
			// reset in progress to false and return; the next event should drive the actual
			// processing of the config change and replace whatever was previously
			// in progress
			if configChangeRequiresUpsert &&
				cfg.ConditionTrue(v1.ImageChangesInProgress) {
				h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
				logrus.Printf("CRDUPDATE change in progress from true to false for config change")
				return h.crdwrapper.UpdateStatus(cfg)
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
			return h.crdwrapper.UpdateStatus(cfg)
		}
		if stillWaitingForSecret {
			// means we previously udpated cfg but nothing has changed wrt the secret's presence
			return nil
		}

		if cfg.ConditionFalse(v1.MigrationInProgress) &&
			len(cfg.Status.Version) > 0 &&
			v1.GitVersionString() != cfg.Status.Version {
			logrus.Printf("Undergoing migration from %s to %s", cfg.Status.Version, v1.GitVersionString())
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.MigrationInProgress)
			logrus.Println("CRDUPDATE turn migration on")
			return h.crdwrapper.UpdateStatus(cfg)
		}

		if !configChanged &&
			cfg.ConditionTrue(v1.SamplesExist) &&
			cfg.ConditionFalse(v1.ImageChangesInProgress) &&
			cfg.Condition(v1.MigrationInProgress).LastUpdateTime.Before(&cfg.Condition(v1.ImageChangesInProgress).LastUpdateTime) &&
			v1.GitVersionString() != cfg.Status.Version {
			if cfg.ConditionFalse(v1.ImportImageErrorsExist) {
				cfg.Status.Version = v1.GitVersionString()
				logrus.Printf("The samples are now at version %s", cfg.Status.Version)
				logrus.Println("CRDUPDATE upd status version")
				return h.crdwrapper.UpdateStatus(cfg)
			}
			logrus.Printf("An image import error occurred applying the latest configuration on version %s, problem resolution needed", v1.GitVersionString())
			return nil
		}

		// once the status version is in sync, we can turn off the migration condition
		if cfg.ConditionTrue(v1.MigrationInProgress) &&
			v1.GitVersionString() == cfg.Status.Version {
			h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.MigrationInProgress)
			logrus.Println("CRDUPDATE turn migration off")
			return h.crdwrapper.UpdateStatus(cfg)
		}

		if len(cfg.Spec.Architectures) == 0 {
			cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.X86Architecture)
		}

		if len(cfg.Spec.InstallType) == 0 {
			cfg.Spec.InstallType = v1.CentosSamplesDistribution
		}

		h.StoreCurrentValidConfig(cfg)

		// this boolean is driven by VariableConfigChanged based on comparing spec/status skip lists and
		// cross referencing with any image import errors
		if configChangeRequiresImportErrorUpdate && !configChangeRequiresUpsert {
			importError := cfg.Condition(v1.ImportImageErrorsExist)
			logrus.Printf("CRDUPDATE change import error status to %v with current list of error imagestreams %s", importError.Status, importError.Reason)
			return h.crdwrapper.UpdateStatus(cfg)
		}

		if configChanged && !configChangeRequiresUpsert && cfg.ConditionTrue(v1.SamplesExist) {
			logrus.Printf("CRDUPDATE bypassing upserts for non invasive config change after initial create")
			return h.crdwrapper.UpdateStatus(cfg)
		}

		if !cfg.ConditionTrue(v1.ImageChangesInProgress) {
			h.creationInProgress = true
			defer func() { h.creationInProgress = false }()
			if len(h.imagestreamFile) == 0 || len(h.templateFile) == 0 {
				for _, arch := range cfg.Spec.Architectures {
					dir := h.GetBaseDir(arch, cfg)
					files, err := h.Filefinder.List(dir)
					if err != nil {
						err = h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error reading in content : %v")
						logrus.Printf("CRDUPDATE file list err update")
						h.crdwrapper.UpdateStatus(cfg)
						return err
					}
					err = h.processFiles(dir, files, cfg)
					if err != nil {
						err = h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error processing content : %v")
						logrus.Printf("CRDUPDATE proc file err update")
						h.crdwrapper.UpdateStatus(cfg)
						return err
					}
				}
			}

			err = h.createSamples(cfg)
			if err != nil {
				h.processError(cfg, v1.ImageChangesInProgress, corev1.ConditionUnknown, err, "error creating samples: %v")
				e := h.crdwrapper.UpdateStatus(cfg)
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
				if !cfg.NameInReason(progressing.Reason, isName) && !skipped {
					progressing.Reason = progressing.Reason + isName + " "
				}
			}
			logrus.Debugf("Handle Reason field set to %s", progressing.Reason)
			cfg.ConditionUpdate(progressing)

			// now that we employ status subresources, we can't populate
			// the conditions on create; so we do initialize here, which is our "step 1"
			// of the "make a change" flow in our state machine
			cfg = h.initConditions(cfg)

			logrus.Printf("CRDUPDATE progressing true update")
			err = h.crdwrapper.UpdateStatus(cfg)
			if err != nil {
				return err
			}
			return nil
		}

		if !cfg.ConditionTrue(v1.SamplesExist) {
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.SamplesExist)
			// flush updates from processing
			logrus.Printf("CRDUPDATE exist true update")
			return h.crdwrapper.UpdateStatus(cfg)
		}

	}
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
