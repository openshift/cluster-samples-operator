FROM registry.svc.ci.openshift.org/openshift/release:golang-1.10 AS builder
WORKDIR /go/src/github.com/openshift/cluster-samples-operator
COPY . .
RUN make build

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
COPY --from=builder /go/src/github.com/openshift/cluster-samples-operator/cluster-samples-operator /usr/bin/
COPY deploy/image-references deploy/00-crd.yaml deploy/01-namespace.yaml deploy/02-sa.yaml  deploy/03-rbac.yaml  deploy/04-openshift-rbac.yaml  deploy/05-operator.yaml /manifests/
COPY tmp/build/assets /opt/openshift/
RUN useradd cluster-samples-operator
USER cluster-samples-operator
ENTRYPOINT []
CMD ["/usr/bin/cluster-samples-operator"]
LABEL io.openshift.release.operator true
