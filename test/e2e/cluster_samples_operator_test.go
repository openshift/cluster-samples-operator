package e2e_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/operator-framework/operator-sdk/pkg/sdk"

	imageapiv1 "github.com/openshift/api/image/v1"
	samplesapi "github.com/openshift/cluster-samples-operator/pkg/apis/samplesoperator/v1alpha1"

	ocfgapi "github.com/openshift/cluster-version-operator/pkg/apis/config.openshift.io/v1"
)

func verifySRAvailable(t *testing.T) {
	sr := &samplesapi.SamplesResource{
		TypeMeta: metav1.TypeMeta{
			Kind:       "SamplesResource",
			APIVersion: samplesapi.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-samples",
			Namespace: "openshift-cluster-samples-operator",
		},
	}
	err := wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		if err := sdk.Get(sr); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Errorf("error waiting for samples resource to appear: %v", err)
	}

}

func TestImageStreamAvailable(t *testing.T) {
	verifySRAvailable(t)

	istag := &imageapiv1.ImageStreamTag{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ImageStreamTag",
			APIVersion: imageapiv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ruby:2.4",
			Namespace: "openshift",
		},
	}
	err := wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		if err := sdk.Get(istag); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Errorf("error waiting for example imagestreamtag to appear: %v", err)
	}
}

func TestClusterVersionStatusAvailable(t *testing.T) {
	verifySRAvailable(t)

	state := &ocfgapi.ClusterVersion{
		TypeMeta: metav1.TypeMeta{
			APIVersion: ocfgapi.SchemeGroupVersion.String(),
			Kind:       "ClusterVersion",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      samplesapi.SamplesResourceName,
			Namespace: "openshift-cluster-samples-operator",
		},
	}
	err := wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		if err := sdk.Get(state); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Errorf("error waiting for cluster version to appear: %v", err)
	}

}
