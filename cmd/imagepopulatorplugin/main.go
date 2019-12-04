/*
Copyright 2019 The Kubernetes Authors.

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

package main

import (
	"flag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"

	"github.com/kubernetes-csi/csi-driver-image-populator/pkg/image"
	"k8s.io/client-go/tools/clientcmd"
)

func init() {
	flag.Set("logtostderr", "true")
}

var (
	endpoint   = flag.String("endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	driverName = flag.String("drivername", "image.csi.k8s.io", "name of the driver")
	nodeID     = flag.String("nodeid", "", "node id")
	masterURL  = flag.String("masterURL", "", "master url")
	kubeconfig = flag.String("kubeconfig", "", "kubeconfig path")
)

func main() {
	flag.Parse()

	handle()
	os.Exit(0)
}

func handle() {
	config, err := clientcmd.BuildConfigFromFlags(*masterURL, *kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	kubeClient, err := kubernetes.NewForConfig(rest.AddUserAgent(config, "mpi-operator"))
	if err != nil {
		panic(err.Error())
	}
	driver := image.NewDriver(*driverName, *nodeID, *endpoint, kubeClient)
	driver.Run()
}
