package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/kubernetes-incubator/apiserver-builder/pkg/controller"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/util/logs"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/google/machineset"

	"sigs.k8s.io/cluster-api/pkg/controller/config"
	"sigs.k8s.io/cluster-api/pkg/controller/machinedeployment"
	"sigs.k8s.io/cluster-api/pkg/controller/sharedinformers"
)

func init() {
	config.ControllerConfig.AddFlags(pflag.CommandLine)
}

func GetAllControllers(config *rest.Config) []controller.Controller {
	shutdown := make(chan struct{})
	si := sharedinformers.NewSharedInformers(config, shutdown)
	return []controller.Controller{
		machinedeployment.NewMachineDeploymentController(config, si),
		machineset.NewMachineSetController(config, si, nil),
	}
}

func main() {
	// the following line exists to make glog happy, for more information, see: https://github.com/kubernetes/kubernetes/issues/17162
	flag.CommandLine.Parse([]string{})

	// Map go flags to pflag
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	pflag.Parse()

	logs.InitLogs()
	defer logs.FlushLogs()

	config, err := controller.GetConfig(config.ControllerConfig.Kubeconfig)
	if err != nil {
		glog.Fatalf("Could not create Config for talking to the apiserver: %v", err)
	}

	controllers := GetAllControllers(config)
	controller.StartControllerManager(controllers...)

	// Blockforever
	select {}
}
