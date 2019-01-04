// Copyright 2018 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"

	sampopclient "github.com/openshift/cluster-samples-operator/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeset "k8s.io/client-go/kubernetes"
)

var (
	kubeClient *kubeset.Clientset
)

func TestMain(m *testing.M) {
	kubeconfig, err := sampopclient.GetConfig()
	if err != nil {
		fmt.Printf("%#v", err)
		os.Exit(1)
	}

	kubeClient, err = kubeset.NewForConfig(kubeconfig)
	if err != nil {
		fmt.Printf("%#v", err)
		os.Exit(1)
	}

	// e2e test job does not guarantee our operator is up before
	// launching the test, so we need to do so.
	err = waitForOperator()
	if err != nil {
		fmt.Println("failed waiting for operator to start")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func waitForOperator() error {
	depClient := kubeClient.AppsV1().Deployments("openshift-cluster-samples-operator")
	err := wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		_, err := depClient.Get("cluster-samples-operator", metav1.GetOptions{})
		if err != nil {
			fmt.Printf("error waiting for operator deployment to exist: %v\n", err)
			return false, nil
		}
		fmt.Println("found operator deployment")
		return true, nil
	})
	return err
}
