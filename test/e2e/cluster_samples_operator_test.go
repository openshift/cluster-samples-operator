package e2e

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/util/retry"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	configv1 "github.com/openshift/api/config/v1"
	imageapiv1 "github.com/openshift/api/image/v1"
	operatorsv1api "github.com/openshift/api/operator/v1"
	templatev1 "github.com/openshift/api/template/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imageset "github.com/openshift/client-go/image/clientset/versioned"
	templateset "github.com/openshift/client-go/template/clientset/versioned"
	samplesapi "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"
	sampopclient "github.com/openshift/cluster-samples-operator/pkg/client"
	sampleclientv1 "github.com/openshift/cluster-samples-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
	"github.com/openshift/cluster-samples-operator/pkg/stub"
	kubeset "k8s.io/client-go/kubernetes"
)

var (
	kubeConfig     *rest.Config
	operatorClient *configv1client.ConfigV1Client
	kubeClient     *kubeset.Clientset
	imageClient    *imageset.Clientset
	templateClient *templateset.Clientset
	crClient       *sampleclientv1.Clientset
)

const (
	imagestreamsKey = "imagestreams"
	templatesKey    = "templates"
)

func setupClients(t *testing.T) {
	var err error
	if kubeConfig == nil {
		kubeConfig, err = sampopclient.GetConfig()
		if err != nil {
			t.Fatalf("%#v", err)
		}
	}
	if operatorClient == nil {
		operatorClient, err = configv1client.NewForConfig(kubeConfig)
		if err != nil {
			t.Fatalf("%#v", err)
		}
	}
	if kubeClient == nil {
		kubeClient, err = kubeset.NewForConfig(kubeConfig)
		if err != nil {
			t.Fatalf("%#v", err)
		}
	}
	if imageClient == nil {
		imageClient, err = imageset.NewForConfig(kubeConfig)
		if err != nil {
			t.Fatalf("%#v", err)
		}
	}
	if templateClient == nil {
		templateClient, err = templateset.NewForConfig(kubeConfig)
		if err != nil {
			t.Fatalf("%#v", err)
		}
	}
	if crClient == nil {
		crClient, err = sampleclientv1.NewForConfig(kubeConfig)
		if err != nil {
			t.Fatalf("%#v", err)
		}
	}
}

func dumpPod(t *testing.T) {
	podClient := kubeClient.CoreV1().Pods("openshift-cluster-samples-operator")
	podList, err := podClient.List(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("error list pods %v", err)
	}
	for _, pod := range podList.Items {
		if strings.HasPrefix(pod.Name, "cluster-samples-operator") {
			req := podClient.GetLogs(pod.Name, &corev1.PodLogOptions{})
			readCloser, err := req.Stream()
			if err != nil {
				t.Fatalf("error getting pod logs %v", err)
			}
			b, err := ioutil.ReadAll(readCloser)
			if err != nil {
				t.Fatalf("error reading pod stream %v", err)
			}
			podLog := string(b)
			t.Logf("pod logs:  %s", podLog)
		}
	}
}

func verifyOperatorUp(t *testing.T) *samplesapi.Config {
	setupClients(t)
	var cfg *samplesapi.Config
	var err error
	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("error waiting for samples resource to appear: %v", err)
	}
	return cfg
}

func verifySecretPresent(t *testing.T) {
	setupClients(t)
	secClient := kubeClient.CoreV1().Secrets("openshift")
	err := wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		_, err := secClient.Get(samplesapi.SamplesRegistryCredentials, metav1.GetOptions{})
		if err != nil {
			if !kerrors.IsNotFound(err) {
				t.Logf("%v", err)
			}
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("timeout for secret getting created cfg %#v", verifyOperatorUp(t))
	}
}

