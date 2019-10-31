module github.com/openshift/cluster-samples-operator

go 1.12

require (
	cloud.google.com/go v0.38.0 // indirect
	github.com/NYTimes/gziphandler v1.1.1 // indirect
	github.com/coreos/bbolt v1.3.3 // indirect
	github.com/coreos/pkg v0.0.0-20180928190104-399ea9e2e55f // indirect
	github.com/dgrijalva/jwt-go v3.2.0+incompatible // indirect
	github.com/emicklei/go-restful v2.11.1+incompatible // indirect
	github.com/evanphx/json-patch v4.5.0+incompatible // indirect
	github.com/golang/groupcache v0.0.0-20191027212112-611e8accdfc9 // indirect
	github.com/google/btree v1.0.0 // indirect
	github.com/googleapis/gnostic v0.3.1 // indirect
	github.com/gorilla/websocket v1.4.1 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.1.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway v1.11.3 // indirect
	github.com/hashicorp/golang-lru v0.5.3 // indirect
	github.com/imdario/mergo v0.3.5 // indirect
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/json-iterator/go v1.1.8 // indirect
	github.com/jteeuwen/go-bindata v3.0.7+incompatible
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/onsi/ginkgo v1.10.2 // indirect
	github.com/onsi/gomega v1.7.0 // indirect
	github.com/openshift/api v0.0.0-20190904155310-a25bb2adc83e
	github.com/openshift/client-go v0.0.0-20190813201236-5a5508328169
	github.com/openshift/library-go v0.0.0-20190904120025-7d4acc018c61
	github.com/prometheus/client_golang v0.9.2
	github.com/prometheus/client_model v0.0.0-20190812154241-14fe0d1b01d4
	github.com/prometheus/common v0.2.0
	github.com/sirupsen/logrus v1.4.2
	github.com/soheilhy/cmux v0.1.4 // indirect
	github.com/spf13/cobra v0.0.0-20180319062004-c439c4fa0937
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/tmc/grpc-websocket-proxy v0.0.0-20190109142713-0ad062ec5ee5 // indirect
	github.com/ugorji/go v1.1.7 // indirect
	github.com/xiang90/probing v0.0.0-20190116061207-43a291ad63a2 // indirect
	go.etcd.io/bbolt v1.3.3 // indirect
	go.uber.org/zap v1.12.0 // indirect
	golang.org/x/oauth2 v0.0.0-20190604053449-0f29369cfe45 // indirect
	golang.org/x/sys v0.0.0-20190826190057-c7b8b68b1456 // indirect
	google.golang.org/appengine v1.6.2 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/api v0.0.0-20191016110408-35e52d86657a
	k8s.io/apimachinery v0.0.0-20191004115801-a2eda9f80ab8
	k8s.io/apiserver v0.0.0-20191016112112-5190913f932d // indirect
	k8s.io/client-go v11.0.0+incompatible
	k8s.io/code-generator v0.0.0-20191029223907-9f431a56fdbc
	k8s.io/kube-openapi v0.0.0-20190918143330-0270cf2f1c1d // indirect
	sigs.k8s.io/structured-merge-diff v0.0.0-20191023203907-336d3378ca53 // indirect
)

//replace (
//	k8s.io/api => github.com/openshift/kubernetes-api v0.0.0-20190815163222-ffaba947a5e9
//	k8s.io/apimachinery => github.com/openshift/kubernetes-apimachinery v0.0.0-20190823072306-d19092a3cc0a
//	k8s.io/apiserver => k8s.io/apiserver v0.0.0-20190313205120-8b27c41bdbb1
//	k8s.io/client-go => github.com/openshift/kubernetes-client-go v11.0.1-0.20190701222832-70952d66b5d1+incompatible
//	k8s.io/kube-openapi => k8s.io/kube-openapi v0.0.0-20190228160746-b3a7cee44a30
//	sigs.k8s.io/structured-merge-diff => sigs.k8s.io/structured-merge-diff v0.0.0-20190302045857-e85c7b244fd2
//)
