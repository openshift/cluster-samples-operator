package stub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"

	imagev1 "github.com/openshift/api/image/v1"
	v1 "github.com/openshift/api/samples/v1"

	"github.com/openshift/cluster-samples-operator/pkg/cache"
	"github.com/openshift/cluster-samples-operator/pkg/metrics"
	"github.com/openshift/cluster-samples-operator/pkg/util"
)

func (h *Handler) processImageStreamWatchEvent(is *imagev1.ImageStream, deleted bool) error {
	cfg, filePath, doUpsert, updateCfgOnly, err := h.prepSamplesWatchEvent("imagestream", is.Name, is.Annotations, deleted)
	if cfg != nil && updateCfgOnly {
		dbg := fmt.Sprintf("clear out removed imagestream %s from progressing/error", is.Name)
		logrus.Printf("CRDUPDATE %s", dbg)
		return h.crdwrapper.UpdateStatus(cfg, dbg)
	}
	logrus.Debugf("Imagestream %s watch event do upsert %v; no errors in prep %v,  possibly update operator conditions %v", is.Name, doUpsert, err == nil, cfg != nil)
	if cfg != nil {
		if util.IsUnsupportedArch(cfg) {
			logrus.Printf("ignoring watch event for imagestream %s ignored because we are on %s",
				is.Name, cfg.Spec.Architectures[0])
			return nil
		}
	}
	if !doUpsert {
		if err != nil {
			return err
		}
		if cfg == nil {
			return nil
		}
		cfg = h.refetchCfgMinimizeConflicts(cfg)

		// if we initiated the change, it will be in our cache, and we want to process
		// the in progress, import error conditions
		anyChange := false
		//nonMatchDetail := ""
		if cache.UpsertsAmount() > 0 {
			cache.AddReceivedEventFromUpsert(is)
			if !util.ConditionTrue(cfg, v1.ImageChangesInProgress) || !util.ConditionTrue(cfg, v1.SamplesExist) {
				logrus.Printf("caching imagestream event %s because we have not yet completed all the samples upserts", is.Name)
				return nil
			}
			if !cache.AllUpsertEventsArrived() {
				logrus.Printf("caching imagestream event %s because we have not received all %d imagestream events", is.Name, cache.UpsertsAmount())
				return nil
			}
			cache.ClearUpsertsCache()
		}

		_, skipped := h.skippedImagestreams[is.Name]
		writeStatus := false
		dbg := ""
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		if util.ConditionTrue(cfg, v1.ImageChangesInProgress) && cache.AllUpsertEventsArrived() {
			dbg = "clearing in progress with every imagestream reporting in"
			writeStatus = true
			h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
		}
		if skipped && util.NameInReason(cfg, util.Condition(cfg, v1.ImportImageErrorsExist).Reason, is.Name) {
			writeStatus = true
			if len(dbg) > 0 {
				dbg = dbg + "; "
			}
			dbg = fmt.Sprintf("%sclear %s from import errors", dbg, is.Name)
			h.clearStreamFromImportError(is.Name, util.Condition(cfg, v1.ImportImageErrorsExist), cfg)
		}
		if !skipped {
			cfg, anyChange = h.processImportStatus(is, cfg)
			if anyChange {
				writeStatus = true
				if len(dbg) > 0 {
					dbg = dbg + "; "
				}
				dbg = fmt.Sprintf("%supdate import errors for %s", dbg, is.Name)
			}
		}
		if writeStatus {
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}

		return nil

	}

	// prepWatchEvent has determined we actually need to do the upsert

	if cfg == nil {
		// prepSamplesWatch will handle logging here as needed, as well as provide the appropriate
		// non-nil or nil setting for err
		return err
	}

	imagestream, err := h.Fileimagegetter.Get(filePath)
	if err != nil {
		// still attempt to report error in status
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", filePath)
		dbg := fmt.Sprintf("event img update err bad fs read %s", filePath)
		logrus.Printf("CRDUPDATE %s", dbg)
		h.crdwrapper.UpdateStatus(cfg, dbg)
		// if we get this, don't bother retrying
		return nil
	}
	if deleted {
		// set is to nil so upsert will create
		is = nil
	}

	cache.AddUpsert(imagestream.Name)

	if is != nil {
		is = is.DeepCopy()
	}
	err = h.upsertImageStream(imagestream, is, cfg)
	if err != nil {
		cache.RemoveUpsert(imagestream.Name)
		if kerrors.IsAlreadyExists(err) {
			// it the main loop created, we will let it set the image change reason field
			return nil
		}
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error replacing imagestream %s", imagestream.Name)
		dbg := fmt.Sprintf("CRDUPDATE event img update err bad api obj update %s", imagestream.Name)
		logrus.Printf("CRDUPDATE %s", dbg)
		return h.crdwrapper.UpdateStatus(cfg, dbg)
	}
	// now update progressing condition
	cfg = h.refetchCfgMinimizeConflicts(cfg)
	progressing := util.Condition(cfg, v1.ImageChangesInProgress)
	now := kapis.Now()
	progressing.LastUpdateTime = now
	progressing.LastTransitionTime = now
	logrus.Debugf("Handle changing processing from false to true for imagestream %s", imagestream.Name)
	progressing.Status = corev1.ConditionTrue
	if !util.NameInReason(cfg, progressing.Reason, imagestream.Name) {
		progressing.Reason = progressing.Reason + imagestream.Name + " "
	}
	util.ConditionUpdate(cfg, progressing)
	dbg := fmt.Sprintf("progressing true update for imagestream %s", imagestream.Name)
	logrus.Printf("CRDUPDATE %s", dbg)
	return h.crdwrapper.UpdateStatus(cfg, dbg)

}

