package stub

import (
	"fmt"
	"strings"

	imagev1 "github.com/openshift/api/image/v1"
	corev1 "k8s.io/api/core/v1"
	kapis "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// The importTag related functions are for our import image retry and are
// copied to some degree from the `oc import-image` code, but with various simplifications around options and
// error detection, as we generally assume the samples imagestreams are valid
// aside from not needing all the complexity, avoiding having to vendor in openshift/origin is worth
// some redundancy

func importTag(stream *imagev1.ImageStream, tag string) (*imagev1.ImageStreamImport, error) {

	// follow any referential tags to the destination
	finalTag, existing, multiple, err := followTagReferenceV1(stream, tag)
	if err != nil {
		return nil, err
	}

	if existing == nil || existing.From == nil {
		return nil, nil
	}

	// disallow re-importing anything other than DockerImage
	if existing.From != nil && existing.From.Kind != "DockerImage" {
		return nil, fmt.Errorf("tag %q points to existing %s %q, it cannot be re-imported", tag, existing.From.Kind, existing.From.Name)
	}

	// set the target item to import
	if multiple {
		tag = finalTag
	}

	// and create accompanying ImageStreamImport
	return newImageStreamImportTags(stream, map[string]string{tag: existing.From.Name}), nil
}

func followTagReferenceV1(stream *imagev1.ImageStream, tag string) (finalTag string, ref *imagev1.TagReference, multiple bool, err error) {
	seen := sets.NewString()
	for {
		if seen.Has(tag) {
			// circular reference; should not exist with samples but we sanity check
			// to avoid infinite loop
			return tag, nil, multiple, fmt.Errorf("circular reference stream %s tag %s", stream.Name, tag)
		}
		seen.Insert(tag)

		var tagRef imagev1.TagReference
		for _, t := range stream.Spec.Tags {
			if t.Name == tag {
				tagRef = t
				break
			}
		}

		if tagRef.From == nil || tagRef.From.Kind != "ImageStreamTag" {
			// terminating tag
			return tag, &tagRef, multiple, nil
		}

		// The reference needs to be followed with two format patterns:
		// a) sameis:sometag and b) sometag
		if strings.Contains(tagRef.From.Name, ":") {
			tagref := splitImageStreamTag(tagRef.From.Name)
			// sameis:sometag - follow the reference as sometag
			tag = tagref
		} else {
			// sometag - follow the reference
			tag = tagRef.From.Name
		}
		multiple = true
	}
}

func newImageStreamImportTags(stream *imagev1.ImageStream, tags map[string]string) *imagev1.ImageStreamImport {
	isi := newImageStreamImport(stream)
	for tag, from := range tags {
		var insecure, scheduled bool
		oldTagFound := false
		var oldTag imagev1.TagReference
		for _, t := range stream.Spec.Tags {
			if t.Name == tag {
				oldTag = t
				oldTagFound = true
				break
			}
		}

		if oldTagFound {
			insecure = oldTag.ImportPolicy.Insecure
			scheduled = oldTag.ImportPolicy.Scheduled
		}
		isi.Spec.Images = append(isi.Spec.Images, imagev1.ImageImportSpec{
			From: corev1.ObjectReference{
				Kind: "DockerImage",
				Name: from,
			},
			To: &corev1.LocalObjectReference{Name: tag},
			ImportPolicy: imagev1.TagImportPolicy{
				Insecure:  insecure,
				Scheduled: scheduled,
			},
			ReferencePolicy: getReferencePolicy(),
		})
	}
	return isi
}

func newImageStreamImport(stream *imagev1.ImageStream) *imagev1.ImageStreamImport {
	isi := &imagev1.ImageStreamImport{
		ObjectMeta: kapis.ObjectMeta{
			Name:            stream.Name,
			Namespace:       stream.Namespace,
			ResourceVersion: stream.ResourceVersion,
		},
		Spec: imagev1.ImageStreamImportSpec{Import: true},
	}
	return isi
}

func splitImageStreamTag(nameAndTag string) (tag string) {
	parts := strings.SplitN(nameAndTag, ":", 2)
	if len(parts) > 1 {
		tag = parts[1]
	}
	if len(tag) == 0 {
		tag = "latest"
	}
	return tag
}

func getReferencePolicy() imagev1.TagReferencePolicy {
	ref := imagev1.TagReferencePolicy{}
	ref.Type = imagev1.LocalTagReferencePolicy
	return ref
}
