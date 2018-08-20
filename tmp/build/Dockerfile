FROM alpine:3.6

RUN adduser -D cluster-samples-operator-tmp
USER cluster-samples-operator-tmp

ADD tmp/_output/bin/cluster-samples-operator-tmp /usr/local/bin/cluster-samples-operator-tmp
