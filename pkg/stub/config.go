package stub

import (
	"fmt"
	"strings"

	operatorsv1api "github.com/openshift/api/operator/v1"
	v1 "github.com/openshift/api/samples/v1"
	"github.com/openshift/cluster-samples-operator/pkg/metrics"
	"github.com/openshift/cluster-samples-operator/pkg/util"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (h *Handler) ClearStatusConfigForRemoved(cfg *v1.Config) {
	cfg.Status.Architectures = []string{}
}

func (h *Handler) StoreCurrentValidConfig(cfg *v1.Config) {
	cfg.Status.SamplesRegistry = cfg.Spec.SamplesRegistry
	cfg.Status.Architectures = cfg.Spec.Architectures
	cfg.Status.SkippedImagestreams = cfg.Spec.SkippedImagestreams
	cfg.Status.SkippedTemplates = cfg.Spec.SkippedTemplates
}

func (h *Handler) SpecValidation(cfg *v1.Config) error {
	// the first thing this should do is check that all the config values
	// are "valid" (the architecture name is known, the distribution name is known, etc)
	// if that fails, we should immediately error out and set ConfigValid to false.
	for _, arch := range cfg.Spec.Architectures {
		switch arch {
		case v1.X86Architecture:
		case v1.AMDArchitecture:
		case v1.PPCArchitecture:
		case v1.S390Architecture:
		default:
			err := fmt.Errorf("architecture %s unsupported; only support %s", arch, strings.Join([]string{v1.X86Architecture, v1.AMDArchitecture, v1.PPCArchitecture, v1.S390Architecture}, ","))
			return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
		}
	}

	// only if the values being requested are valid, should we then proceed to check
	// them against the previous values(if we've stored any previous values)

	// if we have not had a valid Config processed, allow caller to try with
	// the cfg contents
	if !util.ConditionTrue(cfg, v1.SamplesExist) && !util.ConditionTrue(cfg, v1.ImageChangesInProgress) {
		logrus.Println("Spec is valid because this operator has not processed a config yet")
		return nil
	}

	if len(cfg.Status.Architectures) > 0 {
		if len(cfg.Status.Architectures) != len(cfg.Spec.Architectures) {
			err := fmt.Errorf("cannot change architectures from %#v to %#v", cfg.Status.Architectures, cfg.Spec.Architectures)
			return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
		}
		for i, arch := range cfg.Status.Architectures {
			// make 'em keep the order consistent ;-/
			if arch != cfg.Spec.Architectures[i] {
				err := fmt.Errorf("cannot change architectures from %s to %s", strings.TrimSpace(strings.Join(cfg.Status.Architectures, " ")), strings.TrimSpace(strings.Join(cfg.Spec.Architectures, " ")))
				return h.processError(cfg, v1.ConfigurationValid, corev1.ConditionFalse, err, "%v")
			}
		}
	}
	h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.ConfigurationValid)
	return nil
}

