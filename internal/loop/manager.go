package loop

import (
	"fmt"
	"time"

	"chrome-agent/internal/state"
	"chrome-agent/internal/task"
	"chrome-agent/pkg/logger"
)

// Manager handles loop execution
type Manager struct {
	taskManager *task.Manager
	executor    *task.Executor
	logger      *logger.Logger
}

// NewManager creates a new loop manager
func NewManager(taskManager *task.Manager, executor *task.Executor, log *logger.Logger) *Manager {
	return &Manager{
		taskManager: taskManager,
		executor:    executor,
		logger:      log,
	}
}

// ExecuteLoop executes loopable tasks until condition is met
func (m *Manager) ExecuteLoop() error {
	if !m.taskManager.IsLoopable() {
		return fmt.Errorf("task is not loopable")
	}

	spec := m.taskManager.GetSpec()
	loop, err := m.taskManager.GetLoop()
	if err != nil {
		return fmt.Errorf("failed to get loop: %w", err)
	}
	if loop == nil {
		return fmt.Errorf("loop not found")
	}

	m.logger.Info("Starting loop execution: %s", spec.LoopCondition.Description)

	cycleNumber := 0
	startTime := time.Now()

	for {
		cycleNumber++
		m.logger.Info("=== Cycle %d ===", cycleNumber)

		// Check if condition is met
		conditionMet, err := m.taskManager.CheckLoopCondition()
		if err != nil {
			return fmt.Errorf("failed to check loop condition: %w", err)
		}

		if conditionMet {
			m.logger.Info("Loop condition met, completing loop")
			if err := m.taskManager.CompleteLoop(); err != nil {
				return fmt.Errorf("failed to complete loop: %w", err)
			}
			break
		}

		// Update progress display
		if loop.Type == state.LoopTypeTime {
			elapsed, _ := m.taskManager.GetElapsedTime()
			remaining := time.Duration(loop.TargetValue)*time.Second - elapsed
			m.logger.Info("Elapsed: %v, Remaining: %v", elapsed, remaining)
		} else if loop.Type == state.LoopTypeQuota {
			m.logger.Info("Progress: %.0f / %.0f", loop.CurrentValue, loop.TargetValue)
		}

		// Execute subtasks for this cycle
		for _, rule := range spec.SubtaskRules {
			subtaskID, err := m.taskManager.CreateSubtask(rule.Name, cycleNumber)
			if err != nil {
				return fmt.Errorf("failed to create subtask: %w", err)
			}

			if err := m.taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusInProgress); err != nil {
				return fmt.Errorf("failed to update subtask status: %w", err)
			}

			context := fmt.Sprintf("Cycle %d of loop execution", cycleNumber)
			result, err := m.executor.ExecuteSubtaskRule(rule, context)

			if err != nil {
				m.logger.Error("Subtask execution failed: %v", err)
				if err := m.taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
					return fmt.Errorf("failed to update subtask status: %w", err)
				}
				// Continue to next subtask or cycle
				continue
			}

			if err := m.taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusCompleted); err != nil {
				return fmt.Errorf("failed to update subtask status: %w", err)
			}

			if err := m.taskManager.UpdateSubtaskResult(subtaskID, result); err != nil {
				return fmt.Errorf("failed to update subtask result: %w", err)
			}

			// Update quota if applicable
			if loop.Type == state.LoopTypeQuota && result.Success {
				// Increment quota by 1 (or extract from result if available)
				currentLoop, _ := m.taskManager.GetLoop()
				if currentLoop != nil {
					newValue := currentLoop.CurrentValue + 1
					if err := m.taskManager.UpdateLoopProgress(newValue); err != nil {
						return fmt.Errorf("failed to update loop progress: %w", err)
					}
				}
			}

			m.logger.Info("Subtask '%s' completed in cycle %d", rule.Name, cycleNumber)
		}

		// Update time-based progress
		if loop.Type == state.LoopTypeTime {
			elapsed := time.Since(startTime).Seconds()
			if err := m.taskManager.UpdateLoopProgress(elapsed); err != nil {
				return fmt.Errorf("failed to update loop progress: %w", err)
			}
		}

		// Refresh loop to get updated values
		loop, err = m.taskManager.GetLoop()
		if err != nil {
			return fmt.Errorf("failed to refresh loop: %w", err)
		}
	}

	m.logger.Info("Loop execution completed after %d cycles", cycleNumber)
	return nil
}

