package image

import (
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"k8s.io/kubernetes/pkg/util/mount"
)

func mountTargetPath(provisionedRoot, targetPath string, options []string) error {
	mounter := mount.New("")
	if err := mounter.Mount(provisionedRoot, targetPath, "", options); err != nil {
		return errors.Wrapf(err, "mount %s to %s with options=%s failed", provisionedRoot, targetPath, options)
	}
	return nil
}

func unmountTargetPath(targetPath string) error {
	notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
	if err != nil {
		return errors.Wrapf(err, "unmount %s failed", targetPath)
	}
	if !notMnt {
		err := mount.New("").Unmount(targetPath)
		if err != nil {
			return errors.Wrapf(err, "unmount %s failed", targetPath)
		}
	}
	glog.V(4).Infof("path %s has been unmounted.", targetPath)
	return nil
}