func (h *Handler) upsertImageStream(imagestreamInOperatorImage, imagestreamInCluster *imagev1.ImageStream, opcfg *v1.Config) error {
	// handle jenkins mutations if needed
	imagestreamInOperatorImage = jenkinsOverrides(imagestreamInOperatorImage)

	// whether we are now skipping this imagestream, or are upserting it, remove any prior errors from the import error
	// condition; in the skip case, we don't want errors to a now skipped stream blocking availability status; in the upsert
	// case, any errors will cause the imagestream controller to attempt another image import
	h.mapsMutex.Lock()
	defer h.mapsMutex.Unlock()
	h.clearStreamFromImportError(imagestreamInOperatorImage.Name, util.Condition(opcfg, v1.ImportImageErrorsExist), opcfg)

	if _, isok := h.skippedImagestreams[imagestreamInOperatorImage.Name]; isok {
		if imagestreamInCluster != nil {
			if imagestreamInCluster.Labels == nil {
				imagestreamInCluster.Labels = make(map[string]string)
			}
			imagestreamInCluster.Labels[v1.SamplesManagedLabel] = "false"
			h.imageclientwrapper.Update(imagestreamInCluster)
			// if we get an error, we'll just try to remove the label next
			// time; and we'll examine the skipped lists on delete
		}
		return nil
	}

	h.updateDockerPullSpec([]string{"docker.io", "registry.redhat.io", "registry.access.redhat.com", "quay.io", "registry.svc.ci.openshift.org"}, imagestreamInOperatorImage, opcfg)

	if imagestreamInOperatorImage.Labels == nil {
		imagestreamInOperatorImage.Labels = make(map[string]string)
	}
	if imagestreamInOperatorImage.Annotations == nil {
		imagestreamInOperatorImage.Annotations = make(map[string]string)
	}
	imagestreamInOperatorImage.Labels[v1.SamplesManagedLabel] = "true"
	imagestreamInOperatorImage.Annotations[v1.SamplesVersionAnnotation] = h.version

	if imagestreamInCluster == nil {
		_, err := h.imageclientwrapper.Create(imagestreamInOperatorImage)
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

	// with the advent of removing EOL images in 4.2, we cannot just update the imagestream
	// with client-go/Update as that will replace vs. patch; we need to preserve EOL specs
	// if the imagestream was created during 4.1 as to allow pullthrough on builds for example
	// so we manually merge the tags in the existing image stream that do not exist in the one
	// in the payload
	tagsToAdd := []imagev1.TagReference{}
	for _, existingTag := range imagestreamInCluster.Spec.Tags {
		found := false
		for _, newTag := range imagestreamInOperatorImage.Spec.Tags {
			if newTag.Name == existingTag.Name {
				found = true
				break
			}
		}
		if !found {
			tagsToAdd = append(tagsToAdd, existingTag)
		}
	}
	for _, tag := range tagsToAdd {
		imagestreamInOperatorImage.Spec.Tags = append(imagestreamInOperatorImage.Spec.Tags, tag)
	}
	imagestreamInOperatorImage.ResourceVersion = imagestreamInCluster.ResourceVersion
	_, err := h.imageclientwrapper.Update(imagestreamInOperatorImage)
	if err != nil {
		return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "imagestream update error: %v")
	}
	logrus.Printf("updated imagestream %s", imagestreamInCluster.Name)
	return nil
}

