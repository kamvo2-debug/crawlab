package service

import (
	"errors"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/cenkalti/backoff/v4"
	"github.com/crawlab-team/crawlab/core/constants"
	"github.com/crawlab-team/crawlab/core/grpc/server"
	"github.com/crawlab-team/crawlab/core/interfaces"
	"github.com/crawlab-team/crawlab/core/models/common"
	"github.com/crawlab-team/crawlab/core/models/models"
	"github.com/crawlab-team/crawlab/core/models/service"
	"github.com/crawlab-team/crawlab/core/node/config"
	"github.com/crawlab-team/crawlab/core/notification"
	"github.com/crawlab-team/crawlab/core/schedule"
	"github.com/crawlab-team/crawlab/core/system"
	"github.com/crawlab-team/crawlab/core/task/handler"
	"github.com/crawlab-team/crawlab/core/task/scheduler"
	"github.com/crawlab-team/crawlab/core/utils"
	"github.com/crawlab-team/crawlab/grpc"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongo2 "go.mongodb.org/mongo-driver/mongo"
)

type MasterService struct {
	// dependencies
	cfgSvc           interfaces.NodeConfigService
	server           *server.GrpcServer
	taskSchedulerSvc *scheduler.Service
	taskHandlerSvc   *handler.Service
	scheduleSvc      *schedule.Service
	systemSvc        *system.Service
	healthSvc        *HealthService

	// settings
	monitorInterval time.Duration

	// internals
	interfaces.Logger
}

func (svc *MasterService) Start() {
	// gRPC server is now started earlier in main.go to avoid race conditions
	// No need to start it here anymore

	// register to db
	if err := svc.Register(); err != nil {
		panic(err)
	}

	// start health service
	go svc.healthSvc.Start(func() bool {
		// Master-specific health check: verify gRPC server and core services are running
		return svc.server != nil && 
			svc.taskSchedulerSvc != nil && 
			svc.taskHandlerSvc != nil && 
			svc.scheduleSvc != nil
	})

	// mark as ready after registration
	svc.healthSvc.SetReady(true)

	// create indexes
	go common.InitIndexes()

	// start monitoring worker nodes
	go svc.startMonitoring()

	// start task handler
	go svc.taskHandlerSvc.Start()

	// start task scheduler
	go svc.taskSchedulerSvc.Start()

	// start schedule service
	go svc.scheduleSvc.Start()

	// wait for quit signal
	svc.Wait()

	// stop
	svc.Stop()
}

func (svc *MasterService) Wait() {
	utils.DefaultWait()
}

func (svc *MasterService) Stop() {
	_ = svc.server.Stop()
	svc.taskHandlerSvc.Stop()
	if svc.healthSvc != nil {
		svc.healthSvc.Stop()
	}
	svc.Infof("master[%s] service has stopped", svc.cfgSvc.GetNodeKey())
}

func (svc *MasterService) startMonitoring() {
	svc.Infof("master[%s] monitoring started", svc.cfgSvc.GetNodeKey())

	// ticker
	ticker := time.NewTicker(svc.monitorInterval)

	for {
		// monitor
		err := svc.monitor()
		if err != nil {
			svc.Errorf("master[%s] monitor error: %v", svc.cfgSvc.GetNodeKey(), err)
		}

		// wait
		<-ticker.C
	}
}

func (svc *MasterService) Register() (err error) {
	nodeKey := svc.cfgSvc.GetNodeKey()
	nodeName := svc.cfgSvc.GetNodeName()
	nodeMaxRunners := svc.cfgSvc.GetMaxRunners()
	node, err := service.NewModelService[models.Node]().GetOne(bson.M{"key": nodeKey}, nil)
	if err != nil && err.Error() == mongo2.ErrNoDocuments.Error() {
		// not exists
		svc.Infof("master[%s] does not exist in db", nodeKey)
		node := models.Node{
			Key:        nodeKey,
			Name:       nodeName,
			MaxRunners: nodeMaxRunners,
			IsMaster:   true,
			Status:     constants.NodeStatusOnline,
			Enabled:    true,
			Active:     true,
			ActiveAt:   time.Now(),
		}
		node.SetCreated(primitive.NilObjectID)
		node.SetUpdated(primitive.NilObjectID)
		_, err := service.NewModelService[models.Node]().InsertOne(node)
		if err != nil {
			svc.Errorf("save master[%s] to db error: %v", nodeKey, err)
			return err
		}
		svc.Infof("added master[%s] to db", nodeKey)
		return nil
	} else if err == nil {
		// exists
		svc.Infof("master[%s] exists in db", nodeKey)
		node.Status = constants.NodeStatusOnline
		node.Active = true
		node.ActiveAt = time.Now()
		err = service.NewModelService[models.Node]().ReplaceById(node.Id, *node)
		if err != nil {
			svc.Errorf("update master[%s] in db error: %v", nodeKey, err)
			return err
		}
		svc.Infof("updated master[%s] in db", nodeKey)
		return nil
	} else {
		// error
		return err
	}
}

