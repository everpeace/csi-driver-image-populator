/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package image

import (
	"fmt"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"strconv"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kubernetes-csi/drivers/pkg/csi-common"
)

// TODO implement GC
type nodeServer struct {
	*csicommon.DefaultNodeServer
	driverName     string
	buildah        *buildah
	recorder       record.EventRecorder
	volumeStatuses map[string]*volumeStatus
}

type volumePhase string

var (
	// publish states
	volumePhaseInitState         volumePhase = "InitState"
	volumePhaseContainerCreated  volumePhase = "ContainerCreated"
	volumePhaseContainerMounted  volumePhase = "ContainerMounted"
	volumePhaseTargetPathMounted volumePhase = "TargetPathMounted"
	volumePhasePublished         volumePhase = "Published"

	// unpublish states
	volumePhaseTargetPathUnMounted  volumePhase = "TargetPathUnMounted"
	volumePhaseContainerCommitted   volumePhase = "ContainerCommitted"
	volumePhaseContainerUnMounted   volumePhase = "ContainerUnMounted"
	volumePhaseContainerImagePushed volumePhase = "ContainerImagePushed"
	volumePhaseCnotainerDeleted     volumePhase = "ContainerDeleted"
	volumePhaseUnPublished          volumePhase = "UnPublished"

	// evnets
	eventReasonFailed              string = "Failed"
	eventReasonPullingForVolume    string = "PullingForVolume"
	eventReasonPulledForVolume     string = "PulledForVolume"
	eventReasonCommittingForVolume string = "CommittingForVolume"
	eventReasonCommittedForVolume  string = "CommittedForVolume"
	eventReasonPushingForVolume    string = "PushingForVolume"
	eventReasonPushedForVolume     string = "PushedForVolume"
)

type volumeStatus struct {
	config          *volumeConfig
	phase           volumePhase
	provisionedRoot string
}

type volumeConfig struct {
	dockerConfigJson  string
	image             string
	postPublish       bool
	postPublishImage  string
	postPublishSquash bool
	isEphemeral       bool
	podMetadata       metav1.ObjectMeta
}

func (ns *nodeServer) initVolumeStatus(volumeId string, config *volumeConfig) error {
	if st, ok := ns.volumeStatuses[volumeId]; ok {
		return fmt.Errorf("volumeId=%s has not been fully unpublished. volumePhase=%s", volumeId, st.phase)
	}
	ns.volumeStatuses[volumeId] = &volumeStatus{
		config: config,
		phase:  volumePhaseInitState,
	}
	return nil
}

func (ns *nodeServer) deleteVolumeStatus(volumeId string) {
	if _, ok := ns.volumeStatuses[volumeId]; ok {
		delete(ns.volumeStatuses, volumeId)
	}
}

func (ns *nodeServer) getVolumeStatus(volumeId string) *volumeStatus {
	return ns.volumeStatuses[volumeId]
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	// Check arguments
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}
	volumeId := req.GetVolumeId()
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	targetPath := req.GetTargetPath()
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	// initialize volume status
	volConfig, err := getVolumeConfig(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := ns.initVolumeStatus(volumeId, volConfig); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// publish
	if err := ns.publishVolume(ctx, req, volumeId); err != nil {
		glog.Errorf("publishing volume(%s) failed: %s", volumeId, err.Error())
		glog.Error("rolling back the publish process")
		ns.recorder.Eventf(
			&corev1.Pod{ObjectMeta: volConfig.podMetadata}, corev1.EventTypeNormal,
			eventReasonFailed, "failed creating volume with image=%s : %s", volConfig.image, err,
		)
		if errRollback := ns.rollbackPublishVolume(ctx, req, volumeId); errRollback != nil {
			glog.Errorf("rolling back publishing volume(%s) process failed: %s", volumeId, err.Error())
			return nil, status.Error(codes.Internal, err.Error())
		}
		ns.deleteVolumeStatus(volumeId)
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) publishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest, volumeId string) error {
	volStatus := ns.getVolumeStatus(volumeId)
	if volStatus == nil {
		return errors.Errorf("volumeId=%s is not initialized", volumeId)
	}

	switch volStatus.phase {
	case volumePhaseInitState:
		isExist, err := ns.buildah.isContainerExist(volumeId)
		if err != nil {
			return errors.Wrapf(err, "check buildah container(name=%s) existence failed", volumeId)
		}
		if isExist {
			volStatus.phase = volumePhaseContainerCreated
			return ns.publishVolume(ctx, req, volumeId)
		}

		ns.recorder.Eventf(
			&corev1.Pod{ObjectMeta: volStatus.config.podMetadata}, corev1.EventTypeNormal,
			eventReasonPullingForVolume, "pulling %s", volStatus.config.image,
		)
		if err := ns.buildah.from(volumeId, volStatus.config.image, volStatus.config.dockerConfigJson); err != nil {
			return errors.Wrapf(err, "can't create buildah container(name=%s)", volumeId)
		}
		ns.recorder.Eventf(
			&corev1.Pod{ObjectMeta: volStatus.config.podMetadata}, corev1.EventTypeNormal,
			eventReasonPulledForVolume, "pulled %s", volStatus.config.image,
		)
		volStatus.phase = volumePhaseContainerCreated
		return ns.publishVolume(ctx, req, volumeId)
	case volumePhaseContainerCreated:
		provisionRoot, err := ns.buildah.mount(volumeId)
		if err != nil {
			return errors.Wrapf(err, "can't mount buildah container(name=%s)", volumeId)
		}
		volStatus.provisionedRoot = provisionRoot
		volStatus.phase = volumePhaseContainerMounted
		return ns.publishVolume(ctx, req, volumeId)
	case volumePhaseContainerMounted:
		readOnly := req.GetReadonly()
		options := []string{"bind"}
		if readOnly {
			options = append(options, "ro")
		}
		if err := mountTargetPath(volStatus.provisionedRoot, req.GetTargetPath(), options); err != nil {
			return errors.Wrapf(err,
				"can't mount buildah container(name=%s)'s provisioned root(=%s) to volume targetPath(=%s)",
				volumeId, volStatus.provisionedRoot, req.GetTargetPath(),
			)
		}
		volStatus.phase = volumePhaseTargetPathMounted
		return ns.publishVolume(ctx, req, volumeId)
	case volumePhaseTargetPathMounted:
		volStatus.phase = volumePhasePublished
		return ns.publishVolume(ctx, req, volumeId)
	case volumePhasePublished:
		return nil
	default:
		return errors.Errorf("internal error in publishVolume. volumeId=%s, volumePhase=%s", volumeId, volStatus.phase)
	}
}

