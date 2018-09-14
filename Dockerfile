FROM openshift/origin-release:golang-1.10
COPY . /go/src/github.com/openshift/cluster-samples-operator/
RUN cd /go/src/github.com/openshift/cluster-samples-operator && \
    go build ./cmd/cluster-samples-operator

FROM centos:7
LABEL io.openshift.release.operator true

COPY --from=0 /go/src/github.com/openshift/cluster-samples-operator /usr/bin/
COPY deploy/image-references deploy/crd.yaml deploy/namespace.yaml deploy/operator.yaml deploy/rbac.yaml /manifests/
COPY tmp/build/assets /opt/openshift/

RUN useradd cluster-samples-operator
USER cluster-samples-operator

ENTRYPOINT []
CMD ["/usr/bin/cluster-samples-operator"]