func verifyConditionsCompleteSamplesAdded(t *testing.T) error {
	return wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err := crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.ConditionTrue(samplesapi.SamplesExist) &&
			cfg.ConditionTrue(samplesapi.ConfigurationValid) &&
			cfg.ConditionTrue(samplesapi.ImportCredentialsExist) &&
			cfg.ConditionFalse(samplesapi.ImageChangesInProgress) {
			return true, nil
		}

		return false, nil
	})

}

func verifyConditionsCompleteSamplesRemoved(t *testing.T) error {
	return wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err := crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.ConditionFalse(samplesapi.SamplesExist) &&
			cfg.ConditionFalse(samplesapi.ImageChangesInProgress) &&
			cfg.ConditionFalse(samplesapi.ImportCredentialsExist) {
			return true, nil
		}

		return false, nil
	})
}

func verifyClusterOperatorConditionsComplete(t *testing.T) {
	var state *configv1.ClusterOperator
	var err error
	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		state, err = operatorClient.ClusterOperators().Get(operator.ClusterOperatorName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		availableOK := false
		progressingOK := false
		failingOK := false
		for _, condition := range state.Status.Conditions {
			switch condition.Type {
			case configv1.OperatorAvailable:
				if condition.Status == configv1.ConditionTrue {
					availableOK = true
				}
			case configv1.OperatorFailing:
				if condition.Status == configv1.ConditionFalse {
					failingOK = true
				}
			case configv1.OperatorProgressing:
				if condition.Status == configv1.ConditionFalse {
					progressingOK = true
				}
			}
		}
		if availableOK && progressingOK && failingOK {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("cluster operator conditions never stabilized, cluster op %#v samples resource %#v", state, cfg)
	}
}

func getContentDir(t *testing.T) string {
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("%v", err)
	}
	startDir := filepath.Dir(pwd)
	for true {
		// filepath.Dir will either return . or / it expires path,
		// just go off of len given os.IsPathSeprator is uint8 and
		// conversion from string to uint8 is cumbersome
		if len(startDir) <= 1 {
			break
		}
		if strings.HasSuffix(strings.TrimSpace(startDir), "cluster-samples-operator") {
			break
		}
		startDir = filepath.Dir(startDir)
	}
	contentDir := ""
	_ = filepath.Walk(startDir, func(path string, info os.FileInfo, err error) error {
		if strings.HasSuffix(strings.TrimSpace(path), "ocp-x86_64") {
			contentDir = path
			return fmt.Errorf("found contentDir %s", contentDir)
		}
		return nil
	})
	return contentDir
}

func getSamplesNames(dir string, files []os.FileInfo, t *testing.T) map[string]map[string]bool {

	h := stub.Handler{}
	h.Fileimagegetter = &stub.DefaultImageStreamFromFileGetter{}
	h.Filetemplategetter = &stub.DefaultTemplateFromFileGetter{}
	h.Filefinder = &stub.DefaultResourceFileLister{}

	var err error
	if files == nil {
		files, err = h.Filefinder.List(dir)
	}
	if err != nil {
		t.Fatalf("%v", err)
	}

	names := map[string]map[string]bool{}
	names[imagestreamsKey] = map[string]bool{}
	names[templatesKey] = map[string]bool{}
	for _, file := range files {
		if file.IsDir() {
			subfiles, err := h.Filefinder.List(dir + "/" + file.Name())
			if err != nil {
				t.Fatalf("%v", err)
			}
			subnames := getSamplesNames(dir+"/"+file.Name(), subfiles, t)
			substreams, _ := subnames[imagestreamsKey]
			subtemplates, _ := subnames[templatesKey]
			streams, _ := names[imagestreamsKey]
			templates, _ := names[templatesKey]

			if len(streams) == 0 {
				streams = substreams
			} else {
				for key, value := range substreams {
					streams[key] = value
				}
			}
			if len(templates) == 0 {
				templates = subtemplates
			} else {
				for key, value := range subtemplates {
					templates[key] = value
				}
			}

			names[imagestreamsKey] = streams
			names[templatesKey] = templates

			continue
		}

		if strings.HasSuffix(dir, imagestreamsKey) {
			imagestream, err := h.Fileimagegetter.Get(dir + "/" + file.Name())
			if err != nil {
				t.Fatalf("%v", err)
			}

			streams, _ := names[imagestreamsKey]
			streams[imagestream.Name] = true
		}

		if strings.HasSuffix(dir, templatesKey) {
			template, err := h.Filetemplategetter.Get(dir + "/" + file.Name())
			if err != nil {
				t.Fatalf("%v", err)
			}

			templates, _ := names[templatesKey]
			templates[template.Name] = true
		}
	}
	return names
}

