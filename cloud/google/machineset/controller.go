package machineset

import (
	"k8s.io/client-go/rest"
	"sigs.k8s.io/cluster-api/pkg/controller/sharedinformers"
)

type IgmMachineSetController struct {
	Name string
}

func NewMachineSetController(config *rest.Config, si *sharedinformers.SharedInformers) *IgmMachineSetController {
	return &IgmMachineSetController{Name: "dupa"}
}

func (c *IgmMachineSetController) GetName() string {
	return c.Name
}

func (c *IgmMachineSetController) Run(stopCh <-chan struct{}) {
	return
}
