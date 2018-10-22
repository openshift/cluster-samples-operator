REPO=openshift

all: generate build build-image
generate:
	operator-sdk generate k8s
build:
	go build ./cmd/cluster-samples-operator
build-image:
	# save some time setting up the docker build context by deleting this first.
	rm -f cluster-samples-operator        
	docker build -t docker.io/$(REPO)/origin-cluster-samples-operator:latest .
test:
	go test ./...
clean:
	rm -f cluster-samples-operator

