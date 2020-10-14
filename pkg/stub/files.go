package stub

import (
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	v1 "github.com/openshift/api/samples/v1"

	"github.com/openshift/cluster-samples-operator/pkg/metrics"
)

// mutex for h.imagestreamFiles and h.templateFiles managed by caller h.buildFileMaps
func (h *Handler) processFiles(dir string, files []os.FileInfo, opcfg *v1.Config) error {
	for _, file := range files {
		if file.IsDir() {
			klog.Infof("processing subdir %s from dir %s", file.Name(), dir)
			subfiles, err := h.Filefinder.List(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "error reading in content: %v")
			}
			err = h.processFiles(dir+"/"+file.Name(), subfiles, opcfg)
			if err != nil {
				return err
			}

			continue
		}
		klog.Infof("processing file %s from dir %s", file.Name(), dir)

		if strings.HasSuffix(dir, "imagestreams") {
			path := dir + "/" + file.Name()
			imagestream, err := h.Fileimagegetter.Get(path)
			if err != nil {
				return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", path)
			}
			h.imagestreamFile[imagestream.Name] = path
			metrics.AddStream(imagestream.Name)
			continue
		}

		if strings.HasSuffix(dir, "templates") {
			template, err := h.Filetemplategetter.Get(dir + "/" + file.Name())
			if err != nil {
				return h.processError(opcfg, v1.SamplesExist, corev1.ConditionUnknown, err, "%v error reading file %s", dir+"/"+file.Name())
			}

			h.templateFile[template.Name] = dir + "/" + file.Name()

		}
	}
	return nil
}

func (h *Handler) GetBaseDir(arch string, opcfg *v1.Config) (dir string) {
	// invalid settings have already been sorted out by SpecValidation
	switch arch {
	case v1.X86Architecture:
		dir = x86ContentRootDir
	case v1.AMDArchitecture:
		dir = x86ContentRootDir
	case v1.PPCArchitecture:
		dir = ppcContentRootDir
	case v1.S390Architecture:
		dir = zContentRootDir
	default:
	}
	return dir
}
