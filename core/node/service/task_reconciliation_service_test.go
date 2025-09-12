package service

import (
	"testing"
	"time"

	"github.com/crawlab-team/crawlab/core/constants"
	"github.com/crawlab-team/crawlab/core/models/models"
	modelService "github.com/crawlab-team/crawlab/core/models/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestTaskReconciliationService_HandleTasksForOfflineNode tests the handling of tasks when a node goes offline
func TestTaskReconciliationService_HandleTasksForOfflineNode(t *testing.T) {
	// Skip if no database connection
	if testing.Short() {
		t.Skip("Skipping database integration test")
	}

	// Setup test data
	nodeId := primitive.NewObjectID()
	taskId1 := primitive.NewObjectID()
	taskId2 := primitive.NewObjectID()

	node := &models.Node{}
	node.Id = nodeId
	node.Key = "test-worker-node"

	// Create running tasks on the node
	runningTask1 := models.Task{}
	runningTask1.Id = taskId1
	runningTask1.NodeId = nodeId
	runningTask1.Status = constants.TaskStatusRunning
	runningTask1.SetCreated(primitive.NilObjectID)
	runningTask1.SetUpdated(primitive.NilObjectID)

	runningTask2 := models.Task{}
	runningTask2.Id = taskId2
	runningTask2.NodeId = nodeId
	runningTask2.Status = constants.TaskStatusRunning
	runningTask2.SetCreated(primitive.NilObjectID)
	runningTask2.SetUpdated(primitive.NilObjectID)

	// Insert tasks into database
	taskSvc := modelService.NewModelService[models.Task]()
	_, err := taskSvc.InsertOne(runningTask1)
	require.NoError(t, err)
	_, err = taskSvc.InsertOne(runningTask2)
	require.NoError(t, err)

	// Create reconciliation service
	reconciliationSvc := NewTaskReconciliationService(nil)

	// Test handling tasks for offline node
	reconciliationSvc.HandleTasksForOfflineNode(node)

	// Verify tasks are marked as node_disconnected
	task1, err := taskSvc.GetById(taskId1)
	require.NoError(t, err)
	assert.Equal(t, constants.TaskStatusNodeDisconnected, task1.Status)
	assert.Contains(t, task1.Error, "temporarily disconnected due to worker node offline")

	task2, err := taskSvc.GetById(taskId2)
	require.NoError(t, err)
	assert.Equal(t, constants.TaskStatusNodeDisconnected, task2.Status)
	assert.Contains(t, task2.Error, "temporarily disconnected due to worker node offline")

	// Cleanup
	_ = taskSvc.DeleteById(taskId1)
	_ = taskSvc.DeleteById(taskId2)
}

// TestTaskReconciliationService_CheckTaskCompletion tests task completion detection
func TestTaskReconciliationService_CheckTaskCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database integration test")
	}

	reconciliationSvc := NewTaskReconciliationService(nil)

	tests := []struct {
		name           string
		taskStatus     string
		taskError      string
		expectedStatus string
	}{
		{
			name:           "Finished task",
			taskStatus:     constants.TaskStatusFinished,
			taskError:      "",
			expectedStatus: constants.TaskStatusFinished,
		},
		{
			name:           "Error task",
			taskStatus:     constants.TaskStatusError,
			taskError:      "Some error",
			expectedStatus: constants.TaskStatusError,
		},
		{
			name:           "Running task with error should be marked as error",
			taskStatus:     constants.TaskStatusRunning,
			taskError:      "Connection lost",
			expectedStatus: constants.TaskStatusError,
		},
		{
			name:           "Running task without error should be finished",
			taskStatus:     constants.TaskStatusRunning,
			taskError:      "",
			expectedStatus: constants.TaskStatusFinished,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create and insert test task
			taskId := primitive.NewObjectID()
			task := models.Task{}
			task.Id = taskId
			task.Status = tt.taskStatus
			task.Error = tt.taskError
			task.SetCreated(primitive.NilObjectID)
			task.SetUpdated(primitive.NilObjectID)

			taskSvc := modelService.NewModelService[models.Task]()
			_, err := taskSvc.InsertOne(task)
			require.NoError(t, err)

			// Test status detection
			status := reconciliationSvc.checkTaskCompletion(&task)
			assert.Equal(t, tt.expectedStatus, status)

			// Cleanup
			_ = taskSvc.DeleteById(taskId)
		})
	}
}

