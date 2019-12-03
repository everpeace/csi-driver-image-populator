package image

import (
	"fmt"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	TimeoutError = errors.New("Timeout")
)

type buildah struct {
	driverName string
	execPath   string
	timeout    time.Duration
}

func (b *buildah) runCmd(args []string) ([]byte, error) {
	execPath := b.execPath

	cmd := exec.Command(execPath, args...)

	timeout := false
	if b.timeout > 0 {
		timer := time.AfterFunc(b.timeout, func() {
			timeout = true
			// TODO: cmd.Stop()
		})
		defer timer.Stop()
	}

	output, execErr := cmd.CombinedOutput()
	if execErr != nil {
		if timeout {
			return nil, TimeoutError
		}
	}
	return output, execErr
}

func (b *buildah) isContainerExist(containerName string) (bool, error) {
	args := []string{
		"containers",
		"--format", "{{.ContainerName}}",
		"--noheading",
		"--filter", fmt.Sprintf("name=%s", containerName),
	}

	output, err := b.runCmd(args)

	glog.V(4).Infof("buildah %s: %s", strings.Join(args, " "), string(output))
	if err != nil {
		return false, errors.Wrapf(err, "'buildah %s' failed", strings.Join(args, " "))
	}

	if !regexp.MustCompile(fmt.Sprintf("^%s", containerName)).Match(output) {
		// buildah containers command exit with 0. but volumeId can't find.
		return false, nil
	}

	return true, nil
}

func (b *buildah) from(containerName, image, dockerConfigJson string) error {
	args := []string{"from", "--name", containerName, "--pull"}
	if dockerConfigJson != "" {
		authFilePath, cleanupFunc, err := b.createDockerAuth(containerName, dockerConfigJson)
		if err != nil {
			return errors.Wrapf(err, "can't create authfile=%s", authFilePath)
		}
		defer cleanupFunc()
		args = append(args, "--authfile", authFilePath)
	}
	args = append(args, image)

	output, err := b.runCmd(args)

	glog.V(4).Infof("buildah %s: %s", strings.Join(args, " "), string(output))
	if err != nil {
		return errors.Wrapf(err, "'buildah %s' failed", strings.Join(args, " "))
	}
	return nil
}

func (b *buildah) mount(containerName string) (string, error) {
	args := []string{"mount", containerName}
	output, err := b.runCmd(args)
	glog.V(4).Infof("buildah %s: %s", strings.Join(args, " "), string(output))
	if err != nil {
		return "", errors.Wrapf(err, "'buildah %s' failed", strings.Join(args, " "))
	}
	provisionedRoot := strings.TrimSpace(string(output[:]))
	glog.V(4).Infof("container(name=%s)'s mount point at %s\n", containerName, provisionedRoot)
	return provisionedRoot, nil
}

func (b *buildah) commit(containerName, image string, squash bool) error {
	args := []string{"commit"}
	if squash {
		args = append(args, "--squash")
	}
	args = append(args, containerName, image)

	output, err := b.runCmd(args)

	glog.V(4).Infof("buildah %s: %s", strings.Join(args, " "), string(output))
	if err != nil {
		return errors.Wrapf(err, "'buildah %s' failed", strings.Join(args, " "))
	}
	return nil
}

func (b *buildah) umount(containerName string) error {
	args := []string{"umount", containerName}
	output, err := b.runCmd(args)
	glog.V(4).Infof("buildah %s: %s", strings.Join(args, " "), string(output))
	if err != nil {
		return errors.Wrapf(err, "'buildah %s' failed", strings.Join(args, " "))
	}
	return nil
}

func (b *buildah) push(containerName, image, dockerConfigJson string) error {
	args := []string{"push"}
	if dockerConfigJson != "" {
		authFilePath, cleanupFunc, err := b.createDockerAuth(containerName, dockerConfigJson)
		if err != nil {
			return errors.Wrapf(err, "can't create authfile=%s", authFilePath)
		}
		defer cleanupFunc()
		args = append(args, "--authfile", authFilePath)
	}
	args = append(args, image)
	output, err := b.runCmd(args)
	glog.V(4).Infof("buildah %s: %s", strings.Join(args, " "), string(output))
	if err != nil {
		return errors.Wrapf(err, "'buildah %s' failed", strings.Join(args, " "))
	}
	return nil
}

func (b *buildah) delete(containerName string) error {
	args := []string{"delete", containerName}
	output, err := b.runCmd(args)
	glog.V(4).Infof("buildah %s: %s", strings.Join(args, " "), string(output))
	if err != nil {
		return errors.Wrapf(err, "'buildah %s' failed", strings.Join(args, " "))
	}
	return err
}

func (b *buildah) createDockerAuth(containerName, dockerConfigJson string) (string, func(), error) {
	file, err := ioutil.TempFile("", fmt.Sprintf("%s-%s-", b.driverName, containerName))
	cleanUpAuthFile := func() {
		if err := os.Remove(file.Name()); err != nil {
			glog.Errorf("can't delete %s: %s", file.Name(), err.Error())
		}
	}

	if err != nil {
		cleanUpAuthFile()
		return "", nil, err
	}
	if err = file.Chmod(0700); err != nil {
		cleanUpAuthFile()
		return "", nil, err
	}
	if _, err = file.Write(([]byte)(dockerConfigJson)); err != nil {
		cleanUpAuthFile()
		return "", nil, err
	}
	return file.Name(), cleanUpAuthFile, nil
}
