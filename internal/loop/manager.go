package loop

import (
	"fmt"
	"strings"
	"time"

	"chrome-agent/internal/llm"
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
	llmCoord    *llm.Coordinator
}

// NewManager creates a new loop manager
func NewManager(taskManager *task.Manager, executor *task.Executor, log *logger.Logger) *Manager {
	return &Manager{
		taskManager: taskManager,
		executor:    executor,
		logger:      log,
		tuiModel:    nil,
		llmCoord:    executor.GetLLMCoordinator(),
	}
}

// SetLLMCoordinator sets the LLM coordinator for loop decisions
func (m *Manager) SetLLMCoordinator(coord *llm.Coordinator) {
	m.llmCoord = coord
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

		// Check mechanical condition first (safety net)
		mechanicalConditionMet, err := m.taskManager.CheckLoopCondition()
		if err != nil {
			return fmt.Errorf("failed to check loop condition: %w", err)
		}

		// Use LLM to evaluate loop completion (primary decision maker)
		var shouldComplete bool
		var llmReasoning string
		if m.llmCoord != nil {
			// Build execution context for LLM
			contextStr := m.buildLoopContext(cycleNumber, spec)
			
			var progress float64
			if loop.Type == state.LoopTypeTime {
				elapsed, _ := m.taskManager.GetElapsedTime()
				progress = elapsed.Seconds()
			} else {
				progress = loop.CurrentValue
			}
			
			loopTypeStr := string(loop.Type)
			m.logger.Info("Asking LLM to evaluate loop completion - Objective: %s, Cycle: %d, Progress: %.2f/%.2f, Type: %s", 
				spec.Objective, cycleNumber, progress, loop.TargetValue, loopTypeStr)
			
			shouldComplete, llmReasoning, err = m.llmCoord.EvaluateLoopCompletion(
				contextStr,
				loopTypeStr,
				progress,
				loop.TargetValue,
				cycleNumber,
				spec.Objective,
			)
			if err != nil {
				m.logger.Warn("LLM loop evaluation failed: %v, using mechanical check", err)
				m.logger.Info("LLM error details: %v", err)
				shouldComplete = mechanicalConditionMet
			} else {
				m.logger.Info("LLM loop evaluation: should_complete=%v", shouldComplete)
				m.logger.Info("LLM reasoning: %s", llmReasoning)
			}
		} else {
			// Fallback to mechanical check if no LLM coordinator
			shouldComplete = mechanicalConditionMet
		}

		// Complete loop if LLM says so OR mechanical condition met (safety net)
		if shouldComplete || mechanicalConditionMet {
			if shouldComplete && !mechanicalConditionMet {
				m.logger.Info("LLM decided to complete loop early (before mechanical condition)")
				m.logger.Info("Reasoning: %s", llmReasoning)
			} else {
				m.logger.Info("Loop condition met, completing loop")
			}
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
			
			// Log subtask details
			m.logger.Info("Executing subtask: %s (ID: %d, Cycle: %d, Description: %s)", rule.Name, subtaskID, cycleNumber, rule.Description)
			m.logger.Info("Task objective: %s", spec.Objective)
			
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
			deadlockRecoveryAttempts := 0
			const maxDeadlockRecoveryAttempts = 3
			
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
				
				// Check for deadlock (same error repeating)
				isDeadlock := strings.Contains(execErr.Error(), "DEADLOCK:")
				if isDeadlock && m.llmCoord != nil {
					deadlockRecoveryAttempts++
					m.logger.Warn("Deadlock detected (attempt %d/%d), asking LLM for recovery strategy", deadlockRecoveryAttempts, maxDeadlockRecoveryAttempts)
					m.logger.Info("Deadlock details - Subtask: %s, Error: %s, Cycle: %d", rule.Name, execErr.Error(), cycleNumber)
					
					// Check if we've exceeded max deadlock recovery attempts
					if deadlockRecoveryAttempts > maxDeadlockRecoveryAttempts {
						m.logger.Error("Max deadlock recovery attempts (%d) exceeded for subtask '%s', skipping subtask", maxDeadlockRecoveryAttempts, rule.Name)
						break // Skip this subtask
					}
					
					contextStr := m.executor.BuildContextString(execCtx, rule)
					m.logger.Info("Asking LLM for deadlock recovery - Context: %s", contextStr)
					
					action, reasoning, err := m.llmCoord.EvaluateDeadlock(
						rule.Name,
						"", // Tool name extracted from error if needed
						execErr.Error(),
						3, // Consecutive failures (extracted from error message)
						contextStr,
						spec.Objective,
					)
					if err != nil {
						m.logger.Warn("LLM deadlock evaluation failed: %v", err)
						m.logger.Info("LLM error details: %v", err)
					} else {
						m.logger.Info("LLM deadlock recovery decision: %s", action)
						m.logger.Info("LLM reasoning: %s", reasoning)
						
						// Send prompt to TUI if available
						if m.tuiModel != nil {
							options := []string{"recover_state", "skip", "adjust_approach", "end_cycle", "end_task"}
							m.tuiModel.SendPrompt("deadlock", reasoning, options, func(selected string) {
								action = selected
							})
						}
						
						// Execute LLM decision
						switch action {
						case "end_task":
							m.logger.Info("LLM decided to end task due to deadlock - Reason: %s", reasoning)
							return fmt.Errorf("LLM decided to end task due to deadlock: %w", execErr)
						case "end_cycle":
							m.logger.Info("LLM decided to end cycle due to deadlock - Reason: %s", reasoning)
							break // Exit retry loop, continue to next subtask
						case "recover_state":
							m.logger.Info("LLM decided to recover state to resolve deadlock - Reason: %s", reasoning)
							
							// Check if this is a browser session loss error
							isSessionLoss := strings.Contains(execErr.Error(), "tool state") && 
								strings.Contains(execErr.Error(), "lost")
							
							if isSessionLoss {
								// For browser session loss, try to re-establish the session first
								m.logger.Info("Detected browser session loss, attempting to re-establish session...")
								
								// Get state snapshot for URL
								if savedStateSnapshot == "" {
									snapshot, err := m.taskManager.GetLatestStateSnapshot(subtaskID)
									if err == nil && snapshot != nil {
										savedStateSnapshot = snapshot.SnapshotData
									}
								}
								
								// Try to re-establish browser session
								if err := m.executor.ReestablishBrowserSession(savedStateSnapshot); err != nil {
									m.logger.Warn("Failed to re-establish browser session: %v", err)
									m.logger.Warn("Browser session recovery failed, skipping subtask")
									break
								}
								
								// Session re-established, now restore full state
								if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
									if err := m.executor.RestoreState(savedStateSnapshot); err != nil {
										m.logger.Warn("State restoration after session recovery failed: %v", err)
									}
								}
								
								m.logger.Info("Browser session and state recovered successfully, retrying subtask")
								// Clear error patterns and retry
								continue
							} else {
								// For other types of state loss, use standard restoration
								if savedStateSnapshot == "" {
									snapshot, err := m.taskManager.GetLatestStateSnapshot(subtaskID)
									if err == nil && snapshot != nil {
										savedStateSnapshot = snapshot.SnapshotData
									}
								}
								if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
									if err := m.executor.RestoreState(savedStateSnapshot); err != nil {
										m.logger.Warn("State recovery failed: %v", err)
									} else {
										m.logger.Info("State recovered successfully, retrying subtask")
									}
								}
								// Retry after state recovery
								continue
							}
						case "adjust_approach":
							m.logger.Info("LLM decided to adjust approach due to deadlock - skipping subtask - Reason: %s", reasoning)
							break // Skip this subtask
						case "skip":
							fallthrough
						default:
							m.logger.Info("LLM decided to skip subtask due to deadlock - Reason: %s", reasoning)
							break // Skip this subtask
						}
					}
				}
				
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
					m.logger.Error("Retry limit (%d) exceeded for subtask '%s' (ID: %d)", subtask.MaxRetries, rule.Name, subtaskID)
					m.logger.Info("Subtask details - Name: %s, Description: %s, Error: %s", rule.Name, rule.Description, execErr.Error())
					
					// Check if loop quota is met before asking LLM
					quotaMet, err := m.taskManager.CheckLoopCondition()
					if err != nil {
						m.logger.Warn("Failed to check loop condition: %v", err)
					} else if quotaMet {
						m.logger.Info("Loop quota condition met (quota check returned true), completing loop gracefully")
						if err := m.taskManager.CompleteLoop(); err != nil {
							return fmt.Errorf("failed to complete loop: %w", err)
						}
						return nil
					}
					
					// Ask LLM what to do when retries are exhausted (only if quota not met)
					if m.llmCoord != nil {
						contextStr := m.executor.BuildContextString(execCtx, rule)
						m.logger.Info("Asking LLM for retry exhaustion decision - Context: %s", contextStr)
						
						action, reasoning, err := m.llmCoord.EvaluateRetryExhaustion(
							rule.Name,
							"", // Tool name not available here
							execErr.Error(),
							subtask.RetryCount,
							subtask.MaxRetries,
							contextStr,
							spec.Objective,
						)
						if err != nil {
							m.logger.Warn("LLM retry exhaustion evaluation failed: %v", err)
							m.logger.Info("LLM error details: %v", err)
							action = "skip" // Default to skip
						} else {
							m.logger.Info("LLM decision for retry exhaustion: %s", action)
							m.logger.Info("LLM reasoning: %s", reasoning)
							
							// Send prompt to TUI if available
							if m.tuiModel != nil {
								options := []string{"skip", "end_cycle", "end_task", "recover_state", "adjust_approach", "keep_session"}
								m.tuiModel.SendPrompt("retry_exhaustion", reasoning, options, func(selected string) {
									action = selected
								})
							}
						}
						
						// Execute LLM decision
						switch action {
						case "end_task":
							m.logger.Info("LLM decided to end task after retry exhaustion - Reason: %s", reasoning)
							return fmt.Errorf("LLM decided to end task after retry exhaustion: %w", execErr)
						case "end_cycle":
							m.logger.Info("LLM decided to end cycle, moving to next cycle - Reason: %s", reasoning)
							break // Exit retry loop, continue to next subtask
						case "recover_state":
							m.logger.Info("LLM decided to recover state - Reason: %s", reasoning)
							
							// Check if this is a browser session loss error
							isSessionLoss := strings.Contains(execErr.Error(), "tool state") && 
								strings.Contains(execErr.Error(), "lost")
							
							if isSessionLoss {
								// For browser session loss, try to re-establish the session first
								m.logger.Info("Detected browser session loss, attempting to re-establish session...")
								
								// Get state snapshot for URL
								if savedStateSnapshot == "" {
									snapshot, err := m.taskManager.GetLatestStateSnapshot(subtaskID)
									if err == nil && snapshot != nil {
										savedStateSnapshot = snapshot.SnapshotData
									}
								}
								
								// Try to re-establish browser session
								if err := m.executor.ReestablishBrowserSession(savedStateSnapshot); err != nil {
									m.logger.Warn("Failed to re-establish browser session: %v", err)
									m.logger.Warn("Browser session recovery failed, skipping subtask")
									break
								}
								
								// Session re-established, now restore full state
								if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
									if err := m.executor.RestoreState(savedStateSnapshot); err != nil {
										m.logger.Warn("State restoration after session recovery failed: %v", err)
									}
								}
								
								m.logger.Info("Browser session and state recovered successfully, retrying subtask")
								// Clear error patterns and retry
								continue
							} else {
								// For other types of state loss, use standard restoration
								if savedStateSnapshot == "" {
									snapshot, err := m.taskManager.GetLatestStateSnapshot(subtaskID)
									if err == nil && snapshot != nil {
										savedStateSnapshot = snapshot.SnapshotData
									}
								}
								if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
									if err := m.executor.RestoreState(savedStateSnapshot); err != nil {
										m.logger.Warn("State recovery failed: %v", err)
									} else {
										m.logger.Info("State recovered successfully, retrying subtask")
										continue
									}
								}
								// If recovery failed, fall through to skip
								m.logger.Warn("State recovery unavailable or failed, skipping subtask")
								break
							}
						case "adjust_approach":
							m.logger.Info("LLM decided to adjust approach - skipping subtask - Reason: %s", reasoning)
							break // Skip this subtask
						case "keep_session":
							m.logger.Info("LLM decided to keep session alive and retry - Reason: %s", reasoning)
							// Session is preserved, retry the subtask
							continue
						case "skip":
							fallthrough
						default:
							m.logger.Info("LLM decided to skip subtask and continue - Reason: %s", reasoning)
							break // Skip this subtask
						}
					}
					
					if err := m.taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
						return fmt.Errorf("failed to update subtask status: %w", err)
					}
					// Continue to next subtask
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
	
	// Save sessions after loop completion
	if err := m.executor.SaveSessions(); err != nil {
		m.logger.Warn("Failed to save sessions after loop: %v", err)
	}
	
	return nil
}

// buildLoopContext builds execution context string for LLM evaluation
func (m *Manager) buildLoopContext(cycleNumber int, spec *task.TaskSpec) string {
	var context strings.Builder
	
	context.WriteString(fmt.Sprintf("Loop Execution Context:\n"))
	context.WriteString(fmt.Sprintf("- Objective: %s\n", spec.Objective))
	context.WriteString(fmt.Sprintf("- Cycle Number: %d\n", cycleNumber))
	
	// Get recent subtasks
	subtasks, err := m.taskManager.GetSubtasks()
	if err == nil && len(subtasks) > 0 {
		context.WriteString("\nRecent Subtask Results:\n")
		// Show last 5 subtasks
		start := len(subtasks) - 5
		if start < 0 {
			start = 0
		}
		for i := start; i < len(subtasks); i++ {
			st := subtasks[i]
			status := "✓"
			if st.Status == state.SubtaskStatusFailed {
				status = "✗"
			}
			context.WriteString(fmt.Sprintf("  %s %s (cycle %d)\n", status, st.Name, st.CycleNumber))
		}
	}
	
	return context.String()
}