func verifyImageStreamsPresent(t *testing.T, content map[string]bool, timeToCompare *kapis.Time) {
	for key, _ := range content {
		var is *imageapiv1.ImageStream
		var err error

		err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
			is, err = imageClient.ImageV1().ImageStreams("openshift").Get(key, metav1.GetOptions{})
			if err != nil {
				t.Logf("%v", err)
				return false, nil
			}
			if timeToCompare != nil && is.CreationTimestamp.Before(timeToCompare) {
				errstr := fmt.Sprintf("imagestream %s was created at %#v which is still created before time to compare %#v", is.Name, is.CreationTimestamp, timeToCompare)
				t.Log(errstr)
				return false, fmt.Errorf(errstr)
			}
			return true, nil
		})
		if err != nil {
			dumpPod(t)
			cfg := verifyOperatorUp(t)
			t.Fatalf("error waiting for example imagestream %s to appear: %v samples resource %#v", key, err, cfg)
		}
	}
}

func verifyImageChangesInProgress(t *testing.T) {
	var cfg *samplesapi.Config
	var err error
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.ConditionTrue(samplesapi.ImageChangesInProgress) {
			return true, nil
		}
		return false, nil
	})
}

func verifyImageStreamsGone(t *testing.T) {
	time.Sleep(30 * time.Second)
	content := getSamplesNames(getContentDir(t), nil, t)
	streams, _ := content[imagestreamsKey]
	for key, _ := range streams {
		_, err := imageClient.ImageV1().ImageStreams("openshift").Get(key, metav1.GetOptions{})
		if err == nil {
			dumpPod(t)
			cfg := verifyOperatorUp(t)
			t.Fatalf("still have imagestream %s in the openshift namespace when we expect it to be gone, cfg: %#v", key, cfg)
		}
	}
}

func verifyTemplatesPresent(t *testing.T, content map[string]bool, timeToCompare *kapis.Time) {
	for key, _ := range content {
		var template *templatev1.Template
		var err error

		err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
			template, err = templateClient.TemplateV1().Templates("openshift").Get(key, metav1.GetOptions{})
			if err != nil {
				t.Logf("%v", err)
				return false, nil
			}
			if timeToCompare != nil && template.CreationTimestamp.Before(timeToCompare) {
				errstr := fmt.Sprintf("template %s was created at %#v which is still created before time to compare %#v", template.Name, template.CreationTimestamp, timeToCompare)
				t.Log(errstr)
				return false, fmt.Errorf(errstr)
			}
			return true, nil
		})
		if err != nil {
			dumpPod(t)
			cfg := verifyOperatorUp(t)
			t.Fatalf("error waiting for example template %s to appear: %v samples resource %#v", key, err, cfg)
		}
	}
}

func verifyTemplatesGone(t *testing.T) {
	time.Sleep(30 * time.Second)
	content := getSamplesNames(getContentDir(t), nil, t)
	templates, _ := content[templatesKey]
	for key, _ := range templates {
		_, err := templateClient.TemplateV1().Templates("openshift").Get(key, metav1.GetOptions{})
		if err == nil {
			dumpPod(t)
			cfg := verifyOperatorUp(t)
			t.Fatalf("still have template %s in the openshift namespace when we expect it to be gone, cfg: %#v", key, cfg)
		}
	}
}