func (h *Handler) updateDockerPullSpec(oldies []string, imagestream *imagev1.ImageStream, opcfg *v1.Config) {
	logrus.Debugf("updateDockerPullSpec stream %s has repo %s", imagestream.Name, imagestream.Spec.DockerImageRepository)
	// we always want to leave the jenkins images as using the payload set to the IMAGE* env's;
	switch imagestream.Name {
	case "jenkins":
		return
	case "jenkins-agent-base":
		return
	case "jenkins-agent-nodejs":
		return
	case "jenkins-agent-maven":
		return
	}

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
				return oldreg
			}
		}
		// the content from openshift/library has something odd in in ... replace the registry piece
		parts := strings.Split(oldreg, "/")
		oldreg = newreg + "/" + parts[1] + "/" + parts[2]
		logrus.Debugf("coreUpdatePull hasReg2 reg now %s", oldreg)
	} else {
		oldreg = newreg + "/" + oldreg
		logrus.Debugf("coreUpdatePull no hasReg reg now %s", oldreg)
	}

	return oldreg
}

func (h *Handler) getImporErrorMessage(name string, importError *v1.ConfigCondition) string {
	start := strings.Index(importError.Message, "<imagestream/"+name+">")
	end := strings.LastIndex(importError.Message, "<imagestream/"+name+">")
	entireMsg := ""
	if start >= 0 && end > 0 {
		entireMsg = importError.Message[start : end+len("<imagestream/"+name+">")]
	}
	return entireMsg
}

// clearStreamFromImportError assumes the caller has call h.mapsMutex.Lock() and Unlock() appropriately
func (h *Handler) clearStreamFromImportError(name string, importError *v1.ConfigCondition, cfg *v1.Config) *v1.ConfigCondition {
	if util.NameInReason(cfg, importError.Reason, name) {
		logrus.Printf("clearing imagestream %s from the import image error condition", name)
	}
	now := kapis.Now()
	importError.Reason = util.ClearNameInReason(cfg, importError.Reason, name)
	oldStatus := importError.Status
	entireMsg := h.getImporErrorMessage(name, importError)
	if len(entireMsg) > 0 {
		importError.Message = strings.Replace(importError.Message, entireMsg, "", -1)

	}
	if len(strings.TrimSpace(importError.Reason)) == 0 {
		importError.Status = corev1.ConditionFalse
		importError.Reason = ""
		importError.Message = ""
		delete(h.imagestreamRetry, name)
	} else {
		importError.Status = corev1.ConditionTrue
	}
	if oldStatus != importError.Status {
		importError.LastTransitionTime = now
	}
	importError.LastUpdateTime = now
	util.ConditionUpdate(cfg, importError)
	return importError
}

func (h *Handler) buildImageStreamErrorMessage(name, message string) string {
	return "<imagestream/" + name + ">" + message + "<imagestream/" + name + ">"
}

