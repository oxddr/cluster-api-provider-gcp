package machineset

import (
	clusterv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
)

type Actuator interface {
	GroupExists(*clusterv1.MachineSet) (bool, error)
	CreateGroup(*clusterv1.MachineSet) error
	DeleteGroup(*clusterv1.MachineSet) error
	ResizeGroup(*clusterv1.MachineSet) error
	ListMachines(*clusterv1.MachineSet) ([]string, error)
}