func validateContent(t *testing.T, timeToCompare *kapis.Time) {
	contentDir := getContentDir(t)
	content := getSamplesNames(contentDir, nil, t)
	streams, _ := content[imagestreamsKey]
	verifyImageStreamsPresent(t, streams, timeToCompare)
	templates, _ := content[templatesKey]
	verifyTemplatesPresent(t, templates, timeToCompare)
}

func verifyConfigurationValid(t *testing.T, status corev1.ConditionStatus) {
	err := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, e := crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if e != nil {
			t.Logf("%v", e)
			return false, nil
		}
		if cfg.Condition(samplesapi.ConfigurationValid).Status == status {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error waiting for samples resource to update config valid expected status %v err %v Config %#v", status, err, cfg)
	}
}

func verifyDeletedImageStreamRecreated(t *testing.T) {
	err := imageClient.ImageV1().ImageStreams("openshift").Delete("jenkins", &metav1.DeleteOptions{})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error deleting jenkins imagestream %v Config %#v", err, cfg)
	}
	// first make sure image changes makes it to true
	verifyImageChangesInProgress(t)
	// then make sure the image changes are complete before attempting to fetch deleted IS
	verifyConditionsCompleteSamplesAdded(t)
	err = wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		_, err := imageClient.ImageV1().ImageStreams("openshift").Get("jenkins", metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		if kerrors.IsNotFound(err) {
			return false, nil
		}
		t.Logf("%v", err)
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("imagestream not recreated: %v, crd: %#v", err, cfg)
	}
}

func verifyDeletedImageStreamNotRecreated(t *testing.T) {
	err := imageClient.ImageV1().ImageStreams("openshift").Delete("jenkins", &metav1.DeleteOptions{})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error deleting jenkins imagestream %v Config %#v", err, cfg)
	}
	// make sure jenkins imagestream does not appear while unmanaged
	// first, wait sufficiently to make sure delete has gone though
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		_, err := imageClient.ImageV1().ImageStreams("openshift").Get("jenkins", metav1.GetOptions{})
		if kerrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("delete did not occur %v samples resource %#v", err, cfg)
	}
	// now make sure it has not been recreated
	time.Sleep(30 * time.Second)
	_, err = imageClient.ImageV1().ImageStreams("openshift").Get("jenkins", metav1.GetOptions{})
	if err == nil {
		dumpPod(t)
		t.Fatalf("imagestream recreated, cfg: %#v", verifyOperatorUp(t))
	}

}

func verifyDeletedTemplatesRecreated(t *testing.T) {
	err := templateClient.TemplateV1().Templates("openshift").Delete("jenkins-ephemeral", &metav1.DeleteOptions{})
	verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error deleting jenkins imagestream %v samples resource %#v", err, cfg)
	}
	err = wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		_, err := templateClient.TemplateV1().Templates("openshift").Get("jenkins-ephemeral", metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		if kerrors.IsNotFound(err) {
			return false, nil
		}
		t.Logf("%v", err)
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("template not recreated: %v, cfg: %#v", err, cfg)
	}
}

func verifyDeletedTemplatesNotRecreated(t *testing.T) {
	err := templateClient.TemplateV1().Templates("openshift").Delete("jenkins-ephemeral", &metav1.DeleteOptions{})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error deleting jenkins imagestream %v samples resource %#v", err, cfg)
	}
	// make sure jenkins-ephemeral template does not appear while unmanaged
	// first, wait sufficiently to make sure delete has gone though
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		_, err := templateClient.TemplateV1().Templates("openshift").Get("jenkins-ephemeral", metav1.GetOptions{})
		if kerrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("delete did not occur %v samples resource %#v", err, cfg)
	}
	// now make sure it has not been recreated
	time.Sleep(30 * time.Second)
	_, err = templateClient.TemplateV1().Templates("openshift").Get("jenkins-ephemeral", metav1.GetOptions{})
	if err == nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("template recreated samples resource %#v", cfg)
	}

}

