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
	go test -parallel 1 ./cmd/... ./pkg/...
test-e2e:
	KUBERNETES_CONFIG=${KUBECONFIG} go test -parallel 1 -timeout 30m -v ./test/e2e/...
clean:
	rm -f cluster-samples-operator
	rm -rf _output/

CRD_SCHEMA_GEN_VERSION := v1.0.0
crd-schema-gen:
	git clone -b $(CRD_SCHEMA_GEN_VERSION) --single-branch --depth 1 https://github.com/openshift/crd-schema-gen.git $(CRD_SCHEMA_GEN_GOPATH)/src/github.com/openshift/crd-schema-gen
	GOPATH=$(CRD_SCHEMA_GEN_GOPATH) GOBIN=$(CRD_SCHEMA_GEN_GOPATH)/bin go install $(CRD_SCHEMA_GEN_GOPATH)/src/github.com/openshift/crd-schema-gen/cmd/crd-schema-gen
.PHONY: crd-schema-gen

update-codegen-crds: CRD_SCHEMA_GEN_GOPATH :=$(shell mktemp -d)
update-codegen-crds: crd-schema-gen
	$(CRD_SCHEMA_GEN_GOPATH)/bin/crd-schema-gen --apis-dir pkg/apis --manifests-dir manifests/
.PHONY: update-codegen-crds
update-codegen: update-codegen-crds
verify-codegen-crds: CRD_SCHEMA_GEN_GOPATH :=$(shell mktemp -d)
verify-codegen-crds: crd-schema-gen
	$(CRD_SCHEMA_GEN_GOPATH)/bin/crd-schema-gen --apis-dir pkg/apis --manifests-dir manifests/ --verify-only
.PHONY: verify-codegen-crds


verify-codegen: verify-codegen-crds
verify: verify-codegen