func (ns *nodeServer) rollbackPublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest, volumeId string) error {
	volStatus := ns.getVolumeStatus(volumeId)
	if volStatus == nil {
		return errors.Errorf("volumeId=%s is not initialized", volumeId)
	}
	switch volStatus.phase {
	case volumePhaseInitState:
		return nil
	case volumePhaseContainerCreated:
		if err := ns.buildah.delete(volumeId); err != nil {
			return errors.Wrapf(err, "can't delete buildah container(name=%s)", volumeId)
		}
		volStatus.phase = volumePhaseInitState
		return ns.rollbackPublishVolume(ctx, req, volumeId)
	case volumePhaseContainerMounted:
		if err := ns.buildah.umount(volumeId); err != nil {
			return errors.Wrapf(err, "can't umount buildah container(name=%s)", volumeId)
		}
		volStatus.phase = volumePhaseContainerCreated
		return ns.rollbackPublishVolume(ctx, req, volumeId)
	case volumePhaseTargetPathMounted:
		if err := unmountTargetPath(req.GetTargetPath()); err != nil {
			return errors.Wrapf(err, "can't unmount volume(volumeId=%s) targetPath(=%s)", volumeId, req.GetTargetPath())
		}
		volStatus.phase = volumePhaseContainerMounted
		return ns.rollbackPublishVolume(ctx, req, volumeId)
	default:
		return errors.Errorf("internal error in rollbackPublishVolume. volumeId=%s, volumePhase=%s", volumeId, volStatus.phase)
	}
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	// Check arguments
	volumeId := req.GetVolumeId()
	if len(volumeId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	if err := ns.unPublishVolume(ctx, req, volumeId); err != nil {
		glog.Errorf("unpublishing volume(%s) failed: %s", volumeId, err.Error())
		volStatus := ns.getVolumeStatus(volumeId)
		if volStatus != nil {
			ns.recorder.Eventf(
				&corev1.Pod{ObjectMeta: volStatus.config.podMetadata}, corev1.EventTypeNormal,
				eventReasonFailed, "failed deleting volume: %s", err,
			)
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	ns.deleteVolumeStatus(volumeId)

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) unPublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest, volumeId string) error {
	volStatus := ns.getVolumeStatus(volumeId)
	if volStatus == nil {
		return errors.Errorf("volumeId=%s is not initialized", volumeId)
	}
	switch volStatus.phase {
	case volumePhasePublished:
		if err := unmountTargetPath(req.GetTargetPath()); err != nil {
			return errors.Wrapf(err, "can't unmount volume(volumeId=%s) targetPath(=%s)", volumeId, req.GetTargetPath())
		}
		volStatus.phase = volumePhaseTargetPathUnMounted
		return ns.unPublishVolume(ctx, req, volumeId)
	case volumePhaseTargetPathUnMounted:
		if !volStatus.config.postPublish {
			volStatus.phase = volumePhaseContainerImagePushed
			return ns.unPublishVolume(ctx, req, volumeId)
		}
		ns.recorder.Eventf(
			&corev1.Pod{ObjectMeta: volStatus.config.podMetadata}, corev1.EventTypeNormal,
			eventReasonCommittingForVolume, "committing volume as %s", volStatus.config.postPublishImage,
		)
		if err := ns.buildah.commit(req.GetVolumeId(), volStatus.config.postPublishImage, volStatus.config.postPublishSquash); err != nil {
			return errors.Wrapf(err, "can't commit buildah container(name=%s)", volumeId)
		}
		ns.recorder.Eventf(
			&corev1.Pod{ObjectMeta: volStatus.config.podMetadata}, corev1.EventTypeNormal,
			eventReasonCommittedForVolume, "committed volume as %s", volStatus.config.postPublishImage,
		)
		volStatus.phase = volumePhaseContainerCommitted
		return ns.unPublishVolume(ctx, req, volumeId)
	case volumePhaseContainerCommitted:
		if err := ns.buildah.umount(req.GetVolumeId()); err != nil {
			return errors.Wrapf(err, "can't umount buildah container(name=%s)", volumeId)
		}
		volStatus.phase = volumePhaseContainerUnMounted
		return ns.unPublishVolume(ctx, req, volumeId)
	case volumePhaseContainerUnMounted:
		ns.recorder.Eventf(
			&corev1.Pod{ObjectMeta: volStatus.config.podMetadata}, corev1.EventTypeNormal,
			eventReasonPushingForVolume, "pushing %s", volStatus.config.postPublishImage,
		)
		if err := ns.buildah.push(req.GetVolumeId(), volStatus.config.postPublishImage, volStatus.config.dockerConfigJson); err != nil {
			return errors.Wrapf(err, "can't push image(=%s)", volStatus.config.postPublishImage)
		}
		ns.recorder.Eventf(
			&corev1.Pod{ObjectMeta: volStatus.config.podMetadata}, corev1.EventTypeNormal,
			eventReasonPushedForVolume, "pushed %s", volStatus.config.postPublishImage,
		)
		volStatus.phase = volumePhaseContainerImagePushed
		return ns.unPublishVolume(ctx, req, volumeId)
	case volumePhaseContainerImagePushed:
		if err := ns.buildah.delete(req.GetVolumeId()); err != nil {
			return errors.Wrapf(err, "can't delete buildah container(name=%s)", volumeId)
		}
		volStatus.phase = volumePhaseCnotainerDeleted
		return ns.unPublishVolume(ctx, req, volumeId)
	case volumePhaseCnotainerDeleted:
		volStatus.phase = volumePhaseUnPublished
		return ns.unPublishVolume(ctx, req, volumeId)
	case volumePhaseUnPublished:
		return nil
	default:
		return errors.Errorf("internal error in unPublishVolume. volumeId=%s, volumePhase=%s", volumeId, volStatus.phase)
	}
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return &csi.NodeStageVolumeResponse{}, nil
}

func getVolumeConfig(req *csi.NodePublishVolumeRequest) (*volumeConfig, error) {
	volumeContext := req.GetVolumeContext()
	secrets := req.GetSecrets()

	volumeConfig := &volumeConfig{}
	podMetadata := &metav1.ObjectMeta{}

	podNamespace, ok := volumeContext["csi.storage.k8s.io/pod.namespace"]
	if !ok {
		return nil, errors.New("CSIDriver's spec.podInfoOnMount must be true")
	}
	podMetadata.Namespace = podNamespace

	podName, ok := volumeContext["csi.storage.k8s.io/pod.name"]
	if !ok {
		return nil, errors.New("CSIDriver's spec.podInfoOnMount must be true")
	}
	podMetadata.Name = podName

	podUid, ok := volumeContext["csi.storage.k8s.io/pod.uid"]
	if !ok {
		return nil, errors.New("CSIDriver's spec.podInfoOnMount must be true")
	}
	podMetadata.UID = types.UID(podUid)

	isEpehemeralStr, ok := volumeContext["csi.storage.k8s.io/ephemeral"]
	if !ok {
		return nil, errors.New("csi.storage.k8s.io/ephemeral is missing in volume context")
	}

	isEphemeral, err := strconv.ParseBool(isEpehemeralStr)
	if err != nil {
		return nil, errors.New("csi.storage.k8s.io/ephemeral must be boolean string")
	}
	volumeConfig.isEphemeral = isEphemeral

	if len(secrets) > 0 {
		dockerConfigJson, ok := secrets[".dockerconfigjson"]
		if !ok {
			return nil, errors.New("specified secret must have key='.dockerconfigjson'")
		}
		volumeConfig.dockerConfigJson = dockerConfigJson
	}

	image, ok := volumeContext["image"]
	if !ok {
		return nil, errors.New("it must specify image")
	}
	volumeConfig.image = image

	if postPublish, ok := volumeContext["post-publish"]; ok {
		b, err := strconv.ParseBool(postPublish)
		if err != nil {
			return nil, errors.New("post-publish must be boolean string")
		}
		volumeConfig.postPublish = b
	}

	postPublishImage, ok := volumeContext["post-publish-image"]
	if volumeConfig.postPublish && !ok {
		return nil, errors.New("post-publish-image must be specified when post-publish is true")
	}
	volumeConfig.postPublishImage = postPublishImage

	if postPublishSquash, ok := volumeContext["post-publish-squash"]; ok {
		b, err := strconv.ParseBool(postPublishSquash)
		if err != nil {
			return nil, errors.New("post-publish-squash must be boolean string")
		}
		volumeConfig.postPublishSquash = b
	}

	return volumeConfig, nil
}

func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
