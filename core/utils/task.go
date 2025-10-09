package utils

import (
	"github.com/crawlab-team/crawlab/core/constants"
)

func IsCancellable(status string) bool {
	switch status {
	case constants.TaskStatusPending,
		constants.TaskStatusAssigned,
		constants.TaskStatusRunning:
		return true
	default:
		return false
	}
}
