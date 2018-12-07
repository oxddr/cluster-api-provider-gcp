/*
Copyright 2018 The Kubernetes Authors.

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

package google

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/golang/glog"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	gceconfigv1 "sigs.k8s.io/cluster-api-provider-gcp/pkg/apis/gceproviderconfig/v1alpha1"
	clusterv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var MachineSetActuator *GCEMachineSetClient

type GCEMachineSetClient struct {
	client         client.Client
	computeService GCEClientComputeService
	eventRecorder  record.EventRecorder
}

type MachineSetActuatorParams struct {
	Client          client.Client
	CloudConfigPath string
	ComputeService  GCEClientComputeService
	EventRecorder   record.EventRecorder
}

func NewMachineSetActuator(params MachineSetActuatorParams) (*GCEMachineSetClient, error) {
	// TODO(janluk):
	computeService, err := getOrNewComputeServiceForMachine(params.ComputeService, params.CloudConfigPath)
	if err != nil {
		return nil, err
	}

	return &GCEMachineSetClient{
		computeService: computeService,
		eventRecorder:  params.EventRecorder,
	}, nil
}

func (gce *GCEMachineSetClient) igmIfExists(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) (*compute.InstanceGroupManager, error) {
	machineConfig, err := machineProviderFromProviderConfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return nil, gce.handleMachineSetError(machineSet, err)
	}

	clusterConfig, err := clusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return nil, gce.handleMachineSetError(machineSet, err)
	}

	igm, err := gce.computeService.InstanceGroupManagersGet(clusterConfig.Project, machineConfig.Zone, machineSet.ObjectMeta.Name)
	if err != nil {
		if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return igm, nil
}

func (gce *GCEMachineSetClient) Create(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) error {
	igm, err := gce.igmIfExists(cluster, machineSet)
	if err != nil {
		return err
	}

	if igm == nil {
		glog.Infof("Skipped creating IGM [%v] that already exists", machineSet.ObjectMeta.Name)
		return nil
	}

	machineConfig, err := machineProviderFromProviderConfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return gce.handleMachineSetError(machineSet, err)
	}

	clusterConfig, err := clusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return gce.handleMachineSetError(machineSet, err)
	}

	templateName, err := gce.getOrCreateInstanceTemplate(machineConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return gce.handleMachineSetError(machineSet, err)
	}

	op, err := gce.computeService.InstanceGroupManagersInsert(clusterConfig.Project, machineConfig.Zone, &compute.InstanceGroupManager{
		InstanceTemplate: fmt.Sprintf("global/instanceTemplates/%s", templateName),
		Name:             machineSet.ObjectMeta.Name,
		TargetSize:       int64(*machineSet.Spec.Replicas),
	})
	if err == nil {
		err = gce.computeService.WaitForOperation(clusterConfig.Project, op)
	}
	if err != nil {
		return gce.handleMachineSetError(machineSet, err)
	}
	return nil
}

func (gce *GCEMachineSetClient) getOrCreateInstanceTemplate(machineConfig *gceconfigv1.GCEMachineProviderConfig) (string, error) {
	return "hardcoded-template", nil
}

func (gce *GCEMachineSetClient) handleMachineSetError(machineSet *clusterv1.MachineSet, err error) error {
	if gce.client != nil {
		message := fmt.Sprintf("error creating GCE IGM: %v", err)
		machineSet.Status.ErrorMessage = &message
		// gce.machineSetClient.UpdateStatus(machineSet)
		// TODO(janluk): panic?
	}
	glog.Errorf("Machine set error: %v", err)
	return err
}

func (gce *GCEMachineSetClient) Delete(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) error {
	igm, err := gce.igmIfExists(cluster, machineSet)
	if err != nil {
		return err
	}

	if igm == nil {
		glog.Infof("Skipped deleting IGM [%v] that is already deleted", machineSet.ObjectMeta.Name)
		return nil
	}

	machineConfig, err := machineProviderFromProviderConfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return gce.handleMachineSetError(machineSet, err)
	}

	clusterConfig, err := clusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return gce.handleMachineSetError(machineSet, err)
	}

	op, err := gce.computeService.InstanceGroupManagersDelete(clusterConfig.Project, machineConfig.Zone, machineSet.Name)
	if err == nil {
		err = gce.computeService.WaitForOperation(clusterConfig.Project, op)
	}
	if err != nil {
		return gce.handleMachineSetError(machineSet, err)
	}

	gce.eventRecorder.Eventf(machineSet, corev1.EventTypeNormal, "Deleted", "Deleted IGM %v", machineSet.Name)

	return nil
}

func (gce *GCEMachineSetClient) Resize(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) error {
	igm, err := gce.igmIfExists(cluster, machineSet)
	if err != nil {
		return err
	}

	if igm == nil {
		glog.Infof("Skipped deleting IGM [%v] that is already deleted", machineSet.ObjectMeta.Name)
		return nil
	}

	machineConfig, err := machineProviderFromProviderConfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return gce.handleMachineSetError(machineSet, err)
	}

	clusterConfig, err := clusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return gce.handleMachineSetError(machineSet, err)
	}

	op, err := gce.computeService.InstanceGroupManagersResize(clusterConfig.Project, machineConfig.Zone, machineSet.Name, int64(*machineSet.Spec.Replicas))
	if err == nil {
		err = gce.computeService.WaitForOperation(clusterConfig.Project, op)
	}
	if err != nil {
		return gce.handleMachineSetError(machineSet, err)
	}
	return nil
}

func (gce *GCEMachineSetClient) ListMachines(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) ([]string, error) {
	igm, err := gce.igmIfExists(cluster, machineSet)
	if err != nil {
		return nil, err
	}

	if igm == nil {
		return nil, errors.New("asdsad")
	}

	machineConfig, err := machineProviderFromProviderConfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return nil, gce.handleMachineSetError(machineSet, err)
	}

	clusterConfig, err := clusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		// TODO(janluk): proper error handling
		return nil, gce.handleMachineSetError(machineSet, err)
	}

	resp, err := gce.computeService.InstanceGroupManagersListInstances(clusterConfig.Project, machineConfig.Zone, machineSet.Name)
	if err != nil {
		return nil, err
	}

	if err != nil {
		return nil, err
	}
	machines := make([]string, 0, len(resp.ManagedInstances))
	for _, mi := range resp.ManagedInstances {
		machines = append(machines, mi.Instance)
	}
	return machines, nil
}
