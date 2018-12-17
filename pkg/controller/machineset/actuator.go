package machineset

import (
	clusterv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
)

type Actuator interface {
	Delete(*clusterv1.Cluster, *clusterv1.MachineSet) error
	Resize(*clusterv1.Cluster, *clusterv1.MachineSet) error
	ListMachines(*clusterv1.Cluster, *clusterv1.MachineSet) ([]string, error)
}