func TestImageStreamInOpenshiftNamespace(t *testing.T) {
	verifyOperatorUp(t)
	validateContent(t, nil)
	err := verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		t.Fatalf("Config did not stabilize on startup %#v", verifyOperatorUp(t))
	}
	verifySecretPresent(t)
	verifyClusterOperatorConditionsComplete(t)
	t.Logf("Config after TestImageStreamInOpenshiftNamespace: %#v", verifyOperatorUp(t))
}

func TestRecreateConfigAfterDelete(t *testing.T) {
	cfg := verifyOperatorUp(t)

	oldTime := cfg.CreationTimestamp
	now := kapis.Now()

	err := crClient.Samples().Configs().Delete(samplesapi.ConfigName, &metav1.DeleteOptions{})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error deleting Config %v", err)
	}

	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if cfg.CreationTimestamp == oldTime {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("creation times the same after delete: %v, %v, %#v", oldTime, cfg.CreationTimestamp, cfg)
	}

	err = verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		cfg = verifyOperatorUp(t)
		t.Fatalf("samples not re-established after delete %#v", cfg)
	}

	validateContent(t, &now)
	verifyClusterOperatorConditionsComplete(t)
	t.Logf("Config after TestRecreateConfigAfterDelete: %#v", verifyOperatorUp(t))
}

func TestSpecManagementStateField(t *testing.T) {
	now := kapis.Now()
	cfg := verifyOperatorUp(t)
	oldTime := cfg.CreationTimestamp
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.ManagementState = operatorsv1api.Removed
		cfg, err := crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.Status.ManagementState != operatorsv1api.Removed {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("cfg status mgmt never went to removed %#v", verifyOperatorUp(t))
	}

	err = verifyConditionsCompleteSamplesRemoved(t)
	if err != nil {
		dumpPod(t)
		cfg = verifyOperatorUp(t)
		t.Fatalf("samples not removed in time %#v", cfg)
	}

	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.CreationTimestamp != oldTime {
			return false, fmt.Errorf("Config was recreated when it should not have been: old create time %v new create time %v", oldTime, cfg.CreationTimestamp)
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		cfg = verifyOperatorUp(t)
		t.Fatalf("%v and %#v", err, cfg)
	}

	verifyImageStreamsGone(t)
	verifyTemplatesGone(t)

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg = verifyOperatorUp(t)
		cfg.Spec.ManagementState = operatorsv1api.Managed
		cfg, err = crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, cfg)
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.Status.ManagementState != operatorsv1api.Managed {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("cfg status mgmt never went to managed %#v", verifyOperatorUp(t))
	}

	err = verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		cfg = verifyOperatorUp(t)
		t.Fatalf("samples not re-established when set to managed %#v", cfg)
	}

	validateContent(t, &now)

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg = verifyOperatorUp(t)
		cfg.Spec.ManagementState = operatorsv1api.Unmanaged
		cfg, err = crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.Status.ManagementState != operatorsv1api.Unmanaged {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("cfg status mgmt never went to unmanaged %#v", verifyOperatorUp(t))
	}

	verifyDeletedImageStreamNotRecreated(t)
	verifyDeletedTemplatesNotRecreated(t)
	// get timestamp to check against in progress condition
	now = kapis.Now()
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// now switch back to default managed for any subsequent tests
		// and confirm all the default samples content exists
		cfg = verifyOperatorUp(t)
		cfg.Spec.ManagementState = operatorsv1api.Managed
		cfg, err = crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, cfg)
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.Status.ManagementState != operatorsv1api.Managed {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("cfg status mgmt never went to managed %#v", verifyOperatorUp(t))
	}

	// wait for it to get into pending
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.Condition(samplesapi.ImageChangesInProgress).LastUpdateTime.After(now.Time) {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error waiting for Config to get into pending: %v samples resource %#v", err, cfg)
	}
	// now wait for it to get out of pending
	err = verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		cfg = verifyOperatorUp(t)
		t.Fatalf("samples not re-established when set to managed %#v", cfg)
	}

	validateContent(t, nil)
	verifyClusterOperatorConditionsComplete(t)
	t.Logf("Config after TestSpecManagementStateField: %#v", verifyOperatorUp(t))
}

