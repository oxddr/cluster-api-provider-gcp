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

package machineset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	"sigs.k8s.io/cluster-api/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var controllerKind = clusterv1alpha1.SchemeGroupVersion.WithKind("MachineSet")

// stateConfirmationTimeout is the amount of time allowed to wait for desired state.
var stateConfirmationTimeout = 10 * time.Second

// stateConfirmationInterval is the amount of time between polling for the desired state.
// The polling is against a local memory cache.
var stateConfirmationInterval = 100 * time.Millisecond

// Add creates a new MachineSet Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.

func AddWithActuator(mgr manager.Manager, actuator Actuator) error {
	r := newReconciler(mgr, actuator)
	return add(mgr, r, r.MachineSetToMachines)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, actuator Actuator) *ReconcileMachineSet {
	return &ReconcileMachineSet{
		Client:   mgr.GetClient(),
		scheme:   mgr.GetScheme(),
		actuator: actuator,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler, mapFn handler.ToRequestsFunc) error {
	// Create a new controller
	c, err := controller.New("machineset-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to MachineSet
	err = c.Watch(&source.Kind{Type: &clusterv1alpha1.MachineSet{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Map Machine changes to MachineSets using ControllerRef
	err = c.Watch(
		&source.Kind{Type: &clusterv1alpha1.Machine{}},
		&handler.EnqueueRequestForOwner{IsController: true, OwnerType: &clusterv1alpha1.MachineSet{}},
	)
	if err != nil {
		return err
	}

	// Map Machine changes to MachineSets by machining labels
	err = c.Watch(
		&source.Kind{Type: &clusterv1alpha1.Machine{}},
		&handler.EnqueueRequestsFromMapFunc{ToRequests: mapFn})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileMachineSet{}

// ReconcileMachineSet reconciles a MachineSet object
type ReconcileMachineSet struct {
	client.Client
	scheme   *runtime.Scheme
	actuator Actuator
}

func (r *ReconcileMachineSet) MachineSetToMachines(o handler.MapObject) []reconcile.Request {
	result := []reconcile.Request{}
	m := &clusterv1alpha1.Machine{}
	key := client.ObjectKey{Namespace: o.Meta.GetNamespace(), Name: o.Meta.GetName()}
	err := r.Client.Get(context.Background(), key, m)
	if err != nil {
		glog.Errorf("Unable to retrieve Machine %v from store: %v", key, err)
		return nil
	}

	for _, ref := range m.ObjectMeta.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return result
		}
	}

	mss := r.getMachineSetsForMachine(m)
	if len(mss) == 0 {
		glog.V(4).Infof("Found no machine set for machine: %v", m.Name)
		return nil
	}

	for _, ms := range mss {
		result = append(result, reconcile.Request{
			NamespacedName: client.ObjectKey{Namespace: ms.Namespace, Name: ms.Name}})
	}

	return result
}

// Reconcile reads that state of the cluster for a MachineSet object and makes changes based on the state read
// and what is in the MachineSet.Spec
// Automatically generate RBAC rules to allow the Controller to read and write Deployments
// +kubebuilder:rbac:groups=cluster.k8s.io,resources=machinesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.k8s.io,resources=machines,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileMachineSet) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	ctx := context.TODO()

	// Fetch the MachineSet instance
	machineSet := &clusterv1alpha1.MachineSet{}
	err := r.Get(ctx, request.NamespacedName, machineSet)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	glog.V(4).Infof("Reconcile machineset %v", machineSet.Name)
	allMachines := &clusterv1alpha1.MachineList{}

	err = r.Client.List(context.Background(), client.InNamespace(machineSet.Namespace), allMachines)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list machines, %v", err)
	}

	// Filter out irrelevant machines (deleting/mismatch labels) and claim orphaned machines.
	var filteredMachines []*clusterv1alpha1.Machine
	for idx := range allMachines.Items {
		machine := &allMachines.Items[idx]
		if shouldExcludeMachine(machineSet, machine) {
			continue
		}
		// Attempt to adopt machine if it meets previous conditions and it has no controller ref.
		if metav1.GetControllerOf(machine) == nil {
			// TODO(janluk): kill orphans?
			continue
			// if err := r.adoptOrphan(ms, machine); err != nil {
			// 	glog.Warningf("failed to adopt machine %v into machineset %v. %v", machine.Name, ms.Name, err)
			// 	continue
			// }
		}
		filteredMachines = append(filteredMachines, machine)
	}

	syncErr := r.syncReplicas(ctx, machineSet, filteredMachines)

	ms := machineSet.DeepCopy()
	newStatus := r.calculateStatus(ms, filteredMachines)

	// Always updates status as machines come up or die.
	updatedMS, err := updateMachineSetStatus(r.Client, ms, newStatus)
	if err != nil {
		if syncErr != nil {
			return reconcile.Result{}, fmt.Errorf("failed to sync machines. %v. failed to update machine set status. %v", syncErr, err)
		}
		return reconcile.Result{}, fmt.Errorf("failed to update machine set status. %v", err)
	}

	var replicas int32
	if updatedMS.Spec.Replicas != nil {
		replicas = *updatedMS.Spec.Replicas
	}

	// Resync the MachineSet after MinReadySeconds as a last line of defense to guard against clock-skew.
	// Clock-skew is an issue as it may impact whether an available replica is counted as a ready replica.
	// A replica is available if the amount of time since last transition exceeds MinReadySeconds.
	// If there was a clock skew, checking whether the amount of time since last transition to ready state
	// exceeds MinReadySeconds could be incorrect.
	// To avoid an available replica stuck in the ready state, we force a reconcile after MinReadySeconds,
	// at which point it should confirm any available replica to be available.
	if syncErr == nil && updatedMS.Spec.MinReadySeconds > 0 &&
		updatedMS.Status.ReadyReplicas == replicas &&
		updatedMS.Status.AvailableReplicas != replicas {

		return reconcile.Result{Requeue: true}, nil
	}

	return reconcile.Result{}, nil
}

// syncReplicas essentially scales machine resources up and down.
func (c *ReconcileMachineSet) syncReplicas(ctx context.Context, ms *clusterv1alpha1.MachineSet, machines []*clusterv1alpha1.Machine) error {
	if ms.Spec.Replicas == nil {
		return fmt.Errorf("the Replicas field in Spec for machineset %v is nil, this should not be allowed.", ms.Name)
	}

	cluster, err := c.getCluster(ctx, ms)
	if err != nil {
		return err
	}

	if err := c.actuator.Resize(cluster, ms); err != nil {
		glog.Errorf("Error resizing machine set %v", ms.Name)
		return nil
	}

	vms, err := c.actuator.ListMachines(cluster, ms)
	if err != nil {
		glog.Errorf("Error listing machines for machine set %v after resize", ms.Name)
		return err
	}

	existingMachines := make(map[string]bool)
	for _, m := range machines {
		existingMachines[m.Name] = true
	}

	var createdMachines []*clusterv1alpha1.Machine
	var errstrings []string
	existingVms := make(map[string]bool)
	for _, vm := range vms {
		parts := strings.Split(vm, "/")
		vm = parts[len(parts)-1]
		existingVms[vm] = true
		if !existingMachines[vm] {

			machine := c.createMachine(vm, ms)
			err := c.Client.Create(context.Background(), machine)
			if err != nil {
				glog.Errorf("unable to create a machine = %s, due to %v", machine.Name, err)
				errstrings = append(errstrings, err.Error())
				continue
			}
			createdMachines = append(createdMachines, machine)
		}
	}
	if len(errstrings) > 0 {
		return fmt.Errorf(strings.Join(errstrings, "; "))
	}

	err = c.waitForMachineCreation(createdMachines)
	if err != nil {
		return err
	}

	var machinesToDelete []*clusterv1alpha1.Machine
	for _, m := range machines {
		if !existingVms[m.Name] {
			machinesToDelete = append(machinesToDelete, m)
		}
	}

	// TODO: Add cap to limit concurrent delete calls.
	diff := len(machinesToDelete)
	errCh := make(chan error, diff)
	var wg sync.WaitGroup
	wg.Add(diff)
	for _, machine := range machinesToDelete {
		go func(targetMachine *clusterv1alpha1.Machine) {
			defer wg.Done()
			err := c.Client.Delete(context.Background(), targetMachine)
			if err != nil {
				glog.Errorf("unable to delete a machine = %s, due to %v", targetMachine.Name, err)
				errCh <- err
			}
		}(machine)
	}
	wg.Wait()

	select {
	case err := <-errCh:
		// all errors have been reported before and they're likely to be the same, so we'll only return the first one we hit.
		if err != nil {
			return err
		}
	default:
	}
	return c.waitForMachineDeletion(machinesToDelete)

}

// createMachine creates a machine resource.
// the name of the newly created resource is going to be created by the API server, we set the generateName field
func (c *ReconcileMachineSet) createMachine(machineName string, ms *clusterv1alpha1.MachineSet) *clusterv1alpha1.Machine {
	gv := clusterv1alpha1.SchemeGroupVersion
	machine := &clusterv1alpha1.Machine{
		TypeMeta: metav1.TypeMeta{
			Kind:       gv.WithKind("Machine").Kind,
			APIVersion: gv.String(),
		},
		ObjectMeta: ms.Spec.Template.ObjectMeta,
		Spec:       ms.Spec.Template.Spec,
	}
	machine.ObjectMeta.Name = machineName
	machine.ObjectMeta.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(ms, controllerKind)}
	machine.Namespace = ms.Namespace

	return machine
}

// shoudExcludeMachine returns true if the machine should be filtered out, false otherwise.
func shouldExcludeMachine(ms *clusterv1alpha1.MachineSet, machine *clusterv1alpha1.Machine) bool {
	// Ignore inactive machines.
	if metav1.GetControllerOf(machine) != nil && !metav1.IsControlledBy(machine, ms) {
		glog.V(4).Infof("%s not controlled by %v", machine.Name, ms.Name)
		return true
	}
	if !hasMatchingLabels(ms, machine) {
		return true
	}
	return false
}

func (c *ReconcileMachineSet) adoptOrphan(ms *clusterv1alpha1.MachineSet, machine *clusterv1alpha1.Machine) error {
	// Add controller reference.
	ownerRefs := machine.ObjectMeta.GetOwnerReferences()
	if ownerRefs == nil {
		ownerRefs = []metav1.OwnerReference{}
	}

	newRef := *metav1.NewControllerRef(ms, controllerKind)
	ownerRefs = append(ownerRefs, newRef)
	machine.ObjectMeta.SetOwnerReferences(ownerRefs)
	if err := c.Client.Update(context.Background(), machine); err != nil {
		glog.Warningf("Failed to update machine owner reference. %v", err)
		return err
	}
	return nil
}

func getMachinesToDelete(filteredMachines []*clusterv1alpha1.Machine, diff int) []*clusterv1alpha1.Machine {
	// TODO: Define machines deletion policies.
	// see: https://github.com/kubernetes/kube-deploy/issues/625
	return filteredMachines[:diff]
}

func (c *ReconcileMachineSet) waitForMachineCreation(machineList []*clusterv1alpha1.Machine) error {
	for _, machine := range machineList {
		pollErr := util.Poll(stateConfirmationInterval, stateConfirmationTimeout, func() (bool, error) {
			err := c.Client.Get(context.Background(),
				client.ObjectKey{Namespace: machine.Namespace, Name: machine.Name},
				&clusterv1alpha1.Machine{})
			glog.Error(err)
			if err == nil {
				return true, nil
			}
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		})
		if pollErr != nil {
			glog.Error(pollErr)
			return fmt.Errorf("failed waiting for machine object to be created. %v", pollErr)
		}
	}
	return nil
}

func (c *ReconcileMachineSet) waitForMachineDeletion(machineList []*clusterv1alpha1.Machine) error {
	for _, machine := range machineList {
		pollErr := util.Poll(stateConfirmationInterval, stateConfirmationTimeout, func() (bool, error) {
			m := &clusterv1alpha1.Machine{}
			err := c.Client.Get(context.Background(),
				client.ObjectKey{Namespace: machine.Namespace, Name: machine.Name},
				m)
			if apierrors.IsNotFound(err) || !m.DeletionTimestamp.IsZero() {
				return true, nil
			}
			return false, err
		})
		if pollErr != nil {
			glog.Error(pollErr)
			return fmt.Errorf("failed waiting for machine object to be deleted. %v", pollErr)
		}
	}
	return nil
}

func (c *ReconcileMachineSet) getCluster(ctx context.Context, ms *clusterv1alpha1.MachineSet) (*clusterv1alpha1.Cluster, error) {
	clusterList := clusterv1alpha1.ClusterList{}
	err := c.Client.List(ctx, client.InNamespace(ms.Namespace), &clusterList)
	if err != nil {
		return nil, err
	}

	switch len(clusterList.Items) {
	case 0:
		return nil, errors.New("no clusters defined")
	case 1:
		return &clusterList.Items[0], nil
	default:
		return nil, errors.New("multiple clusters defined")
	}
}
