package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"chrome-agent/internal/exception"
	"chrome-agent/internal/llm"
	"chrome-agent/internal/loop"
	"chrome-agent/internal/mcp"
	"chrome-agent/internal/state"
	"chrome-agent/internal/task"
	"chrome-agent/pkg/logger"
	"chrome-agent/pkg/tui"
)

func main() {
	var (
		taskFile  = flag.String("task", "task.txt", "Path to task specification file")
		mcpConfig = flag.String("mcp", "mcp-config.json", "Path to MCP configuration file")
		dbPath    = flag.String("db", "agent.db", "Path to SQLite database file")
		logLevel  = flag.String("log", "INFO", "Log level: DEBUG, INFO, WARN, ERROR")
		useTUI    = flag.Bool("tui", false, "Enable TUI mode")
	)
	flag.Parse()

	// Initialize TUI if enabled
	var tuiModel *tui.Model
	if *useTUI {
		var err error
		tuiModel, err = tui.Start()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start TUI: %v\n", err)
			os.Exit(1)
		}
		defer tuiModel.Stop()
	}

	// Initialize logger
	var logLevelEnum logger.Level
	switch *logLevel {
	case "DEBUG":
		logLevelEnum = logger.DEBUG
	case "WARN":
		logLevelEnum = logger.WARN
	case "ERROR":
		logLevelEnum = logger.ERROR
	default:
		logLevelEnum = logger.INFO
	}
	log := logger.New(logLevelEnum, "agent")
	
	// Connect logger to TUI if enabled
	if tuiModel != nil {
		log.SetTUIBackend(tuiModel)
		log.SetTUIEnabled(true)
	}

	log.Info("=== Chrome Agent Starting ===")

	// Load MCP definition
	log.Info("Loading MCP configuration from: %s", *mcpConfig)
	mcpDef, err := mcp.LoadDefinition(*mcpConfig)
	if err != nil {
		log.Error("Failed to load MCP definition: %v", err)
		os.Exit(1)
	}

	// Initialize MCP client
	mcpClient := mcp.NewClient(mcpDef)

	// Validate MCP connection and output status report
	log.Info("Validating MCP server connection...")
	if mcpDef.Transport.HTTP != nil {
		log.Info("MCP server URL: %s", mcpDef.Transport.HTTP.URL)
		log.Debug("Connecting to HTTP MCP server...")
	} else {
		log.Debug("MCP transport type: %s", mcpDef.Transport.Type)
	}

	// Add timeout context for validation
	validationDone := make(chan bool, 1)
	var statusReport *mcp.StatusReport
	var validationErr error

	go func() {
		statusReport, validationErr = mcpClient.ValidateConnection()
		validationDone <- true
	}()

	select {
	case <-validationDone:
		if validationErr != nil {
			log.Error("MCP server validation failed: %v", validationErr)
			if statusReport != nil && statusReport.ErrorMessage != "" {
				log.Error("Error details: %s", statusReport.ErrorMessage)
			}
			os.Exit(1)
		}
	case <-time.After(15 * time.Second):
		log.Error("MCP server validation timed out after 15 seconds")
		log.Error("Please check:")
		log.Error("  1. Is the MCP server running at %s?", mcpDef.Transport.HTTP.URL)
		log.Error("  2. Is the server accessible?")
		log.Error("  3. Check server logs for errors")
		os.Exit(1)
	}

	// Output status report
	fmt.Print(mcp.FormatStatusReport(*statusReport))
	if !statusReport.Connected {
		log.Error("MCP server is not connected")
		os.Exit(1)
	}

	// Validate that tools are available
	if statusReport.ToolsCount == 0 {
		log.Error("MCP server has no available tools")
		log.Error("Cannot proceed with task planning - tools are required")
		log.Error("Please check your MCP server configuration and ensure tools are properly registered")
		os.Exit(1)
	}

	log.Info("MCP server validated: %d tools available", statusReport.ToolsCount)

	// Initialize state database
	log.Info("Initializing database: %s", *dbPath)
	db, err := state.NewDB(*dbPath)
	if err != nil {
		log.Error("Failed to initialize database: %v", err)
		os.Exit(1)
	}
	defer db.Close()

	// Initialize LLM coordinator
	log.Info("Initializing LLM coordinator...")
	llmCoord, err := llm.NewCoordinator()
	if err != nil {
		log.Error("Failed to initialize LLM coordinator: %v", err)
		os.Exit(1)
	}

	// Load and parse task (with LLM planning if needed)
	log.Info("Loading task from: %s", *taskFile)
	taskSpec, err := task.ParseTaskOrPlan(*taskFile, llmCoord, mcpClient, log)
	if err != nil {
		log.Error("Failed to parse or plan task: %v", err)
		os.Exit(1)
	}

	log.Info("Task objective: %s", taskSpec.Objective)
	if taskSpec.LoopCondition != nil {
		log.Info("Loop condition: %s", taskSpec.LoopCondition.Description)
	}
	log.Info("Number of subtasks: %d", len(taskSpec.SubtaskRules))
	if len(taskSpec.ExceptionRules) > 0 {
		log.Info("Exception rules: %d", len(taskSpec.ExceptionRules))
	}

	// Create task manager
	taskManager, err := task.NewManager(db, taskSpec)
	if err != nil {
		log.Error("Failed to create task manager: %v", err)
		os.Exit(1)
	}

	// Create task executor
	executor := task.NewExecutor(mcpClient, llmCoord, log)

	// Create exception handler
	exceptionHandler := exception.NewHandler(taskManager, db, log, taskSpec)

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Warn("Interrupt signal received, shutting down gracefully...")
		if err := taskManager.UpdateTaskStatus(state.TaskStatusPaused); err != nil {
			log.Error("Failed to update task status: %v", err)
		}
		if err := mcpClient.Disconnect(); err != nil {
			log.Error("Failed to disconnect MCP client: %v", err)
		}
		os.Exit(0)
	}()

	// Update task status to in_progress
	if err := taskManager.UpdateTaskStatus(state.TaskStatusInProgress); err != nil {
		log.Error("Failed to update task status: %v", err)
		os.Exit(1)
	}

	// Check for existing unresolved exceptions requiring intervention
	needsIntervention, exceptions, err := exceptionHandler.CheckForIntervention()
	if err != nil {
		log.Error("Failed to check for interventions: %v", err)
		os.Exit(1)
	}

	if needsIntervention {
		log.Warn("Found %d unresolved exceptions requiring intervention", len(exceptions))
		for _, ex := range exceptions {
			log.Warn("  - Exception ID %d: %s", ex.ID, ex.ErrorMessage)
		}
		log.Warn("Please resolve these issues and restart the agent")
		os.Exit(1)
	}

	// Update TUI with initial task state
	if tuiModel != nil {
		spec := taskManager.GetSpec()
		taskState := &tui.TaskState{
			Objective:   spec.Objective,
			Status:      "running",
			IsLoopable:  taskManager.IsLoopable(),
		}
		if spec.LoopCondition != nil {
			taskState.LoopType = spec.LoopCondition.Type
			taskState.LoopTarget = spec.LoopCondition.TargetValue
		}
		tuiModel.SendUpdate(tui.TaskUpdateMsg{State: taskState})
	}

	// Execute task
	var execErr error
	if taskManager.IsLoopable() {
		log.Info("Task is loopable, starting loop execution...")
		loopManager := loop.NewManager(taskManager, executor, log)
		if tuiModel != nil {
			loopManager.SetTUIModel(tuiModel)
		}
		execErr = loopManager.ExecuteLoop()
	} else {
		log.Info("Executing single task...")
		execErr = executeSingleTask(taskManager, executor, exceptionHandler, log, tuiModel)
	}
	
	// Update TUI with final status
	if tuiModel != nil {
		finalState := &tui.TaskState{
			Objective: taskManager.GetSpec().Objective,
			Status:    "completed",
		}
		if execErr != nil {
			finalState.Status = "failed"
		}
		tuiModel.SendUpdate(tui.TaskUpdateMsg{State: finalState})
	}

	// Handle execution result
	if execErr != nil {
		log.Error("Task execution failed: %v", execErr)
		
		// Print LLM token usage statistics even on failure
		stats := printTokenStatistics(llmCoord, log)
		
		// Update TUI with stats
		if tuiModel != nil {
			tuiModel.SendUpdate(tui.StatsUpdateMsg{Stats: stats})
		}
		
		// Check if it's a planning failure (non-recoverable)
		if err := taskManager.UpdateTaskStatusWithReason(state.TaskStatusFailed, execErr.Error()); err != nil {
			log.Error("Failed to update task status: %v", err)
		}
		os.Exit(1)
	}

	// Mark task as completed
	if err := taskManager.UpdateTaskStatus(state.TaskStatusCompleted); err != nil {
		log.Error("Failed to update task status: %v", err)
		os.Exit(1)
	}

	log.Info("=== Task Completed Successfully ===")

	// Output summary
	taskRecord, err := taskManager.GetTask()
	if err == nil {
		log.Info("Task ID: %d", taskRecord.ID)
		log.Info("Task Status: %s", taskRecord.Status)
		if taskRecord.FailReason != "" {
			log.Info("Fail Reason: %s", taskRecord.FailReason)
		}
	}

	subtasks, err := taskManager.GetSubtasks()
	if err == nil {
		completed := 0
		failed := 0
		for _, st := range subtasks {
			if st.Status == state.SubtaskStatusCompleted {
				completed++
			} else if st.Status == state.SubtaskStatusFailed {
				failed++
			}
		}
		log.Info("Subtasks: %d completed, %d failed, %d total", completed, failed, len(subtasks))
	}

	// Print LLM token usage statistics
	stats := printTokenStatistics(llmCoord, log)
	
	// Update TUI with stats
	if tuiModel != nil {
		tuiModel.SendUpdate(tui.StatsUpdateMsg{Stats: stats})
	}

	// Disconnect MCP client
	if err := mcpClient.Disconnect(); err != nil {
		log.Error("Failed to disconnect MCP client: %v", err)
	}
}

