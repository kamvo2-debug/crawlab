package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/crawlab-team/crawlab/core/constants"
	"github.com/crawlab-team/crawlab/core/grpc/server"
	"github.com/crawlab-team/crawlab/core/interfaces"
	"github.com/crawlab-team/crawlab/core/models/models"
	"github.com/crawlab-team/crawlab/core/models/service"
	"github.com/crawlab-team/crawlab/core/task/handler"
	"github.com/crawlab-team/crawlab/core/utils"
	"github.com/crawlab-team/crawlab/grpc"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TaskReconciliationService handles task status reconciliation for node disconnection scenarios
type TaskReconciliationService struct {
	server         *server.GrpcServer
	taskHandlerSvc *handler.Service // access to task handlers and their status caches
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

// triggerWorkerStatusSync triggers synchronization of cached status from worker to database
func (svc *TaskReconciliationService) triggerWorkerStatusSync(task *models.Task) error {
	// Check if we have access to task handler service (only on worker nodes)
	if svc.taskHandlerSvc == nil {
		return fmt.Errorf("task handler service not available - not on worker node")
	}

	// Get the task runner for this task
	taskRunner := svc.taskHandlerSvc.GetTaskRunner(task.Id)
	if taskRunner == nil {
		return fmt.Errorf("no active task runner found for task %s", task.Id.Hex())
	}

	// Cast to concrete Runner type to access status cache methods
	runner, ok := taskRunner.(*handler.Runner)
	if !ok {
		return fmt.Errorf("task runner is not of expected type for task %s", task.Id.Hex())
	}

	// Trigger sync of pending status updates
	if err := runner.SyncPendingStatusUpdates(); err != nil {
		return fmt.Errorf("failed to sync pending status updates: %w", err)
	}

	svc.Infof("successfully triggered status sync for task[%s]", task.Id.Hex())
	return nil
}

// HandleNodeReconnection reconciles tasks that were marked as disconnected when the node comes back online
// Now leverages worker-side status cache for more accurate reconciliation
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
		// First, try to trigger status sync from worker cache if we're on the worker node
		if err := svc.triggerWorkerStatusSync(&task); err != nil {
			svc.Debugf("could not trigger worker status sync for task[%s]: %v", task.Id.Hex(), err)
		}

		actualStatus, err := svc.GetActualTaskStatusFromWorker(node, &task)
		if err != nil {
			svc.Warnf("failed to get actual status for task[%s] from reconnected node[%s]: %v", task.Id.Hex(), node.Key, err)
			// If we can't determine the actual status, keep the current status and add a note
			// Don't assume abnormal - we simply don't have enough information
			if task.Error == "" {
				task.Error = "Unable to verify task status after node reconnection - status may be stale"
			}
			// Skip status update since we don't know the actual state
			continue
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
// Now prioritizes worker-side status cache over heuristics
func (svc *TaskReconciliationService) GetActualTaskStatusFromWorker(node *models.Node, task *models.Task) (status string, err error) {
	// First priority: get status from worker-side task runner cache
	cachedStatus, err := svc.getStatusFromWorkerCache(task)
	if err == nil && cachedStatus != "" {
		svc.Debugf("retrieved cached status for task[%s]: %s", task.Id.Hex(), cachedStatus)
		return cachedStatus, nil
	}

	// Second priority: query process status from worker
	actualProcessStatus, err := svc.queryProcessStatusFromWorker(node, task)
	if err != nil {
		svc.Warnf("failed to query process status from worker node[%s] for task[%s]: %v", node.Key, task.Id.Hex(), err)
		// Return error instead of falling back to unreliable heuristics
		return "", fmt.Errorf("unable to determine actual task status: %w", err)
	}

	// Synchronize task status with actual process status
	return svc.syncTaskStatusWithProcess(task, actualProcessStatus)
}

// getStatusFromWorkerCache retrieves task status from worker-side task runner cache
func (svc *TaskReconciliationService) getStatusFromWorkerCache(task *models.Task) (string, error) {
	// Check if we have access to task handler service (only on worker nodes)
	if svc.taskHandlerSvc == nil {
		return "", fmt.Errorf("task handler service not available - not on worker node")
	}

	// Get the task runner for this task
	taskRunner := svc.taskHandlerSvc.GetTaskRunner(task.Id)
	if taskRunner == nil {
		return "", fmt.Errorf("no active task runner found for task %s", task.Id.Hex())
	}

	// Cast to concrete Runner type to access status cache methods
	runner, ok := taskRunner.(*handler.Runner)
	if !ok {
		return "", fmt.Errorf("task runner is not of expected type for task %s", task.Id.Hex())
	}

	// Get cached status from the runner
	cachedSnapshot := runner.GetCachedTaskStatus()
	if cachedSnapshot == nil {
		return "", fmt.Errorf("no cached status available for task %s", task.Id.Hex())
	}

	svc.Infof("retrieved cached status for task[%s]: %s (cached at %v)",
		task.Id.Hex(), cachedSnapshot.Status, cachedSnapshot.Timestamp)
	return cachedSnapshot.Status, nil
}

// queryProcessStatusFromWorker directly queries the worker node for the actual process status
func (svc *TaskReconciliationService) queryProcessStatusFromWorker(node *models.Node, task *models.Task) (processStatus string, err error) {
	// Check if there's an active stream for this task
	_, hasActiveStream := svc.server.TaskSvr.GetSubscribeStream(task.Id)

	// Check if the node is still connected via subscription
	nodeStream, nodeConnected := svc.server.NodeSvr.GetSubscribeStream(node.Id)
	if !nodeConnected {
		return "", fmt.Errorf("node[%s] is not connected", node.Key)
	}

	// Query the worker for actual process status
	if nodeStream != nil && task.Pid > 0 {
		// Send a process status query to the worker
		actualStatus, err := svc.requestProcessStatusFromWorker(nodeStream, task, 5*time.Second)
		if err != nil {
			return "", fmt.Errorf("failed to get process status from worker: %w", err)
		}
		return actualStatus, nil
	}

	// If we can't query the worker directly, return error
	if hasActiveStream {
		return constants.TaskStatusRunning, nil // Task likely still running if stream exists
	}
	return "", fmt.Errorf("unable to determine process status for task[%s] on node[%s]", task.Id.Hex(), node.Key)
}

// requestProcessStatusFromWorker sends a status query request to the worker node
func (svc *TaskReconciliationService) requestProcessStatusFromWorker(nodeStream grpc.NodeService_SubscribeServer, task *models.Task, timeout time.Duration) (string, error) {
	// Check if task has a valid PID
	if task.Pid <= 0 {
		return "", fmt.Errorf("task[%s] has invalid PID: %d", task.Id.Hex(), task.Pid)
	}

	// Get the node for this task
	node, err := service.NewModelService[models.Node]().GetById(task.NodeId)
	if err != nil {
		return "", fmt.Errorf("failed to get node[%s] for task[%s]: %w", task.NodeId.Hex(), task.Id.Hex(), err)
	}

	// Attempt to query worker directly
	workerStatus, err := svc.queryWorkerProcessStatus(node, task, timeout)
	if err != nil {
		return "", fmt.Errorf("worker process status query failed: %w", err)
	}

	svc.Infof("successfully queried worker process status for task[%s]: %s", task.Id.Hex(), workerStatus)
	return workerStatus, nil
}

// mapProcessStatusToTaskStatus converts gRPC process status to task status
func (svc *TaskReconciliationService) mapProcessStatusToTaskStatus(processStatus grpc.ProcessStatus, exitCode int32, task *models.Task) string {
	switch processStatus {
	case grpc.ProcessStatus_PROCESS_RUNNING:
		return constants.TaskStatusRunning
	case grpc.ProcessStatus_PROCESS_FINISHED:
		// Process finished - check exit code to determine success or failure
		if exitCode == 0 {
			return constants.TaskStatusFinished
		}
		return constants.TaskStatusError
	case grpc.ProcessStatus_PROCESS_ERROR:
		return constants.TaskStatusError
	case grpc.ProcessStatus_PROCESS_NOT_FOUND:
		// Process not found - could mean it finished and was cleaned up
		// Check if task was recently active to determine likely outcome
		if time.Since(task.UpdatedAt) < 5*time.Minute {
			// Recently active task with missing process - likely completed
			if task.Error != "" {
				return constants.TaskStatusError
			}
			return constants.TaskStatusFinished
		}
		// Old task with missing process - probably error
		return constants.TaskStatusError
	case grpc.ProcessStatus_PROCESS_ZOMBIE:
		// Zombie process indicates abnormal termination
		return constants.TaskStatusError
	case grpc.ProcessStatus_PROCESS_UNKNOWN:
		fallthrough
	default:
		// Unknown status - return error instead of using heuristics
		svc.Warnf("unknown process status %v for task[%s]", processStatus, task.Id.Hex())
		return constants.TaskStatusError
	}
}

// createWorkerClient creates a gRPC client connection to a worker node
// This is a placeholder for future implementation when worker discovery is available
func (svc *TaskReconciliationService) createWorkerClient(node *models.Node) (grpc.TaskServiceClient, error) {
	// TODO: Implement worker node discovery and connection
	// This would require:
	// 1. Worker nodes to register their gRPC server endpoints
	// 2. A service discovery mechanism
	// 3. Connection pooling and management
	//
	// For now, return an error to indicate this functionality is not yet available
	return nil, fmt.Errorf("direct worker client connections not yet implemented - need worker discovery infrastructure")
}

// queryWorkerProcessStatus attempts to query a worker node directly for process status
// This demonstrates the intended future architecture for worker communication
func (svc *TaskReconciliationService) queryWorkerProcessStatus(node *models.Node, task *models.Task, timeout time.Duration) (string, error) {
	// This is the intended implementation once worker discovery is available

	// 1. Create gRPC client to worker
	client, err := svc.createWorkerClient(node)
	if err != nil {
		return "", fmt.Errorf("failed to create worker client: %w", err)
	}

	// 2. Create timeout context
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 3. Send process status request
	req := &grpc.TaskServiceCheckProcessRequest{
		TaskId: task.Id.Hex(),
		Pid:    int32(task.Pid),
	}

	resp, err := client.CheckProcess(ctx, req)
	if err != nil {
		return "", fmt.Errorf("worker process status query failed: %w", err)
	}

	// 4. Convert process status to task status
	taskStatus := svc.mapProcessStatusToTaskStatus(resp.Status, resp.ExitCode, task)

	svc.Infof("worker reported process status for task[%s]: process_status=%s, exit_code=%d, mapped_to=%s",
		task.Id.Hex(), resp.Status.String(), resp.ExitCode, taskStatus)

	return taskStatus, nil
}

// syncTaskStatusWithProcess ensures task status matches the actual process status
func (svc *TaskReconciliationService) syncTaskStatusWithProcess(task *models.Task, actualProcessStatus string) (string, error) {
	// If the actual process status differs from the database status, we need to sync
	if task.Status != actualProcessStatus {
		svc.Infof("syncing task[%s] status from '%s' to '%s' based on actual process status",
			task.Id.Hex(), task.Status, actualProcessStatus)

		// Update the task status in the database to match reality
		err := svc.updateTaskStatusReliably(task, actualProcessStatus)
		if err != nil {
			svc.Errorf("failed to sync task[%s] status: %v", task.Id.Hex(), err)
			return task.Status, err // Return original status if sync fails
		}
	}

	return actualProcessStatus, nil
}

// updateTaskStatusReliably updates task status with retry logic and validation
func (svc *TaskReconciliationService) updateTaskStatusReliably(task *models.Task, newStatus string) error {
	// Update task with the new status
	task.Status = newStatus

	// Add appropriate error message for certain status transitions
	switch newStatus {
	case constants.TaskStatusError:
		if task.Error == "" {
			task.Error = "Task status synchronized from actual process state"
		}
	case constants.TaskStatusFinished:
		// Clear error message for successfully completed tasks
		task.Error = ""
	case constants.TaskStatusAbnormal:
		if task.Error == "" {
			task.Error = "Task marked as abnormal during status reconciliation"
		}
	case constants.TaskStatusNodeDisconnected:
		// Don't modify error message for disconnected status - keep existing context
		// The disconnect reason should already be in the error field
	}

	// Update with retry logic
	return backoff.Retry(func() error {
		return service.NewModelService[models.Task]().ReplaceById(task.Id, *task)
	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(500*time.Millisecond), 3))
}

// StartPeriodicReconciliation starts a background service to periodically reconcile task status
func (svc *TaskReconciliationService) StartPeriodicReconciliation() {
	go svc.runPeriodicReconciliation()
}

// runPeriodicReconciliation periodically checks and reconciles task status with actual process status
func (svc *TaskReconciliationService) runPeriodicReconciliation() {
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	for range ticker.C {
		err := svc.reconcileRunningTasks()
		if err != nil {
			svc.Errorf("failed to reconcile running tasks: %v", err)
		}
	}
}

// reconcileRunningTasks finds all running tasks and reconciles their status with actual process status
func (svc *TaskReconciliationService) reconcileRunningTasks() error {
	// Find all tasks that might need reconciliation
	query := bson.M{
		"status": bson.M{
			"$in": []string{
				constants.TaskStatusRunning,
				constants.TaskStatusNodeDisconnected,
			},
		},
	}

	tasks, err := service.NewModelService[models.Task]().GetMany(query, nil)
	if err != nil {
		return err
	}

	svc.Debugf("found %d tasks to reconcile", len(tasks))

	for _, task := range tasks {
		err := svc.reconcileTaskStatus(&task)
		if err != nil {
			svc.Errorf("failed to reconcile task[%s]: %v", task.Id.Hex(), err)
		}
	}

	return nil
}

// reconcileTaskStatus reconciles a single task's status with its actual process status
func (svc *TaskReconciliationService) reconcileTaskStatus(task *models.Task) error {
	// Get the node for this task
	node, err := service.NewModelService[models.Node]().GetById(task.NodeId)
	if err != nil {
		svc.Warnf("failed to get node[%s] for task[%s]: %v", task.NodeId.Hex(), task.Id.Hex(), err)
		return err
	}

	// Get actual status from worker
	actualStatus, err := svc.GetActualTaskStatusFromWorker(node, task)
	if err != nil {
		svc.Warnf("failed to get actual status for task[%s]: %v", task.Id.Hex(), err)
		// Don't change the status if we can't determine the actual state
		// This is more honest than making assumptions
		return err
	}

	// If status changed, update it
	if actualStatus != task.Status {
		svc.Infof("reconciling task[%s] status from '%s' to '%s'", task.Id.Hex(), task.Status, actualStatus)
		return svc.updateTaskStatusReliably(task, actualStatus)
	}

	return nil
}

// ForceReconcileTask forces reconciliation of a specific task (useful for manual intervention)
func (svc *TaskReconciliationService) ForceReconcileTask(taskId primitive.ObjectID) error {
	task, err := service.NewModelService[models.Task]().GetById(taskId)
	if err != nil {
		return fmt.Errorf("failed to get task[%s]: %w", taskId.Hex(), err)
	}

	return svc.reconcileTaskStatus(task)
}

func NewTaskReconciliationService(server *server.GrpcServer, taskHandlerSvc *handler.Service) *TaskReconciliationService {
	return &TaskReconciliationService{
		server:         server,
		taskHandlerSvc: taskHandlerSvc,
		Logger:         utils.NewLogger("TaskReconciliationService"),
	}
}

// ValidateTaskStatus ensures task status is consistent with actual process state
func (svc *TaskReconciliationService) ValidateTaskStatus(task *models.Task) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}

	// Get the node for this task
	node, err := service.NewModelService[models.Node]().GetById(task.NodeId)
	if err != nil {
		return fmt.Errorf("failed to get node for task: %w", err)
	}

	// Get actual status
	actualStatus, err := svc.GetActualTaskStatusFromWorker(node, task)
	if err != nil {
		return fmt.Errorf("failed to get actual task status: %w", err)
	}

	// If status is inconsistent, log it and optionally fix it
	if actualStatus != task.Status {
		svc.Warnf("task[%s] status inconsistency detected: database='%s', actual='%s'",
			task.Id.Hex(), task.Status, actualStatus)

		// Optionally auto-correct the status
		return svc.updateTaskStatusReliably(task, actualStatus)
	}

	return nil
}

