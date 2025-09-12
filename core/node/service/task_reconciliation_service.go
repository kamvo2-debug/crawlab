package service

import (
	"context"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/crawlab-team/crawlab/core/constants"
	"github.com/crawlab-team/crawlab/core/grpc/server"
	"github.com/crawlab-team/crawlab/core/interfaces"
	"github.com/crawlab-team/crawlab/core/models/models"
	"github.com/crawlab-team/crawlab/core/models/service"
	"github.com/crawlab-team/crawlab/core/utils"
	"github.com/crawlab-team/crawlab/grpc"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TaskReconciliationService handles task status reconciliation for node disconnection scenarios
type TaskReconciliationService struct {
	server *server.GrpcServer
	interfaces.Logger
}

// HandleTasksForOfflineNode updates all running tasks on an offline node to abnormal status
func (svc *TaskReconciliationService) HandleTasksForOfflineNode(node *models.Node) {
	// Find all running tasks on the offline node
	query := bson.M{
		"node_id": node.Id,
		"status":  constants.TaskStatusRunning,
	}

	runningTasks, err := service.NewModelService[models.Task]().GetMany(query, nil)
	if err != nil {
		svc.Errorf("failed to get running tasks for offline node[%s]: %v", node.Key, err)
		return
	}

	if len(runningTasks) == 0 {
		svc.Debugf("no running tasks found for offline node[%s]", node.Key)
		return
	}

	svc.Infof("updating %d running tasks to abnormal status for offline node[%s]", len(runningTasks), node.Key)

	// Update each task status to node_disconnected (recoverable)
	for _, task := range runningTasks {
		task.Status = constants.TaskStatusNodeDisconnected
		task.Error = "Task temporarily disconnected due to worker node offline"

		// Update the task in database
		err := backoff.Retry(func() error {
			return service.NewModelService[models.Task]().ReplaceById(task.Id, task)
		}, backoff.WithMaxRetries(backoff.NewConstantBackOff(500*time.Millisecond), 3))

		if err != nil {
			svc.Errorf("failed to update task[%s] status for offline node[%s]: %v", task.Id.Hex(), node.Key, err)
		} else {
			svc.Debugf("updated task[%s] status to abnormal for offline node[%s]", task.Id.Hex(), node.Key)
		}
	}
}

// HandleNodeReconnection reconciles tasks that were marked as disconnected when the node comes back online
func (svc *TaskReconciliationService) HandleNodeReconnection(node *models.Node) {
	// Find all disconnected tasks on this node
	query := bson.M{
		"node_id": node.Id,
		"status":  constants.TaskStatusNodeDisconnected,
	}

	disconnectedTasks, err := service.NewModelService[models.Task]().GetMany(query, nil)
	if err != nil {
		svc.Errorf("failed to get disconnected tasks for reconnected node[%s]: %v", node.Key, err)
		return
	}

	if len(disconnectedTasks) == 0 {
		svc.Debugf("no disconnected tasks found for reconnected node[%s]", node.Key)
		return
	}

	svc.Infof("reconciling %d disconnected tasks for reconnected node[%s]", len(disconnectedTasks), node.Key)

	// For each disconnected task, try to get its actual status from the worker node
	for _, task := range disconnectedTasks {
		actualStatus, err := svc.GetActualTaskStatusFromWorker(node, &task)
		if err != nil {
			svc.Warnf("failed to get actual status for task[%s] from reconnected node[%s]: %v", task.Id.Hex(), node.Key, err)
			// If we can't determine the actual status, mark as abnormal after reconnection failure
			task.Status = constants.TaskStatusAbnormal
			task.Error = "Could not reconcile task status after node reconnection"
		} else {
			// Update with actual status from worker
			task.Status = actualStatus
			switch actualStatus {
			case constants.TaskStatusFinished:
				task.Error = "" // Clear error message for successfully completed tasks
			case constants.TaskStatusError:
				task.Error = "Task encountered an error during node disconnection"
			}
		}

		// Update the task in database
		err = backoff.Retry(func() error {
			return service.NewModelService[models.Task]().ReplaceById(task.Id, task)
		}, backoff.WithMaxRetries(backoff.NewConstantBackOff(500*time.Millisecond), 3))

		if err != nil {
			svc.Errorf("failed to update reconciled task[%s] status for node[%s]: %v", task.Id.Hex(), node.Key, err)
		} else {
			svc.Infof("reconciled task[%s] status from 'node_disconnected' to '%s' for node[%s]", task.Id.Hex(), task.Status, node.Key)
		}
	}
}

// GetActualTaskStatusFromWorker queries the worker node to get the actual status of a task
func (svc *TaskReconciliationService) GetActualTaskStatusFromWorker(node *models.Node, task *models.Task) (status string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if there's an active stream for this task
	_, hasActiveStream := svc.server.TaskSvr.GetSubscribeStream(task.Id)

	// Check if the node is still connected via subscription
	nodeStream, nodeConnected := svc.server.NodeSvr.GetSubscribeStream(node.Id)
	if !nodeConnected {
		svc.Warnf("node[%s] is not connected, using fallback detection for task[%s]", node.Key, task.Id.Hex())
		return svc.inferTaskStatusFromStream(task.Id, hasActiveStream), nil
	}

	// Try to get more accurate status by checking recent task activity
	actualStatus, err := svc.detectTaskStatusFromActivity(task, hasActiveStream)
	if err != nil {
		svc.Warnf("failed to detect task status from activity for task[%s]: %v", task.Id.Hex(), err)
		return svc.inferTaskStatusFromStream(task.Id, hasActiveStream), nil
	}

	// Ping the node to verify it's responsive
	if nodeStream != nil {
		select {
		case <-ctx.Done():
			svc.Warnf("timeout while pinging node[%s] for task[%s]", node.Key, task.Id.Hex())
			return svc.inferTaskStatusFromStream(task.Id, hasActiveStream), nil
		default:
			// Send a heartbeat to verify node responsiveness
			err := nodeStream.Send(&grpc.NodeServiceSubscribeResponse{
				Code: grpc.NodeServiceSubscribeCode_HEARTBEAT,
			})
			if err != nil {
				svc.Warnf("failed to ping node[%s] for task status check: %v", node.Key, err)
				return svc.inferTaskStatusFromStream(task.Id, hasActiveStream), nil
			}
		}
	}

	return actualStatus, nil
}

// detectTaskStatusFromActivity analyzes task activity to determine its actual status
func (svc *TaskReconciliationService) detectTaskStatusFromActivity(task *models.Task, hasActiveStream bool) (string, error) {
	// Check if task has been updated recently (within last 30 seconds)
	if time.Since(task.UpdatedAt) < 30*time.Second {
		// Task was recently updated, likely still active
		if hasActiveStream {
			return constants.TaskStatusRunning, nil
		}
		// Recently updated but no stream - check if it finished
		return svc.checkTaskCompletion(task), nil
	}

	// Task hasn't been updated recently
	if !hasActiveStream {
		// No stream and no recent activity - likely finished or failed
		return svc.checkTaskCompletion(task), nil
	}

	// Has stream but no recent updates - might be stuck
	return constants.TaskStatusRunning, nil
}

// checkTaskCompletion determines if a task completed successfully or failed
func (svc *TaskReconciliationService) checkTaskCompletion(task *models.Task) string {
	// Refresh task from database to get latest status
	latestTask, err := service.NewModelService[models.Task]().GetById(task.Id)
	if err != nil {
		svc.Warnf("failed to refresh task[%s] from database: %v", task.Id.Hex(), err)
		return constants.TaskStatusError
	}

	// If task status was already updated to a final state, return that
	switch latestTask.Status {
	case constants.TaskStatusFinished, constants.TaskStatusError, constants.TaskStatusCancelled:
		return latestTask.Status
	case constants.TaskStatusRunning:
		// Task still shows as running but has no active stream - likely finished
		if latestTask.Error != "" {
			return constants.TaskStatusError
		}
		return constants.TaskStatusFinished
	default:
		// Unknown or intermediate status
		return constants.TaskStatusError
	}
}

// inferTaskStatusFromStream provides a fallback status inference based on stream presence
func (svc *TaskReconciliationService) inferTaskStatusFromStream(taskId primitive.ObjectID, hasActiveStream bool) string {
	if !hasActiveStream {
		// No active stream could mean:
		// 1. Task finished successfully
		// 2. Task failed and stream was closed
		// 3. Worker disconnected ungracefully
		//
		// To determine which, we should check the task in the database
		task, err := service.NewModelService[models.Task]().GetById(taskId)
		if err != nil {
			// If we can't find the task, assume it's in an error state
			return constants.TaskStatusError
		}

		// If the task was last seen running and now has no stream,
		// it likely finished or errored
		switch task.Status {
		case constants.TaskStatusRunning:
			// Task was running but stream is gone - likely finished
			return constants.TaskStatusFinished
		case constants.TaskStatusPending, constants.TaskStatusAssigned:
			// Task never started running - likely error
			return constants.TaskStatusError
		default:
			// Return the last known status
			return task.Status
		}
	}

	// Stream exists, so task is likely still running
	return constants.TaskStatusRunning
}

func NewTaskReconciliationService(server *server.GrpcServer) *TaskReconciliationService {
	return &TaskReconciliationService{
		server: server,
		Logger: utils.NewLogger("TaskReconciliationService"),
	}
}

// Singleton pattern
var taskReconciliationService *TaskReconciliationService
var taskReconciliationServiceOnce sync.Once

func GetTaskReconciliationService() *TaskReconciliationService {
	taskReconciliationServiceOnce.Do(func() {
		taskReconciliationService = NewTaskReconciliationService(nil) // Will be set by the master service
	})
	return taskReconciliationService
}