// printTokenStatistics prints LLM token usage statistics and returns stats
func printTokenStatistics(llmCoord *llm.Coordinator, log *logger.Logger) *tui.Stats {
	log.Info("=== LLM Token Usage Statistics ===")
	totalPrompt, totalCompletion, totalTokens, callCount, breakdown := llmCoord.GetTokenStatistics()
	log.Info("Total LLM Calls: %d", callCount)
	log.Info("Total Tokens: %d (Prompt: %d, Completion: %d)", totalTokens, totalPrompt, totalCompletion)
	
	if callCount > 0 {
		log.Info("Breakdown by call type:")
		typeStats := make(map[string]struct {
			count int
			prompt int
			completion int
			total int
		})
		
		for _, usage := range breakdown {
			stats := typeStats[usage.CallType]
			stats.count++
			stats.prompt += usage.PromptTokens
			stats.completion += usage.CompletionTokens
			stats.total += usage.TotalTokens
			typeStats[usage.CallType] = stats
		}
		
		for callType, stats := range typeStats {
			log.Info("  %s: %d calls, %d tokens (prompt: %d, completion: %d)",
				callType, stats.count, stats.total, stats.prompt, stats.completion)
		}
		
		if callCount > 1 {
			avgTokens := float64(totalTokens) / float64(callCount)
			log.Info("Average tokens per call: %.1f", avgTokens)
		}
	} else {
		log.Info("No LLM calls were made")
	}
	
	return &tui.Stats{
		TotalTokens:      totalTokens,
		PromptTokens:     totalPrompt,
		CompletionTokens: totalCompletion,
		LLMCalls:         callCount,
	}
}

