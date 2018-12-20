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

package controller

import (
	"sigs.k8s.io/cluster-api-provider-gcp/pkg/cloud/google"
	"sigs.k8s.io/cluster-api-provider-gcp/pkg/controller/machineset"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// AddToManagerFuncs is a list of functions to add all Controllers to the Manager
var AddToManagerFuncs []func(manager.Manager) error

// AddToManager adds all Controllers to the Manager
func AddToManager(m manager.Manager) error {
	return addToManager(m, AddToManagerFuncs)
}

func addToManager(m manager.Manager, addToManagerFuncs []func(manager.Manager) error) error {
	for _, f := range addToManagerFuncs {
		if err := f(m); err != nil {
			return err
		}
	}

	return nil
}

func AddToManagerWithMachineSet(m manager.Manager) error {
	addToManagerFuncs := append(AddToManagerFuncs, func(m manager.Manager) error {
		return machineset.AddWithActuator(m, google.MachineSetActuator)
	})
	return addToManager(m, addToManagerFuncs)

}
