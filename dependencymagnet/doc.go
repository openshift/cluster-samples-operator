// +build tools

// dependencymagnet imports code that is not an explicit go dependency, but is used in things
// like Makefile targets.
package dependencymagnet

import (
	_ "github.com/jteeuwen/go-bindata/go-bindata"
	_ "github.com/openshift/library-go/alpha-build-machinery"
	_ "k8s.io/code-generator"
	_ "github.com/openshift/api/samples"
)
