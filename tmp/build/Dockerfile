FROM alpine:3.6

RUN adduser -D cluster-samples-operator
USER cluster-samples-operator

ADD tmp/_output/bin/cluster-samples-operator /usr/local/bin/cluster-samples-operator
