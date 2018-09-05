package google

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	compute "google.golang.org/api/compute/v1"
	gceconfigv1 "sigs.k8s.io/cluster-api-provider-gcp/cloud/google/gceproviderconfig/v1alpha1"
	clusterv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	client "sigs.k8s.io/cluster-api/pkg/client/clientset_generated/clientset/typed/cluster/v1alpha1"
)

type MachineSetActuatorParams struct {
	ComputeService GCEClientComputeService
	V1Alpha1Client client.ClusterV1alpha1Interface
}

func NewMachineSetActuator(params MachineSetActuatorParams) (*GCEClient, error) {
	computeService, err := getOrNewComputeServiceForMachine(params.ComputeService)
	if err != nil {
		return nil, err
	}

	scheme, err := gceconfigv1.NewScheme()
	if err != nil {
		return nil, err
	}
	codec, err := gceconfigv1.NewCodec()
	if err != nil {
		return nil, err
	}

	sshCreds, err := getSshCreds()
	if err != nil {
		return nil, err
	}

	return &GCEClient{
		computeService:         computeService,
		scheme:                 scheme,
		gceProviderConfigCodec: codec,
		sshCreds:               *sshCreds,
		v1Alpha1Client:         params.V1Alpha1Client,
		// kubeadmToken:   kubeadmToken,
		// machineClient: machineClient,
		// machineSetClient: machineSetClient,
	}, nil
}

func (gce *GCEClient) GroupExists(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) (bool, error) {
	igm, err := gce.getIgm(cluster, machineSet)
	if err != nil {
		return false, err
	}
	return (igm != nil), err
}

func (gce *GCEClient) getIgm(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) (*compute.InstanceGroupManager, error) {
	machineConfig, err := gce.machineproviderconfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		return nil, err
	}
	clusterConfig, err := gce.gceProviderConfigCodec.ClusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		return nil, err
	}

	igm, err := gce.computeService.InstanceGroupManagersGet(clusterConfig.Project, machineConfig.Zone, machineSet.ObjectMeta.Name)
	if err != nil {
		// TODO: Use formal way to check for error code 404
		if strings.Contains(err.Error(), "Error 404") {
			return nil, nil
		}
		return nil, err
	}
	return igm, nil
}

func (gce *GCEClient) CreateGroup(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) error {
	exist, err := gce.GroupExists(cluster, machineSet)
	if err != nil {
		return err
	}
	if exist {
		glog.Warningf("Trying to create existing IGM [%v]", machineSet.ObjectMeta.Name)
		return nil
	}

	machineConfig, err := gce.machineproviderconfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		return err
	}
	clusterConfig, err := gce.gceProviderConfigCodec.ClusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		return err
	}

	templateName := "template-name" // TODO(janluk): fix
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

func (gce *GCEClient) handleMachineSetError(machineSet *clusterv1.MachineSet, err error) error {
	if gce.v1Alpha1Client != nil {
		message := fmt.Sprintf("error creating GCE IGM: %v", err)
		machineSet.Status.ErrorMessage = &message
		gce.v1Alpha1Client.MachineSets(machineSet.Namespace).UpdateStatus(machineSet)
	}
	glog.Errorf("Machine set error: %v", err)
	return err
}

func (gce *GCEClient) DeleteGroup(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) error {
	exist, err := gce.GroupExists(cluster, machineSet)
	if err != nil {
		return err
	}
	if !exist {
		glog.Warningf("Trying to delete non-existing IGM [%v]", machineSet.ObjectMeta.Name)
		return nil
	}
	machineConfig, err := gce.machineproviderconfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		return err
	}
	clusterConfig, err := gce.gceProviderConfigCodec.ClusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		return err
	}
	op, err := gce.computeService.InstanceGroupManagersDelete(clusterConfig.Project, machineConfig.Zone, machineSet.Name)
	if err == nil {
		err = gce.computeService.WaitForOperation(clusterConfig.Project, op)
	}
	if err != nil {
		return gce.handleMachineSetError(machineSet, err)
	}
	return nil
}

func (gce *GCEClient) ResizeGroup(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) error {
	machineConfig, err := gce.machineproviderconfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		return err
	}
	clusterConfig, err := gce.gceProviderConfigCodec.ClusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		return err
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

func (gce *GCEClient) ListMachines(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) ([]string, error) {
	instances, err := gce.listManagedInstances(cluster, machineSet)
	if err != nil {
		return nil, err
	}
	machines := make([]string, 0, len(instances))
	for _, mi := range instances {
		machines = append(machines, mi.Instance)
	}
	return machines, nil
}

func (gce *GCEClient) listManagedInstances(cluster *clusterv1.Cluster, machineSet *clusterv1.MachineSet) ([]*compute.ManagedInstance, error) {
	machineConfig, err := gce.machineproviderconfig(machineSet.Spec.Template.Spec.ProviderConfig)
	if err != nil {
		return nil, err
	}
	clusterConfig, err := gce.gceProviderConfigCodec.ClusterProviderFromProviderConfig(cluster.Spec.ProviderConfig)
	if err != nil {
		return nil, err
	}

	resp, err := gce.computeService.InstanceGroupManagersListManagedInstances(clusterConfig.Project, machineConfig.Zone, machineSet.Name)
	if err != nil {
		return nil, err
	}
	return resp.ManagedInstances, nil
}
