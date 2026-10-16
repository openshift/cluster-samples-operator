package operator

import (
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func TestConfigNamespaceSecretInformerScopesListAndWatch(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	factory := newConfigNamespaceSecretInformerFactory(client)
	factory.Core().V1().Secrets().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)
	factory.Start(stopCh)

	const expectedSelector = "metadata.name=pull-secret"
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		listSeen, watchSeen := false, false
		for _, action := range client.Actions() {
			if action.GetNamespace() != "openshift-config" || action.GetResource().Resource != "secrets" {
				t.Fatalf("unexpected informer action: %#v", action)
			}
			switch action := action.(type) {
			case kubetesting.ListAction:
				listSeen = true
				if got := action.GetListRestrictions().Fields.String(); got != expectedSelector {
					t.Fatalf("list field selector = %q, want %q", got, expectedSelector)
				}
			case kubetesting.WatchAction:
				watchSeen = true
				if got := action.GetWatchRestrictions().Fields.String(); got != expectedSelector {
					t.Fatalf("watch field selector = %q, want %q", got, expectedSelector)
				}
			default:
				t.Fatalf("unexpected informer action: %#v", action)
			}
		}
		if listSeen && watchSeen {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("informer did not list and watch the pull-secret")
		}
	}
}
