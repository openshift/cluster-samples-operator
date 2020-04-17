package e2e

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
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
	samplesapi "github.com/openshift/api/samples/v1"
	templatev1 "github.com/openshift/api/template/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	imageset "github.com/openshift/client-go/image/clientset/versioned"
	sampleclientv1 "github.com/openshift/client-go/samples/clientset/versioned"
	templateset "github.com/openshift/client-go/template/clientset/versioned"
	sampopclient "github.com/openshift/cluster-samples-operator/pkg/client"
	operator "github.com/openshift/cluster-samples-operator/pkg/operatorstatus"
	"github.com/openshift/cluster-samples-operator/pkg/stub"
	"github.com/openshift/cluster-samples-operator/pkg/util"
	kubeset "k8s.io/client-go/kubernetes"
)

var (
	kubeConfig     *rest.Config
	configClient   *configv1client.ConfigV1Client
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
	if configClient == nil {
		configClient, err = configv1client.NewForConfig(kubeConfig)
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
	podClient := kubeClient.CoreV1().Pods(samplesapi.OperatorNamespace)
	podList, err := podClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("error list pods %v", err)
	}
	t.Logf("dumpPods have %d items in list", len(podList.Items))
	for _, pod := range podList.Items {
		t.Logf("dumpPods looking at pod %s in phase %s", pod.Name, pod.Status.Phase)
		if strings.HasPrefix(pod.Name, "cluster-samples-operator") &&
			pod.Status.Phase == corev1.PodRunning {
			for _, container := range pod.Spec.Containers {
				req := podClient.GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name})
				readCloser, err := req.Stream(context.TODO())
				if err != nil {
					t.Fatalf("error getting pod logs for container %s: %s", container.Name, err.Error())
				}
				b, err := ioutil.ReadAll(readCloser)
				if err != nil {
					t.Fatalf("error reading pod stream %s", err.Error())
				}
				podLog := string(b)
				t.Logf("pod logs for container %s:  %s", container.Name, podLog)

			}
		}
	}
}

func verifyOperatorUp(t *testing.T) *samplesapi.Config {
	setupClients(t)
	var cfg *samplesapi.Config
	var err error
	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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

func verifyX86(t *testing.T) bool {
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		cfg := verifyOperatorUp(t)
		if len(cfg.Status.Architectures) == 0 {
			return false, nil
		}
		return true, nil
	})
	cfg := verifyOperatorUp(t)
	if err != nil {
		t.Fatalf("architecture field was not set after 1 minute: %#v", cfg)
	}
	if cfg.Status.Architectures[0] != samplesapi.X86Architecture && cfg.Status.Architectures[0] != samplesapi.AMDArchitecture {
		return false
	}
	return true
}

func verifyIPv6(t *testing.T) bool {
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		networkConfig, err := configClient.Networks().Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !stub.IsRetryableAPIError(err) && err != nil {
			t.Logf("verifyIPv6 got unretryable error %s", err.Error())
			return false, err
		}
		if err != nil {
			t.Logf("verifyIPv6 got retryable error %s", err.Error())
			return false, nil
		}
		if len(networkConfig.Status.ClusterNetwork) == 0 {
			t.Logf("verifyIPv6 sees no cluster networks in network config yet")
			return false, nil
		}
		for _, entry := range networkConfig.Status.ClusterNetwork {
			t.Logf("verifyIPv6 looking at CIDR %s", entry.CIDR)
			if len(entry.CIDR) > 0 {
				ip, _, err := net.ParseCIDR(entry.CIDR)
				if err != nil {
					return false, err
				}
				if ip.To4() != nil {
					t.Logf("verifyIPv6 found ipv4 %s", ip.String())
					return true, nil
				}
				t.Logf("verifyIPv6 IP %s not ipv4", ip.String())
			}
		}
		t.Logf("verifyIPv6 done looping through cluster networks, found no ipv4")
		if len(networkConfig.Status.ClusterNetwork) == 0 {
			return false, nil
		}
		return false, fmt.Errorf("verifyIPv6 determined this is a IPIv6 env")
	})
	if err != nil {
		t.Logf("verifyIpv6 either could not access network cluster config or ipv6 only: %s", err.Error())
		return true
	}
	t.Logf("verifyIpv6 saying not to abort for ipv6")
	return false
}

func verifyConditionsCompleteSamplesAdded(t *testing.T) error {
	return wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err := crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if util.ConditionTrue(cfg, samplesapi.SamplesExist) &&
			util.ConditionTrue(cfg, samplesapi.ConfigurationValid) &&
			util.ConditionTrue(cfg, samplesapi.ImportCredentialsExist) &&
			util.ConditionFalse(cfg, samplesapi.ImageChangesInProgress) {
			return true, nil
		}

		return false, nil
	})

}