// IsTaskStatusFinal returns true if the task status represents a final state
func (svc *TaskReconciliationService) IsTaskStatusFinal(status string) bool {
	switch status {
	case constants.TaskStatusFinished, constants.TaskStatusError, constants.TaskStatusCancelled, constants.TaskStatusAbnormal:
		return true
	default:
		return false
	}
}

// ShouldReconcileTask determines if a task needs status reconciliation
func (svc *TaskReconciliationService) ShouldReconcileTask(task *models.Task) bool {
	// Don't reconcile tasks in final states unless they're very old and might be stuck
	if svc.IsTaskStatusFinal(task.Status) {
		return false
	}

	// Always reconcile running or disconnected tasks
	switch task.Status {
	case constants.TaskStatusRunning, constants.TaskStatusNodeDisconnected:
		return true
	case constants.TaskStatusPending, constants.TaskStatusAssigned:
		// Reconcile if task has been pending/assigned for too long
		return time.Since(task.CreatedAt) > 10*time.Minute
	default:
		return false
	}
}

// Singleton pattern
var taskReconciliationService *TaskReconciliationService
var taskReconciliationServiceOnce sync.Once

func GetTaskReconciliationService() *TaskReconciliationService {
	taskReconciliationServiceOnce.Do(func() {
		// Get the server from gRPC server singleton
		grpcServer := server.GetGrpcServer()
		// Try to get task handler service (will be nil on master nodes)
		var taskHandlerSvc *handler.Service
		if !utils.IsMaster() {
			// Only worker nodes have task handler service
			taskHandlerSvc = handler.GetTaskHandlerService()
		}
		taskReconciliationService = NewTaskReconciliationService(grpcServer, taskHandlerSvc)
	})
	return taskReconciliationService
}
