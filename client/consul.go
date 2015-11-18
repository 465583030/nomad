package client

import (
	"fmt"
	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/nomad/nomad/structs"
	"log"
	"path"
	"sync"
	"time"
)

const (
	syncInterval = 5 * time.Second
)

type trackedService struct {
	allocId string
	task    *structs.Task
	service *structs.Service
}

type ConsulClient struct {
	client     *consul.Client
	logger     *log.Logger
	shutdownCh chan struct{}

	trackedServices map[string]*trackedService
	trackedSrvLock  sync.Mutex
}

func NewConsulClient(logger *log.Logger, consulAddr string) (*ConsulClient, error) {
	var err error
	var c *consul.Client
	cfg := consul.DefaultConfig()
	cfg.Address = consulAddr
	if c, err = consul.NewClient(cfg); err != nil {
		return nil, err
	}

	consulClient := ConsulClient{
		client:          c,
		logger:          logger,
		trackedServices: make(map[string]*trackedService),
		shutdownCh:      make(chan struct{}),
	}

	return &consulClient, nil
}

func (c *ConsulClient) Register(task *structs.Task, allocID string) error {
	var mErr multierror.Error
	for _, service := range task.Services {
		c.logger.Printf("[INFO] Registering service %s with Consul.", service.Name)
		if err := c.registerService(service, task, allocID); err != nil {
			mErr.Errors = append(mErr.Errors, err)
		}
		ts := &trackedService{
			allocId: allocID,
			task:    task,
			service: service,
		}
		c.trackedSrvLock.Lock()
		c.trackedServices[service.Id] = ts
		c.trackedSrvLock.Unlock()
	}

	return mErr.ErrorOrNil()
}

func (c *ConsulClient) Deregister(task *structs.Task) error {
	var mErr multierror.Error
	for _, service := range task.Services {
		c.logger.Printf("[INFO] De-Registering service %v with Consul", service.Name)
		if err := c.deregisterService(service.Id); err != nil {
			c.logger.Printf("[ERROR] Error in de-registering service %v from Consul", service.Name)
			mErr.Errors = append(mErr.Errors, err)
		}
		c.trackedSrvLock.Lock()
		delete(c.trackedServices, service.Id)
		c.trackedSrvLock.Unlock()
	}
	return mErr.ErrorOrNil()
}

func (c *ConsulClient) ShutDown() {
	close(c.shutdownCh)
}

func (c *ConsulClient) findPortAndHostForLabel(portLabel string, task *structs.Task) (string, int) {
	for _, network := range task.Resources.Networks {
		if p, ok := network.MapLabelToValues()[portLabel]; ok {
			return network.IP, p
		}
	}
	return "", 0
}

func (c *ConsulClient) SyncWithConsul() {
	sync := time.After(syncInterval)
	agent := c.client.Agent()

	for {
		select {
		case <-sync:
			sync = time.After(syncInterval)
			var consulServices map[string]*consul.AgentService
			var err error

			// Get the list of the services that Consul knows about
			if consulServices, err = agent.Services(); err != nil {
				c.logger.Printf("[DEBUG] Error while syncing services with Consul: %v", err)
				continue
			}

			// See if we have services that Consul doesn't know about yet.
			// Register with Consul the services which are not registered
			for serviceId := range c.trackedServices {
				if _, ok := consulServices[serviceId]; !ok {
					ts := c.trackedServices[serviceId]
					c.registerService(ts.service, ts.task, ts.allocId)
				}
			}

			// See if consul thinks we have some services which are not running
			// anymore on the node. We de-register those services
			for serviceId := range consulServices {
				if serviceId == "consul" {
					continue
				}
				if _, ok := c.trackedServices[serviceId]; !ok {
					if err := c.deregisterService(serviceId); err != nil {
						c.logger.Printf("[DEBUG] Error while de-registering service with ID: %s", serviceId)
					}
				}
			}
		case <-c.shutdownCh:
			c.logger.Printf("[INFO] Shutting down Consul Client")
			return
		}
	}
}

func (c *ConsulClient) registerService(service *structs.Service, task *structs.Task, allocID string) error {
	var mErr multierror.Error
	service.Id = fmt.Sprintf("%s-%s", allocID, task.Name)
	host, port := c.findPortAndHostForLabel(service.PortLabel, task)
	if host == "" || port == 0 {
		return fmt.Errorf("The port:%s marked for registration of service: %s couldn't be found", service.PortLabel, service.Name)
	}
	checks := c.makeChecks(service, host, port)
	asr := &consul.AgentServiceRegistration{
		ID:      service.Id,
		Name:    service.Name,
		Tags:    service.Tags,
		Port:    port,
		Address: host,
		Checks:  checks,
	}
	if err := c.client.Agent().ServiceRegister(asr); err != nil {
		c.logger.Printf("[ERROR] Error while registering service %v with Consul: %v", service.Name, err)
		mErr.Errors = append(mErr.Errors, err)
	}
	return mErr.ErrorOrNil()
}

func (c *ConsulClient) deregisterService(serviceId string) error {
	if err := c.client.Agent().ServiceDeregister(serviceId); err != nil {
		return err
	}
	return nil
}

func (c *ConsulClient) makeChecks(service *structs.Service, ip string, port int) []*consul.AgentServiceCheck {
	var checks []*consul.AgentServiceCheck
	for _, check := range service.Checks {
		c := &consul.AgentServiceCheck{
			Interval: check.Interval.String(),
			Timeout:  check.Timeout.String(),
		}
		switch check.Type {
		case structs.ServiceCheckHTTP:
			baseUrl := fmt.Sprintf("%s://%s:%d", check.Protocol, ip, port)
			c.HTTP = path.Join(baseUrl, check.Path)
		case structs.ServiceCheckTCP:
			c.TCP = fmt.Sprintf("%s:%d", ip, port)
		case structs.ServiceCheckScript:
			c.Script = check.Script // TODO This needs to include the path of the alloc dir and based on driver types
		}
		checks = append(checks, c)
	}
	return checks
}
