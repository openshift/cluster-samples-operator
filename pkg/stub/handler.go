package stub

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"

	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	imagev1 "github.com/openshift/api/image/v1"
	templatev1 "github.com/openshift/api/template/v1"

	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	imagev1lister "github.com/openshift/client-go/image/listers/image/v1"
	templatev1client "github.com/openshift/client-go/template/clientset/versioned/typed/template/v1"
	templatev1lister "github.com/openshift/client-go/template/listers/template/v1"
	configv1lister "github.com/openshift/cluster-samples-operator/pkg/generated/listers/samples/v1"

	operatorsv1api "github.com/openshift/api/operator/v1"
	v1 "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	"github.com/openshift/cluster-samples-operator/pkg/cache"
	operatorstatus "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"

	sampleclientv1 "github.com/openshift/cluster-samples-operator/pkg/generated/clientset/versioned/typed/samples/v1"
)

const (
	x86OCPContentRootDir = "/opt/openshift/operator/ocp-x86_64"
	installtypekey       = "keyForInstallTypeField"
	regkey               = "keyForSamplesRegistryField"
	skippedstreamskey    = "keyForSkippedImageStreamsField"
	skippedtempskey      = "keyForSkippedTemplatesField"
)

func NewSamplesOperatorHandler(kubeconfig *restclient.Config,
	configLister configv1lister.ConfigLister,
	streamLister imagev1lister.ImageStreamNamespaceLister,
	templateLister templatev1lister.TemplateNamespaceLister,
	openshiftNamespaceSecretLister corev1lister.SecretNamespaceLister,
	configNamespaceSecretLister corev1lister.SecretNamespaceLister) (*Handler, error) {
	h := &Handler{}

	h.initter = &defaultInClusterInitter{}
	h.initter.init(h, kubeconfig)

	crdWrapper := &generatedCRDWrapper{}
	client, err := sampleclientv1.NewForConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	crdWrapper.client = client.Configs()
	crdWrapper.lister = configLister

	h.crdwrapper = crdWrapper

	h.crdlister = configLister
	h.streamlister = streamLister
	h.tplstore = templateLister
	h.opshiftsecretlister = openshiftNamespaceSecretLister
	h.cfgsecretlister = configNamespaceSecretLister

	h.Fileimagegetter = &DefaultImageStreamFromFileGetter{}
	h.Filetemplategetter = &DefaultTemplateFromFileGetter{}
	h.Filefinder = &DefaultResourceFileLister{}

	h.imageclientwrapper = &defaultImageStreamClientWrapper{h: h, lister: streamLister}
	h.templateclientwrapper = &defaultTemplateClientWrapper{h: h, lister: templateLister}
	h.secretclientwrapper = &defaultSecretClientWrapper{coreclient: h.coreclient, opnshftlister: openshiftNamespaceSecretLister, cfglister: configNamespaceSecretLister}
	h.cvowrapper = operatorstatus.NewClusterOperatorHandler(h.configclient)

	h.skippedImagestreams = make(map[string]bool)
	h.skippedTemplates = make(map[string]bool)

	h.CreateDefaultResourceIfNeeded(nil)

	h.imagestreamFile = make(map[string]string)
	h.templateFile = make(map[string]string)

	h.mapsMutex = sync.Mutex{}
	h.version = os.Getenv("RELEASE_VERSION")

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

	crdlister           configv1lister.ConfigLister
	streamlister        imagev1lister.ImageStreamNamespaceLister
	tplstore            templatev1lister.TemplateNamespaceLister
	opshiftsecretlister corev1lister.SecretNamespaceLister
	cfgsecretlister     corev1lister.SecretNamespaceLister

	Fileimagegetter    ImageStreamFromFileGetter
	Filetemplategetter TemplateFromFileGetter
	Filefinder         ResourceFileLister

	skippedTemplates    map[string]bool
	skippedImagestreams map[string]bool

	imagestreamFile map[string]string
	templateFile    map[string]string

	mapsMutex sync.Mutex

	upsertInProgress bool
	secretRetryCount int8
	version          string
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

	if cfg.DeletionTimestamp != nil {
		// we do no return the cfg in this case because we do not want to bother with any progress tracking
		logrus.Printf("Received watch event %s but not upserting since deletion of the Config is in progress", kind+"/"+name)
		// note, the imagestream watch cache gets cleared once the deletion/finalizer processing commences
		return nil, "", false, nil
	}

	if cfg.ConditionFalse(v1.ImageChangesInProgress) {
		// we do no return the cfg in these cases because we do not want to bother with any progress tracking
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
	// on pod restarts samples watch events come in before the first
	// Config event, or we might not get an event at all if there were no changes,
	// so build list here
	h.buildFileMaps(cfg, false)

	// make sure skip filter list is ready
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
		// but return cfg to potentially toggle pending/import error condition
		return cfg, "", false, nil
	}

	if deleted && (kind == "template" || cache.UpsertsAmount() == 0) {
		logrus.Printf("going to recreate deleted managed sample %s/%s", kind, name)
		return cfg, filePath, true, nil
	}

	if cfg.ConditionFalse(v1.ImageChangesInProgress) &&
		(cfg.ConditionTrue(v1.MigrationInProgress) || h.version != cfg.Status.Version) {
		// we have gotten events for items early in the migration list but we have not
		// finished processing the list
		// avoid (re)upsert, but check import status
		logrus.Printf("watch event for %s/%s while migration in progress, image in progress is false; will not update sample because of this event", kind, name)
		return cfg, "", false, nil
	}

	if annotations != nil {
		isv, ok := annotations[v1.SamplesVersionAnnotation]
		logrus.Debugf("Comparing %s/%s version %s ok %v with git version %s", kind, name, isv, ok, h.version)
		if ok && isv == h.version {
			logrus.Debugf("Not upserting %s/%s cause operator version matches", kind, name)
			// but return cfg to potentially toggle pending condition
			return cfg, "", false, nil
		}
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
		if condition.Status != newStatus {
			condition.LastTransitionTime = now
		}
		condition.Status = newStatus
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
				logrus.Printf("create default config access error %v", err)
				return false, nil
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
		cfg.Spec.ManagementState = operatorsv1api.Managed
		h.AddFinalizer(cfg)
		// we should get a watch event for the default pull secret, but just in case
		// we miss the watch event, as well as reducing churn with not starting the
		// imagestream creates until we get the event, we'll do a one time copy attempt
		// here ... we don't track errors cause if it doen't work with this one time,
		// we'll then fall back on the watch events, sync intervals, etc.
		h.copyDefaultClusterPullSecret(nil)
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
	cfg.Condition(v1.RemovePending)
	cfg.Condition(v1.MigrationInProgress)
	cfg.Condition(v1.ImportImageErrorsExist)
	return cfg
}

func (h *Handler) CleanUpOpenshiftNamespaceOnDelete(cfg *v1.Config) error {
	h.buildSkipFilters(cfg)

	iopts := metav1.ListOptions{LabelSelector: v1.SamplesManagedLabel + "=true"}

	streamList, err := h.imageclientwrapper.List(iopts)
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
				err = h.imageclientwrapper.Delete(stream.Name, &metav1.DeleteOptions{})
				if err != nil && !kerrors.IsNotFound(err) {
					logrus.Warnf("Problem deleting openshift imagestream %s on Config delete: %#v", stream.Name, err)
					return err
				}
				cache.ImageStreamMassDeletesAdd(stream.Name)
			}
		}
	}

	cache.ClearUpsertsCache()

	tempList, err := h.templateclientwrapper.List(iopts)
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
				err = h.templateclientwrapper.Delete(temp.Name, &metav1.DeleteOptions{})
				if err != nil && !kerrors.IsNotFound(err) {
					logrus.Warnf("Problem deleting openshift template %s on Config delete: %#v", temp.Name, err)
					return err
				}
				cache.TemplateMassDeletesAdd(temp.Name)
			}
		}
	}

	// FYI we no longer delete the credential because the payload imagestreams like cli, must-gather that
	// this operator initially installs via its manifest, but does not manage, needs the pull image secret

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
		if !secretsWeCareAbout(dockercfgSecret) {
			return nil
		}

		// if we miss a delete event in the openshift namespace (since we cannot
		// add a finalizer in our namespace secret), we our watch
		// on the openshift-config pull secret should still repopulate;
		// if that gets deleted, the whole cluster is hosed; plus, there is talk
		// of moving data like that to a special config namespace that is somehow
		// protected

		cfg, _ := h.crdwrapper.Get(v1.ConfigName)
		if cfg != nil {
			return h.processSecretEvent(cfg, dockercfgSecret, event)
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
			// in things we just deleted ... if an upsert is still in progress, return an error;
			// the creation loop checks for deletion timestamp and aborts when it sees it;
			// but we don't use in progress condition here, as the upsert cycle might also be complete;
			// but ImageInProess is still true, so we use a local variable which is only set while upserts are happening; otherwise
			// here, we get started with the delete, which will ultimately reset the conditions
			// and samples, when it is false again, regardless of whether the imagestream imports are done
			if h.upsertInProgress {
				return fmt.Errorf("A delete attempt has come in while creating samples; initiating retry; creation loop should abort soon")
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
				dbg := "exist false update"
				logrus.Printf("CRDUPDATE %s", dbg)
				err = h.crdwrapper.UpdateStatus(cfg, dbg)
				if err != nil {
					logrus.Printf("error on Config update after setting exists condition to false (returning error to retry): %v", err)
					return err
				}
			} else {
				logrus.Println("Initiating finalizer processing for a SampleResource delete attempt")
				h.RemoveFinalizer(cfg)
				dbg := "remove finalizer update"
				logrus.Printf("CRDUPDATE %s", dbg)
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

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if cfg.ConditionUnknown(v1.ImportCredentialsExist) {
			// retry the default cred copy if it failed previously
			err := h.copyDefaultClusterPullSecret(nil)
			if err == nil {
				cfg = h.refetchCfgMinimizeConflicts(cfg)
				h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.ImportCredentialsExist)
				dbg := "cleared import cred unknown"
				logrus.Printf("CRDUPDATE %s", dbg)
				return h.crdwrapper.UpdateStatus(cfg, dbg)
			}
		}

		// Every time we see a change to the Config object, update the ClusterOperator status
		// based on the current conditions of the Config.
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		err := h.cvowrapper.UpdateOperatorStatus(cfg)
		if err != nil {
			logrus.Errorf("error updating cluster operator status: %v", err)
			return err
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		doit, cfgUpdate, err := h.ProcessManagementField(cfg)
		if !doit || err != nil {
			if err != nil || cfgUpdate {
				// flush status update
				dbg := "process mgmt update"
				logrus.Printf("CRDUPDATE %s", dbg)
				return h.crdwrapper.UpdateStatus(cfg, dbg)
			}
			return err
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		existingValidStatus := cfg.Condition(v1.ConfigurationValid).Status
		err = h.SpecValidation(cfg)
		if err != nil {
			// flush status update
			dbg := "bad spec validation update"
			logrus.Printf("CRDUPDATE %s", dbg)
			// only retry on error updating the Config; do not return
			// the error from SpecValidation which denotes a bad config
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}
		// if a bad config was corrected, update and return
		if existingValidStatus != cfg.Condition(v1.ConfigurationValid).Status {
			dbg := "spec corrected"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		h.buildSkipFilters(cfg)
		configChanged := false
		configChangeRequiresUpsert := false
		configChangeRequiresImportErrorUpdate := false
		registryChanged := false
		unskippedStreams := map[string]bool{}
		unskippedTemplates := map[string]bool{}
		if cfg.Spec.ManagementState == cfg.Status.ManagementState {
			cfg = h.refetchCfgMinimizeConflicts(cfg)
			configChanged, configChangeRequiresUpsert, configChangeRequiresImportErrorUpdate, registryChanged, unskippedStreams, unskippedTemplates = h.VariableConfigChanged(cfg)
			logrus.Debugf("config changed %v upsert needed %v import error upd needed %v exists/true %v progressing/false %v op version %s status version %s",
				configChanged,
				configChangeRequiresUpsert,
				configChangeRequiresImportErrorUpdate,
				cfg.ConditionTrue(v1.SamplesExist),
				cfg.ConditionFalse(v1.ImageChangesInProgress),
				h.version,
				cfg.Status.Version)
			// so ignore if config does not change and the samples exist and
			// we are not in progress and at the right level
			if !configChanged &&
				cfg.ConditionTrue(v1.SamplesExist) &&
				cfg.ConditionFalse(v1.ImageChangesInProgress) &&
				h.version == cfg.Status.Version {
				logrus.Debugf("At steady state: config the same and exists is true, in progress false, and version correct")

				// once the status version is in sync, we can turn off the migration condition
				if cfg.ConditionTrue(v1.MigrationInProgress) {
					h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.MigrationInProgress)
					dbg := " turn migration off"
					logrus.Printf("CRDUPDATE %s", dbg)
					return h.crdwrapper.UpdateStatus(cfg, dbg)
				}

				// in case this is a bring up after initial install, we take a pass
				// and see if any samples were deleted while samples operator was down
				h.buildFileMaps(cfg, false)
				// passing in false means if the samples is present, we leave it alone
				_, err = h.createSamples(cfg, false, registryChanged, unskippedStreams, unskippedTemplates)
				return err
			}
			// if config changed requiring an upsert, but a prior config action is still in progress,
			// reset in progress to false and return; the next event should drive the actual
			// processing of the config change and replace whatever was previously
			// in progress
			if configChangeRequiresUpsert &&
				cfg.ConditionTrue(v1.ImageChangesInProgress) {
				h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
				dbg := "change in progress from true to false for config change"
				logrus.Printf("CRDUPDATE %s", dbg)
				return h.crdwrapper.UpdateStatus(cfg, dbg)
			}
		}

		cfg.Status.ManagementState = operatorsv1api.Managed
		// if coming from remove turn off
		if cfg.ConditionTrue(v1.RemovePending) {
			now := kapis.Now()
			condition := cfg.Condition(v1.RemovePending)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionFalse
			cfg.ConditionUpdate(condition)
		}

		// if trying to do rhel to the default registry.redhat.io registry requires the secret
		// be in place since registry.redhat.io requires auth to pull; if it is not ready
		// error state will be logged by WaitingForCredential
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		stillWaitingForSecret, callSDKToUpdate := h.WaitingForCredential(cfg)
		if callSDKToUpdate {
			// flush status update ... the only error generated by WaitingForCredential, not
			// by api obj access
			dbg := "Config update ignored since need the RHEL credential"
			logrus.Printf("CRDUPDATE %s", dbg)
			// if update to set import cred condition to false fails, return that error
			// to requeue
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}
		if stillWaitingForSecret {
			// means we previously udpated cfg but nothing has changed wrt the secret's presence
			return nil
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if cfg.ConditionFalse(v1.MigrationInProgress) &&
			len(cfg.Status.Version) > 0 &&
			h.version != cfg.Status.Version {
			logrus.Printf("Undergoing migration from %s to %s", cfg.Status.Version, h.version)
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.MigrationInProgress)
			dbg := "turn migration on"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if !configChanged &&
			cfg.ConditionTrue(v1.SamplesExist) &&
			cfg.ConditionFalse(v1.ImageChangesInProgress) &&
			cfg.Condition(v1.MigrationInProgress).LastUpdateTime.Before(&cfg.Condition(v1.ImageChangesInProgress).LastUpdateTime) &&
			h.version != cfg.Status.Version {
			if cfg.ConditionTrue(v1.ImportImageErrorsExist) {
				logrus.Printf("An image import error occurred applying the latest configuration on version %s; this operator will periodically retry the import, or an administrator can investigate and remedy manually", h.version)
			}
			cfg.Status.Version = h.version
			logrus.Printf("The samples are now at version %s", cfg.Status.Version)
			dbg := "upd status version"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
			/*if cfg.ConditionFalse(v1.ImportImageErrorsExist) {
				cfg.Status.Version = h.version
				logrus.Printf("The samples are now at version %s", cfg.Status.Version)
				logrus.Println("CRDUPDATE upd status version")
				return h.crdwrapper.UpdateStatus(cfg)
			}
			logrus.Printf("An image import error occurred applying the latest configuration on version %s, problem resolution needed", h.version
			return nil

			*/
		}

		if len(cfg.Spec.Architectures) == 0 {
			cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.X86Architecture)
		}

		h.StoreCurrentValidConfig(cfg)

		// now that we have stored the skip lists in status,
		// cycle through the skip lists and update the managed flag if needed
		for _, name := range cfg.Status.SkippedTemplates {
			h.setSampleManagedLabelToFalse("template", name)
		}
		for _, name := range cfg.Status.SkippedImagestreams {
			h.setSampleManagedLabelToFalse("imagestream", name)
		}

		// this boolean is driven by VariableConfigChanged based on comparing spec/status skip lists and
		// cross referencing with any image import errors
		if configChangeRequiresImportErrorUpdate && !configChangeRequiresUpsert {
			dbg := "config change did not require upsert but did change import errors"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		if configChanged && !configChangeRequiresUpsert && cfg.ConditionTrue(v1.SamplesExist) {
			dbg := "bypassing upserts for non invasive config change after initial create"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		if !cfg.ConditionTrue(v1.ImageChangesInProgress) {
			// pass in true to force rebuild of maps, which we do here because at this point
			// we have taken on some form of config change
			err = h.buildFileMaps(cfg, true)
			if err != nil {
				return err
			}

			h.upsertInProgress = true
			turnFlagOff := func(h *Handler) { h.upsertInProgress = false }
			defer turnFlagOff(h)
			abortForDelete, err := h.createSamples(cfg, true, registryChanged, unskippedStreams, unskippedTemplates)
			// we prioritize enabling delete vs. any error processing from createSamples (though at the moment that
			// method only returns nil error when it returns true for abortForDelete) as a subsequent delete's processing will
			// immediately remove the cfg obj and cluster operator object that we just posted some error notice in
			if abortForDelete {
				// a delete has been initiated; let's revert in progress to false and allow the delete to complete,
				// including its removal of any sample we might have upserted with the above createSamples call
				h.GoodConditionUpdate(h.refetchCfgMinimizeConflicts(cfg), corev1.ConditionFalse, v1.ImageChangesInProgress)
				// note, the imagestream watch cache gets cleared once the deletion/finalizer processing commences
				dbg := "progressing false because delete has arrived"
				logrus.Printf("CRDUPDATE %s", dbg)
				return h.crdwrapper.UpdateStatus(cfg, dbg)
			}

			if err != nil {
				cfg = h.refetchCfgMinimizeConflicts(cfg)
				h.processError(cfg, v1.ImageChangesInProgress, corev1.ConditionUnknown, err, "error creating samples: %v")
				dbg := "setting in progress to unknown"
				logrus.Printf("CRDUPDATE %s", dbg)
				e := h.crdwrapper.UpdateStatus(cfg, dbg)
				if e != nil {
					return e
				}
				return err
			}
			now := kapis.Now()
			cfg = h.refetchCfgMinimizeConflicts(cfg)
			progressing := cfg.Condition(v1.ImageChangesInProgress)
			progressing.LastUpdateTime = now
			progressing.LastTransitionTime = now
			logrus.Debugf("Handle changing processing from false to true")
			progressing.Status = corev1.ConditionTrue
			for isName := range h.imagestreamFile {
				_, skipped := h.skippedImagestreams[isName]
				unskipping := len(unskippedStreams) > 0
				_, unskipped := unskippedStreams[isName]
				if unskipping && !unskipped {
					continue
				}
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

			dbg := "progressing true update"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		if !cfg.ConditionTrue(v1.SamplesExist) {
			cfg = h.refetchCfgMinimizeConflicts(cfg)
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.SamplesExist)
			dbg := "exist true update"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		// it is possible that all the cached imagestream events show that
		// the image imports are complete; hence we would not get any more
		// events until the next relist to clear out in progress; so let's
		// cycle through them here now
		if cache.AllUpsertEventsArrived() && cfg.ConditionTrue(v1.ImageChangesInProgress) {
			keysToClear := []string{}
			anyChange := false
			ac := false
			cfg = h.refetchCfgMinimizeConflicts(cfg)
			for key, is := range cache.GetUpsertImageStreams() {
				if is == nil {
					// never got update, refetch
					var e error
					is, e = h.imageclientwrapper.Get(key)
					if e != nil {
						keysToClear = append(keysToClear, key)
						continue
					}
				}
				cfg, _, ac = h.processImportStatus(is, cfg)
				anyChange = anyChange || ac
			}
			for _, key := range keysToClear {
				cache.RemoveUpsert(key)
				cfg.ClearNameInReason(cfg.Condition(v1.ImageChangesInProgress).Reason, key)
				cfg.ClearNameInReason(cfg.Condition(v1.ImportImageErrorsExist).Reason, key)
			}
			if len(strings.TrimSpace(cfg.Condition(v1.ImageChangesInProgress).Reason)) == 0 {
				h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
				logrus.Println("The last in progress imagestream has completed (config event loop)")
			}
			if anyChange {
				dbg := "updating in progress after examining cached imagestream events"
				logrus.Printf("CRDUPDATE %s", dbg)
				err = h.crdwrapper.UpdateStatus(cfg, dbg)
				if err == nil && cfg.ConditionFalse(v1.ImageChangesInProgress) {
					// only clear out cache if we got the update through
					cache.ClearUpsertsCache()
				}
				return err
			}
		}
	}
	return nil
}

func (h *Handler) setSampleManagedLabelToFalse(kind, name string) error {
	var err error
	switch kind {
	case "imagestream":
		var stream *imagev1.ImageStream
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			stream, err = h.imageclientwrapper.Get(name)
			if err == nil && stream != nil && stream.Labels != nil {
				label, _ := stream.Labels[v1.SamplesManagedLabel]
				if label == "true" {
					stream.Labels[v1.SamplesManagedLabel] = "false"
					_, err = h.imageclientwrapper.Update(stream)
				}
			}
			return err
		})
	case "template":
		var tpl *templatev1.Template
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			tpl, err = h.templateclientwrapper.Get(name)
			if err == nil && tpl != nil && tpl.Labels != nil {
				label, _ := tpl.Labels[v1.SamplesManagedLabel]
				if label == "true" {
					tpl.Labels[v1.SamplesManagedLabel] = "false"
					_, err = h.templateclientwrapper.Update(tpl)
				}
			}
			return err
		})
	}
	return nil
}

// abortForDelete is used in various portions of the control flow to abort upsert or watch processing
// if a deletion has been initiated and we want to process the config object's finalizer
func (h *Handler) abortForDelete(cfg *v1.Config) bool {
	// reminder refetchCfgMinimizeConflicts accesses the lister/watch cache and does not perform API calls
	cfg = h.refetchCfgMinimizeConflicts(cfg)
	if cfg.DeletionTimestamp != nil {
		return true
	}
	return false
}

// rc:  error - any api errors during upserts
// rc:  bool - if we abort because we detected a delete
func (h *Handler) createSamples(cfg *v1.Config, updateIfPresent, registryChanged bool, unskippedStreams, unskippedTemplates map[string]bool) (bool, error) {
	// first, got through the list and prime our upsert cache
	// prior to any actual upserts
	imagestreams := []*imagev1.ImageStream{}
	for _, fileName := range h.imagestreamFile {
		if h.abortForDelete(cfg) {
			return true, nil
		}
		imagestream, err := h.Fileimagegetter.Get(fileName)
		if err != nil {
			return false, err
		}

		// if unskippedStreams has >0 entries, then we are down this path to only upsert the streams
		// listed there
		if len(unskippedStreams) > 0 {
			if _, ok := unskippedStreams[imagestream.Name]; !ok {
				continue
			}
		}

		if _, isok := h.skippedImagestreams[imagestream.Name]; !isok {
			if updateIfPresent {
				cache.AddUpsert(imagestream.Name)
			}
			imagestreams = append(imagestreams, imagestream)
		}

	}
	for _, imagestream := range imagestreams {
		if h.abortForDelete(cfg) {
			return true, nil
		}
		is, err := h.imageclientwrapper.Get(imagestream.Name)
		if err != nil && !kerrors.IsNotFound(err) {
			return false, err
		}

		if err == nil && !updateIfPresent {
			continue
		}

		if kerrors.IsNotFound(err) { // testing showed that we get an empty is vs. nil in this case
			is = nil
		}

		err = h.upsertImageStream(imagestream, is, cfg)
		if err != nil {
			if updateIfPresent {
				cache.RemoveUpsert(imagestream.Name)
			}
			return false, err
		}
	}

	// if after initial startup, and not migration, the only cfg change was changing the registry, since that does not impact
	// the templates, we can move on
	if len(unskippedTemplates) == 0 && registryChanged && cfg.ConditionTrue(v1.SamplesExist) && cfg.ConditionFalse(v1.MigrationInProgress) {
		return false, nil
	}

	for _, fileName := range h.templateFile {
		if h.abortForDelete(cfg) {
			return true, nil
		}
		template, err := h.Filetemplategetter.Get(fileName)
		if err != nil {
			return false, err
		}

		// if unskippedTemplates has >0 entries, then we are down this path to only upsert the templates
		// listed there
		if len(unskippedTemplates) > 0 {
			if _, ok := unskippedTemplates[template.Name]; !ok {
				continue
			}
		}

		t, err := h.templateclientwrapper.Get(template.Name)
		if err != nil && !kerrors.IsNotFound(err) {
			return false, err
		}

		if err == nil && !updateIfPresent {
			continue
		}

		if kerrors.IsNotFound(err) { // testing showed that we get an empty is vs. nil in this case
			t = nil
		}

		err = h.upsertTemplate(template, t, cfg)
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

// refetchCfgMinimizeConflicts is a best effort attempt to get a later version of the config object
// just prior to updating the status; if there are any issues retrieving (unlikely since we are accessing
// the informer's lister) we'll just use the current copy we got from the event or prior fetch
func (h *Handler) refetchCfgMinimizeConflicts(cfg *v1.Config) *v1.Config {
	c, e := h.crdwrapper.Get(cfg.Name)
	if e == nil {
		return c
	}
	return cfg
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
