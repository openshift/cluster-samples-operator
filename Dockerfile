FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.22-openshift-4.17 AS builder
WORKDIR /go/src/github.com/openshift/cluster-samples-operator
COPY . .
RUN make build

FROM registry.ci.openshift.org/ocp/4.17:base-rhel9
COPY --from=builder /go/src/github.com/openshift/cluster-samples-operator/cluster-samples-operator /usr/bin/
RUN ln -f /usr/bin/cluster-samples-operator /usr/bin/cluster-samples-operator-watch
COPY manifests/image-references manifests/0* /manifests/
COPY vendor/github.com/openshift/api/samples/*/zz_generated.crd-manifests/*.yaml /manifests/
COPY assets/operator/ocp-x86_64 /opt/openshift/operator/x86_64
COPY assets/operator/ocp-s390x /opt/openshift/operator/s390x
COPY assets/operator/ocp-ppc64le /opt/openshift/operator/ppc64le
COPY assets/operator/ocp-aarch64 /opt/openshift/operator/aarch64
RUN useradd cluster-samples-operator
USER cluster-samples-operator
ENTRYPOINT []
CMD ["/usr/bin/cluster-samples-operator"]
LABEL io.openshift.release.operator true
