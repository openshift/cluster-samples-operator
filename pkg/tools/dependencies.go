// +build tools
package tools

// This package contains import references to packages required only for the
// build process.
import (
	_ "github.com/openshift/build-machinery-go/make/lib"
	_ "github.com/openshift/build-machinery-go/make/targets/openshift"
	_ "github.com/openshift/build-machinery-go/make/targets/openshift/operator"
)