// VariableConfigChanged return variable explanations
// first boolean: did the config change at all
// second boolean: does the config change require a samples upsert; for example, simply adding
// to a skip list does not require a samples upsert
// third boolean: even if an upsert is not needed, update the config instance to clear out image import errors
// fourth boolean: if the registry changed, that means a) we have to update all imagestreams regardless of skip lists;
// and b) we don't have to update the templates if that is the only change
// first map: imagestreams that were unskipped (imagestream updates can be expensive, so an optimization)
// second map: templates that were unskipped
func (h *Handler) VariableConfigChanged(cfg *v1.Config) (bool, bool, bool, bool, map[string]bool, map[string]bool) {
	configChangeAtAll := false
	configChangeRequireUpsert := false
	clearImageImportErrors := false
	registryChange := false

	logrus.Debugf("the before skipped templates %#v", cfg.Status.SkippedTemplates)
	logrus.Debugf("the after skipped templates %#v and associated map %#v", cfg.Spec.SkippedTemplates, h.skippedTemplates)
	logrus.Debugf("the before skipped streams %#v", cfg.Status.SkippedImagestreams)
	logrus.Debugf("the after skipped streams %#v and associated map %#v", cfg.Spec.SkippedImagestreams, h.skippedImagestreams)

	unskippedTemplates := map[string]bool{}
	// capture additions/subtractions from skipped list in general change boolean
	if len(cfg.Spec.SkippedTemplates) != len(cfg.Status.SkippedTemplates) {
		configChangeAtAll = true
	}

	// build the list of skipped templates; assumes buildSkipFilters called beforehand
	for _, tpl := range cfg.Status.SkippedTemplates {
		// if something that was skipped (in Status list) is no longer skipped, build list/capture that fact
		// this should capture reductions in the skipped list, as well as changing entries, but the number of skips
		// staying the same
		if _, ok := h.skippedTemplates[tpl]; !ok {
			unskippedTemplates[tpl] = true
			configChangeAtAll = true // set to true in case sizes are equal, but contents have changed
			configChangeRequireUpsert = true
		}
	}

	if configChangeAtAll {
		logrus.Printf("SkippedTemplates changed from %#v to %#v", cfg.Status.SkippedTemplates, cfg.Spec.SkippedTemplates)
	}

	unskippedStreams := map[string]bool{}
	if cfg.Spec.SamplesRegistry != cfg.Status.SamplesRegistry {
		logrus.Printf("SamplesRegistry changed from %s to %s", cfg.Status.SamplesRegistry, cfg.Spec.SamplesRegistry)
		configChangeAtAll = true
		configChangeRequireUpsert = true
		registryChange = true
		return configChangeAtAll, configChangeRequireUpsert, clearImageImportErrors, registryChange, unskippedStreams, unskippedTemplates
	}

	streamChange := false
	// capture additions/subtractions from skipped list in general change boolean
	if len(cfg.Spec.SkippedImagestreams) != len(cfg.Status.SkippedImagestreams) {
		configChangeAtAll = true
		streamChange = true
	}

	streamsThatWereSkipped := map[string]bool{}
	// build the list of skipped imagestreams; assumes buildSkipFilters called beforehand
	for _, stream := range cfg.Status.SkippedImagestreams {
		streamsThatWereSkipped[stream] = true
		// if something that was skipped (in Status list) is no longer skipped, build list/capture that fact
		// this should capture reductions in the skipped list, as well as changing entries, but the number of skips
		// staying the same
		if _, ok := h.skippedImagestreams[stream]; !ok {
			unskippedStreams[stream] = true
			configChangeAtAll = true // set to true in case sizes are equal, but contents have changed
			configChangeRequireUpsert = true
			streamChange = true
		}
	}

	if streamChange {
		logrus.Printf("SkippedImagestreams changed from %#v to %#v", cfg.Status.SkippedImagestreams, cfg.Spec.SkippedImagestreams)
	}

	// see if need to update the cfg from the main loop to clear out any prior image import
	// errors for skipped streams
	for _, stream := range cfg.Spec.SkippedImagestreams {
		importErrors := util.Condition(cfg, v1.ImportImageErrorsExist)
		beforeError := util.NameInReason(cfg, importErrors.Reason, stream)
		h.clearStreamFromImportError(stream, util.Condition(cfg, v1.ImportImageErrorsExist), cfg)
		if beforeError {
			clearImageImportErrors = true
			// we do not break here cause we want to clear out all possible streams
		}
	}

	return configChangeAtAll, configChangeRequireUpsert, clearImageImportErrors, registryChange, unskippedStreams, unskippedTemplates
}

