REPO=openshift

all: generate build build-image
generate:
	operator-sdk generate k8s
build:
	go build -ldflags '-X github.com/openshift/cluster-samples-operator/vendor/k8s.io/client-go/pkg/version.gitVersion=$(shell git describe) -X github.com/openshift/cluster-samples-operator/vendor/k8s.io/client-go/pkg/version.gitCommit=$(shell git rev-parse HEAD)' ./cmd/cluster-samples-operator
build-image:
	# save some time setting up the docker build context by deleting this first.
	rm -f cluster-samples-operator        
	docker build -t docker.io/$(REPO)/origin-cluster-samples-operator:latest .
test: test-unit test-e2e
test-unit:
	go test ./cmd/... ./pkg/...
test-e2e:
	KUBERNETES_CONFIG=${KUBECONFIG} go test -timeout 30m -v ./test/e2e/...
clean:
	rm -f cluster-samples-operator

