package stub

import (
	"fmt"
	"strings"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (h *Handler) processImageStreamWatchEvent(is *imagev1.ImageStream, deleted bool) error {
	// our pattern is the top most caller locks the mutex

	cfg, filePath, doUpsert, err := h.prepSamplesWatchEvent("imagestream", is.Name, is.Annotations, deleted)
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
		// need to check for error conditions outside of the spec/status comparison because in an error scenario
		// you can end up with less status tags than spec tags (especially if one tag refs another)
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
				if mostRecentErrorGeneration >= latestGeneration {
					logrus.Warningf("Image import for imagestream %s tag %s generation %v failed with detailed message %s", is.Name, statusTag.Tag, mostRecentErrorGeneration, message)
					anyErrors = true
					// add this imagestream to the Reason field
					if !strings.Contains(importError.Reason, is.Name+" ") {
						importError.Reason = importError.Reason + is.Name + " "
						importError.Message = importError.Message + "imagestream/" + is.Name + ": " + message + "; "
					}
					break
				}
			}
		}
		if !anyErrors {
			if len(is.Spec.Tags) == len(is.Status.Tags) {
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
			logrus.Debugf("CRDUPDATE no migration / no pending / no error imgstr update")
			return h.crdwrapper.UpdateStatus(cfg)
		}

		// clear out error for this stream if there were errors previously but no longer are
		// think a scheduled import failing then recovering
		if strings.Contains(importError.Reason, is.Name) && !anyErrors {
			now := kapis.Now()
			importError.Reason = strings.Replace(importError.Reason, is.Name+" ", "", -1)
			if len(strings.TrimSpace(importError.Reason)) == 0 {
				importError.Status = corev1.ConditionFalse
				importError.Reason = ""
				importError.Message = ""
			} else {
				importError.Status = corev1.ConditionTrue
			}
			importError.LastTransitionTime = now
			importError.LastUpdateTime = now
			cfg.ConditionUpdate(importError)
			logrus.Debugf("CRDUPDATE no error imgstr update")
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
		logrus.Debugf("CRDUPDATE event img update err bad fs read")
		h.crdwrapper.UpdateStatus(cfg)
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
		logrus.Debugf("CRDUPDATE event img update err bad api obj update")
		h.crdwrapper.UpdateStatus(cfg)
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
	logrus.Debugf("CRDUPDATE progressing true update for imagestream %s", imagestream.Name)
	return h.crdwrapper.UpdateStatus(cfg)

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
