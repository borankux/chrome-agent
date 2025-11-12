package exception

import (
	"fmt"
	"regexp"
	"strings"

	"chrome-agent/internal/state"
	"chrome-agent/internal/task"
	"chrome-agent/pkg/logger"
)

// Handler handles exceptions and human intervention
type Handler struct {
	taskManager *task.Manager
	db          *state.DB
	logger      *logger.Logger
	spec        *task.TaskSpec
}

// NewHandler creates a new exception handler
func NewHandler(taskManager *task.Manager, db *state.DB, log *logger.Logger, spec *task.TaskSpec) *Handler {
	return &Handler{
		taskManager: taskManager,
		db:          db,
		logger:      log,
		spec:        spec,
	}
}

// HandleException processes an exception
func (h *Handler) HandleException(subtaskID int64, err error) (bool, error) {
	errorMsg := err.Error()
	h.logger.Error("Exception occurred: %s", errorMsg)

	// Check exception rules
	var requiresIntervention bool
	var retryable bool
	var maxRetries int = 3

	for _, rule := range h.spec.ExceptionRules {
		matched, err := regexp.MatchString(rule.Pattern, errorMsg)
		if err != nil {
			continue
		}
		if matched {
			requiresIntervention = rule.RequiresIntervention
			retryable = rule.Retryable
			maxRetries = rule.MaxRetries
			break
		}
	}

	// Default: check for common intervention-required patterns
	if !requiresIntervention {
		interventionPatterns := []string{
			"authentication",
			"login",
			"captcha",
			"verification",
			"permission denied",
			"access denied",
			"human intervention",
		}
		for _, pattern := range interventionPatterns {
			if strings.Contains(strings.ToLower(errorMsg), pattern) {
				requiresIntervention = true
				break
			}
		}
	}

	// Record exception
	exceptionID, err := h.db.CreateException(subtaskID, errorMsg, requiresIntervention)
	if err != nil {
		return false, fmt.Errorf("failed to record exception: %w", err)
	}

	if requiresIntervention {
		h.logger.Warn("=== HUMAN INTERVENTION REQUIRED ===")
		h.logger.Warn("Exception ID: %d", exceptionID)
		h.logger.Warn("Subtask ID: %d", subtaskID)
		h.logger.Warn("Error: %s", errorMsg)
		h.logger.Warn("Please resolve the issue and resume execution")
		h.logger.Warn("================================")

		// Pause task and loop
		if err := h.taskManager.UpdateTaskStatus(state.TaskStatusPaused); err != nil {
			return false, fmt.Errorf("failed to pause task: %w", err)
		}
		if err := h.taskManager.PauseLoop(); err != nil {
			// Ignore error if no loop exists
		}

		return true, nil // Intervention required
	}

	if retryable {
		// Check retry count
		subtasks, err := h.db.GetSubtasksByTaskID(h.taskManager.GetTaskID())
		if err != nil {
			return false, err
		}

		retryCount := 0
		for _, st := range subtasks {
			if st.ID == subtaskID {
				// Count previous failures for this subtask name
				for _, otherSt := range subtasks {
					if otherSt.Name == st.Name && otherSt.Status == state.SubtaskStatusFailed {
						retryCount++
					}
				}
				break
			}
		}

		if retryCount < maxRetries {
			h.logger.Info("Exception is retryable (attempt %d/%d), will retry", retryCount+1, maxRetries)
			return false, nil // Can retry
		} else {
			h.logger.Warn("Max retries (%d) exceeded, requiring intervention", maxRetries)
			requiresIntervention = true
			if _, err := h.db.CreateException(subtaskID, fmt.Sprintf("Max retries exceeded: %s", errorMsg), true); err != nil {
				return false, err
			}
			if err := h.taskManager.UpdateTaskStatus(state.TaskStatusPaused); err != nil {
				return false, err
			}
			return true, nil // Intervention required
		}
	}

	// Non-retryable, non-intervention exception - mark subtask as failed and continue
	h.logger.Warn("Non-retryable exception, marking subtask as failed")
	return false, nil
}

// CheckForIntervention checks if there are unresolved exceptions requiring intervention
func (h *Handler) CheckForIntervention() (bool, []*state.Exception, error) {
	exceptions, err := h.db.GetUnresolvedExceptions()
	if err != nil {
		return false, nil, err
	}

	interventionRequired := false
	interventionExceptions := make([]*state.Exception, 0)

	for _, ex := range exceptions {
		if ex.RequiresIntervention {
			interventionRequired = true
			interventionExceptions = append(interventionExceptions, ex)
		}
	}

	return interventionRequired, interventionExceptions, nil
}

// ResolveException marks an exception as resolved
func (h *Handler) ResolveException(exceptionID int64) error {
	return h.db.ResolveException(exceptionID)
}

// ResumeAfterIntervention resumes task execution after intervention
func (h *Handler) ResumeAfterIntervention() error {
	// Resolve all unresolved exceptions
	exceptions, err := h.db.GetUnresolvedExceptions()
	if err != nil {
		return err
	}

	for _, ex := range exceptions {
		if err := h.db.ResolveException(ex.ID); err != nil {
			return err
		}
	}

	// Resume task and loop
	if err := h.taskManager.UpdateTaskStatus(state.TaskStatusInProgress); err != nil {
		return err
	}
	if err := h.taskManager.ResumeLoop(); err != nil {
		// Ignore error if no loop exists
	}

	h.logger.Info("Task resumed after human intervention")
	return nil
}

