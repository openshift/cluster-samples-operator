package stub

import (
	"fmt"

	"github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (h *Handler) manageDockerCfgSecret(deleted bool, Config *v1.Config, s *corev1.Secret) error {
	secret := s
	var err error
	if secret == nil {
		secret, err = h.secretclientwrapper.Get(h.namespace, v1.SamplesRegistryCredentials)
		if err != nil && kerrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if secret.Name != v1.SamplesRegistryCredentials {
		return nil
	}

	var newStatus corev1.ConditionStatus
	if deleted {
		err := h.secretclientwrapper.Delete("openshift", secret.Name, &metav1.DeleteOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return h.processError(Config, v1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to delete before create dockerconfig secret the openshift namespace: %v")
		}
		logrus.Printf("registry dockerconfig secret %s was deleted", secret.Name)
		newStatus = corev1.ConditionFalse
	} else {
		secretToCreate := corev1.Secret{}
		secret.DeepCopyInto(&secretToCreate)
		secretToCreate.Namespace = ""
		secretToCreate.ResourceVersion = ""
		secretToCreate.UID = ""
		secretToCreate.Annotations = make(map[string]string)
		secretToCreate.Annotations[v1.SamplesVersionAnnotation] = v1.GitVersionString()

		s, err := h.secretclientwrapper.Get("openshift", secret.Name)
		if err != nil && !kerrors.IsNotFound(err) {
			return h.processError(Config, v1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to get registry dockerconfig secret in openshift namespace : %v")
		}
		if err != nil {
			s = nil
		}
		if s != nil {
			logrus.Printf("updating dockerconfig secret %s in openshift namespace", v1.SamplesRegistryCredentials)
			_, err = h.secretclientwrapper.Update("openshift", &secretToCreate)
		} else {
			logrus.Printf("creating dockerconfig secret %s in openshift namespace", v1.SamplesRegistryCredentials)
			_, err = h.secretclientwrapper.Create("openshift", &secretToCreate)
		}
		if err != nil {
			return h.processError(Config, v1.ImportCredentialsExist, corev1.ConditionUnknown, err, "failed to create/update registry dockerconfig secret in openshif namespace : %v")
		}
		newStatus = corev1.ConditionTrue
	}

	h.GoodConditionUpdate(Config, newStatus, v1.ImportCredentialsExist)

	return nil
}

// WaitingForCredential determines whether we should proceed with processing the sample resource event,
// where we should *NOT* proceed if we are RHEL and using the default redhat registry;  The return from
// this method is in 2 flavors:  1) if the first boolean is true, tell the caller to just return nil to the sdk;
// 2) the second boolean being true means we've updated the Config with cred exists == false and the caller should call
// the sdk to update the object
func (h *Handler) WaitingForCredential(cfg *v1.Config) (bool, bool) {
	if cfg.ConditionTrue(v1.ImportCredentialsExist) {
		return false, false
	}

	// if trying to do rhel to the default registry.redhat.io registry requires the secret
	// be in place since registry.redhat.io requires auth to pull; since it is not ready
	// log error state
	if cfg.Spec.InstallType == v1.RHELSamplesDistribution &&
		(cfg.Spec.SamplesRegistry == "" || cfg.Spec.SamplesRegistry == "registry.redhat.io") {
		cred := cfg.Condition(v1.ImportCredentialsExist)
		// - if import cred is false, and the message is empty, that means we have NOT registered the error, and need to do so
		// - if cred is false, and the message is there, we can just return nil to the sdk, which "true" for the boolean return value indicates;
		// not returning the same error multiple times to the sdk avoids additional churn; once the secret comes in, it will update the Config
		// with cred == true, and then we'll get another Config event that will trigger config processing
		if len(cred.Message) > 0 {
			return true, false
		}
		err := fmt.Errorf("Cannot create rhel imagestreams to registry.redhat.io without the credentials being available")
		h.processError(cfg, v1.ImportCredentialsExist, corev1.ConditionFalse, err, "%v")
		return true, true
	}
	// this is either centos, or the cluster admin is using their own registry for rhel content, so we do not
	// enforce the need for the credential
	return false, false
}