func (svc *MasterService) monitor() (err error) {
	// update master node status in db
	if err := svc.updateMasterNodeStatus(); err != nil {
		if errors.Is(err, mongo2.ErrNoDocuments) {
			return nil
		}
		return err
	}

	// all worker nodes
	workerNodes, err := svc.getAllWorkerNodes()
	if err != nil {
		return err
	}

	// iterate all worker nodes
	wg := sync.WaitGroup{}
	wg.Add(len(workerNodes))
	for _, n := range workerNodes {
		go func(n *models.Node) {
			defer wg.Done()

			// subscribe
			ok := svc.subscribeNode(n)
			if !ok {
				go svc.setWorkerNodeOffline(n)
				return
			}

			// ping client
			ok = svc.pingNodeClient(n)
			if !ok {
				go svc.setWorkerNodeOffline(n)
				return
			}

			// update node available runners
			_ = svc.updateNodeRunners(n)
		}(&n)
	}

	wg.Wait()

	return nil
}

func (svc *MasterService) getAllWorkerNodes() (nodes []models.Node, err error) {
	query := bson.M{
		"key":    bson.M{"$ne": svc.cfgSvc.GetNodeKey()}, // not self
		"active": true,                                   // active
	}
	nodes, err = service.NewModelService[models.Node]().GetMany(query, nil)
	if err != nil {
		if errors.Is(err, mongo2.ErrNoDocuments) {
			return nil, nil
		}
		svc.Errorf("get all worker nodes error: %v", err)
		return nil, err
	}
	return nodes, nil
}

func (svc *MasterService) updateMasterNodeStatus() (err error) {
	nodeKey := svc.cfgSvc.GetNodeKey()
	node, err := service.NewModelService[models.Node]().GetOne(bson.M{"key": nodeKey}, nil)
	if err != nil {
		return err
	}
	oldStatus := node.Status

	node.Status = constants.NodeStatusOnline
	node.Active = true
	node.ActiveAt = time.Now()
	newStatus := node.Status

	err = service.NewModelService[models.Node]().ReplaceById(node.Id, *node)
	if err != nil {
		return err
	}

	if utils.IsPro() {
		if oldStatus != newStatus {
			go svc.sendNotification(node)
		}
	}

	return nil
}

func (svc *MasterService) setWorkerNodeOffline(node *models.Node) {
	node.Status = constants.NodeStatusOffline
	node.Active = false
	err := backoff.Retry(func() error {
		return service.NewModelService[models.Node]().ReplaceById(node.Id, *node)
	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(1*time.Second), 3))
	if err != nil {
		log.Errorf("failed to set worker node[%s] offline: %v", node.Key, err)
	}
	svc.sendNotification(node)
}

func (svc *MasterService) subscribeNode(n *models.Node) (ok bool) {
	_, ok = svc.server.NodeSvr.GetSubscribeStream(n.Id)
	return ok
}

func (svc *MasterService) pingNodeClient(n *models.Node) (ok bool) {
	stream, ok := svc.server.NodeSvr.GetSubscribeStream(n.Id)
	if !ok {
		svc.Errorf("cannot get worker node client[%s]", n.Key)
		return false
	}
	err := stream.Send(&grpc.NodeServiceSubscribeResponse{
		Code: grpc.NodeServiceSubscribeCode_HEARTBEAT,
	})
	if err != nil {
		svc.Errorf("failed to ping worker node client[%s]: %v", n.Key, err)
		return false
	}
	return true
}

func (svc *MasterService) updateNodeRunners(node *models.Node) (err error) {
	query := bson.M{
		"node_id": node.Id,
		"status":  constants.TaskStatusRunning,
	}
	runningTasksCount, err := service.NewModelService[models.Task]().Count(query)
	if err != nil {
		svc.Errorf("failed to count running tasks for node[%s]: %v", node.Key, err)
		return err
	}
	node.CurrentRunners = runningTasksCount
	err = service.NewModelService[models.Node]().ReplaceById(node.Id, *node)
	if err != nil {
		svc.Errorf("failed to update node runners for node[%s]: %v", node.Key, err)
		return err
	}
	return nil
}

func (svc *MasterService) sendNotification(node *models.Node) {
	if !utils.IsPro() {
		return
	}
	go notification.GetNotificationService().SendNodeNotification(node)
}

func newMasterService() *MasterService {
	return &MasterService{
		cfgSvc:           config.GetNodeConfigService(),
		monitorInterval:  15 * time.Second,
		server:           server.GetGrpcServer(),
		taskSchedulerSvc: scheduler.GetTaskSchedulerService(),
		taskHandlerSvc:   handler.GetTaskHandlerService(),
		scheduleSvc:      schedule.GetScheduleService(),
		systemSvc:        system.GetSystemService(),
		healthSvc:        GetHealthService(),
		Logger:           utils.NewLogger("MasterService"),
	}
}

var masterService *MasterService
var masterServiceOnce sync.Once

func GetMasterService() *MasterService {
	masterServiceOnce.Do(func() {
		masterService = newMasterService()
	})
	return masterService
}
