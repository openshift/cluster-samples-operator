package stub

import (
	"fmt"
	"slices"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/openshift/api/samples/v1"
	"github.com/openshift/cluster-samples-operator/pkg/util"

	"github.com/sirupsen/logrus"
)

func (h *Handler) processImageCondition() error {
	l, err := h.configmapclientwrapper.List()
	if err != nil {
		return err
	}

	list := []*corev1.ConfigMap{}
	for _, cm := range l {
		if cm.Name == util.IST2ImageMap {
			continue
		}
		list = append(list, cm)
	}

	cfg, err := h.crdwrapper.Get(v1.ConfigName)
	if err != nil {
		return err
	}

	prefix := ""
	if list == nil || len(list) == 0 {
		if h.upsertInProgress {
			return fmt.Errorf("cannot update status conditions because bulk imagestream upserts are still in progress")
		}
		if util.ConditionTrue(cfg, v1.ImageChangesInProgress) && util.ConditionTrue(cfg, v1.SamplesExist) {
			h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
			prefix = "progressing false"
		}
		if util.ConditionTrue(cfg, v1.ImportImageErrorsExist) {
			h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImportImageErrorsExist)
			if len(prefix) == 0 {
				prefix = "importerrors false"
			} else {
				prefix = prefix + "/importerrors false"
			}
		}

	} else {
		anyErrors := false
		for _, cm := range list {
			if util.ImageStreamErrorExists(cm) {
				anyErrors = true
				break

			}
		}
		switch {
		case anyErrors && util.ConditionTrue(cfg, v1.ImageChangesInProgress):
			h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImageChangesInProgress)
			prefix = "progressing false"
		}
		switch {
		case anyErrors && util.ConditionFalse(cfg, v1.ImportImageErrorsExist):
			h.GoodConditionUpdate(cfg, corev1.ConditionTrue, v1.ImportImageErrorsExist)
			importError := util.Condition(cfg, v1.ImportImageErrorsExist)
			importError.Message = h.buildImageStreamErrorMessage()
			prefix = prefix + " true"
			if len(prefix) == 0 {
				prefix = "importerrors true"
			} else {
				prefix = prefix + "/ importerrors true"
			}
		case !anyErrors && util.ConditionTrue(cfg, v1.ImportImageErrorsExist):
			h.GoodConditionUpdate(cfg, corev1.ConditionFalse, v1.ImportImageErrorsExist)
			if len(prefix) == 0 {
				prefix = "importerrors false"
			} else {
				prefix = prefix + "/ importerrors false"
			}
		}

	}
	if len(prefix) > 0 {
		dbg := fmt.Sprintf("%s update", prefix)
		logrus.Printf("CRDUPDATE %s", dbg)
		return h.crdwrapper.UpdateStatus(cfg, dbg)
	}
	return nil

}

func (h *Handler) activeImageStreams() []string {
	streams := []string{}
	list, err := h.configmapclientwrapper.List()
	if err != nil {
		return streams
	}
	if list == nil {
		list = []*corev1.ConfigMap{}
	}
	for _, cm := range list {
		if cm.Name == util.IST2ImageMap {
			continue
		}
		streams = append(streams, cm.Name)
	}
	slices.Sort(streams)
	return streams
}

func (h *Handler) imageStreamHasErrors(isName string) bool {
	cm, err := h.configmapclientwrapper.Get(isName)
	if err != nil {
		if !kerrors.IsNotFound(err) {
			logrus.Warningf("unexpected error on get of configmap %s: %s", isName, err.Error())
		}
		return false
	}
	if util.ImageStreamErrorExists(cm) {
		return true
	}
	return false
}

func (h *Handler) storeImageStreamTagError(isName, tagName, message string) error {
	cmFromControllerCache, err := h.configmapclientwrapper.Get(isName)
	if err != nil {
		if !kerrors.IsNotFound(err) {
			logrus.Warningf("unexpected error on get of configmap %s: %s", isName, err.Error())
			return err
		}
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      isName,
				Namespace: v1.OperatorNamespace,
				Labels: map[string]string{
					util.ImageStreamErrorLabel: "true",
				},
			},
			Data: map[string]string{
				tagName: message,
			},
		}
		cm, err = h.configmapclientwrapper.Create(cm)
		if err != nil && !kerrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}
	// need to deep copy like we do in controller logic on configmap events, but this call
	// can stem from non-configmap events; need to copy before we change cached copy
	cm := &corev1.ConfigMap{}
	if cmFromControllerCache != nil {
		cmFromControllerCache.DeepCopyInto(cm)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Labels[util.ImageStreamErrorLabel] = "true"
	cm.Data[tagName] = message
	_, err = h.configmapclientwrapper.Update(cm)
	return err
}

func (h *Handler) clearImageStreamTagError(isName string, tags []string) error {
	cmFromControllerCache, err := h.configmapclientwrapper.Get(isName)
	if err != nil {
		if !kerrors.IsNotFound(err) {
			logrus.Warningf("unexpected error on get of configmap %s: %s", isName, err.Error())
			return err
		}
		logrus.Printf("clearImageStreamTagError: stream %s already deleted so no worries on clearing tags", isName)
		return nil
	}
	if cmFromControllerCache == nil || cmFromControllerCache.Data == nil {
		return nil
	}
	// need to deep copy like we do in controller logic on configmap events, but this call
	// can stem from non-configmap events; need to copy before we change cached copy
	cm := &corev1.ConfigMap{}
	cmFromControllerCache.DeepCopyInto(cm)
	hasTag := false
	for _, tagName := range tags {
		_, ok := cm.Data[tagName]
		if !ok {
			continue
		}
		hasTag = true
		logrus.Printf("clearing error messages from configmap for stream %s and tag %s", isName, tagName)
		delete(cm.Data, tagName)

	}

	if hasTag {
		_, err = h.configmapclientwrapper.Update(cm)
	}
	return err

}
