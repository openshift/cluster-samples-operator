package stub

import (
	templatev1 "github.com/openshift/api/template/v1"
	"github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
)

func (h *Handler) processTemplateWatchEvent(t *templatev1.Template, deleted bool) error {
	// our pattern is the top most caller locks the mutex

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
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", filePath)
		logrus.Printf("CRDUPDATE event temp udpate err")
		h.crdwrapper.UpdateStatus(cfg)
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
		h.processError(cfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error replacing template %s", template.Name)
		logrus.Printf("CRDUPDATE event temp update err bad api obj update")
		h.crdwrapper.UpdateStatus(cfg)
		return err
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
			h.templateclientwrapper.Update("openshift", templateInCluster)
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
	templateInOperatorImage.Annotations[v1.SamplesVersionAnnotation] = v1.GitVersionString()

	if templateInCluster == nil {
		_, err := h.templateclientwrapper.Create("openshift", templateInOperatorImage)
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
	_, err := h.templateclientwrapper.Update("openshift", templateInOperatorImage)
	if err != nil {
		return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "template update error: %v")
	}
	logrus.Printf("updated template %s", templateInCluster.Name)
	return nil
}
