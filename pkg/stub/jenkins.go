package stub

import (
	"os"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/sirupsen/logrus"
)

func tagInPayload(tag, env string, stream *imagev1.ImageStream) *imagev1.ImageStream {
	imageRef := os.Getenv(env)
	if len(imageRef) == 0 {
		logrus.Warningf("The environment variable %s was not set and we cannot update the %s:%s image references", env, stream.Name, tag)
		return stream
	}
	for _, tagSpec := range stream.Spec.Tags {
		if tagSpec.Name == tag {
			logrus.Printf("updating image ref for tag %s in stream %s with image %s", tag, stream.Name, imageRef)
			tagSpec.From.Name = imageRef
			break
		}
	}
	return stream
}

func jenkinsOverrides(imagestream *imagev1.ImageStream) *imagev1.ImageStream {
	// we override what is at openshift/library for the jenkins images
	// to point to what we have seeded in the payload
	switch {
	case imagestream.Name == "jenkins":
		return tagInPayload("2", "IMAGE_JENKINS", imagestream)
	case imagestream.Name == "jenkins-agent-maven":
		return tagInPayload("v4.0", "IMAGE_AGENT_MAVEN", imagestream)
	case imagestream.Name == "jenkins-agent-nodejs":
		return tagInPayload("v4.0", "IMAGE_AGENT_NODEJS", imagestream)
	}
	// otherwise return as is
	return imagestream
}