func verifyConditionsCompleteSamplesRemoved(t *testing.T) error {
	return wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		cfg, err := crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if util.ConditionFalse(cfg, samplesapi.SamplesExist) &&
			util.ConditionFalse(cfg, samplesapi.ImageChangesInProgress) {
			return true, nil
		}

		return false, nil
	})
}

func verifyClusterOperatorConditionsComplete(t *testing.T, expectedVersion string, mgmtCfg operatorsv1api.ManagementState) {
	var state *configv1.ClusterOperator
	var err error
	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		state, err = configClient.ClusterOperators().Get(context.TODO(), operator.ClusterOperatorName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		availableOK, progressingOK, degradedOK, versionOK, reasonOK := false, false, false, false, false
		availableReason, progressingReason, degradedReason := "", "", ""
		for _, condition := range state.Status.Conditions {
			switch condition.Type {
			case configv1.OperatorAvailable:
				if condition.Status == configv1.ConditionTrue {
					availableOK = true
				}
				availableReason = condition.Reason
			case configv1.OperatorDegraded:
				if condition.Status == configv1.ConditionFalse {
					degradedOK = true
				}
				degradedReason = condition.Reason
			case configv1.OperatorProgressing:
				if condition.Status == configv1.ConditionFalse {
					progressingOK = true
				}
				progressingReason = condition.Reason
			}
		}
		if len(state.Status.Versions) > 0 && state.Status.Versions[0].Name == "operator" && state.Status.Versions[0].Version == expectedVersion {
			versionOK = true
		}

		if mgmtCfg != operatorsv1api.Managed {
			goodValue := "Currently" + string(mgmtCfg)
			if availableReason == goodValue && progressingReason == goodValue && degradedReason == goodValue {
				reasonOK = true
			}
		} else {
			reasonOK = true
		}

		t.Logf("availableOK %v progressingOK %v degradedOK %v versionOK %v reasonOK %v",
			availableOK,
			progressingOK,
			degradedOK,
			versionOK,
			reasonOK)

		if availableOK && progressingOK && degradedOK && versionOK && reasonOK {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("cluster operator conditions never stabilized, expected version %s cluster op %#v samples resource %#v", expectedVersion, state, cfg)
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
	version := verifyOperatorUp(t).Status.Version
	for key := range content {
		var is *imageapiv1.ImageStream
		var err error

		err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
			is, err = imageClient.ImageV1().ImageStreams("openshift").Get(context.TODO(), key, metav1.GetOptions{})
			if err != nil {
				t.Logf("%v", err)
				return false, nil
			}
			if timeToCompare != nil && is.CreationTimestamp.Before(timeToCompare) {
				errstr := fmt.Sprintf("imagestream %s was created at %#v which is still created before time to compare %#v", is.Name, is.CreationTimestamp, timeToCompare)
				t.Log(errstr)
				return false, fmt.Errorf(errstr)
			}
			isv, ok := is.Annotations[samplesapi.SamplesVersionAnnotation]
			if !ok {
				t.Logf("imagestrem %s does not have version annotation", is.Name)
				return false, nil
			}
			if len(version) > 0 && isv != version {
				t.Logf("imagestream %s is at version %s but we expect it to be version %s", is.Name, isv, version)
				return false, nil
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
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if util.ConditionTrue(cfg, samplesapi.ImageChangesInProgress) {
			return true, nil
		}
		return false, nil
	})
}

func verifyImageStreamsGone(t *testing.T) {
	time.Sleep(30 * time.Second)
	content := getSamplesNames(getContentDir(t), nil, t)
	streams, _ := content[imagestreamsKey]
	for key := range streams {
		_, err := imageClient.ImageV1().ImageStreams("openshift").Get(context.TODO(), key, metav1.GetOptions{})
		if err == nil {
			dumpPod(t)
			cfg := verifyOperatorUp(t)
			t.Fatalf("still have imagestream %s in the openshift namespace when we expect it to be gone, cfg: %#v", key, cfg)
		}
	}
}

func verifyTemplatesPresent(t *testing.T, content map[string]bool, timeToCompare *kapis.Time) {
	version := verifyOperatorUp(t).Status.Version
	for key := range content {
		var template *templatev1.Template
		var err error

		err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
			template, err = templateClient.TemplateV1().Templates("openshift").Get(context.TODO(), key, metav1.GetOptions{})
			if err != nil {
				t.Logf("%v", err)
				return false, nil
			}
			if timeToCompare != nil && template.CreationTimestamp.Before(timeToCompare) {
				errstr := fmt.Sprintf("template %s was created at %#v which is still created before time to compare %#v", template.Name, template.CreationTimestamp, timeToCompare)
				t.Log(errstr)
				return false, fmt.Errorf(errstr)
			}
			tv, ok := template.Annotations[samplesapi.SamplesVersionAnnotation]
			if !ok {
				t.Logf("template %s does not have version annotation", template.Name)
				return false, nil
			}
			if len(version) > 0 && tv != version {
				t.Logf("template %s is at version %s but we expect it to be at version %s", template.Name, tv, version)
				return false, nil
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
	for key := range templates {
		_, err := templateClient.TemplateV1().Templates("openshift").Get(context.TODO(), key, metav1.GetOptions{})
		if err == nil {
			dumpPod(t)
			cfg := verifyOperatorUp(t)
			t.Fatalf("still have template %s in the openshift namespace when we expect it to be gone, cfg: %#v", key, cfg)
		}
	}
}

func validateContent(t *testing.T, timeToCompare *kapis.Time) {
	if !verifyX86(t) {
		return
	}
	if verifyIPv6(t) {
		return
	}

	contentDir := getContentDir(t)
	content := getSamplesNames(contentDir, nil, t)
	streams, _ := content[imagestreamsKey]
	verifyImageStreamsPresent(t, streams, timeToCompare)
	templates, _ := content[templatesKey]
	verifyTemplatesPresent(t, templates, timeToCompare)
}

func verifyConfigurationValid(t *testing.T, status corev1.ConditionStatus) {
	err := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, e := crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
		if e != nil {
			t.Logf("%v", e)
			return false, nil
		}
		if util.Condition(cfg, samplesapi.ConfigurationValid).Status == status {
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
	err := imageClient.ImageV1().ImageStreams("openshift").Delete(context.TODO(), "jenkins", metav1.DeleteOptions{})
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
		_, err := imageClient.ImageV1().ImageStreams("openshift").Get(context.TODO(), "jenkins", metav1.GetOptions{})
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

func verifySkippedStreamManagedLabel(t *testing.T, value string) {
	err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		stream, err := imageClient.ImageV1().ImageStreams("openshift").Get(context.TODO(), "jenkins", metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return true, nil
		}
		if stream.Labels != nil {
			label, _ := stream.Labels[samplesapi.SamplesManagedLabel]
			if label == value {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("label update did not occur %v samples resource %#v", err, cfg)
	}

}

func verifySkippedTemplateManagedLabel(t *testing.T, value string) {
	err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		stream, err := templateClient.TemplateV1().Templates("openshift").Get(context.TODO(), "jenkins-ephemeral", metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return true, nil
		}
		if stream.Labels != nil {
			label, _ := stream.Labels[samplesapi.SamplesManagedLabel]
			if label == value {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("label update did not occur %v samples resource %#v", err, cfg)
	}

}

func verifyDeletedImageStreamNotRecreated(t *testing.T) {
	if !verifyX86(t) {
		return
	}
	if verifyIPv6(t) {
		return
	}

	err := imageClient.ImageV1().ImageStreams("openshift").Delete(context.TODO(), "jenkins", metav1.DeleteOptions{})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error deleting jenkins imagestream %v Config %#v", err, cfg)
	}
	// make sure jenkins imagestream does not appear while unmanaged
	// first, wait sufficiently to make sure delete has gone though
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		_, err := imageClient.ImageV1().ImageStreams("openshift").Get(context.TODO(), "jenkins", metav1.GetOptions{})
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
	_, err = imageClient.ImageV1().ImageStreams("openshift").Get(context.TODO(), "jenkins", metav1.GetOptions{})
	if err == nil {
		dumpPod(t)
		t.Fatalf("imagestream recreated, cfg: %#v", verifyOperatorUp(t))
	}

}

func verifyDeletedTemplatesRecreated(t *testing.T) {
	err := templateClient.TemplateV1().Templates("openshift").Delete(context.TODO(), "jenkins-ephemeral", metav1.DeleteOptions{})
	verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error deleting jenkins imagestream %v samples resource %#v", err, cfg)
	}
	err = wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		_, err := templateClient.TemplateV1().Templates("openshift").Get(context.TODO(), "jenkins-ephemeral", metav1.GetOptions{})
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
	if !verifyX86(t) {
		return
	}
	if verifyIPv6(t) {
		return
	}

	err := templateClient.TemplateV1().Templates("openshift").Delete(context.TODO(), "jenkins-ephemeral", metav1.DeleteOptions{})
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("error deleting jenkins imagestream %v samples resource %#v", err, cfg)
	}
	// make sure jenkins-ephemeral template does not appear while unmanaged
	// first, wait sufficiently to make sure delete has gone though
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		_, err := templateClient.TemplateV1().Templates("openshift").Get(context.TODO(), "jenkins-ephemeral", metav1.GetOptions{})
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
	_, err = templateClient.TemplateV1().Templates("openshift").Get(context.TODO(), "jenkins-ephemeral", metav1.GetOptions{})
	if err == nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("template recreated samples resource %#v", cfg)
	}

}

func TestImageStreamInOpenshiftNamespace(t *testing.T) {
	cfg := verifyOperatorUp(t)
	validateContent(t, nil)
	err := verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		t.Fatalf("Config did not stabilize on startup %#v", verifyOperatorUp(t))
	}
	verifyClusterOperatorConditionsComplete(t, cfg.Status.Version, cfg.Status.ManagementState)
	t.Logf("Config after TestImageStreamInOpenshiftNamespace: %#v", verifyOperatorUp(t))
}

func TestRecreateConfigAfterDelete(t *testing.T) {
	now := kapis.Now()
	// do a few deletes/recreates in rapid succession vs. a single iteration to make
	// sure we cover the various timing windows when samples creation is in progress
	for i := 0; i < 3; i++ {
		cfg := verifyOperatorUp(t)

		oldTime := cfg.CreationTimestamp

		err := crClient.SamplesV1().Configs().Delete(context.TODO(), samplesapi.ConfigName, metav1.DeleteOptions{})
		if err != nil {
			dumpPod(t)
			t.Fatalf("error deleting Config %v", err)
		}

		// make sure the cfg object is recreated vs. just finding the one we  tried to delete
		err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
			cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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
	}

	err := verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		cfg := verifyOperatorUp(t)
		t.Fatalf("samples not re-established after delete %#v", cfg)
	}

	validateContent(t, &now)
	cfg := verifyOperatorUp(t)
	verifyClusterOperatorConditionsComplete(t, cfg.Status.Version, cfg.Status.ManagementState)
	t.Logf("Config after TestRecreateConfigAfterDelete: %#v", cfg)
}

func TestSpecManagementStateField(t *testing.T) {
	now := kapis.Now()
	cfg := verifyOperatorUp(t)
	oldTime := cfg.CreationTimestamp
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.ManagementState = operatorsv1api.Removed
		cfg, err := crClient.SamplesV1().Configs().Update(context.TODO(), cfg, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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

	verifyClusterOperatorConditionsComplete(t, cfg.Status.Version, cfg.Status.ManagementState)

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg = verifyOperatorUp(t)
		cfg.Spec.ManagementState = operatorsv1api.Managed
		cfg, err = crClient.SamplesV1().Configs().Update(context.TODO(), cfg, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, cfg)
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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
		cfg, err = crClient.SamplesV1().Configs().Update(context.TODO(), cfg, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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
	verifyClusterOperatorConditionsComplete(t, cfg.Status.Version, cfg.Status.ManagementState)

	// get timestamp to check against in progress condition
	now = kapis.Now()
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// now switch back to default managed for any subsequent tests
		// and confirm all the default samples content exists
		cfg = verifyOperatorUp(t)
		cfg.Spec.ManagementState = operatorsv1api.Managed
		cfg, err = crClient.SamplesV1().Configs().Update(context.TODO(), cfg, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, cfg)
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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
		cfg, err = crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if util.Condition(cfg, samplesapi.ImageChangesInProgress).LastUpdateTime.After(now.Time) {
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
	verifyClusterOperatorConditionsComplete(t, cfg.Status.Version, cfg.Status.ManagementState)
	t.Logf("Config after TestSpecManagementStateField: %#v", verifyOperatorUp(t))
}

func TestSkippedProcessing(t *testing.T) {
	if !verifyX86(t) {
		return
	}
	if verifyIPv6(t) {
		return
	}

	err := verifyConditionsCompleteSamplesAdded(t)
	if err != nil {
		dumpPod(t)
		t.Fatalf("samples not stable at start of skip cfg chg test %#v", verifyOperatorUp(t))
	}
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.SkippedImagestreams = append(cfg.Spec.SkippedImagestreams, "jenkins")
		cfg.Spec.SkippedTemplates = append(cfg.Spec.SkippedTemplates, "jenkins-ephemeral")
		cfg, err := crClient.SamplesV1().Configs().Update(context.TODO(), cfg, metav1.UpdateOptions{})
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

	verifySkippedStreamManagedLabel(t, "false")
	verifySkippedTemplateManagedLabel(t, "false")
	verifyDeletedImageStreamNotRecreated(t)
	verifyDeletedTemplatesNotRecreated(t)

	// reset skipped list back
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.SkippedImagestreams = []string{}
		cfg.Spec.SkippedTemplates = []string{}
		cfg, err = crClient.SamplesV1().Configs().Update(context.TODO(), cfg, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}
	// verify status skipped has been reset
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err := crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
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

	verifySkippedStreamManagedLabel(t, "true")
	verifySkippedTemplateManagedLabel(t, "true")

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
	if !verifyX86(t) {
		return
	}
	if verifyIPv6(t) {
		return
	}

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

func coreTestUpgrade(t *testing.T) {
	cfg := verifyOperatorUp(t)
	var err error
	if cfg.Status.ManagementState == operatorsv1api.Managed {
		err = verifyConditionsCompleteSamplesAdded(t)
		if err != nil {
			dumpPod(t)
			t.Fatalf("samples not stable at start of upgrade test %#v", verifyOperatorUp(t))
		}
	}

	newVersion := kapis.Now().String()
	t.Logf("current version %s version for upgrade %s", cfg.Status.Version, newVersion)

	// update env to trigger upgrade
	depClient := kubeClient.AppsV1().Deployments(samplesapi.OperatorNamespace)
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		dep, err := depClient.Get(context.TODO(), "cluster-samples-operator", metav1.GetOptions{})
		if err != nil {
			t.Logf("error waiting for operator deployment to exist: %v\n", err)
			return false, nil
		}
		t.Logf("found operator deployment")
		for i, env := range dep.Spec.Template.Spec.Containers[0].Env {
			t.Logf("looking at env %s", env.Name)
			if strings.TrimSpace(env.Name) == "RELEASE_VERSION" {
				t.Logf("updating RELEASE_VERSION env to %s", newVersion)
				dep.Spec.Template.Spec.Containers[0].Env[i].Value = newVersion
				_, err := depClient.Update(context.TODO(), dep, metav1.UpdateOptions{})
				if err == nil {
					return true, nil
				}
				t.Logf("%v", err)
				if kerrors.IsConflict(err) {
					return false, nil
				}
				if stub.IsRetryableAPIError(err) {
					return false, nil
				}
				return false, err
			}

		}
		return false, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("problem updating deployment env")
	}

	if cfg.Status.ManagementState == operatorsv1api.Managed {
		err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
			cfg := verifyOperatorUp(t)
			if util.ConditionTrue(cfg, samplesapi.MigrationInProgress) {
				return true, nil
			}
			return false, nil
		})
	}

	if err != nil {
		dumpPod(t)
		t.Fatalf("did not enter migration mode in time %#v", verifyOperatorUp(t))
	}

	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg := verifyOperatorUp(t)
		if cfg.Status.Version == newVersion {
			return true, nil
		}
		return false, nil
	})

	verifyClusterOperatorConditionsComplete(t, newVersion, cfg.Status.ManagementState)

	if cfg.Status.ManagementState == operatorsv1api.Managed {
		validateContent(t, nil)
	}

}

func changeMgmtState(t *testing.T, state operatorsv1api.ManagementState) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cfg := verifyOperatorUp(t)
		cfg.Spec.ManagementState = state
		cfg, err := crClient.SamplesV1().Configs().Update(context.TODO(), cfg, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("error updating Config %v and %#v", err, verifyOperatorUp(t))
	}
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		cfg, err := crClient.SamplesV1().Configs().Get(context.TODO(), samplesapi.ConfigName, metav1.GetOptions{})
		if err != nil {
			t.Logf("%v", err)
			return false, nil
		}
		if cfg.Status.ManagementState != state {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		dumpPod(t)
		t.Fatalf("cfg status mgmt never went to %s %#v", string(state), verifyOperatorUp(t))
	}

}

func TestUpgradeUnmanaged(t *testing.T) {
	changeMgmtState(t, operatorsv1api.Unmanaged)
	coreTestUpgrade(t)
}

func TestUpgradeRemoved(t *testing.T) {
	changeMgmtState(t, operatorsv1api.Removed)
	coreTestUpgrade(t)
}

func TestUpgradeManaged(t *testing.T) {
	changeMgmtState(t, operatorsv1api.Managed)
	coreTestUpgrade(t)
}
