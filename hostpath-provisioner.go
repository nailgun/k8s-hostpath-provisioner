/*
Copyright 2016-2017 The Kubernetes Authors, Nailgun

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
	"errors"
	"flag"
	"os"
	"fmt"
	"path"
	"time"
	"strconv"
	"syscall"

	"github.com/golang/glog"
	"github.com/kubernetes-incubator/external-storage/lib/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	resyncPeriod              = 15 * time.Second
	provisionerName           = "nailgun.name/hostpath"
	exponentialBackOffOnError = false
	failedRetryThreshold      = 5
	leasePeriod               = controller.DefaultLeaseDuration
	retryPeriod               = controller.DefaultRetryPeriod
	renewDeadline             = controller.DefaultRenewDeadline
	termLimit                 = controller.DefaultTermLimit
	storageClassParamName     = "hostPathName"
	nodeAnnotationFormat      = "hostpath.nailgun.name/%v"
	pvcAnnotation             = "nailgun.name/hostpath-node"
)

type hostPathProvisioner struct {
	nodeName string
	rootPath string
	clientset *kubernetes.Clientset
}

func NewHostPathProvisioner(rootPath *string, clientset *kubernetes.Clientset) controller.Provisioner {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		glog.Fatal("env variable NODE_NAME must be set so that this provisioner can identify itself")
	}
	return &hostPathProvisioner{
		nodeName: nodeName,
		rootPath: *rootPath,
		clientset: clientset,
	}
}

func (p *hostPathProvisioner) Provision(options controller.VolumeOptions) (*v1.PersistentVolume, error) {
	myStorageTypeName := options.Parameters[storageClassParamName]
	if myStorageTypeName == "" {
		return nil, fmt.Errorf("Parameter `%v` not set for PersistentVolume `%v`", storageClassParamName, options.PVName)
	}

	node, err := p.clientset.CoreV1().Nodes().Get(p.nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	nodeAnnotations := node.ObjectMeta.Annotations
	nodeAnnotationName := fmt.Sprintf(nodeAnnotationFormat, myStorageTypeName)

	nodeStoragePath := nodeAnnotations[nodeAnnotationName]
	if nodeStoragePath == "" {
		return nil, &controller.IgnoredError{fmt.Sprintf("no `%v` annotation on this node", nodeAnnotationName)}
	}

	if requestedNodeName, exist := options.PVC.Annotations[pvcAnnotation]; exist {
		if requestedNodeName != p.nodeName {
			return nil, &controller.IgnoredError{fmt.Sprintf("PVC requests node `%v`", requestedNodeName)}
		}
	}

	hostPath := path.Join(nodeStoragePath, options.PVName)
	localPath := path.Join(p.rootPath, hostPath)

	if err := os.MkdirAll(localPath, 0777); err != nil {
		return nil, err
	}

	glog.Infof("Created directory: %v", localPath)

	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: options.PVName,
			Annotations: map[string]string{
				"nodeName": p.nodeName,
			},
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: options.PersistentVolumeReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: hostPath,
				},
			},
		},
	}

	return pv, nil
}

func (p *hostPathProvisioner) Delete(volume *v1.PersistentVolume) error {
	ann, ok := volume.Annotations["nodeName"]
	if !ok {
		return errors.New("nodeName annotation not found on PV")
	}
	if ann != p.nodeName {
		return &controller.IgnoredError{"nodeName annotation on PV does not match ours"}
	}

	hostPath := volume.Spec.PersistentVolumeSource.HostPath.Path
	localPath := path.Join(p.rootPath, hostPath)

	if err := os.RemoveAll(localPath); err != nil {
		return err
	}

	glog.Infof("Removed directory: %v", localPath)
	return nil
}

func main() {
	syscall.Umask(0)

	masterUrl := flag.String("master", "", "Kubernetes master url")
	kubeconfigPath := flag.String("kubeconfig", "", "absolute path to the kubeconfig file (use in-cluster config if not set)")
	rootPath := flag.String("root", "/", "absolute path of host root mountpoint")
	flag.Parse()
	flag.Set("logtostderr", "true")

	config, err := clientcmd.BuildConfigFromFlags(*masterUrl, *kubeconfigPath)
	if err != nil {
		glog.Fatalf("Failed to create client: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatalf("Failed to create client: %v", err)
	}

	serverVersion, err := clientset.Discovery().ServerVersion()
	if err != nil {
		glog.Fatalf("Error getting server version: %v", err)
	}
	serverVersionMajor, err := strconv.Atoi(serverVersion.Major)
	if err != nil {
		glog.Fatalf("Error getting server version: %v", err)
	}
	serverVersionMinor, err := strconv.Atoi(serverVersion.Minor)
	if err != nil {
		glog.Fatalf("Error getting server version: %v", err)
	}
	if serverVersionMajor < 1 || serverVersionMinor < 5 {
		glog.Fatalf("Unsupported server version: %v.%v", serverVersionMajor, serverVersionMinor)
	}

	hostPathProvisioner := NewHostPathProvisioner(rootPath, clientset)
	pc := controller.NewProvisionController(clientset, resyncPeriod, provisionerName, hostPathProvisioner, serverVersion.GitVersion, exponentialBackOffOnError, failedRetryThreshold, leasePeriod, renewDeadline, retryPeriod, termLimit)
	pc.Run(wait.NeverStop)
}
