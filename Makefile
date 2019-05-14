REPO=openshift

all: generate build build-image
generate:
	./hack/codegen/update-generated.sh
build:
#FYI if we ever add back in git version into the binary, compile like
#go build -ldflags '-X github.com/openshift/cluster-samples-operator/vendor/k8s.io/client-go/pkg/version.gitVersion=$(shell git describe) -X github.com/openshift/cluster-samples-operator/vendor/k8s.io/client-go/pkg/version.gitCommit=$(shell git rev-parse HEAD)' ./cmd/cluster-samples-operator
	go build ./cmd/cluster-samples-operator
build-image:
	# save some time setting up the docker build context by deleting this first.
	rm -f cluster-samples-operator        
	docker build -t docker.io/$(REPO)/origin-cluster-samples-operator:latest .
test: test-unit test-e2e
test-unit:
	go test ./cmd/... ./pkg/...
test-e2e:
	KUBERNETES_CONFIG=${KUBECONFIG} go test -parallel 1 -timeout 30m -v ./test/e2e/...
clean:
	rm -f cluster-samples-operator
	rm -rf _output/
update-codegen-crds:
	# reference local api def since we to date don't store this type in openshift/api
	#go run ./vendor/github.com/openshift/library-go/cmd/crd-schema-gen/main.go --domain openshift.io --apis-dir vendor/github.com/openshift/api --manifests-dir manifests/
	go run ./vendor/github.com/openshift/library-go/cmd/crd-schema-gen/main.go --domain operator.openshift.io --apis-dir pkg/apis --manifests-dir manifests/
update-codegen: update-codegen-crds
verify-codegen-crds:
	# reference local api def since we to date don't store this type in openshift/api
	#go run ./vendor/github.com/openshift/library-go/cmd/crd-schema-gen/main.go --domain openshift.io --apis-dir vendor/github.com/openshift/api --manifests-dir manifests/ --verify-only
	go run ./vendor/github.com/openshift/library-go/cmd/crd-schema-gen/main.go --domain operator.openshift.io --apis-dir pkg/apis --manifests-dir manifests/ --verify-only
verify-codegen: verify-codegen-crds
verify: verify-codegen
