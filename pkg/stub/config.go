package stub

import (
	"fmt"
	"strings"

	operatorsv1api "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (h *Handler) ClearStatusConfigForRemoved(cfg *v1.Config) {
	cfg.Status.InstallType = ""
	cfg.Status.Architectures = []string{}
}

func (h *Handler) StoreCurrentValidConfig(cfg *v1.Config) {
	cfg.Status.SamplesRegistry = cfg.Spec.SamplesRegistry
	cfg.Status.InstallType = cfg.Spec.InstallType
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
func (h *Handler) VariableConfigChanged(cfg *v1.Config) (bool, bool, bool) {
	if cfg.Spec.SamplesRegistry != cfg.Status.SamplesRegistry {
		logrus.Printf("SamplesRegistry changed from %s to %s", cfg.Status.SamplesRegistry, cfg.Spec.SamplesRegistry)
		return true, true, false
	}

	logrus.Debugf("cfg skipped streams %#v", cfg.Spec.SkippedImagestreams)

	if len(cfg.Spec.SkippedImagestreams) != len(cfg.Status.SkippedImagestreams) {
		logrus.Printf("SkippedImagestreams changed in size from %#v to %#v", cfg.Status.SkippedImagestreams, cfg.Spec.SkippedImagestreams)
		if len(cfg.Spec.SkippedImagestreams) < len(cfg.Status.SkippedImagestreams) {
			// skip list reduced, meaning we need to upsert some samples we were ignoring
			return true, true, false
		}
		for _, stream := range cfg.Status.SkippedImagestreams {
			// even if the skipped list has been increased, if a stream we were skipping
			// has been removed, we need to upsert; assumes buildSkipFilters called beforehand
			if _, ok := h.skippedImagestreams[stream]; !ok {
				return true, true, true
			}
		}
		// otherwise, we've only added to the skip list, so don't upsert,but also see if we
		// need to  update the cfg from the main loop to clear out any prior image import
		// errors for skipped streams
		clearImageImportErrors := false
		for _, stream := range cfg.Spec.SkippedImagestreams {
			importErrors := cfg.Condition(v1.ImportImageErrorsExist)
			beforeError := cfg.NameInReason(importErrors.Reason, stream)
			importErrors = h.clearStreamFromImportError(stream, cfg.Condition(v1.ImportImageErrorsExist), cfg)
			afterError := cfg.NameInReason(importErrors.Reason, stream)
			if beforeError && !afterError {
				clearImageImportErrors = true
				// we do not break here cause we want to clear out all possible streams
			}
		}
		return true, false, clearImageImportErrors
	}

	clearImageImportErrors := false
	changeInContent := false
	for i, skip := range cfg.Spec.SkippedImagestreams {
		if skip != cfg.Status.SkippedImagestreams[i] {
			changeInContent = true
			importErrors := cfg.Condition(v1.ImportImageErrorsExist)
			beforeError := cfg.NameInReason(importErrors.Reason, skip)
			logrus.Printf("SkippedImagestreams changed in content from %s to %s", cfg.Status.SkippedImagestreams[i], skip)
			importErrors = h.clearStreamFromImportError(skip, importErrors, cfg)
			afterError := cfg.NameInReason(importErrors.Reason, skip)
			if beforeError && !afterError {
				clearImageImportErrors = true
			}
		}
	}
	if changeInContent {
		return changeInContent, changeInContent, clearImageImportErrors
	}

	if len(cfg.Spec.SkippedTemplates) != len(cfg.Status.SkippedTemplates) {
		logrus.Printf("SkippedTemplates changed from %#v to %#v", cfg.Status.SkippedTemplates, cfg.Spec.SkippedTemplates)
		if len(cfg.Spec.SkippedTemplates) < len(cfg.Status.SkippedTemplates) {
			return true, true, false
		}
		for _, tpl := range cfg.Status.SkippedTemplates {
			// even if the skipped list has been increased, if a tpl we were skipping
			// has been removed, we need to upsert; assumes buildSkipFilters called beforehand
			if _, ok := h.skippedTemplates[tpl]; !ok {
				return true, true, false
			}
		}
		return true, false, false
	}

	for i, skip := range cfg.Spec.SkippedTemplates {
		if skip != cfg.Status.SkippedTemplates[i] {
			logrus.Printf("SkippedTemplates changed in content from %s to %s", cfg.Status.SkippedTemplates[i], skip)
			return true, true, false
		}
	}

	logrus.Debugf("Incoming Config unchanged from last processed version")
	return false, false, false
}

func (h *Handler) buildSkipFilters(opcfg *v1.Config) {
	h.mapsMutex.Lock()
	defer h.mapsMutex.Unlock()
	newStreamMap := make(map[string]bool)
	newTempMap := make(map[string]bool)
	for _, st := range opcfg.Status.SkippedTemplates {
		newTempMap[st] = true
	}
	for _, si := range opcfg.Status.SkippedImagestreams {
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
		if cfg.ConditionTrue(v1.ImageChangesInProgress) && cfg.ConditionTrue(v1.RemovePending) {
			return false, false, nil
		}

		if cfg.ConditionTrue(v1.ImageChangesInProgress) && !cfg.ConditionTrue(v1.RemovePending) {
			now := kapis.Now()
			condition := cfg.Condition(v1.RemovePending)
			condition.LastTransitionTime = now
			condition.LastUpdateTime = now
			condition.Status = corev1.ConditionTrue
			cfg.ConditionUpdate(condition)
			return false, true, nil
		}

		// turn off on hold if need be
		if cfg.ConditionTrue(v1.RemovePending) && cfg.ConditionFalse(v1.ImageChangesInProgress) {
			now := kapis.Now()
			condition := cfg.Condition(v1.RemovePending)
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
			h.ClearStatusConfigForRemoved(cfg)
			return false, true, nil
		}
		return false, false, nil
	case operatorsv1api.Managed:
		if cfg.Spec.ManagementState != cfg.Status.ManagementState {
			logrus.Println("management state set to managed")
			if cfg.Spec.InstallType == v1.RHELSamplesDistribution &&
				cfg.ConditionFalse(v1.ImportCredentialsExist) {
				h.copyDefaultClusterPullSecret(nil)
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
