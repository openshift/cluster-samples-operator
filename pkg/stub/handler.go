package stub

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	configv1 "github.com/openshift/api/config/v1"
	imagev1 "github.com/openshift/api/image/v1"
	operatorsv1api "github.com/openshift/api/operator/v1"
	v1 "github.com/openshift/api/samples/v1"
	templatev1 "github.com/openshift/api/template/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	imagev1lister "github.com/openshift/client-go/image/listers/image/v1"
	sampleclientv1 "github.com/openshift/client-go/samples/clientset/versioned/typed/samples/v1"
	configv1lister "github.com/openshift/client-go/samples/listers/samples/v1"
	templatev1client "github.com/openshift/client-go/template/clientset/versioned/typed/template/v1"
	templatev1lister "github.com/openshift/client-go/template/listers/template/v1"

	"github.com/openshift/cluster-samples-operator/pkg/cache"
	sampopclient "github.com/openshift/cluster-samples-operator/pkg/client"
	"github.com/openshift/cluster-samples-operator/pkg/metrics"
	operatorstatus "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
	"github.com/openshift/cluster-samples-operator/pkg/util"
)

const (
	x86ContentRootDir = "/opt/openshift/operator/x86_64"
	armContentRootDir = "/opt/openshift/operator/aarch64"
	ppcContentRootDir = "/opt/openshift/operator/ppc64le"
	zContentRootDir   = "/opt/openshift/operator/s390x"
	installtypekey    = "keyForInstallTypeField"
	regkey            = "keyForSamplesRegistryField"
	skippedstreamskey = "keyForSkippedImageStreamsField"
	skippedtempskey   = "keyForSkippedTemplatesField"
)