func (h *Handler) processImportStatus(is *imagev1.ImageStream, cfg *v1.Config) (*v1.Config, bool) {
	anyErrors := false
	importError := util.Condition(cfg, v1.ImportImageErrorsExist)
	anyConditionUpdate := false
	// in case we have to manipulate imagestream retry map
	h.mapsMutex.Lock()
	defer h.mapsMutex.Unlock()

	// need to check for error conditions outside of the spec/status comparison because in an error scenario
	// you can end up with less status tags than spec tags (especially if one tag refs another), but don't cite
	// errors if we are now in the skip list; it is possible and imagestream is not in the skip list, we get an
	// import error, and then a cluster admin puts the imagestream in the skip list
	_, skipped := h.skippedImagestreams[is.Name]
	if !skipped {
		// so reaching this point means we have a prior upsert in progress, and we just want to track the status
		logrus.Debugf("checking tag spec/status for %s spec len %d status len %d", is.Name, len(is.Spec.Tags), len(is.Status.Tags))

		// get the retry time for this imagestream
		now := kapis.Now()
		lastRetryTime, ok := h.imagestreamRetry[is.Name]
		retryIfNeeded := false
		if !ok {
			retryIfNeeded = true
		} else {
			// a little bit less than the 15 minute relist interval
			tenMinutesAgo := now.Time.Add(-10 * time.Minute)
			retryIfNeeded = lastRetryTime.Time.Before(tenMinutesAgo)
		}

		for _, statusTag := range is.Status.Tags {
			// if an error occurred with the latest generation, let's give up as we are no longer "in progress"
			// in that case as well, but mark the import failure
			if statusTag.Conditions != nil && len(statusTag.Conditions) > 0 {
				var latestGeneration int64
				var mostRecentErrorGeneration int64
				message := ""
				for _, condition := range statusTag.Conditions {
					if condition.Generation > latestGeneration {
						latestGeneration = condition.Generation
					}
					if condition.Status == corev1.ConditionFalse {
						if condition.Generation > mostRecentErrorGeneration {
							mostRecentErrorGeneration = condition.Generation
							message = condition.Message
						}
					}
				}
				if mostRecentErrorGeneration > 0 && mostRecentErrorGeneration >= latestGeneration {
					logrus.Warningf("Image import for imagestream %s tag %s generation %v failed with detailed message %s", is.Name, statusTag.Tag, mostRecentErrorGeneration, message)
					anyErrors = true
					// add this imagestream to the Reason field;
					// we don't want to initiate imports repeatedly, but we do want to retry periodically as part
					// of relist

					// if a first time failure, or need to retry
					if !util.NameInReason(cfg, importError.Reason, is.Name) ||
						retryIfNeeded {
						h.imagestreamRetry[is.Name] = now
						if !util.NameInReason(cfg, importError.Reason, is.Name) {
							importError.Reason = importError.Reason + is.Name + " "
							importError.Message = importError.Message + h.buildImageStreamErrorMessage(is.Name, message)
						}
						if importError.Status != corev1.ConditionTrue {
							importError.Status = corev1.ConditionTrue
							importError.LastTransitionTime = now
						}
						// we always bump last update here as a signal that another import attempt is happening
						importError.LastUpdateTime = now
						util.ConditionUpdate(cfg, importError)
						anyConditionUpdate = true
						// initiate a retry (if same error happens again, imagestream status does not change, we won't get an event, and we do not try again)
						imgImport, err := importTag(is, statusTag.Tag)
						if err != nil {
							logrus.Warningf("attempted to define and imagestreamimport for imagestream/tag %s/%s but got err %v; simply moving on", is.Name, statusTag.Tag, err)
							break
						}
						if imgImport == nil {
							break
						}
						imgImport, err = h.imageclientwrapper.ImageStreamImports("openshift").Create(context.TODO(), imgImport, kapis.CreateOptions{})
						if err != nil {
							logrus.Warningf("attempted to initiate an imagestreamimport retry for imagestream/tag %s/%s but got err %v; simply moving on", is.Name, statusTag.Tag, err)
							break
						}
						metrics.ImageStreamImportRetry(is.Name)
						logrus.Printf("initiated an imagestreamimport retry for imagestream/tag %s/%s", is.Name, statusTag.Tag)

					}

					// if failed previously, but the message has changed, update it
					if !strings.Contains(importError.Message, message) {
						msg := h.getImporErrorMessage(is.Name, importError)
						importError.Message = strings.Replace(importError.Message, msg, h.buildImageStreamErrorMessage(is.Name, message), -1)
						anyConditionUpdate = true
					}

					util.ConditionUpdate(cfg, importError)
				}
			}
		}
	}

	logrus.Debugf("any errors %v for %s", anyErrors, is.Name)

	logrus.Printf("There are no more image imports in flight for imagestream %s", is.Name)
	cache.RemoveUpsert(is.Name)

	// clear out error for this stream if there were errors previously but no longer are
	// think a scheduled import failing then recovering
	if util.NameInReason(cfg, importError.Reason, is.Name) && !anyErrors {
		logrus.Printf("There are no image import errors for %s", is.Name)
		h.clearStreamFromImportError(is.Name, importError, cfg)
		anyConditionUpdate = true
	}

	return cfg, anyConditionUpdate
}
