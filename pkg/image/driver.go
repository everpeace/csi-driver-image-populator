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
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"

	"github.com/kubernetes-csi/drivers/pkg/csi-common"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	clientgokubescheme "k8s.io/client-go/kubernetes/scheme"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
)

type driver struct {
	csiDriver  *csicommon.CSIDriver
	driverName string
	endpoint   string
	kubeClient kubernetes.Interface

	ns *nodeServer

	cap   []*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
}

var (
	version = "0.0.1"
)

func NewDriver(driverName, nodeID, endpoint string, kubeClient kubernetes.Interface) *driver {
	glog.Infof("Driver: %v version: %v", driverName, version)

	d := &driver{
		driverName: driverName,
		endpoint:   endpoint,
		csiDriver:  csicommon.NewCSIDriver(driverName, version, nodeID),
		kubeClient: kubeClient,
	}

	d.csiDriver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	// image plugin does not support ControllerServiceCapability now.
	// If support is added, it should set to appropriate
	// ControllerServiceCapability RPC types.
	d.csiDriver.AddControllerServiceCapabilities([]csi.ControllerServiceCapability_RPC_Type{csi.ControllerServiceCapability_RPC_UNKNOWN})

	d.ns = NewNodeServer(d)
	return d
}

func NewNodeServer(d *driver) *nodeServer {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: d.kubeClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(clientgokubescheme.Scheme, corev1.EventSource{Component: d.driverName})
	return &nodeServer{
		DefaultNodeServer: csicommon.NewDefaultNodeServer(d.csiDriver),
		buildah:           &buildah{driverName: d.driverName, execPath: "/bin/buildah"},
		volumeStatuses:    map[string]*volumeStatus{},
		recorder:          recorder,
	}
}

func (d *driver) Run() {
	s := csicommon.NewNonBlockingGRPCServer()
	s.Start(d.endpoint,
		csicommon.NewDefaultIdentityServer(d.csiDriver),
		&controllerServer{csicommon.NewDefaultControllerServer(d.csiDriver)},
		d.ns)
	s.Wait()
}
