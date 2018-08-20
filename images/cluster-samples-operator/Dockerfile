# The FROM field specified in the upstream dockerfile should make sense for centos builds.
# It will be overwritten for brew builds by the oit utility. In this case, for example,
# oit is configured to make the FROM field specify a RHEL image base.
FROM centos:7

RUN INSTALL_PKGS="dejavu-sans-fonts wget rsync gettext git tar zip unzip" && \
    yum install -y $INSTALL_PKGS && \
    rpm -V $INSTALL_PKGS && \
    yum clean all  && \
    localedef -f UTF-8 -i en_US en_US.UTF-8

COPY cluster-samples-operator /usr/bin

LABEL io.k8s.display-name="OpenShift Cluster Samples Operator" \
      io.k8s.description="This is an operator responsible for maintaining the sample imagestreams and templates loaded in the openshift project in the cluster." \
      io.openshift.tags="openshift,operator"

USER 1001
ENTRYPOINT []
CMD ["/usr/bin/cluster-samples-operator"]
