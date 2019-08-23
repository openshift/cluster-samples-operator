package stub

import (
	"fmt"
	"strings"

	imagev1 "github.com/openshift/api/image/v1"
	v1 "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	"github.com/openshift/cluster-samples-operator/pkg/cache"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (h *Handler) processImageStreamWatchEvent(is *imagev1.ImageStream, deleted bool) error {
	cfg, filePath, doUpsert, err := h.prepSamplesWatchEvent("imagestream", is.Name, is.Annotations, deleted)
	if cfg != nil && cfg.ConditionTrue(v1.ImageChangesInProgress) {
		logrus.Printf("Imagestream %s watch event do upsert %v; no errors in prep %v,  possibly update operator conditions %v", is.Name, doUpsert, err == nil, cfg != nil)
	} else {
		logrus.Debugf("Imagestream %s watch event do upsert %v; no errors in prep %v,  possibly update operator conditions %v", is.Name, doUpsert, err == nil, cfg != nil)
	}
	if !doUpsert {
		if err != nil {
			return err
		}
		if cfg == nil {
			return nil
		}

		// if we initiated the change, it will be in our cache, and we want to process
		// the in progress, import error conditions
		anyChange := false
		if cache.UpsertsAmount() > 0 {
			cache.AddReceivedEventFromUpsert(is)
			if !cfg.ConditionTrue(v1.ImageChangesInProgress) || !cfg.ConditionTrue(v1.SamplesExist) {
				logrus.Printf("caching imagestream event %s because we have not yet completed all the samples upserts", is.Name)
				return nil
			}
			if !cache.AllUpsertEventsArrived() {
				logrus.Printf("caching imagestream event %s because we have not received all %d imagestream events", is.Name, cache.UpsertsAmount())
				return nil
			}
			streams := cache.GetUpsertImageStreams()
			keysToClear := []string{}
			cfg = h.refetchCfgMinimizeConflicts(cfg)
			for key, is := range streams {
				if is == nil {
					// never got update, refetch
					var e error
					is, e = h.imageclientwrapper.Get(key)
					if e != nil {
						keysToClear = append(keysToClear, key)
						anyChange = true
						continue
					}
				}

				// see if we should turn off pending, clear errors
				nonMatchDetail := ""
				cfg, nonMatchDetail, anyChange = h.processImportStatus(is, cfg)
				if !anyChange {
					logrus.Printf("imagestream %s still not finished with its image imports, including %s", is.Name, nonMatchDetail)
					// cached instances are processed at the end of the handler Config event flow
					return nil
				}
			}
			for _, key := range keysToClear {
				cache.RemoveUpsert(key)
				cfg.ClearNameInReason(cfg.Condition(v1.ImageChangesInProgress).Reason, key)
				cfg.ClearNameInReason(cfg.Condition(v1.ImportImageErrorsExist).Reason, key)
			}
			// at this point, all the processImportStatus calls have come back with anyChange == true
			// unless we were unable to fetch any of the imagestreams
			// and if necessary processImportStatus would have set in progress to false as needed;
			if anyChange {
				dbg := fmt.Sprintf("updating progress/error condition (within caching block) after results for %s", is.Name)
				logrus.Printf("CRDUPDATE %s", dbg)
				err = h.crdwrapper.UpdateStatus(cfg, dbg)
				// we used to not clear the cache until we confirmed the update occurred; but for
				// whatever reason, we started seeing more conflicts here on the k8s 1.13 rebase;
				// and it turns out, after the `cache.UpsertsAmount() > 0` check above it is tricky
				// to discern a retry because of an err here vs. our not having finished upserts yet;
				// so since we have decided the condition, most likely we are done; so clear the cache
				// and fall into our steady state process imagestream event individually
				cache.ClearUpsertsCache()
			}
			return err
		}
		// otherwise, if someone else changed it in such a way that we don't want to
		// upsert it again, or we missed an event and this is a relist, or we had an update conflict
		// after completing cache process, update conditions as needed
		// as needed
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		cfg, _, anyChange = h.processImportStatus(is, cfg)
		if anyChange {
			dbg := fmt.Sprintf("updating progress/error condition after results for %s", is.Name)
			logrus.Printf("CRDUPDATE %s", dbg)
			return h.crdwrapper.UpdateStatus(cfg, dbg)
		}
		return nil

	}

	// prepWatchEvent has determined we actually need to do the upsert

	if cfg == nil {
		return fmt.Errorf("cannot upsert imagestream %s because could not obtain Config", is.Name)
	}

	if cfg.ClusterNeedsCreds() {
		return fmt.Errorf("cannot upsert imagestream %s because rhel credentials do not exist", is.Name)
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
	progressing := cfg.Condition(v1.ImageChangesInProgress)
	now := kapis.Now()
	progressing.LastUpdateTime = now
	progressing.LastTransitionTime = now
	logrus.Debugf("Handle changing processing from false to true for imagestream %s", imagestream.Name)
	progressing.Status = corev1.ConditionTrue
	if !cfg.NameInReason(progressing.Reason, imagestream.Name) {
		progressing.Reason = progressing.Reason + imagestream.Name + " "
	}
	cfg.ConditionUpdate(progressing)
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
	h.clearStreamFromImportError(imagestreamInOperatorImage.Name, opcfg.Condition(v1.ImportImageErrorsExist), opcfg)

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
	if len(opcfg.Spec.SamplesRegistry) == 0 {
		return
	}

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

func (h *Handler) clearStreamFromImportError(name string, importError *v1.ConfigCondition, cfg *v1.Config) *v1.ConfigCondition {
	if cfg.NameInReason(importError.Reason, name) {
		logrus.Printf("clearing imagestream %s from the import image error condition", name)
	}
	oldStatus := importError.Status
	start := strings.Index(importError.Message, "<imagestream/"+name+">")
	end := strings.LastIndex(importError.Message, "<imagestream/"+name+">")
	if start >= 0 && end > 0 {
		now := kapis.Now()
		importError.Reason = cfg.ClearNameInReason(importError.Reason, name)
		entireMsg := importError.Message[start : end+len("<imagestream/"+name+">")]
		importError.Message = strings.Replace(importError.Message, entireMsg, "", -1)
		if len(strings.TrimSpace(importError.Reason)) == 0 {
			importError.Status = corev1.ConditionFalse
			importError.Reason = ""
			importError.Message = ""
		} else {
			importError.Status = corev1.ConditionTrue
		}
		if oldStatus != importError.Status {
			importError.LastTransitionTime = now
		}
		importError.LastUpdateTime = now
		cfg.ConditionUpdate(importError)
	}
	return importError
}

func (h *Handler) processImportStatus(is *imagev1.ImageStream, cfg *v1.Config) (*v1.Config, string, bool) {
	pending := false
	anyErrors := false
	importError := cfg.Condition(v1.ImportImageErrorsExist)
	nonMatchDetail := ""
	anyConditionUpdate := false

	// need to check for error conditions outside of the spec/status comparison because in an error scenario
	// you can end up with less status tags than spec tags (especially if one tag refs another), but don't cite
	// errors if we are now in the skip list; it is possible and imagestream is not in the skip list, we get an
	// import error, and then a cluster admin puts the imagestream in the skip list
	_, skipped := h.skippedImagestreams[is.Name]
	if !skipped {
		// so reaching this point means we have a prior upsert in progress, and we just want to track the status
		logrus.Debugf("checking tag spec/status for %s spec len %d status len %d", is.Name, len(is.Spec.Tags), len(is.Status.Tags))

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
					if condition.Status == corev1.ConditionFalse &&
						len(condition.Message) > 0 {
						if condition.Generation > mostRecentErrorGeneration {
							mostRecentErrorGeneration = condition.Generation
							message = condition.Message
						}
					}
				}
				if mostRecentErrorGeneration > 0 && mostRecentErrorGeneration >= latestGeneration {
					logrus.Warningf("Image import for imagestream %s tag %s generation %v failed with detailed message %s", is.Name, statusTag.Tag, mostRecentErrorGeneration, message)
					anyErrors = true
					// add this imagestream to the Reason field
					if !cfg.NameInReason(importError.Reason, is.Name) {
						now := kapis.Now()
						importError.Reason = importError.Reason + is.Name + " "
						importError.Message = importError.Message + "<imagestream/" + is.Name + ">" + message + "<imagestream/" + is.Name + ">"
						importError.Status = corev1.ConditionTrue
						importError.LastTransitionTime = now
						importError.LastUpdateTime = now
						cfg.ConditionUpdate(importError)
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
						imgImport, err = h.imageclient.ImageStreamImports("openshift").Create(imgImport)
						if err != nil {
							logrus.Warningf("attempted to initiate an imagestreamimport retry for imagestream/tag %s/%s but got err %v; simply moving on", is.Name, statusTag.Tag, err)
							break
						}
						logrus.Printf("initiated an imagestreamimport retry for imagestream/tag %s/%s", is.Name, statusTag.Tag)

					}
					break
				}
			}
		}

		if !anyErrors {
			// it is possible after an upgrade that tags can be removed because of EOL processing;
			// since we do not delete those EOL images from the imagestream status (so as to not break
			// existing deployments referencing specific tags), it is possible that a valid configuration
			// has less tags in the spec than the status (vs. an equal amount being the only valid combination)
			if len(is.Spec.Tags) <= len(is.Status.Tags) {
				for _, specTag := range is.Spec.Tags {
					matched := false
					for _, statusTag := range is.Status.Tags {
						logrus.Debugf("checking spec tag %s against status tag %s with num items %d", specTag.Name, statusTag.Tag, len(statusTag.Items))
						if specTag.Name == statusTag.Tag {
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
									nonMatchDetail = fmt.Sprintf("spec tag %s is at generation %d, but status tag %s is at generation %d", specTag.Name, *specTag.Generation, statusTag.Tag, event.Generation)
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
				nonMatchDetail = "the number of status tags is less than the number of spec tags"
			}
		}
	} else {
		logrus.Debugf("no error/progress checks cause stream %s is skipped", is.Name)
		// but if skipped, clear out any errors, since we do not care about errors for skipped
		h.clearStreamFromImportError(is.Name, importError, cfg)
	}

	processing := cfg.Condition(v1.ImageChangesInProgress)

	logrus.Debugf("pending is %v any errors %v for %s", pending, anyErrors, is.Name)

	// we check for processing == true here as well to avoid churn on relists
	if !pending {
		if !anyErrors {
			h.clearStreamFromImportError(is.Name, importError, cfg)
		}
		if processing.Status == corev1.ConditionTrue {
			now := kapis.Now()
			// remove this imagestream name, including the space separator, from processing
			logrus.Debugf("current reason %s ", processing.Reason)
			replaceOccurs := cfg.NameInReason(processing.Reason, is.Name)
			if replaceOccurs {
				processing.Reason = cfg.ClearNameInReason(processing.Reason, is.Name)
				logrus.Debugf("processing reason now %s", processing.Reason)
				if len(strings.TrimSpace(processing.Reason)) == 0 && processing.Status != corev1.ConditionFalse {
					logrus.Println("The last in progress imagestream has completed (import status check)")
					processing.Status = corev1.ConditionFalse
					processing.Reason = ""
				}
				processing.LastTransitionTime = now
				processing.LastUpdateTime = now
				cfg.ConditionUpdate(processing)
				logrus.Printf("There are no more image imports in flight for imagestream %s", is.Name)
				anyConditionUpdate = true
			}
		}
	}

	// clear out error for this stream if there were errors previously but no longer are
	// think a scheduled import failing then recovering
	if cfg.NameInReason(importError.Reason, is.Name) && !anyErrors {
		logrus.Printf("There are no image import errors for %s", is.Name)
		h.clearStreamFromImportError(is.Name, importError, cfg)
	}

	return cfg, nonMatchDetail, anyConditionUpdate
}
