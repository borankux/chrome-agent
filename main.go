package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"chrome-agent/internal/exception"
	"chrome-agent/internal/llm"
	"chrome-agent/internal/loop"
	"chrome-agent/internal/mcp"
	"chrome-agent/internal/state"
	"chrome-agent/internal/task"
	"chrome-agent/pkg/logger"
)

func main() {
	var (
		taskFile    = flag.String("task", "task.txt", "Path to task specification file")
		mcpConfig   = flag.String("mcp", "mcp-config.json", "Path to MCP configuration file")
		dbPath      = flag.String("db", "agent.db", "Path to SQLite database file")
		logLevel    = flag.String("log", "INFO", "Log level: DEBUG, INFO, WARN, ERROR")
	)
	flag.Parse()

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
	statusReport, err := mcpClient.ValidateConnection()
	if err != nil {
		log.Error("MCP server validation failed: %v", err)
		os.Exit(1)
	}

	// Output status report
	fmt.Print(mcp.FormatStatusReport(*statusReport))
	if !statusReport.Connected {
		log.Error("MCP server is not connected")
		os.Exit(1)
	}

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

	// Load and parse task
	log.Info("Loading task from: %s", *taskFile)
	taskSpec, err := task.ParseTaskFile(*taskFile)
	if err != nil {
		log.Error("Failed to parse task file: %v", err)
		os.Exit(1)
	}

	log.Info("Task objective: %s", taskSpec.Objective)
	if taskSpec.LoopCondition != nil {
		log.Info("Loop condition: %s", taskSpec.LoopCondition.Description)
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

	// Execute task
	var execErr error
	if taskManager.IsLoopable() {
		log.Info("Task is loopable, starting loop execution...")
		loopManager := loop.NewManager(taskManager, executor, log)
		execErr = loopManager.ExecuteLoop()
	} else {
		log.Info("Executing single task...")
		execErr = executeSingleTask(taskManager, executor, exceptionHandler, log)
	}

	// Handle execution result
	if execErr != nil {
		log.Error("Task execution failed: %v", execErr)
		if err := taskManager.UpdateTaskStatus(state.TaskStatusFailed); err != nil {
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

	// Disconnect MCP client
	if err := mcpClient.Disconnect(); err != nil {
		log.Error("Failed to disconnect MCP client: %v", err)
	}
}

func executeSingleTask(taskManager *task.Manager, executor *task.Executor, exceptionHandler *exception.Handler, log *logger.Logger) error {
	spec := taskManager.GetSpec()

	for _, rule := range spec.SubtaskRules {
		subtaskID, err := taskManager.CreateSubtask(rule.Name, 0)
		if err != nil {
			return fmt.Errorf("failed to create subtask: %w", err)
		}

		if err := taskManager.UpdateSubtaskStatus(subtaskID, state.SubtaskStatusInProgress); err != nil {
			return fmt.Errorf("failed to update subtask status: %w", err)
		}

		result, err := executor.ExecuteSubtaskRule(rule, "Single task execution")

		if err != nil {
			log.Error("Subtask execution failed: %v", err)
			
			// Handle exception
			needsIntervention, handleErr := exceptionHandler.HandleException(subtaskID, err)
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

