#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

rm -rf pkg/generated

bash vendor/k8s.io/code-generator/generate-groups.sh \
deepcopy,client,lister,informer \
github.com/openshift/cluster-samples-operator/pkg/generated \
github.com/openshift/cluster-samples-operator/pkg/apis \
samples:v1 \
--go-header-file "./hack/codegen/boilerplate.go.txt"