func NewSamplesOperatorHandler(kubeconfig *restclient.Config,
	listers *sampopclient.Listers) (*Handler, error) {
	h := &Handler{}

	h.initter = &defaultInClusterInitter{}
	h.initter.init(h, kubeconfig)

	crdWrapper := &generatedCRDWrapper{}
	client, err := sampleclientv1.NewForConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	crdWrapper.client = client.Configs()
	crdWrapper.lister = listers.Config

	h.crdwrapper = crdWrapper

	h.crdlister = listers.Config
	h.streamlister = listers.ImageStreams
	h.tplstore = listers.Templates
	h.cfgsecretlister = listers.ConfigNamespaceSecrets

	h.Fileimagegetter = &DefaultImageStreamFromFileGetter{}
	h.Filetemplategetter = &DefaultTemplateFromFileGetter{}
	h.Filefinder = &DefaultResourceFileLister{}

	h.imageclientwrapper = &defaultImageStreamClientWrapper{h: h, lister: listers.ImageStreams}
	h.templateclientwrapper = &defaultTemplateClientWrapper{h: h, lister: listers.Templates}
	h.configmapclientwrapper = &defaultConfigMapClientWrapper{h: h, lister: listers.ConfigMaps}
	h.cvowrapper = operatorstatus.NewClusterOperatorHandler(h.configclient)

	h.skippedImagestreams = make(map[string]bool)
	h.skippedTemplates = make(map[string]bool)

	h.imagestreamFile = make(map[string]string)
	h.templateFile = make(map[string]string)
	h.imagestreatagToImage = make(map[string]string)
	h.CreateDefaultResourceIfNeeded(nil)

	h.imagestreamRetry = make(map[string]metav1.Time)

	h.mapsMutex = sync.Mutex{}
	h.version = os.Getenv("RELEASE_VERSION")

	metrics.InitializeMetricsCollector(listers)

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

	imageclientwrapper     ImageStreamClientWrapper
	templateclientwrapper  TemplateClientWrapper
	configmapclientwrapper ConfigMapClientWrapper

	crdlister        configv1lister.ConfigLister
	streamlister     imagev1lister.ImageStreamNamespaceLister
	tplstore         templatev1lister.TemplateNamespaceLister
	cfgsecretlister  corev1lister.SecretNamespaceLister
	opersecretlister corev1lister.SecretNamespaceLister

	Fileimagegetter    ImageStreamFromFileGetter
	Filetemplategetter TemplateFromFileGetter
	Filefinder         ResourceFileLister

	skippedTemplates    map[string]bool
	skippedImagestreams map[string]bool

	imagestreamFile      map[string]string
	templateFile         map[string]string
	imagestreatagToImage map[string]string

	imagestreamRetry map[string]metav1.Time

	mapsMutex sync.Mutex

	upsertInProgress bool
	secretRetryCount int8
	version          string
	tbrCheckFailed   bool
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
		// if not found, then this also would mean a deletion event
		if kerrors.IsNotFound(err) {
			logrus.Printf("Received watch event %s but not upserting since deletion of the Config is in progress", kind+"/"+name)
			return nil, "", false, nil
		}
		logrus.Printf("Received watch event %s but not upserting since not have the Config yet: %#v %#v", kind+"/"+name, err, cfg)
		return nil, "", false, err
	}

	cfg = cfg.DeepCopy()

	if cfg.DeletionTimestamp != nil {
		// we do no return the cfg in this case because we do not want to bother with any progress tracking
		logrus.Printf("Received watch event %s but not upserting since deletion of the Config is in progress", kind+"/"+name)
		// note, the imagestream watch cache gets cleared once the deletion/finalizer processing commences
		return nil, "", false, nil
	}

	// we do not return the cfg in these cases because we do not want to bother with any progress tracking
	switch cfg.Spec.ManagementState {
	case operatorsv1api.Removed:
		logrus.Debugf("Not upserting %s/%s event because operator is in removed state and image changes are not in progress", kind, name)
		return nil, "", false, nil
	case operatorsv1api.Unmanaged:
		logrus.Debugf("Not upserting %s/%s event because operator is in unmanaged state and image changes are not in progress", kind, name)
		return nil, "", false, nil
	}

	filePath := ""
	// on pod restarts samples watch events come in before the first
	// Config event, or we might not get an event at all if there were no changes;
	// restarts as part of migrations also make things interesting because existing
	// samples may have been deleted on disk, but we'll address that below
	force := metrics.StreamsEmpty()
	h.buildFileMaps(cfg, force)

	// make sure skip filter list is ready
	h.buildSkipFilters(cfg)

	inInventory := false
	skipped := false
	switch kind {
	case "imagestream":
		filePath, inInventory = h.imagestreamFile[name]
		if !inInventory {
			logrus.Debugf("watch stream event %s not part of operators inventory", name)
			// we now have cases where sample providers are deleting entire imagestreams;
			// let's make sure there are no stale entries with inprogress / importerror
			_, err := h.configmapclientwrapper.Get(name)
			if err != nil && kerrors.IsNotFound(err) {
				// ConfigMap should only exist if the imagestream has an error
				return nil, "", false, nil
			}
			if err != nil {
				// GET errors indicate a potential issue with the apiserver, return error and try again
				return nil, "", false, err
			}
			err = h.configmapclientwrapper.Delete(name)
			return nil, "", false, err
		}
		_, skipped = h.skippedImagestreams[name]
	case "template":
		filePath, inInventory = h.templateFile[name]
		if !inInventory {
			// in the case of templates we can just ignore content from prior releases that is no longer
			// part of the current release
			logrus.Printf("watch template event %s not part of operators inventory", name)
			return nil, "", false, nil
		}
		_, skipped = h.skippedTemplates[name]
	}

	if skipped {
		logrus.Printf("watch event %s in skipped list for %s", name, kind)
		// but return cfg to potentially toggle pending/import error condition
		return cfg, "", false, nil
	}

	if deleted { //&& (kind == "template" || cache.UpsertsAmount() == 0) {
		logrus.Printf("going to recreate deleted managed sample %s/%s", kind, name)
		return cfg, filePath, true, nil
	}

	if h.shouldSetVersion(cfg) {
		// we have gotten events for items early in the migration list but we have not
		// finished processing the list
		// avoid (re)upsert, but check import status
		if util.ConditionTrue(cfg, v1.MigrationInProgress) {
			logrus.Printf("watch event for %s/%s while migration in progress, image in progress is false; will not update sample because of this event", kind, name)
		}
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
	condition := util.Condition(cfg, conditionType)
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
		util.ConditionUpdate(cfg, condition)
	}
	if conditionType == v1.ConfigurationValid {
		switch newStatus {
		case corev1.ConditionTrue:
			metrics.ConfigInvalid(false)
		default:
			metrics.ConfigInvalid(true)
		}
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

func ShouldNotGoDegraded(err error) bool {
	// retryable and conflict errors should be self-explanatory for not going degraded;
	// in particular with conflicts, they are quite possible with imagestreams during startup given
	// the multi tag, highly concurrent nature of creating / updating;
	// for invalid, we special case this because that is what the imagestream apiserver endpoint
	// returns if a registry is missing from the images allowed registry list, or is explicitly
	// listed in the blocked registry list
	return IsRetryableAPIError(err) || kerrors.IsConflict(err) || kerrors.IsInvalid(err)
}

// this method assumes it is only called on initial cfg create, or if the Architecture array len == 0
func (h *Handler) updateCfgArch(cfg *v1.Config) *v1.Config {
	switch {
	// if you look at https://golang.org/dl/ the arch symbols for ppc and 390 container
	// the values of our constants below.
	case strings.Contains(runtime.GOARCH, v1.PPCArchitecture):
		cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.PPCArchitecture)
	case strings.Contains(runtime.GOARCH, v1.S390Architecture):
		cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.S390Architecture)
	case strings.Contains(runtime.GOARCH, v1.ARMArchitecture):
		cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.ARMArchitecture)
	case strings.Contains(runtime.GOARCH, v1.AMDArchitecture):
		fallthrough
	case strings.Contains(runtime.GOARCH, v1.X86Architecture):
		cfg.Spec.Architectures = append(cfg.Spec.Architectures, v1.X86Architecture)
	default:
		logrus.Warningf("unsupported hardware architecture indicated by the golang GOARCH variable being set to %s", runtime.GOARCH)
	}
	return cfg
}

