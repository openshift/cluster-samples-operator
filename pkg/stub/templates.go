package stub

import (
	templatev1 "github.com/openshift/api/template/v1"
	v1 "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
)

func (h *Handler) processTemplateWatchEvent(t *templatev1.Template, deleted bool) error {
	// this version check is done in prepSamplesWatchEvent as well, but doing it here for templates
	// allows to bypass reading in the operator's config object (with the number of sample templates,
	// we can observe high fetch rates on the config object)
	// imagestream image import tracking necessitates, after initial install or upgrade, requires the
	// fetch of the config object, so we did not rework to ordering of the version check within that method
	if t.Annotations != nil && !deleted {
		isv, ok := t.Annotations[v1.SamplesVersionAnnotation]
		logrus.Debugf("Comparing template/%s version %s ok %v with git version %s", t.Name, isv, ok, h.version)
		if ok && isv == h.version {
			logrus.Debugf("Not upserting template/%s cause operator version matches", t.Name)
			return nil
		}
	}

	cfg, filePath, doUpsert, err := h.prepSamplesWatchEvent("template", t.Name, t.Annotations, deleted)
	if err != nil {
		return err
	}
	if !doUpsert {
		return nil
	}

	template, err := h.Filetemplategetter.Get(filePath)
	if err != nil {
		// still attempt to report error in status
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", filePath)
		dbg := "event temp udpate err"
		logrus.Printf("CRDUPDATE %s", dbg)
		h.crdwrapper.UpdateStatus(cfg, dbg)
		// if we get this, don't bother retrying
		return nil
	}
	if deleted {
		// set t to nil so upsert will create
		t = nil
	}
	err = h.upsertTemplate(template, t, cfg)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			return nil
		}
		cfg = h.refetchCfgMinimizeConflicts(cfg)
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error replacing template %s", template.Name)
		dbg := "event temp update err bad api obj update"
		logrus.Printf("CRDUPDATE %s", dbg)
		return h.crdwrapper.UpdateStatus(cfg, dbg)
	}
	return nil

}

func (h *Handler) upsertTemplate(templateInOperatorImage, templateInCluster *templatev1.Template, opcfg *v1.Config) error {
	if _, tok := h.skippedTemplates[templateInOperatorImage.Name]; tok {
		if templateInCluster != nil {
			if templateInCluster.Labels == nil {
				templateInCluster.Labels = make(map[string]string)
			}
			templateInCluster.Labels[v1.SamplesManagedLabel] = "false"
			h.templateclientwrapper.Update(templateInCluster)
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
	templateInOperatorImage.Labels[v1.SamplesManagedLabel] = "true"
	templateInOperatorImage.Annotations[v1.SamplesVersionAnnotation] = h.version

	if templateInCluster == nil {
		_, err := h.templateclientwrapper.Create(templateInOperatorImage)
		if err != nil {
			if kerrors.IsAlreadyExists(err) {
				logrus.Printf("template %s recreated since delete event", templateInOperatorImage.Name)
				// return the error so the caller can decide what to do
				return err
			}
			return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "template create error: %v")
		}
		logrus.Printf("created template %s", templateInOperatorImage.Name)
		return nil
	}

	templateInOperatorImage.ResourceVersion = templateInCluster.ResourceVersion
	_, err := h.templateclientwrapper.Update(templateInOperatorImage)
	if err != nil {
		return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "template update error: %v")
	}
	logrus.Printf("updated template %s", templateInCluster.Name)
	return nil
}
