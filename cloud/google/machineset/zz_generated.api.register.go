package machineset

import (
	"github.com/golang/glog"
	"github.com/kubernetes-incubator/apiserver-builder/pkg/controller"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	clusterv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	"sigs.k8s.io/cluster-api/pkg/controller/sharedinformers"
)

type SetActuator interface {
	GroupExists(*clusterv1.MachineSet) (bool, error)
	CreateGroup(*clusterv1.MachineSet) error
	DeleteGroup(*clusterv1.MachineSet) error
	ResizeGroup(*clusterv1.MachineSet) error
	ListMachines(*clusterv1.MachineSet) ([]string, error)
}

// MachineSetController implements the controller.MachineSetController interface
type MachineSetController struct {
	queue *controller.QueueWorker

	// Handles messages
	controller *MachineSetControllerImpl

	Name string

	BeforeReconcile func(key string)
	AfterReconcile  func(key string, err error)

	Informers *sharedinformers.SharedInformers
}

// NewController returns a new MachineSetController for responding to MachineSet events
func NewMachineSetController(config *rest.Config, si *sharedinformers.SharedInformers, actuator SetActuator) *MachineSetController {
	q := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "MachineSet")

	queue := &controller.QueueWorker{q, 10, "MachineSet", nil}
	c := &MachineSetController{queue, nil, "MachineSet", nil, nil, si}

	// For non-generated code to add events
	c.controller = &MachineSetControllerImpl{}
	c.controller.Init(&sharedinformers.ControllerInitArgumentsImpl{si, config, c.LookupAndReconcile}, actuator)

	queue.Reconcile = c.reconcile
	if c.Informers.WorkerQueues == nil {
		c.Informers.WorkerQueues = map[string]*controller.QueueWorker{}
	}
	c.Informers.WorkerQueues["MachineSet"] = queue
	si.Factory.Cluster().V1alpha1().MachineSets().Informer().
		AddEventHandler(&controller.QueueingEventHandler{q, nil, false})
	return c
}

func (c *MachineSetController) GetName() string {
	return c.Name
}

func (c *MachineSetController) LookupAndReconcile(key string) (err error) {
	return c.reconcile(key)
}

func (c *MachineSetController) reconcile(key string) (err error) {
	var namespace, name string

	if c.BeforeReconcile != nil {
		c.BeforeReconcile(key)
	}
	if c.AfterReconcile != nil {
		// Wrap in a function so err is evaluated after it is set
		defer func() { c.AfterReconcile(key, err) }()
	}

	namespace, name, err = cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return
	}

	u, err := c.controller.Get(namespace, name)
	if errors.IsNotFound(err) {
		glog.Infof("Not doing work for MachineSet %v because it has been deleted", key)
		// Set error so it is picked up by AfterReconcile and the return function
		err = nil
		return
	}
	if err != nil {
		glog.Errorf("Unable to retrieve MachineSet %v from store: %v", key, err)
		return
	}

	// Set error so it is picked up by AfterReconcile and the return function
	err = c.controller.Reconcile(u)

	return
}

func (c *MachineSetController) Run(stopCh <-chan struct{}) {
	for _, q := range c.Informers.WorkerQueues {
		q.Run(stopCh)
	}
	controller.GetDefaults(c.controller).Run(stopCh)
}
