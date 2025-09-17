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
func (svc *TaskReconciliationService) GetActualTaskStatusFromWorker(node *models.Node, task *models.Task) (status string, err error) {
	// First, try to get the actual process status from the worker
	actualProcessStatus, err := svc.queryProcessStatusFromWorker(node, task)
	if err != nil {
		svc.Warnf("failed to query process status from worker node[%s] for task[%s]: %v", node.Key, task.Id.Hex(), err)
		// Fall back to heuristic detection
		return svc.detectTaskStatusFromHeuristics(task)
	}

	// Synchronize task status with actual process status
	return svc.syncTaskStatusWithProcess(task, actualProcessStatus)
}

// queryProcessStatusFromWorker directly queries the worker node for the actual process status
func (svc *TaskReconciliationService) queryProcessStatusFromWorker(node *models.Node, task *models.Task) (processStatus string, err error) {
	// Check if there's an active stream for this task
	_, hasActiveStream := svc.server.TaskSvr.GetSubscribeStream(task.Id)

	// Check if the node is still connected via subscription
	nodeStream, nodeConnected := svc.server.NodeSvr.GetSubscribeStream(node.Id)
	if !nodeConnected {
		return svc.inferProcessStatusFromLocalState(task, hasActiveStream)
	}

	// Query the worker for actual process status
	if nodeStream != nil && task.Pid > 0 {
		// Send a process status query to the worker
		actualStatus, err := svc.requestProcessStatusFromWorker(nodeStream, task, 5*time.Second)
		if err != nil {
			svc.Warnf("failed to get process status from worker: %v", err)
			return svc.inferProcessStatusFromLocalState(task, hasActiveStream)
		}
		return actualStatus, nil
	}

	return svc.inferProcessStatusFromLocalState(task, hasActiveStream)
}

// requestProcessStatusFromWorker sends a status query request to the worker node
func (svc *TaskReconciliationService) requestProcessStatusFromWorker(nodeStream grpc.NodeService_SubscribeServer, task *models.Task, timeout time.Duration) (string, error) {
	// Check if task has a valid PID
	if task.Pid <= 0 {
		return svc.inferProcessStatusFromLocalState(task, false)
	}

	// Get the node for this task
	node, err := service.NewModelService[models.Node]().GetById(task.NodeId)
	if err != nil {
		svc.Warnf("failed to get node[%s] for task[%s]: %v", task.NodeId.Hex(), task.Id.Hex(), err)
		_, hasActiveStream := svc.server.TaskSvr.GetSubscribeStream(task.Id)
		return svc.inferProcessStatusFromLocalState(task, hasActiveStream)
	}

	// Attempt to query worker directly (future implementation)
	// This will return an error until worker discovery infrastructure is built
	workerStatus, err := svc.queryWorkerProcessStatus(node, task, timeout)
	if err != nil {
		svc.Debugf("direct worker query not available, falling back to heuristics: %v", err)

		// Fallback to heuristic detection
		_, hasActiveStream := svc.server.TaskSvr.GetSubscribeStream(task.Id)
		return svc.inferProcessStatusFromLocalState(task, hasActiveStream)
	}

	svc.Infof("successfully queried worker process status for task[%s]: %s", task.Id.Hex(), workerStatus)
	return workerStatus, nil
}

// inferProcessStatusFromLocalState uses local information to infer process status
func (svc *TaskReconciliationService) inferProcessStatusFromLocalState(task *models.Task, hasActiveStream bool) (string, error) {
	// Check if task has been updated recently (within last 30 seconds)
	isRecentlyUpdated := time.Since(task.UpdatedAt) < 30*time.Second

	switch {
	case hasActiveStream && isRecentlyUpdated:
		// Active stream and recent updates = likely running
		return constants.TaskStatusRunning, nil

	case !hasActiveStream && isRecentlyUpdated:
		// No stream but recent updates = likely just finished
		if task.Error != "" {
			return constants.TaskStatusError, nil
		}
		return constants.TaskStatusFinished, nil

	case !hasActiveStream && !isRecentlyUpdated:
		// No stream and stale = process likely finished or failed
		return svc.checkFinalTaskState(task), nil

	case hasActiveStream && !isRecentlyUpdated:
		// Stream exists but no recent updates - could be a long-running task
		// Don't assume abnormal - the task might be legitimately running without frequent updates
		return constants.TaskStatusRunning, nil

	default:
		// Fallback
		return constants.TaskStatusError, nil
	}
}

// checkFinalTaskState determines the final state of a task without active streams
func (svc *TaskReconciliationService) checkFinalTaskState(task *models.Task) string {
	// Check the current task status and error state
	switch task.Status {
	case constants.TaskStatusFinished, constants.TaskStatusError, constants.TaskStatusCancelled, constants.TaskStatusAbnormal:
		// Already in a final state
		return task.Status
	case constants.TaskStatusRunning:
		// Running status but no stream = process likely completed
		if task.Error != "" {
			return constants.TaskStatusError
		}
		return constants.TaskStatusFinished
	case constants.TaskStatusPending, constants.TaskStatusAssigned:
		// Never started running but lost connection
		return constants.TaskStatusError
	case constants.TaskStatusNodeDisconnected:
		// Task is marked as disconnected - keep this status since we can't determine final state
		// Don't assume abnormal until we can actually verify the process state
		return constants.TaskStatusNodeDisconnected
	default:
		return constants.TaskStatusError
	}
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
		// Unknown status - use heuristic detection
		_, hasActiveStream := svc.server.TaskSvr.GetSubscribeStream(task.Id)
		status, _ := svc.inferProcessStatusFromLocalState(task, hasActiveStream)
		return status
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

// detectTaskStatusFromHeuristics provides fallback detection when worker communication fails
func (svc *TaskReconciliationService) detectTaskStatusFromHeuristics(task *models.Task) (string, error) {
	// Use improved heuristic detection
	_, hasActiveStream := svc.server.TaskSvr.GetSubscribeStream(task.Id)
	return svc.inferProcessStatusFromLocalState(task, hasActiveStream)
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
	case constants.TaskStatusAbnormal:
		// Abnormal status is also final - keep it
		return latestTask.Status
	case constants.TaskStatusRunning:
		// Task shows as running but has no active stream - need to determine actual status
		if latestTask.Error != "" {
			return constants.TaskStatusError
		}
		return constants.TaskStatusFinished
	case constants.TaskStatusPending, constants.TaskStatusAssigned:
		// Tasks that never started running but lost connection - mark as error
		return constants.TaskStatusError
	case constants.TaskStatusNodeDisconnected:
		// Node disconnected status should be handled by reconnection logic
		// Keep the disconnected status since we don't know the actual final state
		return constants.TaskStatusNodeDisconnected
	default:
		// Unknown status - mark as error
		svc.Warnf("task[%s] has unknown status: %s", task.Id.Hex(), latestTask.Status)
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
		taskReconciliationService = NewTaskReconciliationService(grpcServer)
	})
	return taskReconciliationService
}
