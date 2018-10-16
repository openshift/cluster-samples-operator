#!/bin/sh -eu
cd "$(dirname "$0")/.."

CURRENT_CONTEXT=$(oc config current-context)
SYSTEM_ADMIN_CONTEXT=${CURRENT_CONTEXT%/*}/system:admin

make build-image

oc --context="$SYSTEM_ADMIN_CONTEXT" apply -f deploy/00-crd.yaml 

NAMESPACE=$(
    oc --context="$SYSTEM_ADMIN_CONTEXT" apply \
        -o go-template --template="{{.metadata.name}}" \
        -f ./deploy/01-namespace.yaml
)

oc --context="$SYSTEM_ADMIN_CONTEXT" -n "$NAMESPACE" apply -f ./deploy/02-sa.yaml
oc --context="$SYSTEM_ADMIN_CONTEXT" -n "$NAMESPACE" apply -f ./deploy/03-rbac.yaml
oc --context="$SYSTEM_ADMIN_CONTEXT" apply -f deploy/04-openshift-rbac.yaml
oc --context="$SYSTEM_ADMIN_CONTEXT" apply -f ./deploy/cr.yaml
oc --context="$SYSTEM_ADMIN_CONTEXT" -n "$NAMESPACE" delete --ignore-not-found deploy/cluster-samples-operator
cat ./deploy/05-operator.yaml |
    sed 's/imagePullPolicy: Always/imagePullPolicy: Never/' |
    oc --context="$SYSTEM_ADMIN_CONTEXT" -n "$NAMESPACE" apply -f -
