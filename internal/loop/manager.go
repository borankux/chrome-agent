package loop

import (
	"fmt"
	"time"

	"chrome-agent/internal/state"
	"chrome-agent/internal/task"
	"chrome-agent/pkg/logger"
	"chrome-agent/pkg/tui"
)

// Manager handles loop execution
type Manager struct {
	taskManager *task.Manager
	executor    *task.Executor
	logger      *logger.Logger
	tuiModel    *tui.Model
}

// NewManager creates a new loop manager
func NewManager(taskManager *task.Manager, executor *task.Executor, log *logger.Logger) *Manager {
	return &Manager{
		taskManager: taskManager,
		executor:    executor,
		logger:      log,
		tuiModel:    nil,
	}
}

// SetTUIModel sets the TUI model for updates
func (m *Manager) SetTUIModel(model *tui.Model) {
	m.tuiModel = model
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
			
			// Update TUI
			if m.tuiModel != nil {
				elapsedSeconds := elapsed.Seconds()
				taskState := &tui.TaskState{
					Objective:    spec.Objective,
					Status:       "running",
					IsLoopable:   true,
					LoopType:     "time",
					LoopProgress: elapsedSeconds,
					LoopTarget:   loop.TargetValue,
					CycleNumber:  cycleNumber,
					ElapsedTime:  elapsed,
				}
				m.tuiModel.SendUpdate(tui.TaskUpdateMsg{State: taskState})
			}
		} else if loop.Type == state.LoopTypeQuota {
			m.logger.Info("Progress: %.0f / %.0f", loop.CurrentValue, loop.TargetValue)
			
			// Update TUI
			if m.tuiModel != nil {
				taskState := &tui.TaskState{
					Objective:    spec.Objective,
					Status:       "running",
					IsLoopable:   true,
					LoopType:     "quota",
					LoopProgress: loop.CurrentValue,
					LoopTarget:   loop.TargetValue,
					CycleNumber:  cycleNumber,
				}
				m.tuiModel.SendUpdate(tui.TaskUpdateMsg{State: taskState})
			}
		}

		// Build execution context for this cycle (shared across subtasks)
		execCtx := &task.ExecutionContext{
			CycleNumber: cycleNumber,
			Objective:   spec.Objective,
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
			
			// Update context with current subtask name
			execCtx.SubtaskName = rule.Name
			
			// Update TUI with current subtask
			if m.tuiModel != nil {
				m.tuiModel.SendUpdate(tui.ExecutionUpdateMsg{
					Subtask: rule.Name,
					Tool:    "",
					Status:  "running",
				})
			}
			
			// Execute with retry logic
			var result *task.ExecutionResult
			var execErr error
			var savedStateSnapshot string
			
			for {
				// Get current subtask state
				subtask, err := m.taskManager.GetSubtask(subtaskID)
				if err != nil {
					return fmt.Errorf("failed to get subtask: %w", err)
				}
				
				// Execute the subtask with context (context is updated by executor after execution)
				result, execErr = m.executor.ExecuteSubtaskRuleWithContext(rule, context, execCtx)
				
				if execErr == nil {
					// Success - save the state snapshot for this step
					if result.StateCapture != "" {
						if _, err := m.taskManager.CreateStateSnapshot(subtaskID, cycleNumber, result.StateCapture); err != nil {
							m.logger.Warn("Failed to save state snapshot: %v", err)
						}
					}
					break // Success, exit retry loop
				}
				
				// Execution failed
				m.logger.Error("Subtask execution failed: %v", execErr)
				
				// Check if this is a planning failure - if so, fail the entire loop
				if result != nil && !result.Retryable {
					m.logger.Error("Non-retryable failure detected - loop cannot proceed")
					if err := m.taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
						return fmt.Errorf("failed to update subtask status: %w", err)
					}
					return fmt.Errorf("non-retryable failure in loop: %w", execErr)
				}
				
				// Check retry limit
				if subtask.RetryCount >= subtask.MaxRetries {
					m.logger.Error("Retry limit (%d) exceeded for subtask '%s'", subtask.MaxRetries, rule.Name)
					if err := m.taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
						return fmt.Errorf("failed to update subtask status: %w", err)
					}
					// Mark cycle as failed but continue to next subtask
					break
				}
				
				// Retry: increment retry count
				if err := m.taskManager.IncrementSubtaskRetry(subtaskID); err != nil {
					return fmt.Errorf("failed to increment retry count: %w", err)
				}
				
				// Restore state to before this subtask started
				if savedStateSnapshot == "" {
					// Get the state from before this subtask
					snapshot, err := m.taskManager.GetLatestStateSnapshot(subtaskID)
					if err != nil {
						m.logger.Warn("Failed to get state snapshot: %v", err)
					} else if snapshot != nil {
						savedStateSnapshot = snapshot.SnapshotData
					}
				}
				
				if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
					m.logger.Info("Restoring state for retry (attempt %d/%d)", subtask.RetryCount+1, subtask.MaxRetries)
					if err := m.executor.RestoreState(savedStateSnapshot); err != nil {
						m.logger.Warn("Failed to restore state: %v", err)
					}
				}
				
				m.logger.Info("Retrying subtask '%s' (attempt %d/%d)", rule.Name, subtask.RetryCount+1, subtask.MaxRetries)
			}

			if execErr != nil {
				// Final failure after retries
				if err := m.taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
					return fmt.Errorf("failed to update subtask status: %w", err)
				}
				// Continue to next subtask
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