func TestInstallTypeConfigChangeValidation(t *testing.T) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.InstallType = samplesapi.CentosSamplesDistribution
		cfg, err := crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}

	verifyConfigurationValid(t, corev1.ConditionFalse)

	//reset install type back
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.InstallType = samplesapi.RHELSamplesDistribution
		cfg, err = crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}

	verifyConfigurationValid(t, corev1.ConditionTrue)
	t.Logf("Config after TestInstallTypeConfigChangeValidation: %#v", verifyOperatorUp(t))
}

func TestArchitectureConfigChangeValidation(t *testing.T) {
	err := verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		t.Fatalf("samples not stable at start of arch cfg chg test %#v", verifyOperatorUp(t))
	}
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.Architectures[0] = samplesapi.PPCArchitecture
		cfg, err := crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}

	verifyConfigurationValid(t, corev1.ConditionFalse)

	//reset install type back
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.Architectures[0] = samplesapi.X86Architecture
		cfg, err = crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}

	verifyConfigurationValid(t, corev1.ConditionTrue)
	t.Logf("Config after TestArchitectureConfigChangeValidation: %#v", verifyOperatorUp(t))
}

func TestSkippedProcessing(t *testing.T) {
	err := verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		t.Fatalf("samples not stable at start of skip cfg chg test %#v", verifyOperatorUp(t))
	}
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.SkippedImagestreams = append(cfg.Spec.SkippedImagestreams, "jenkins")
		cfg.Spec.SkippedTemplates = append(cfg.Spec.SkippedTemplates, "jenkins-ephemeral")
		cfg, err := crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating samples resource %v and %#v", err, verifyOperatorUp(t))
	}

	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg := verifyOperatorUp(t)
		if len(cfg.Status.SkippedImagestreams) == 0 || len(cfg.Status.SkippedTemplates) == 0 {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("samples resource skipped lists never processed %#v", verifyOperatorUp(t))
	}

	verifyDeletedImageStreamNotRecreated(t)
	verifyDeletedTemplatesNotRecreated(t)

	// reset skipped list back
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.SkippedImagestreams = []string{}
		cfg.Spec.SkippedTemplates = []string{}
		cfg, err = crClient.Samples().Configs().Update(cfg)
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}
	// verify status skipped has been reset
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err := crClient.Samples().Configs().Get(samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if len(cfg.Status.SkippedImagestreams) == 0 && len(cfg.Status.SkippedTemplates) == 0 {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("samples resource skipped lists never processed %#v", verifyOperatorUp(t))
	}

	// checking in progress before validating content helps
	// makes sure we go into image changes true mode from false
	verifyImageChangesInProgress(t)
	// then make sure image changes complete
	err = verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		t.Fatalf("samples not stable at end of skip cfg chg test %#v", verifyOperatorUp(t))
	}
	validateContent(t, nil)
	t.Logf("Config after TestSkippedProcessing: %#v", verifyOperatorUp(t))
}

func TestRecreateDeletedManagedSample(t *testing.T) {
	verifyOperatorUp(t)
	// first make sure we are at normal state
	err := verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		t.Fatalf("samples not stable at start of delete samples test %#v", verifyOperatorUp(t))
	}
	// then delete samples and make sure they are recreated
	verifyDeletedImageStreamRecreated(t)
	verifyDeletedTemplatesRecreated(t)
	t.Logf("Config after TestRecreateDeletedManagedSample: %#v", verifyOperatorUp(t))
}