// TestTaskReconciliationService_InferTaskStatusFromStream tests fallback status inference
func TestTaskReconciliationService_InferTaskStatusFromStream(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database integration test")
	}

	reconciliationSvc := NewTaskReconciliationService(nil)

	tests := []struct {
		name            string
		taskStatus      string
		hasActiveStream bool
		expectedStatus  string
	}{
		{
			name:            "Running task with no stream should be finished",
			taskStatus:      constants.TaskStatusRunning,
			hasActiveStream: false,
			expectedStatus:  constants.TaskStatusFinished,
		},
		{
			name:            "Pending task with no stream should be error",
			taskStatus:      constants.TaskStatusPending,
			hasActiveStream: false,
			expectedStatus:  constants.TaskStatusError,
		},
		{
			name:            "Assigned task with no stream should be error",
			taskStatus:      constants.TaskStatusAssigned,
			hasActiveStream: false,
			expectedStatus:  constants.TaskStatusError,
		},
		{
			name:            "Task with active stream should be running",
			taskStatus:      constants.TaskStatusRunning,
			hasActiveStream: true,
			expectedStatus:  constants.TaskStatusRunning,
		},
		{
			name:            "Finished task should remain finished",
			taskStatus:      constants.TaskStatusFinished,
			hasActiveStream: false,
			expectedStatus:  constants.TaskStatusFinished,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create and insert test task
			taskId := primitive.NewObjectID()
			task := models.Task{}
			task.Id = taskId
			task.Status = tt.taskStatus
			task.SetCreated(primitive.NilObjectID)
			task.SetUpdated(primitive.NilObjectID)

			taskSvc := modelService.NewModelService[models.Task]()
			_, err := taskSvc.InsertOne(task)
			require.NoError(t, err)

			// Test status inference
			status := reconciliationSvc.inferTaskStatusFromStream(taskId, tt.hasActiveStream)
			assert.Equal(t, tt.expectedStatus, status)

			// Cleanup
			_ = taskSvc.DeleteById(taskId)
		})
	}
}

// TestTaskReconciliationService_DetectTaskStatusFromActivity tests activity-based detection
func TestTaskReconciliationService_DetectTaskStatusFromActivity(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database integration test")
	}

	reconciliationSvc := NewTaskReconciliationService(nil)

	tests := []struct {
		name            string
		updateTime      time.Time
		hasActiveStream bool
		taskStatus      string
		expectedStatus  string
	}{
		{
			name:            "Recently updated task with stream",
			updateTime:      time.Now().Add(-10 * time.Second),
			hasActiveStream: true,
			taskStatus:      constants.TaskStatusRunning,
			expectedStatus:  constants.TaskStatusRunning,
		},
		{
			name:            "Recently updated task without stream",
			updateTime:      time.Now().Add(-10 * time.Second),
			hasActiveStream: false,
			taskStatus:      constants.TaskStatusRunning,
			expectedStatus:  constants.TaskStatusFinished,
		},
		{
			name:            "Old task without stream",
			updateTime:      time.Now().Add(-10 * time.Minute),
			hasActiveStream: false,
			taskStatus:      constants.TaskStatusRunning,
			expectedStatus:  constants.TaskStatusFinished,
		},
		{
			name:            "Old task with stream might be stuck",
			updateTime:      time.Now().Add(-10 * time.Minute),
			hasActiveStream: true,
			taskStatus:      constants.TaskStatusRunning,
			expectedStatus:  constants.TaskStatusRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create task with specific update time
			task := &models.Task{}
			task.Id = primitive.NewObjectID()
			task.Status = tt.taskStatus
			task.UpdatedAt = tt.updateTime
			task.SetCreated(primitive.NilObjectID)
			task.SetUpdated(primitive.NilObjectID)

			// Insert into database
			taskSvc := modelService.NewModelService[models.Task]()
			_, err := taskSvc.InsertOne(*task)
			require.NoError(t, err)

			// Test status detection
			status, err := reconciliationSvc.detectTaskStatusFromActivity(task, tt.hasActiveStream)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, status)

			// Cleanup
			_ = taskSvc.DeleteById(task.Id)
		})
	}
}

// BenchmarkTaskReconciliation benchmarks the reconciliation performance
func BenchmarkTaskReconciliation(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping benchmark test")
	}

	// Setup
	nodeId := primitive.NewObjectID()
	node := &models.Node{}
	node.Id = nodeId
	node.Key = "benchmark-node"

	reconciliationSvc := NewTaskReconciliationService(nil)

	// Create multiple disconnected tasks
	taskSvc := modelService.NewModelService[models.Task]()
	taskIds := make([]primitive.ObjectID, 100)

	for i := 0; i < 100; i++ {
		taskId := primitive.NewObjectID()
		taskIds[i] = taskId

		task := models.Task{}
		task.Id = taskId
		task.NodeId = nodeId
		task.Status = constants.TaskStatusNodeDisconnected
		task.SetCreated(primitive.NilObjectID)
		task.SetUpdated(primitive.NilObjectID)
		_, _ = taskSvc.InsertOne(task)
	}

	// Benchmark reconnection handling
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reconciliationSvc.HandleNodeReconnection(node)
	}

	// Cleanup
	for _, taskId := range taskIds {
		_ = taskSvc.DeleteById(taskId)
	}
}