func executeSingleTask(taskManager *task.Manager, executor *task.Executor, exceptionHandler *exception.Handler, log *logger.Logger, tuiModel *tui.Model) error {
	spec := taskManager.GetSpec()

	// Build execution context
	execCtx := &task.ExecutionContext{
		CycleNumber: 0,
		Objective:   spec.Objective,
	}

	for _, rule := range spec.SubtaskRules {
		subtaskID, err := taskManager.CreateSubtask(rule.Name, 0)
		if err != nil {
			return fmt.Errorf("failed to create subtask: %w", err)
		}

		if err := taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusInProgress); err != nil {
			return fmt.Errorf("failed to update subtask status: %w", err)
		}

		// Update context with current subtask
		execCtx.SubtaskName = rule.Name

		// Execute with retry logic
		var result *task.ExecutionResult
		var execErr error
		var savedStateSnapshot string
		
		for {
			// Get current subtask state
			subtask, err := taskManager.GetSubtask(subtaskID)
			if err != nil {
				return fmt.Errorf("failed to get subtask: %w", err)
			}
			
			// Execute the subtask with context
			result, execErr = executor.ExecuteSubtaskRuleWithContext(rule, "Single task execution", execCtx)
			
			if execErr == nil {
				// Success - save the state snapshot for this step
				if result.StateCapture != "" {
					if _, err := taskManager.CreateStateSnapshot(subtaskID, 0, result.StateCapture); err != nil {
						log.Warn("Failed to save state snapshot: %v", err)
					}
				}
				break // Success, exit retry loop
			}
			
			// Execution failed
			log.Error("Subtask execution failed: %v", execErr)

			// Check if this is a non-retryable failure
			if result != nil && !result.Retryable {
				log.Error("Non-retryable failure detected - task cannot proceed")
				if err := taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
					return fmt.Errorf("failed to update subtask status: %w", err)
				}
				return fmt.Errorf("non-retryable failure: %w", execErr)
			}

			// Check retry limit
			if subtask.RetryCount >= subtask.MaxRetries {
				log.Error("Retry limit (%d) exceeded for subtask '%s'", subtask.MaxRetries, rule.Name)
				
				// Check for deadlock first
				isDeadlock := strings.Contains(execErr.Error(), "DEADLOCK:")
				if isDeadlock {
					log.Warn("Deadlock detected in retry exhaustion, asking LLM for recovery")
					
					// Use executor's LLM coordinator
					llmCoord := executor.GetLLMCoordinator()
					if llmCoord != nil {
						contextStr := executor.BuildContextString(execCtx, rule)
						action, reasoning, err := llmCoord.EvaluateDeadlock(
							rule.Name,
							"",
							execErr.Error(),
							3,
							contextStr,
							spec.Objective,
						)
						if err == nil {
							log.Info("LLM deadlock recovery decision: %s", action)
							log.Debug("LLM reasoning: %s", reasoning)
							
							// Send prompt to TUI if available
							if tuiModel != nil {
								options := []string{"recover_state", "skip", "adjust_approach", "end_task"}
								tuiModel.SendPrompt("deadlock", reasoning, options, func(selected string) {
									action = selected
								})
							}
							
							// Execute decision
							switch action {
							case "end_task":
								return fmt.Errorf("LLM decided to end task due to deadlock: %w", execErr)
							case "recover_state":
								log.Info("Attempting state recovery...")
								if savedStateSnapshot == "" {
									snapshot, err := taskManager.GetLatestStateSnapshot(subtaskID)
									if err == nil && snapshot != nil {
										savedStateSnapshot = snapshot.SnapshotData
									}
								}
								if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
									if err := executor.RestoreState(savedStateSnapshot); err != nil {
										log.Warn("State recovery failed: %v", err)
									}
								}
								// Reset and try once more
								subtask.RetryCount = 0
								continue
							case "skip", "adjust_approach":
								log.Info("Skipping subtask per LLM decision")
								break
							}
						}
					}
				}
				
				// Ask LLM what to do when retries are exhausted
				llmCoord := executor.GetLLMCoordinator()
				if llmCoord != nil {
						contextStr := executor.BuildContextString(execCtx, rule)
					action, reasoning, err := llmCoord.EvaluateRetryExhaustion(
						rule.Name,
						"",
						execErr.Error(),
						subtask.RetryCount,
						subtask.MaxRetries,
						contextStr,
						spec.Objective,
					)
					if err == nil {
						log.Info("LLM retry exhaustion decision: %s", action)
						log.Debug("LLM reasoning: %s", reasoning)
						
						// Send prompt to TUI if available
						if tuiModel != nil {
							options := []string{"skip", "end_task", "recover_state", "adjust_approach"}
							tuiModel.SendPrompt("retry_exhaustion", reasoning, options, func(selected string) {
								action = selected
							})
						}
						
						// Execute LLM decision
						switch action {
						case "end_task":
							return fmt.Errorf("LLM decided to end task after retry exhaustion: %w", execErr)
						case "recover_state":
							log.Info("Attempting state recovery per LLM decision...")
							if savedStateSnapshot == "" {
								snapshot, err := taskManager.GetLatestStateSnapshot(subtaskID)
								if err == nil && snapshot != nil {
									savedStateSnapshot = snapshot.SnapshotData
								}
							}
							if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
								if err := executor.RestoreState(savedStateSnapshot); err != nil {
									log.Warn("State recovery failed: %v", err)
								}
							}
							// Reset retry count and try once more
							subtask.RetryCount = 0
							continue
						case "skip", "adjust_approach":
							log.Info("Skipping subtask per LLM decision")
							break
						}
					}
				}
				
				// Handle exception (fallback)
				needsIntervention, handleErr := exceptionHandler.HandleException(subtaskID, execErr)
				if handleErr != nil {
					return fmt.Errorf("failed to handle exception: %w", handleErr)
				}

				if needsIntervention {
					log.Warn("Human intervention required, pausing execution")
					return fmt.Errorf("execution paused for human intervention")
				}

				if err := taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
					return fmt.Errorf("failed to update subtask status: %w", err)
				}
				// Continue to next subtask
				break
			}
			
			// Retry: increment retry count
			if err := taskManager.IncrementSubtaskRetry(subtaskID); err != nil {
				return fmt.Errorf("failed to increment retry count: %w", err)
			}
			
			// Restore state to before this subtask started
			if savedStateSnapshot == "" {
				// Get the state from before this subtask
				snapshot, err := taskManager.GetLatestStateSnapshot(subtaskID)
				if err != nil {
					log.Warn("Failed to get state snapshot: %v", err)
				} else if snapshot != nil {
					savedStateSnapshot = snapshot.SnapshotData
				}
			}
			
			if savedStateSnapshot != "" && savedStateSnapshot != "{}" {
				log.Info("Restoring state for retry (attempt %d/%d)", subtask.RetryCount+1, subtask.MaxRetries)
				if err := executor.RestoreState(savedStateSnapshot); err != nil {
					log.Warn("Failed to restore state: %v", err)
				}
			}
			
			log.Info("Retrying subtask '%s' (attempt %d/%d)", rule.Name, subtask.RetryCount+1, subtask.MaxRetries)
		}
		
		if execErr != nil {
			// Final failure after retries - skip to next subtask
			if err := taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusFailed); err != nil {
				return fmt.Errorf("failed to update subtask status: %w", err)
			}
			continue
		}

		if err := taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusCompleted); err != nil {
			return fmt.Errorf("failed to update subtask status: %w", err)
		}

		if err := taskManager.UpdateSubtaskResult(subtaskID, result); err != nil {
			return fmt.Errorf("failed to update subtask result: %w", err)
		}

		log.Info("Subtask '%s' completed successfully", rule.Name)
	}

	return nil
}