func redHatRegistriesFound(allowedRegistries map[string]bool) bool {
	// Empty Sample Registry will be allowed as long as allowed registries contanis:
	// - registry.redhat.io
	// - quay.io
	return allowedRegistries["registry.redhat.io"] &&
		allowedRegistries["quay.io"]
}

func redHatRegistriesDomainFound(allowedDomains map[string]bool) bool {
	// Empty Sample Registry will be allowed as long as allowed domains contanis:
	// - registry.redhat.io
	// - quay.io
	// or a domain combination that covers above registries
	return (allowedDomains["registry.redhat.io"] &&
		allowedDomains["quay.io"]) ||
		(allowedDomains["registry.redhat.io"] &&
			allowedDomains["redhat.com"] &&
			allowedDomains["quay.io"]) ||
		(allowedDomains["redhat.io"] &&
			allowedDomains["quay.io"]) ||
		(allowedDomains["redhat.io"] &&
			allowedDomains["redhat.com"] &&
			allowedDomains["quay.io"])

}

var getImageConfig = func(h *Handler) (*configv1.Image, error) {
	// Extracting call to ConfigV1Client as a functor to simplify unit testing
	return h.configclient.Images().Get(context.TODO(), "cluster", metav1.GetOptions{})
}

func (h *Handler) imageConfigBlocksImageStreamCreation(name string) bool {
	//TODO openshift/client-go/config/clientset/versioned/fake and ConfigV1Interface has compile issues with
	// respect to ConfigV1Client (missing method implementations), so for now we cannot use it in our
	// unit tests.  In the actual runtime if we cannot create the client the operator errors out on startup.
	if h.configclient == nil {
		return false
	}
	var imgCfg *configv1.Image
	err := wait.PollImmediate(5*time.Second, 20*time.Second, func() (done bool, err error) {
		// if the image config allowed registry or blocked registry list will prevent the creation of imagestreams,
		// we consider this inaccessible
		//imgCfg, err = h.configclient.Images().Get(context.TODO(), "cluster", metav1.GetOptions{})
		imgCfg, err = getImageConfig(h)
		if err != nil {
			logrus.Printf("unable to retrieve image configuration as part of testing %s connectivity: %s", name, err.Error())
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return true
	}

	ok := false
	// check allowed domain list
	if len(imgCfg.Spec.AllowedRegistriesForImport) > 0 {
		var visitedDomains = make(map[string]bool)

		for _, rl := range imgCfg.Spec.AllowedRegistriesForImport {
			visitedDomains[rl.DomainName] = true
			logrus.Printf("considering allowed registries domain %s for %s", rl.DomainName, name)
			if strings.HasSuffix(name, rl.DomainName) {
				logrus.Printf("the allowed registries domain %s allows imagestream creation access for search param %s", rl.DomainName, name)
				ok = true
				break
			}
		}

		// Checking if Sample Registry is empty (default scenario)
		if len(name) == 0 && redHatRegistriesDomainFound(visitedDomains) {
			logrus.Printf("imagestream creation will be allowed when sample registry config is empty")
			ok = true
		}

		if !ok {
			logrus.Printf("no allowed registries items will permit the use of %s", name)
			return true
		}
	}

	ok = false
	// check allowed specific registry list
	if len(imgCfg.Spec.RegistrySources.AllowedRegistries) > 0 {
		var visitedRegistries = make(map[string]bool)

		for _, r := range imgCfg.Spec.RegistrySources.AllowedRegistries {
			visitedRegistries[strings.TrimSpace(r)] = true
			logrus.Printf("considering allowed registry %s for %s", r, name)
			if strings.TrimSpace(r) == strings.TrimSpace(name) {
				logrus.Printf("the allowed registry %s allows imagestream creation for search param %s", r, name)
				ok = true
				break
			}
		}

		// Checking if Sample Registry is empty (default scenario)
		if len(name) == 0 && redHatRegistriesFound(visitedRegistries) {
			logrus.Printf("imagestream creation will be allowed when sample registry config is empty")
			ok = true
		}

		if !ok {
			logrus.Printf("no allowed registries items will permit the use of %s", name)
			return true
		}

	}

	ok = false
	// check blocked list
	if len(imgCfg.Spec.RegistrySources.BlockedRegistries) > 0 {
		for _, r := range imgCfg.Spec.RegistrySources.BlockedRegistries {
			logrus.Printf("considering blocked registry %s for %s", r, name)
			if strings.TrimSpace(r) == strings.TrimSpace(name) {
				logrus.Printf("the blocked registry %s prevents imagestream creation for search param %s", r, name)
				return true
			}
		}
		logrus.Printf("no blocked registries items will prevent the use of %s", name)
	}

	logrus.Printf("no global imagestream configuration will block imagestream creation using %s", name)

	return false
}

func (h *Handler) tbrInaccessible() bool {
	if h.configclient == nil {
		// unit test environment
		return false
	}
	// even with the connection attempt below, we still do the ipv6/proxy checks in case bot ipv6 and proxy
	// are employed, as we have will return differently here based on which are
	if util.IsIPv6() {
		logrus.Print("registry.redhat.io does not support ipv6, bootstrap to removed")
		return true
	}
	if h.imageConfigBlocksImageStreamCreation("registry.redhat.io") ||
		h.imageConfigBlocksImageStreamCreation("quay.io") {
		return true
	}

	proxy := &configv1.Proxy{}
	var err error
	err = wait.PollImmediate(5*time.Second, 20*time.Second, func() (bool, error) {
		// if a proxy is in play, the registry.redhat.io connection attempt during startup is problematic at best;
		// assume tbr is accessible since a proxy implies external access, and not disconnected
		proxy, err = h.configclient.Proxies().Get(context.TODO(), "cluster", metav1.GetOptions{})
		if err != nil {
			logrus.Printf("unable to retrieve proxy configuration as part of testing registry.redhat.io connectivity: %s", err.Error())
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		// just logging for reference; we will move on to the registry connection tests
		logrus.Printf("was unable to get proxy instance within a reasonable retry window")
	}

	if len(proxy.Status.HTTPSProxy) > 0 || len(proxy.Status.HTTPProxy) > 0 {
		logrus.Printf("with global proxy configured assuming registry.redhat.io is accessible, bootstrap to Managed")
		return false
	}

	err = wait.PollImmediate(20*time.Second, 5*time.Minute, func() (bool, error) {
		// we have seen cases in the field with disconnected cluster where the default connection timeout can be
		// very long (15 minutes in one case); so we do an initial non-tls connection were we can specify a quicker
		// timeout to filter out that scenario and default to tbr inaccessible / Removed in an expedient fashion
		connWithTimeout, err := net.DialTimeout("tcp", "registry.redhat.io:443", 15*time.Second)
		if err != nil {
			logrus.Infof("test connection with timeout failed with %s", err.Error())
			return false, nil
		}
		defer connWithTimeout.Close()
		// still do the tls form of connect (using our connection with the shorter timeout) to confirm
		// ssl handshake is OK
		tlsConf := &tls.Config{
			ServerName: "registry.redhat.io",
		}
		conn := tls.Client(connWithTimeout, tlsConf)
		defer conn.Close()
		err = conn.Handshake()
		if err != nil {
			logrus.Infof("test tls connection to registry.redhat.io experienced SSL handshake error %s", err.Error())
			// these can be intermittent as well so we'll retry
			return false, nil
		}
		logrus.Infof("test connection to registry.redhat.io successful")
		return true, nil

	})

	if err == nil {
		h.tbrCheckFailed = false
		return false
	}

	h.tbrCheckFailed = true
	logrus.Infof("unable to establish HTTPS connection to registry.redhat.io after 3 minutes, bootstrap to Removed")
	return true
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
		cfg = h.updateCfgArch(cfg)

		// build file maps and create configmaps with imagestreamtag to image mappings
		err := h.buildFileMaps(cfg, true)
		if err != nil {
			return nil, err
		}

		switch {
		// TODO as we gain content for non x86 platforms we can remove the nonx86 check
		case util.IsUnsupportedArch(cfg):
			cfg.Spec.ManagementState = operatorsv1api.Removed
			cfg.Status.Version = h.version
		case h.tbrInaccessible():
			cfg.Spec.ManagementState = operatorsv1api.Removed
			cfg.Status.Version = h.version
		default:
			cfg.Spec.ManagementState = operatorsv1api.Managed
		}
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
		// after a restart, this means we are beyond a bootstrap; but let's
		// preserve the state of our initial TBR check
		h.tbrCheckFailed = false
		if cfg.Status.ManagementState == operatorsv1api.Removed {
			op, err := h.cvowrapper.ClusterOperatorWrapper.Get(operatorstatus.ClusterOperatorName)
			if err == nil {
				for _, c := range op.Status.Conditions {
					if c.Reason == operatorstatus.TBR {
						logrus.Print("Samples operator originally bootstrapped as removed because the TBR was inaccessible")
						h.tbrCheckFailed = true
						break
					}
				}
			}
		}
	}

	return cfg, nil
}

func (h *Handler) initConditions(cfg *v1.Config) *v1.Config {
	now := kapis.Now()
	util.Condition(cfg, v1.SamplesExist)
	creds := util.Condition(cfg, v1.ImportCredentialsExist)
	// image registry operator now handles making TBR creds available
	// for imagestreams
	if creds.Status != corev1.ConditionTrue {
		creds.Status = corev1.ConditionTrue
		creds.LastTransitionTime = now
		creds.LastUpdateTime = now
		util.ConditionUpdate(cfg, creds)
	}
	valid := util.Condition(cfg, v1.ConfigurationValid)
	// our default config is valid; since Condition sets new conditions to false
	// if we get false here this is the first pass through; invalid configs
	// are caught above
	if valid.Status != corev1.ConditionTrue {
		valid.Status = corev1.ConditionTrue
		valid.LastUpdateTime = now
		valid.LastTransitionTime = now
		util.ConditionUpdate(cfg, valid)
	}
	util.Condition(cfg, v1.ImageChangesInProgress)
	util.Condition(cfg, v1.RemovePending)
	util.Condition(cfg, v1.MigrationInProgress)
	util.Condition(cfg, v1.ImportImageErrorsExist)
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

	cmList, err := h.configmapclientwrapper.List()
	if err != nil && !kerrors.IsNotFound(err) {
		logrus.Warnf("Problem listing sample operator config maps on Config delete: %v", err.Error())
		return err
	} else {
		for _, cm := range cmList {
			if cm.Name == util.IST2ImageMap {
				continue
			}
			err := h.configmapclientwrapper.Delete(cm.Name)
			if err != nil && !kerrors.IsNotFound(err) {
				logrus.Warnf("Problem deleting samples operator config map %s on Config delete: %v", cm.Name, err.Error())
				return err
			}
		}
	}

	// FYI we no longer delete the credential because the payload imagestreams like cli, must-gather that
	// this operator initially installs via its manifest, but does not manage, needs the pull image secret

	return nil
}

func (h *Handler) Handle(event util.Event) error {
	switch event.Object.(type) {
	case *corev1.ConfigMap:
		cm, _ := event.Object.(*corev1.ConfigMap)
		if cm.Name == util.IST2ImageMap {
			return nil
		}
		return h.processImageCondition()

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
			h.cvowrapper.UpdateOperatorStatus(cfg, true, h.tbrCheckFailed, h.activeImageStreams())
			return nil
		}
		if cfg.DeletionTimestamp != nil {
			h.cvowrapper.UpdateOperatorStatus(cfg, true, h.tbrCheckFailed, h.activeImageStreams())
			// before we kick off the delete cycle though, we make sure a prior creation
			// cycle is not still in progress, because we don't want the create adding back
			// in things we just deleted ... if an upsert is still in progress, return an error;
			// the creation loop checks for deletion timestamp and aborts when it sees it;
			// but we don't use in progress condition here, as the upsert cycle might also be complete;
			// but ImageInProess is still true, so we use a local variable which is only set while upserts are happening; otherwise
			// here, we get started with the delete, which will ultimately reset the conditions
			// and samples, when it is false again, regardless of whether the imagestream imports are done
			if h.upsertInProgress {
				return errors.New("A delete attempt has come in while creating samples; initiating retry; creation loop should abort soon")
			}

			// nuke any registered upserts
			//cache.ClearUpsertsCache()

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
				cfg = h.refetchCfgMinimizeConflicts(cfg)
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
				cfg = h.refetchCfgMinimizeConflicts(cfg)
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
		validArch, _ := h.IsValidArch(cfg)
		if validArch && util.IsUnsupportedArch(cfg) && cfg.Spec.ManagementState == operatorsv1api.Managed {
			// we did not bootstrap as removed in 4.2 for s390/ppc; we just reported complete
			// clean that up to facilitate our mode of operation for those platforms
			cfg.Spec.ManagementState = operatorsv1api.Removed
			dbg := fmt.Sprintf("switch management state to removed for %s", cfg.Spec.Architectures[0])
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.Update(cfg)
		}

		// Every time we see a change to the Config object, update the ClusterOperator status
		// based on the current conditions of the Config.
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		err := h.cvowrapper.UpdateOperatorStatus(cfg, false, h.tbrCheckFailed, h.activeImageStreams())
		if err != nil {
			logrus.Errorf("error updating cluster operator status: %v", err)
			return err
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		updateStatusManagementState, cfgUpdate, err := h.ProcessManagementField(cfg)
		if !updateStatusManagementState || err != nil {
			if err != nil || cfgUpdate {
				// flush status update
				dbg := fmt.Sprintf("process mgmt update spec %s status %s", string(cfg.Spec.ManagementState), string(cfg.Status.ManagementState))
				logrus.Printf("CRDUPDATE %s", dbg)
				return h.crdwrapper.UpdateStatus(cfg, dbg)
			}
			return err
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		existingValidStatus := util.Condition(cfg, v1.ConfigurationValid).Status
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
		if existingValidStatus != util.Condition(cfg, v1.ConfigurationValid).Status {
			dbg := "spec corrected"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		if len(cfg.Spec.Architectures) > 0 &&
			cfg.Spec.Architectures[0] != v1.AMDArchitecture &&
			cfg.Spec.Architectures[0] != v1.ARMArchitecture &&
			cfg.Spec.Architectures[0] != v1.X86Architecture &&
			cfg.Spec.Architectures[0] != v1.S390Architecture &&
			cfg.Spec.Architectures[0] != v1.PPCArchitecture {
			logrus.Printf("samples are not installed on an unsupported architecture")
		}

		h.buildSkipFilters(cfg)
		configChanged := false
		configChangeRequiresUpsert := false
		registryChanged := false
		unskippedStreams := map[string]bool{}
		unskippedTemplates := map[string]bool{}
		if cfg.Spec.ManagementState == cfg.Status.ManagementState {
			cfg = h.refetchCfgMinimizeConflicts(cfg)
			configChanged, configChangeRequiresUpsert, registryChanged, unskippedStreams, unskippedTemplates = h.VariableConfigChanged(cfg)
			logrus.Debugf("config changed %v upsert needed %v exists/true %v progressing/false %v op version %s status version %s",
				configChanged,
				configChangeRequiresUpsert,
				util.ConditionTrue(cfg, v1.SamplesExist),
				util.ConditionFalse(cfg, v1.ImageChangesInProgress),
				h.version,
				cfg.Status.Version)
			// so ignore if config does not change and the samples exist and
			// we are not in progress and at the right level
			if !configChanged &&
				util.ConditionTrue(cfg, v1.SamplesExist) &&
				util.ConditionFalse(cfg, v1.ImageChangesInProgress) &&
				h.version == cfg.Status.Version {
				logrus.Printf("At steady state: config the same and exists is true, in progress false, and version correct")

				cfg = h.refetchCfgMinimizeConflicts(cfg)
				// migration inevitably means we need to refresh the file cache as samples are added and
				// deleted between releases, so force file map building
				if !util.IsUnsupportedArch(cfg) {
					h.buildFileMaps(cfg, true)
					// passing in false means if the samples is present, we leave it alone
					_, err = h.createSamples(cfg, false, registryChanged, unskippedStreams, unskippedTemplates)
				}
				return err
			}
			// if config changed requiring an upsert, but a prior config action is still in progress,
			// reset in progress to false and return; the next event should drive the actual
			// processing of the config change and replace whatever was previously
			// in progress
			if configChangeRequiresUpsert &&
				util.ConditionTrue(cfg, v1.ImageChangesInProgress) {
				cfg = h.refetchCfgMinimizeConflicts(cfg)
				h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
				dbg := "change in progress from true to false for config change"
				logrus.Printf("CRDUPDATE %s", dbg)
				// do not transfer config changes to status since we are just turning off in progress
				// to start a new createSamples cycle
				return h.crdwrapper.UpdateStatus(cfg, dbg)
			}
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if cfg.Status.ManagementState != operatorsv1api.Managed {
			cfg.Status.ManagementState = operatorsv1api.Managed
			dbg := "change status management state to managed"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		// if coming from remove turn off
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if util.ConditionTrue(cfg, v1.RemovePending) {
			now := kapis.Now()
			condition := util.Condition(cfg, v1.RemovePending)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionFalse
			util.ConditionUpdate(cfg, condition)
			dbg := "change remove pending to false"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if !configChanged && h.shouldSetVersion(cfg) {
			if util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
				logrus.Printf("An image import error occurred applying the latest configuration on version %s; this operator will periodically retry the import, or an administrator can investigate and remedy manually", h.version)
			}
			cfg.Status.Version = h.version
			logrus.Printf("The samples are now at version %s", cfg.Status.Version)
			dbg := "upd status version"
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		// cycle through the skip lists and update the managed flag if needed
		for _, name := range cfg.Spec.SkippedTemplates {
			h.setSampleManagedLabelToFalse("template", name)
		}
		for _, name := range cfg.Spec.SkippedImagestreams {
			h.setSampleManagedLabelToFalse("imagestream", name)
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if configChanged && !configChangeRequiresUpsert && util.ConditionTrue(cfg, v1.SamplesExist) {
			dbg := "bypassing upserts for non invasive config change after initial create"
			logrus.Printf("CRDUPDATE %s", dbg)
			cfg = h.refetchCfgMinimizeConflicts(cfg)
			// we have now "processed" the config and are executing changes, transfer current spec to status
			h.StoreCurrentValidConfig(cfg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if !util.ConditionTrue(cfg, v1.SamplesExist) ||
			!util.ConditionFalse(cfg, v1.ImageChangesInProgress) ||
			h.version != cfg.Status.Version ||
			configChanged ||
			updateStatusManagementState {
			logrus.Infof("ENTERING UPSERT / STEADY STATE PATH ExistTrue %v ImageInProgressFalse %v VersionOK %v ConfigChanged %v ManagementStateChanged %v",
				util.ConditionTrue(cfg, v1.SamplesExist),
				util.ConditionFalse(cfg, v1.ImageChangesInProgress),
				h.version == cfg.Status.Version,
				configChanged,
				updateStatusManagementState)
			// pass in true to force rebuild of maps, which we do here because at this point
			// we have taken on some form of config change
			err = h.buildFileMaps(cfg, true)
			if err != nil {
				return err
			}

			h.upsertInProgress = true
			turnFlagOff := func(h *Handler) { h.upsertInProgress = false }
			defer turnFlagOff(h)

			for isName := range h.imagestreamFile {
				_, skipped := h.skippedImagestreams[isName]
				unskipping := len(unskippedStreams) > 0
				_, unskipped := unskippedStreams[isName]
				if (unskipping && !unskipped) || skipped {
					continue
				}

				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      isName,
						Namespace: v1.OperatorNamespace,
					},
					Data: map[string]string{},
				}
				_, err = h.configmapclientwrapper.Create(cm)
				if err != nil && !kerrors.IsAlreadyExists(err) {
					return err
				}
			}

			abortForDelete, err := h.createSamples(cfg, true, registryChanged, unskippedStreams, unskippedTemplates)
			// we prioritize enabling delete vs. any error processing from createSamples (though at the moment that
			// method only returns nil error when it returns true for abortForDelete) as a subsequent delete's processing will
			// immediately remove the cfg obj and cluster operator object that we just posted some error notice in
			if abortForDelete {
				// a delete has been initiated
				// note, the imagestream watch cache gets cleared once the deletion/finalizer processing commences
				dbg := "create samples aborted for delete"
				logrus.Printf("CRDUPDATE %s", dbg)
				return h.crdwrapper.UpdateStatus(cfg, dbg)
			}

			if err != nil {
				if !ShouldNotGoDegraded(err) {
					h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error creating samples: %v")
					dbg := "setting samples exists to unknown"
					logrus.Printf("CRDUPDATE %s", dbg)
					e := h.crdwrapper.UpdateStatus(cfg, dbg)
					if e != nil {
						return e
					}
				} else {
					logrus.Printf("CRDUPDATE error on createSamples but retryable, not marking degraded: %s", err.Error())
				}
				return err
			}

			cfg.Status.Version = h.version
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.SamplesExist)
			h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
			// now that we employ status subresources, we can't populate
			// the conditions on create; so we do initialize here, which is our "step 1"
			// of the "make a change" flow in our state machine
			cfg = h.initConditions(cfg)

			dbg := "samples upserted; set clusteroperator ready, steady state"
			// we have now "processed" the config and are executing changes, transfer current spec to status
			h.StoreCurrentValidConfig(cfg)

			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
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
				stream = stream.DeepCopy()
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
				tpl = tpl.DeepCopy()
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

		if is != nil {
			is = is.DeepCopy()
		}
		err = h.upsertImageStream(imagestream, is, cfg)
		if err != nil {
			return false, err
		}
	}

	// if after initial startup, and not migration, the only cfg change was changing the registry, since that does not impact
	// the templates, we can move on
	if len(unskippedTemplates) == 0 && registryChanged && h.sufficientSteadyStateForAnOperator(cfg) {
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

		t = t.DeepCopy()
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
		return c.DeepCopy()
	}
	return cfg.DeepCopy()
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

func (h *Handler) shouldSetProgressingFalse(cfg *v1.Config) bool {
	present := h.numOfManagedImageStreamsPresent()
	managed := h.numOfManagedImageStreams()
	if present < managed {
		logrus.Printf("shouldSetProgressingFalse: only %d of the %d imagestreams exist", present, managed)
	}
	return util.ConditionTrue(cfg, v1.SamplesExist) &&
		util.ConditionTrue(cfg, v1.ImageChangesInProgress) &&
		(len(h.activeImageStreams()) == 0 || util.ConditionTrue(cfg, v1.ImportImageErrorsExist)) &&
		!h.upsertInProgress &&
		present >= managed
}

func (h *Handler) shouldSetVersion(cfg *v1.Config) bool {
	return util.ConditionTrue(cfg, v1.SamplesExist) &&
		util.ConditionFalse(cfg, v1.ImageChangesInProgress) &&
		!h.upsertInProgress &&
		h.version != cfg.Status.Version &&
		h.areStreamsAtCorrectVersion()
}

func (h *Handler) sufficientSteadyStateForAnOperator(cfg *v1.Config) bool {
	return util.ConditionTrue(cfg, v1.SamplesExist) &&
		util.ConditionFalse(cfg, v1.ImageChangesInProgress) &&
		h.version == cfg.Status.Version
}

func (h *Handler) areStreamsAtCorrectVersion() bool {
	for key, _ := range h.imagestreamFile {
		// remember, our get wrapper goes through the lister, so we are only hitting the controller cache
		skipped, ok := h.skippedImagestreams[key]
		if skipped && ok {
			continue
		}
		is, err := h.imageclientwrapper.Get(key)
		if err != nil {
			if !h.upsertInProgress {
				// not all imagestreams may be created yet during initial or bulk creations
				logrus.Warningf("areStreamsAtCorrectVersion: get of stream %s got error: %s", key, err.Error())
			}
			return false
		}
		if is == nil {
			if !h.upsertInProgress {
				// not all imagestreams may be created yet during initial or bulk creations
				logrus.Warningf("areStreamsAtCorrectVersion: got nil for stream %s but no error", key)
			}
			return false
		}
		if is.Annotations == nil {
			logrus.Warningf("areStreamsAtCorrectVersion: stream %s has no annotation map", key)
			return false
		}
		isv, ok := is.Annotations[v1.SamplesVersionAnnotation]
		if !ok {
			logrus.Warningf("areStreamsAtCorrectVersion: stream %s does not have version annotation", key)
			return false
		}
		if isv != h.version {
			logrus.Warningf("areStreamsAtCorrectVersion: stream %s is at version %s instead of %s", key, isv, h.version)
			return false
		}
	}
	logrus.Printf("areStreamsAtCorrectVersion: all streams are now at the correct version %s", h.version)
	return true
}

func (h *Handler) numOfManagedImageStreams() int {
	rc := len(h.imagestreamFile) - len(h.skippedImagestreams)
	return rc
}

func (h *Handler) numOfManagedImageStreamsPresent() int {
	rc := 0
	for key, _ := range h.imagestreamFile {
		// remember, our get wrapper goes through the lister, so we are only hitting the controller cache
		skipped, ok := h.skippedImagestreams[key]
		if skipped && ok {
			continue
		}
		is, err := h.imageclientwrapper.Get(key)
		if err != nil {
			if !h.upsertInProgress {
				// not all imagestreams may be created yet during initial or bulk creations
				logrus.Warningf("numOfManagedImageStreamsPresent: get of stream %s got error: %s", key, err.Error())
			}
			continue
		}
		if is == nil {
			if !h.upsertInProgress {
				// not all imagestreams may be created yet during initial or bulk creations
				logrus.Warningf("numOfManagedImageStreamsPresent: got nil for stream %s but no error", key)
			}
			continue
		}
		if is.Labels == nil {
			logrus.Warningf("numOfManagedImageStreamsPresent: stream %s has no labels map", key)
			continue
		}
		managed, ok := is.Labels[v1.SamplesManagedLabel]
		if !ok {
			logrus.Warningf("numOfManagedImageStreamsPresent: stream %s does not have managed label", key)
			continue
		}
		if managed != "true" {
			logrus.Warningf("numOfManagedImageStreamsPresent: stream %s has managed value of %s but is not in internal skip list", key, managed)
			continue
		}
		rc++
	}
	return rc
}