func (h *Handler) buildSkipFilters(opcfg *v1.Config) {
	h.mapsMutex.Lock()
	defer h.mapsMutex.Unlock()
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

func (h *Handler) buildFileMaps(cfg *v1.Config, forceRebuild bool) error {
	h.mapsMutex.Lock()
	defer h.mapsMutex.Unlock()
	if len(h.imagestreamFile) == 0 || len(h.templateFile) == 0 || forceRebuild {
		for _, arch := range cfg.Spec.Architectures {
			dir := h.GetBaseDir(arch, cfg)
			files, err := h.Filefinder.List(dir)
			if err != nil {
				cfg = h.refetchCfgMinimizeConflicts(cfg)
				err = h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error reading in content : %v")
				dbg := "file list err update"
				logrus.Printf("CRDUPDATE %s", dbg)
				h.crdwrapper.UpdateStatus(cfg, dbg)
				return err
			}
			metrics.ClearStreams()
			err = h.processFiles(dir, files, cfg)
			if err != nil {
				cfg = h.refetchCfgMinimizeConflicts(cfg)
				err = h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error processing content : %v")
				dbg := "proc file err update"
				logrus.Printf("CRDUPDATE %s", dbg)
				h.crdwrapper.UpdateStatus(cfg, dbg)
				return err
			}
		}
	}
	return nil
}

func (h *Handler) processError(opcfg *v1.Config, ctype v1.ConfigConditionType, cstatus corev1.ConditionStatus, err error, msg string, args ...interface{}) error {
	log := ""
	if args == nil {
		log = fmt.Sprintf(msg, err)
	} else {
		log = fmt.Sprintf(msg, err, args)
	}
	logrus.Println(log)
	status := util.Condition(opcfg, ctype)
	// decision was made to not spam master if
	// duplicate events come it (i.e. status does not
	// change)
	if status.Status != cstatus || status.Message != log {
		now := kapis.Now()
		status.LastUpdateTime = now
		if status.Status != cstatus {
			status.LastTransitionTime = now
		}
		status.Status = cstatus
		status.Message = log
		util.ConditionUpdate(opcfg, status)
	}

	switch ctype {
	case v1.ConfigurationValid:
		metrics.ConfigInvalid(true)
	}

	// return original error
	return err
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
		if cfg.Status.ManagementState != operatorsv1api.Removed && !util.ConditionTrue(cfg, v1.RemovePending) {
			now := kapis.Now()
			condition := util.Condition(cfg, v1.RemovePending)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionTrue
			util.ConditionUpdate(cfg, condition)
			logrus.Printf("Attempting stage 1 Removed management state: RemovePending == true")
			return false, true, nil
		}

		// turn off remove pending once status mgmt state says removed
		if util.ConditionTrue(cfg, v1.RemovePending) && cfg.Status.ManagementState == operatorsv1api.Removed {
			now := kapis.Now()
			condition := util.Condition(cfg, v1.RemovePending)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionFalse
			util.ConditionUpdate(cfg, condition)
			logrus.Printf("Attempting stage 3 Removed management state: RemovePending == false")
			return false, true, nil
		}

		// now actually process removed state
		if cfg.Spec.ManagementState != cfg.Status.ManagementState ||
			util.ConditionTrue(cfg, v1.SamplesExist) {

			logrus.Println("management state set to removed so deleting samples")
			err := h.CleanUpOpenshiftNamespaceOnDelete(cfg)
			if err != nil {
				return false, true, h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "The error %v during openshift namespace cleanup has left the samples in an unknown state")
			}
			// explicitly reset exist/inprogress/error to false
			now := kapis.Now()
			conditionsToSet := []v1.ConfigConditionType{v1.SamplesExist, v1.ImageChangesInProgress, v1.ImportImageErrorsExist}
			for _, c := range conditionsToSet {
				condition := util.Condition(cfg, c)
				condition.LastTransitionTime = now
				condition.LastUpdateTime = now
				condition.Message = ""
				condition.Reason = ""
				condition.Status = corev1.ConditionFalse
				util.ConditionUpdate(cfg, condition)
			}
			cfg.Status.ManagementState = operatorsv1api.Removed
			// after online starter upgrade attempts while this operator was not set to managed,
			// group arch discussion has decided that we report the latest version
			cfg.Status.Version = h.version
			h.ClearStatusConfigForRemoved(cfg)
			logrus.Printf("Attempting stage 2 Removed management state: Status == Removed")
			return false, true, nil
		}

		// after online starter upgrade attempts while this operator was not set to managed,
		// group arch discussion has decided that we report the latest version
		if cfg.Status.Version != h.version {
			cfg.Status.Version = h.version
			return false, true, nil
		}
		return false, false, nil
	case operatorsv1api.Managed:
		//reset bootstrap flag
		h.tbrCheckFailed = false

		if cfg.Spec.ManagementState != cfg.Status.ManagementState {
			logrus.Println("management state set to managed")
		}
		// will set status state to managed at top level caller
		// to deal with config change processing
		return true, false, nil
	case operatorsv1api.Unmanaged:
		//reset bootstrap flag
		h.tbrCheckFailed = false

		if cfg.Spec.ManagementState != cfg.Status.ManagementState {
			logrus.Println("management state set to unmanaged")
			cfg.Status.ManagementState = operatorsv1api.Unmanaged
			// after online starter upgrade attempts while this operator was not set to managed,
			// group arch discussion has decided that we report the latest version
			cfg.Status.Version = h.version
			if util.ConditionTrue(cfg, v1.RemovePending) {
				now := kapis.Now()
				condition := util.Condition(cfg, v1.RemovePending)
				condition.LastTransitionTime = now
				condition.LastUpdateTime = now
				condition.Status = corev1.ConditionFalse
				util.ConditionUpdate(cfg, condition)
			}
			return false, true, nil
		}

		// after online starter upgrade attempts while this operator was not set to managed,
		// group arch discussion has decided that we report the latest version
		if cfg.Status.Version != h.version {
			cfg.Status.Version = h.version
			return false, true, nil
		}
		return false, false, nil
	default:
		// force it to Managed if they passed in something funky, including the empty string
		logrus.Warningf("Unknown management state %s specified; switch to Managed", cfg.Spec.ManagementState)
		cfgvalid := util.Condition(cfg, v1.ConfigurationValid)
		cfgvalid.Message = fmt.Sprintf("Unexpected management state %v received, switching to %v", cfg.Spec.ManagementState, operatorsv1api.Managed)
		now := kapis.Now()
		cfgvalid.LastTransitionTime = now
		cfgvalid.LastUpdateTime = now
		cfgvalid.Status = corev1.ConditionTrue
		metrics.ConfigInvalid(false)
		util.ConditionUpdate(cfg, cfgvalid)
		return true, false, nil
	}
}
