package lib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
)

// Manifest stores Kubernetes object in Raw from a file.
// It stores the GroupVersionKind for the manifest.
type Manifest struct {
	Raw []byte
	GVK schema.GroupVersionKind

	obj *unstructured.Unstructured
}

// UnmarshalJSON unmarshals bytes of single kubernetes object to Manifest.
func (m *Manifest) UnmarshalJSON(in []byte) error {
	if m == nil {
		return errors.New("Manifest: UnmarshalJSON on nil pointer")
	}

	// This happens when marshalling
	// <yaml>
	// ---	(this between two `---`)
	// ---
	// <yaml>
	if bytes.Equal(in, []byte("null")) {
		m.Raw = nil
		return nil
	}

	m.Raw = append(m.Raw[0:0], in...)
	udi, _, err := scheme.Codecs.UniversalDecoder().Decode(in, nil, &unstructured.Unstructured{})
	if err != nil {
		return fmt.Errorf("unable to decode manifest: %v", err)
	}
	ud, ok := udi.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("expected manifest to decode into *unstructured.Unstructured, got %T", ud)
	}

	m.GVK = ud.GroupVersionKind()
	m.obj = ud.DeepCopy()
	return nil
}

// Object returns underlying metav1.Object
func (m *Manifest) Object() metav1.Object { return m.obj }

// ManifestsFromFiles reads files and returns Manifests in the same order.
// files should be list of absolute paths for the manifests on disk.
func ManifestsFromFiles(files []string) ([]Manifest, error) {
	var manifests []Manifest
	var errs []error
	for _, file := range files {
		file, err := os.Open(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("error opening %s: %v", file.Name(), err))
			continue
		}
		defer file.Close()

		ms, err := ParseManifests(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("error parsing %s: %v", file.Name(), err))
			continue
		}
		manifests = append(manifests, ms...)
	}

	agg := utilerrors.NewAggregate(errs)
	if agg != nil {
		return nil, fmt.Errorf("error loading manifests: %v", agg.Error())
	}

	return manifests, nil
}

// ParseManifests parses a YAML or JSON document that may contain one or more
// kubernetes resources.
func ParseManifests(r io.Reader) ([]Manifest, error) {
	d := yaml.NewYAMLOrJSONDecoder(r, 1024)
	var manifests []Manifest
	for {
		m := Manifest{}
		if err := d.Decode(&m); err != nil {
			if err == io.EOF {
				return manifests, nil
			}
			return manifests, fmt.Errorf("error parsing: %v", err)
		}
		m.Raw = bytes.TrimSpace(m.Raw)
		if len(m.Raw) == 0 || bytes.Equal(m.Raw, []byte("null")) {
			continue
		}
		manifests = append(manifests, m)
	}
}
