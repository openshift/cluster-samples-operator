FROM openshift/origin-release:golang-1.10
COPY . /go/src/github.com/openshift/cluster-samples-operator/
RUN cd /go/src/github.com/openshift/cluster-samples-operator && \
    go build ./cmd/cluster-samples-operator

FROM centos:7
LABEL io.openshift.release.operator true

COPY --from=0 /go/src/github.com/openshift/cluster-samples-operator /usr/bin/
COPY deploy/image-references deploy/00-crd.yaml deploy/01-namespace.yaml deploy/03-openshift-rbac.yaml deploy/02-rbac.yaml deploy/04-operator.yaml /manifests/
COPY tmp/build/assets /opt/openshift/

RUN useradd cluster-samples-operator
USER cluster-samples-operator

ENTRYPOINT []
CMD ["/usr/bin/cluster-samples-operator"]
