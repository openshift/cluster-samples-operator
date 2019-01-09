package operator

import (
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
)

func (c *Controller) Bootstrap() error {
	crList, err := c.listers.Config.List(labels.Everything())
	if err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("failed to list samples custom resources: %v", err)
	}

	numCR := len(crList)
	switch {
	case numCR == 1:
		return nil
	case numCR > 1:
		return fmt.Errorf("only 1 samples custom resource supported: %#v", crList)
	}

	_, err = c.handlerStub.CreateDefaultResourceIfNeeded(nil)
	return err
}
