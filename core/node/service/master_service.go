package service

import (
	"errors"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/cenkalti/backoff/v4"
	"github.com/crawlab-team/crawlab/core/constants"
	"github.com/crawlab-team/crawlab/core/grpc/client"
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
	cfgSvc                interfaces.NodeConfigService
	server                *server.GrpcServer
	taskSchedulerSvc      *scheduler.Service
	taskHandlerSvc        *handler.Service
	scheduleSvc           *schedule.Service
	systemSvc             *system.Service
	healthSvc             *HealthService
	nodeMonitoringSvc     *NodeMonitoringService
	taskReconciliationSvc *TaskReconciliationService

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

	// start task reconciliation service for periodic status checks
	go svc.taskReconciliationSvc.StartPeriodicReconciliation()

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
		// monitor worker nodes
		err := svc.monitor()
		if err != nil {
			svc.Errorf("master[%s] monitor error: %v", svc.cfgSvc.GetNodeKey(), err)
		}

		// monitor gRPC client health on master
		svc.monitorGrpcClientHealth()

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
	oldStatus, newStatus, err := svc.nodeMonitoringSvc.UpdateMasterNodeStatus()
	if err != nil {
		if errors.Is(err, mongo2.ErrNoDocuments) {
			return nil
		}
		return err
	}

	// send notification if status changed
	if utils.IsPro() && oldStatus != newStatus {
		go svc.sendMasterStatusNotification(oldStatus, newStatus)
	}

	// all worker nodes
	workerNodes, err := svc.nodeMonitoringSvc.GetAllWorkerNodes()
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

			// if both subscribe and ping succeed, ensure node is marked as online
			go svc.setWorkerNodeOnline(n)

			// handle reconnection - reconcile disconnected tasks
			go svc.taskReconciliationSvc.HandleNodeReconnection(n)

			// update node available runners
			_ = svc.nodeMonitoringSvc.UpdateNodeRunners(n)
		}(&n)
	}

	wg.Wait()

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

	// Update running tasks on the offline node to abnormal status
	svc.taskReconciliationSvc.HandleTasksForOfflineNode(node)

	svc.sendNotification(node)
}

func (svc *MasterService) setWorkerNodeOnline(node *models.Node) {
	// Only update if the node is currently offline
	if node.Status == constants.NodeStatusOnline {
		return
	}

	oldStatus := node.Status
	node.Status = constants.NodeStatusOnline
	node.Active = true
	node.ActiveAt = time.Now()
	err := backoff.Retry(func() error {
		return service.NewModelService[models.Node]().ReplaceById(node.Id, *node)
	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(1*time.Second), 3))
	if err != nil {
		svc.Errorf("failed to set worker node[%s] online: %v", node.Key, err)
		return
	}

	svc.Infof("worker node[%s] status changed from '%s' to 'online'", node.Key, oldStatus)

	// send notification if status changed
	if utils.IsPro() && oldStatus != constants.NodeStatusOnline {
		svc.sendNotification(node)
	}
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

func (svc *MasterService) sendNotification(node *models.Node) {
	if !utils.IsPro() {
		return
	}
	go notification.GetNotificationService().SendNodeNotification(node)
}

func (svc *MasterService) sendMasterStatusNotification(oldStatus, newStatus string) {
	if !utils.IsPro() {
		return
	}
	nodeKey := svc.cfgSvc.GetNodeKey()
	node, err := service.NewModelService[models.Node]().GetOne(bson.M{"key": nodeKey}, nil)
	if err != nil {
		svc.Errorf("failed to get master node for notification: %v", err)
		return
	}
	go notification.GetNotificationService().SendNodeNotification(node)
}

func (svc *MasterService) monitorGrpcClientHealth() {
	grpcClient := client.GetGrpcClient()

	// Check if gRPC client is in a bad state
	if !grpcClient.IsReady() && grpcClient.IsClosed() {
		svc.Warnf("master node gRPC client is in SHUTDOWN state, forcing FULL RESET")
		// Reset the gRPC client to get a fresh instance
		client.ResetGrpcClient()
		svc.Infof("master node gRPC client has been reset")
	}
}

func newMasterService() *MasterService {
	cfgSvc := config.GetNodeConfigService()
	server := server.GetGrpcServer()

	return &MasterService{
		cfgSvc:                cfgSvc,
		monitorInterval:       15 * time.Second,
		server:                server,
		taskSchedulerSvc:      scheduler.GetTaskSchedulerService(),
		taskHandlerSvc:        handler.GetTaskHandlerService(),
		scheduleSvc:           schedule.GetScheduleService(),
		systemSvc:             system.GetSystemService(),
		healthSvc:             GetHealthService(),
		nodeMonitoringSvc:     NewNodeMonitoringService(cfgSvc),
		taskReconciliationSvc: NewTaskReconciliationService(server, handler.GetTaskHandlerService()),
		Logger:                utils.NewLogger("MasterService"),
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
