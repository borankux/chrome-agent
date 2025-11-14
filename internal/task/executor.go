package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"chrome-agent/internal/llm"
	"chrome-agent/internal/mcp"
	"chrome-agent/internal/session"
	"chrome-agent/internal/state"
	"chrome-agent/pkg/logger"
)

// Executor executes subtasks using LLM and MCP
type Executor struct {
	mcpClient      *mcp.Client
	llmCoord       *llm.Coordinator
	logger         *logger.Logger
	executionHistory []ToolCallResult // Track tool execution history
	errorPatterns   map[string]int     // Track consecutive errors for deadlock detection
	sessionManager *session.Manager   // Session management
	db             *state.DB          // Database for session persistence
	currentSessions map[string]*session.Session // toolType -> Session
	browserSessionID string           // Session ID for browser tools (shared across all browser tool calls)
}

// ExecutionResult contains execution result
type ExecutionResult struct {
	Success      bool
	Result       interface{}
	Error        error
	Duration     time.Duration
	ToolCalls    []llm.ToolCall
	Retryable    bool // Whether this error is retryable
	StateCapture string // JSON of captured state before execution
}

// ExecutionContext contains rich context about execution state
type ExecutionContext struct {
	PreviousToolCalls []ToolCallResult
	CurrentState      string // Description of current tool state
	ToolHistory       []string // What tools were used
	LastError         string // Last error if any
	CycleNumber       int
	SubtaskName       string
	Objective         string
}

// ToolCallResult represents the result of a tool call
type ToolCallResult struct {
	ToolName    string
	Arguments   map[string]interface{}
	Success     bool
	Result      interface{}
	Error       string
	Consequence string // What this tool call accomplished/changed
}

// NewExecutor creates a new task executor
func NewExecutor(mcpClient *mcp.Client, llmCoord *llm.Coordinator, log *logger.Logger, db *state.DB) *Executor {
	return &Executor{
		mcpClient:      mcpClient,
		llmCoord:       llmCoord,
		logger:         log,
		executionHistory: make([]ToolCallResult, 0),
		errorPatterns:   make(map[string]int),
		sessionManager: session.NewManager(),
		db:             db,
		currentSessions: make(map[string]*session.Session),
	}
}

// LoadSessions loads active sessions from database
func (e *Executor) LoadSessions() error {
	if e.db == nil {
		return nil
	}

	activeSessions, err := e.db.GetActiveSessions()
	if err != nil {
		return fmt.Errorf("failed to load active sessions: %w", err)
	}

	for _, sess := range activeSessions {
		e.currentSessions[sess.ToolType] = sess
		e.sessionManager.Sessions[sess.ID] = sess
		e.logger.Info("Loaded active session: %s (tool: %s, state: %s)", sess.ID, sess.ToolType, sess.State)
	}

	return nil
}

// SaveSessions saves all sessions to database
func (e *Executor) SaveSessions() error {
	if e.db == nil {
		return nil
	}

	for _, sess := range e.currentSessions {
		// Check if session exists in DB
		_, err := e.db.GetSession(sess.ID)
		if err != nil {
			// Session doesn't exist, create it
			if err := e.db.CreateSession(sess); err != nil {
				e.logger.Warn("Failed to create session %s: %v", sess.ID, err)
			}
		} else {
			// Session exists, update it
			if err := e.db.UpdateSession(sess); err != nil {
				e.logger.Warn("Failed to update session %s: %v", sess.ID, err)
			}
		}
	}

	return nil
}

// getToolType determines tool type from tool name (tool-agnostic)
func (e *Executor) getToolType(toolName string) string {
	// Extract tool type from tool name (e.g., "browser_navigate" -> "browser")
	// This is tool-agnostic - works with any naming convention
	toolNameLower := strings.ToLower(toolName)
	
	// Common patterns
	if strings.Contains(toolNameLower, "browser") {
		return "browser"
	}
	if strings.Contains(toolNameLower, "api") || strings.Contains(toolNameLower, "http") {
		return "api"
	}
	if strings.Contains(toolNameLower, "database") || strings.Contains(toolNameLower, "db") {
		return "database"
	}
	if strings.Contains(toolNameLower, "file") || strings.Contains(toolNameLower, "fs") {
		return "filesystem"
	}
	
	// Default: use first part of tool name or "unknown"
	parts := strings.Split(toolNameLower, "_")
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

// generateBrowserSessionID generates a unique session ID for browser tools
// Format: browser_{uuid} - one session ID per task execution
// IMPORTANT: This should only be called once per executor instance to ensure
// all browser tools use the same session_id and reuse the same browser tab
func (e *Executor) generateBrowserSessionID() string {
	if e.browserSessionID == "" {
		e.browserSessionID = fmt.Sprintf("browser_%s", uuid.New().String())
		e.logger.Info("Generated browser session ID: %s (will be reused for all browser tools)", e.browserSessionID)
	} else {
		e.logger.Debug("Reusing existing browser session ID: %s", e.browserSessionID)
	}
	return e.browserSessionID
}

// injectSessionID injects session_id into browser tool arguments if needed
// This enables session persistence across multiple browser tool calls
func (e *Executor) injectSessionID(toolName string, arguments map[string]interface{}) {
	// Check if this is a browser tool
	toolType := e.getToolType(toolName)
	if toolType != "browser" {
		// Not a browser tool, no session_id needed
		return
	}

	// Check if session_id is already in arguments (LLM might have added it)
	if _, exists := arguments["session_id"]; exists {
		e.logger.Debug("Session ID already present in arguments for tool %s", toolName)
		return
	}

	// Generate session ID if not already generated
	sessionID := e.generateBrowserSessionID()

	// Inject session_id into arguments
	arguments["session_id"] = sessionID
	e.logger.Debug("Injected session_id %s into browser tool %s", sessionID, toolName)
}

// getOrCreateSession gets or creates a session for a tool type
// Sessions are reused across multiple tool calls of the same type
func (e *Executor) getOrCreateSession(toolType string, metadata map[string]interface{}) *session.Session {
	// Check if we have an active session for this tool type
	if sess, exists := e.currentSessions[toolType]; exists {
		if sess.State == session.StateActive || sess.State == session.StateRecovering {
			// Update metadata even when reusing session (e.g., track latest operation)
			if metadata != nil && len(metadata) > 0 {
				if err := e.sessionManager.UpdateMetadata(sess.ID, metadata); err != nil {
					e.logger.Debug("Failed to update session metadata: %v", err)
				}
				// Also update in-memory session
				if sess.Metadata == nil {
					sess.Metadata = make(map[string]interface{})
				}
				for k, v := range metadata {
					sess.Metadata[k] = v
				}
			}
			e.logger.Debug("Reusing existing session %s for tool type %s", sess.ID, toolType)
			return sess
		}
	}

	// Create new session
	sess := e.sessionManager.CreateSession(toolType, metadata)
	e.currentSessions[toolType] = sess
	e.logger.Debug("Created new session %s for tool type %s", sess.ID, toolType)
	
	// Save to database
	if e.db != nil {
		if err := e.db.CreateSession(sess); err != nil {
			e.logger.Warn("Failed to save session to database: %v", err)
		}
	}

	return sess
}

// GetLLMCoordinator returns the LLM coordinator
func (e *Executor) GetLLMCoordinator() *llm.Coordinator {
	return e.llmCoord
}

// ValidateToolAccessible checks if a tool exists and is accessible
func (e *Executor) ValidateToolAccessible(toolName string) error {
	e.logger.Debug("Checking MCP tool availability: %s", toolName)
	
	// Check MCP health before listing tools
	health := e.mcpClient.GetHealth()
	if health.Status == "dead" {
		return fmt.Errorf("MCP server is dead - cannot access tools")
	}
	
	// If health is poor, ask LLM what to do
	if health.Status == "unresponsive" && e.llmCoord != nil {
		contextStr := fmt.Sprintf("Attempting to validate tool %s", toolName)
		action, reasoning, err := e.llmCoord.EvaluateMCPHealth(
			health.Status,
			health.ConsecutiveFailures,
			health.ReconnectAttempts,
			health.TimeSinceLastSuccess,
			contextStr,
			"", // Objective not available here
			"tool validation",
		)
		if err == nil {
			e.logger.Info("LLM MCP health decision: %s", action)
			e.logger.Debug("LLM reasoning: %s", reasoning)
			
			if action == "stop_task" {
				return fmt.Errorf("LLM determined MCP server is dead - cannot proceed: %s", reasoning)
			}
			if action == "retry_connection" {
				if err := e.mcpClient.Reconnect(); err != nil {
					return fmt.Errorf("MCP reconnection failed: %w", err)
				}
			}
		}
	}
	
	tools, err := e.mcpClient.ListTools()
	if err != nil {
		// Check if this is a connection error
		health := e.mcpClient.GetHealth()
		if health.Status == "dead" || health.ConsecutiveFailures >= 5 {
			return fmt.Errorf("MCP server appears dead after %d failures - cannot access tools", health.ConsecutiveFailures)
		}
		return fmt.Errorf("failed to list tools: %w", err)
	}

	for _, tool := range tools {
		if tool.Name == toolName {
			e.logger.Debug("Tool %s is available", toolName)
			return nil
		}
	}

	return fmt.Errorf("tool '%s' not found in available tools", toolName)
}

// ExecuteSubtask executes a subtask (kept for backward compatibility)
// This method is deprecated - use ExecuteSubtaskRule instead
func (e *Executor) ExecuteSubtask(subtaskName string, subtaskDescription string, context string) (*ExecutionResult, error) {
	// Create a temporary rule and use ExecuteSubtaskRule
	rule := SubtaskRule{
		Name:        subtaskName,
		Description: subtaskDescription,
		Steps:       []string{},
	}
	return e.ExecuteSubtaskRule(rule, context)
}

// ExecuteSubtaskRule executes a subtask rule with full context awareness
func (e *Executor) ExecuteSubtaskRule(rule SubtaskRule, context string) (*ExecutionResult, error) {
	return e.ExecuteSubtaskRuleWithContext(rule, context, nil)
}

// ExecuteSubtaskRuleWithContext executes a subtask rule with execution context
func (e *Executor) ExecuteSubtaskRuleWithContext(rule SubtaskRule, context string, execCtx *ExecutionContext) (*ExecutionResult, error) {
	startTime := time.Now()
	result := &ExecutionResult{
		ToolCalls: make([]llm.ToolCall, 0),
		Retryable: true,
	}

	// Build execution context if not provided
	if execCtx == nil {
		execCtx = e.buildExecutionContext(rule.Name, context)
	} else {
		// Update provided context with current execution state
		e.updateExecutionContext(execCtx)
	}

	e.logger.Info("▶ Executing: %s", rule.Name)
	if execCtx != nil && execCtx.CurrentState != "" {
		e.logger.Debug("  Current state: %s", execCtx.CurrentState)
	}
	e.logger.Debug("  Description: %s", rule.Description)

	// Extract tool name and arguments from steps
	toolName, arguments, err := e.extractToolFromSteps(rule.Steps)
	if err != nil {
		// If tool not specified, use LLM with full context to determine tool
		toolName, arguments, err = e.determineToolWithContext(rule, execCtx)
		if err != nil {
			result.Retryable = false
			result.Error = err
			result.Success = false
			result.Duration = time.Since(startTime)
			return result, err
		}
	}

	// Validate tool is accessible
	e.logger.Debug("Validating tool: %s", toolName)
	if err := e.ValidateToolAccessible(toolName); err != nil {
		result.Retryable = false
		result.Error = fmt.Errorf("tool %s is not accessible: %w", toolName, err)
		result.Success = false
		result.Duration = time.Since(startTime)
		e.logger.Error("✗ Tool validation failed: %v", result.Error)
		return result, result.Error
	}

	// Capture state before execution
	stateCapture, err := e.CaptureState()
	if err != nil {
		e.logger.Debug("State capture failed: %v", err)
		stateCapture = "{}"
	}
	result.StateCapture = stateCapture

	// Get or create session for this tool
	toolType := e.getToolType(toolName)
	sess := e.getOrCreateSession(toolType, map[string]interface{}{
		"tool_name": toolName,
		"last_operation": "execute",
	})

	// Check session health before execution
	health := sess.GetHealth()
	if !health.IsHealthy && sess.State == session.StateActive {
		e.logger.Warn("Session %s health degraded: success_rate=%.2f, consecutive_failures=%d", 
			sess.ID, health.SuccessRate, health.ConsecutiveFailures)
	}

	// Check MCP health before execution
	mcpHealth := e.mcpClient.GetHealth()
	if mcpHealth.Status == "dead" {
		// MCP is dead - ask LLM what to do
		if e.llmCoord != nil {
			contextStr := ""
			if execCtx != nil {
				contextStr = e.buildContextString(execCtx, rule)
			}
			objective := ""
			if execCtx != nil {
				objective = execCtx.Objective
			}
			taskProgress := fmt.Sprintf("Subtask: %s, Tool: %s", rule.Name, toolName)
			
			action, reasoning, err := e.llmCoord.EvaluateMCPHealth(
				mcpHealth.Status,
				mcpHealth.ConsecutiveFailures,
				mcpHealth.ReconnectAttempts,
				mcpHealth.TimeSinceLastSuccess,
				contextStr,
				objective,
				taskProgress,
			)
			if err == nil {
				e.logger.Info("LLM MCP health decision: %s", action)
				e.logger.Debug("LLM reasoning: %s", reasoning)
				
				if action == "stop_task" {
					return result, fmt.Errorf("LLM determined MCP server is dead - stopping task: %s", reasoning)
				}
				if action == "retry_connection" {
					e.logger.Info("LLM decided to retry MCP connection")
					if reconnectErr := e.mcpClient.Reconnect(); reconnectErr != nil {
						return result, fmt.Errorf("MCP reconnection failed: %w", reconnectErr)
					}
					// Health may have improved after reconnection
					mcpHealth = e.mcpClient.GetHealth()
				}
			}
		} else {
			// No LLM coordinator - stop task if MCP is dead
			return result, fmt.Errorf("MCP server is dead (%d consecutive failures) - cannot proceed", mcpHealth.ConsecutiveFailures)
		}
	}

	// Inject session_id for browser tools (enables session persistence)
	e.injectSessionID(toolName, arguments)

	// Log concrete details before execution
	e.logger.Info("Executing tool: %s", toolName)
	e.logger.Info("Tool arguments: %v", arguments)
	e.logger.Info("Session details - ID: %s, ToolType: %s, State: %s", sess.ID, toolType, sess.State)
	
	// Log browser session ID to track if it's consistent
	if toolType == "browser" {
		if sessionID, ok := arguments["session_id"].(string); ok {
			e.logger.Info("Browser session_id being used: %s", sessionID)
			if sessionID != e.browserSessionID {
				e.logger.Warn("Session ID mismatch! Expected: %s, Got: %s", e.browserSessionID, sessionID)
			}
		} else {
			e.logger.Warn("No session_id found in browser tool arguments!")
		}
	}
	
	// Check session health before execution (especially for browser tools)
	if toolType == "browser" {
		health := sess.GetHealth()
		e.logger.Info("Session health - SuccessRate: %.2f, ConsecutiveFailures: %d, TotalOps: %d, IsHealthy: %v", 
			health.SuccessRate, health.ConsecutiveFailures, health.TotalOperations, health.IsHealthy)
		
		if !health.IsHealthy && sess.State == session.StateActive {
			e.logger.Warn("Session %s is unhealthy before tool execution - SuccessRate: %.2f, ConsecutiveFailures: %d", 
				sess.ID, health.SuccessRate, health.ConsecutiveFailures)
		}
	}
	
	e.logger.Info("MCP Health - Status: %s, ConsecutiveFailures: %d, ReconnectAttempts: %d", 
		mcpHealth.Status, mcpHealth.ConsecutiveFailures, mcpHealth.ReconnectAttempts)
	
	// For interactive browser tools that need element references, capture fresh snapshot first
	// This ensures we have up-to-date page state and element references
	if toolType == "browser" && e.needsFreshSnapshot(toolName) {
		e.logger.Info("Capturing fresh snapshot before %s to ensure up-to-date element references", toolName)
		snapshotArgs := map[string]interface{}{}
		e.injectSessionID("browser_snapshot", snapshotArgs)
		// Try different possible snapshot tool names
		snapshotTools := []string{"browser_snapshot", "mcp_playwright_browser_snapshot"}
		snapshotCaptured := false
		for _, snapTool := range snapshotTools {
			_, err := e.mcpClient.CallTool(snapTool, snapshotArgs)
			if err == nil {
				e.logger.Info("Fresh snapshot captured successfully before %s", toolName)
				snapshotCaptured = true
				break
			}
		}
		if !snapshotCaptured {
			e.logger.Warn("Failed to capture fresh snapshot before %s, proceeding anyway", toolName)
		}
	}
	
	e.logger.Tool("Executing: %s", toolName)

	toolResult, err := e.mcpClient.CallTool(toolName, arguments)
	
	// Log MCP response
	if err != nil {
		e.logger.Error("MCP tool call failed - Tool: %s, Error: %v", toolName, err)
		// Check MCP health after failure
		mcpHealth = e.mcpClient.GetHealth()
		
		// If MCP appears dead or unresponsive, ask LLM what to do
		if (mcpHealth.Status == "dead" || mcpHealth.Status == "unresponsive") && e.llmCoord != nil {
			contextStr := ""
			if execCtx != nil {
				contextStr = e.buildContextString(execCtx, rule)
			}
			objective := ""
			if execCtx != nil {
				objective = execCtx.Objective
			}
			taskProgress := fmt.Sprintf("Subtask: %s, Tool: %s", rule.Name, toolName)
			
			action, reasoning, err := e.llmCoord.EvaluateMCPHealth(
				mcpHealth.Status,
				mcpHealth.ConsecutiveFailures,
				mcpHealth.ReconnectAttempts,
				mcpHealth.TimeSinceLastSuccess,
				contextStr,
				objective,
				taskProgress,
			)
			if err == nil {
				e.logger.Info("LLM MCP health decision: %s", action)
				e.logger.Debug("LLM reasoning: %s", reasoning)
				
				if action == "stop_task" {
					return result, fmt.Errorf("LLM determined MCP server is dead - stopping task: %s", reasoning)
				}
				if action == "retry_connection" {
					e.logger.Info("LLM decided to retry MCP connection")
					if reconnectErr := e.mcpClient.Reconnect(); reconnectErr != nil {
						e.logger.Error("MCP reconnection failed: %v", reconnectErr)
						return result, fmt.Errorf("MCP reconnection failed: %w", reconnectErr)
					}
					// Retry the tool call after reconnection
					toolResult, err = e.mcpClient.CallTool(toolName, arguments)
					if err != nil {
						// Still failed after reconnection
						mcpHealth = e.mcpClient.GetHealth()
						if mcpHealth.Status == "dead" {
							return result, fmt.Errorf("MCP server appears dead after reconnection attempt")
						}
					} else {
						// Success after reconnection - continue normally
						e.logger.Info("Tool call succeeded after MCP reconnection")
					}
				}
			}
		}
		
		if err != nil {
			// Error is already abstracted by MCP client, but add more context if needed
			abstractError := err.Error()
			e.logger.Error("✗ Tool execution failed")
			e.logger.Error("Tool: %s", toolName)
			e.logger.Error("Error: %s", abstractError)
			
			// Enhanced logging for browser_type failures
			if toolName == "browser_type" || strings.Contains(toolName, "browser_type") {
				e.logger.Error("browser_type failure details:")
				e.logger.Error("  Session ID: %s", e.browserSessionID)
				e.logger.Error("  Tool arguments: %v", arguments)
				e.logger.Error("  Session state: %s", sess.State)
				health := sess.GetHealth()
				e.logger.Error("  Session health - SuccessRate: %.2f, ConsecutiveFailures: %d", 
					health.SuccessRate, health.ConsecutiveFailures)
				e.logger.Error("  MCP health - Status: %s, ConsecutiveFailures: %d", 
					mcpHealth.Status, mcpHealth.ConsecutiveFailures)
			}
			
			// Record failure in session
			e.sessionManager.RecordFailure(sess.ID)
			if e.db != nil {
				if err := e.db.UpdateSession(sess); err != nil {
					e.logger.Debug("Failed to update session in DB: %v", err)
				}
			}

			// Track error pattern for deadlock detection
			errorKey := fmt.Sprintf("%s:%s", toolName, abstractError)
			e.errorPatterns[errorKey]++
			consecutiveFailures := e.errorPatterns[errorKey]
			
			// Check for deadlock (same error 3+ times)
			isDeadlock := consecutiveFailures >= 3
			if isDeadlock {
				e.logger.Warn("  ⚠ Deadlock detected: Same error repeated %d times", consecutiveFailures)
			}
			
			// Ask LLM what to do with the session
			if e.llmCoord != nil {
				health := sess.GetHealth()
				contextStr := ""
				if execCtx != nil {
					contextStr = e.buildContextString(execCtx, rule)
				}
				
				e.logger.Info("Asking LLM for session lifecycle decision - ToolType: %s, Health: success_rate=%.2f, consecutive_failures=%d", 
					toolType, health.SuccessRate, health.ConsecutiveFailures)
				
				sessionAction, reasoning, err := e.llmCoord.EvaluateSessionLifecycle(
					toolType,
					fmt.Sprintf("success_rate=%.2f, consecutive_failures=%d, total_ops=%d", 
						health.SuccessRate, health.ConsecutiveFailures, health.TotalOperations),
					abstractError,
					health.ConsecutiveFailures,
					health.TotalOperations,
					health.SuccessRate,
					contextStr,
					func() string {
						if execCtx != nil {
							return execCtx.Objective
						}
						return ""
					}(),
				)
				if err == nil {
					e.logger.Info("LLM session decision: %s", sessionAction)
					e.logger.Info("LLM reasoning: %s", reasoning)
					
					// Execute LLM session decision
					switch sessionAction {
					case "close":
						e.logger.Warn("LLM decided to close session %s", sess.ID)
						e.sessionManager.CloseSession(sess.ID)
						if e.db != nil {
							e.db.CloseSession(sess.ID)
						}
						delete(e.currentSessions, toolType)
					case "recover":
						e.logger.Info("LLM decided to recover session %s", sess.ID)
						e.sessionManager.UpdateState(sess.ID, session.StateRecovering)
						if e.db != nil {
							e.db.UpdateSession(sess)
						}
					case "recreate":
						e.logger.Info("LLM decided to recreate session for %s", toolType)
						e.logger.Warn("Recreating session - this will create a NEW browser tab!")
						e.logger.Info("Old session ID: %s", sess.ID)
						
						// For browser tools, we should NOT clear browserSessionID
						// because we want to reuse the same session_id with the MCP server
						// The MCP server should handle recreating the browser context for that session_id
						if toolType != "browser" {
							e.sessionManager.CloseSession(sess.ID)
							if e.db != nil {
								e.db.CloseSession(sess.ID)
							}
							delete(e.currentSessions, toolType)
						} else {
							// For browser, just mark session as recovering but keep the session_id
							e.logger.Info("Keeping browser session ID %s - MCP server should recreate context", e.browserSessionID)
							e.sessionManager.UpdateState(sess.ID, session.StateRecovering)
							if e.db != nil {
								e.db.UpdateSession(sess)
							}
						}
						// New session will be created on next call (or reused for browser)
					case "keep_alive":
						fallthrough
					default:
						e.logger.Debug("LLM decided to keep session alive")
						// Session remains active
					}
				}
			}
			
			// Record failed tool call in history
			e.recordToolCall(toolName, arguments, false, nil, abstractError, "Failed to execute")
			
			// Update execution context with error
			if execCtx != nil {
				e.updateExecutionContext(execCtx)
				execCtx.LastError = abstractError
			}
			
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				Name:      toolName,
				Arguments: arguments,
			})
			result.Error = fmt.Errorf("tool %s failed: %s", toolName, abstractError)
			result.Success = false
			result.Retryable = e.isRetryableError(err)
			result.Duration = time.Since(startTime)
			
			// Store deadlock info in result for handling
			if isDeadlock {
				// Add deadlock flag to error message
				result.Error = fmt.Errorf("DEADLOCK: %v (repeated %d times)", result.Error, consecutiveFailures)
			}
			
			return result, result.Error
		}
	}
	
	// Success - log MCP response
	e.logger.Info("MCP tool call succeeded - Tool: %s", toolName)
	if toolResult != nil {
		// Log result summary (truncate if too long)
		resultStr := string(toolResult)
		if len(resultStr) > 200 {
			resultStr = resultStr[:200] + "..."
		}
		e.logger.Info("MCP response: %s", resultStr)
	}
	
	// Success - record in session
	e.sessionManager.RecordSuccess(sess.ID)
	if e.db != nil {
		if err := e.db.UpdateSession(sess); err != nil {
			e.logger.Debug("Failed to update session in DB: %v", err)
		}
	}
	
	// Success - reset error pattern for this tool
	errorKey := fmt.Sprintf("%s:", toolName)
	for key := range e.errorPatterns {
		if strings.HasPrefix(key, errorKey) {
			delete(e.errorPatterns, key)
		}
	}
	
	// Record successful tool call in history
	e.recordToolCall(toolName, arguments, true, toolResult, "", "Successfully executed")
	
	// Update execution context with success
	if execCtx != nil {
		e.updateExecutionContext(execCtx)
		execCtx.LastError = ""
	}
	
	// Parse tool result
	var parsedResult interface{}
	if err := json.Unmarshal(toolResult, &parsedResult); err != nil {
		parsedResult = string(toolResult)
	}
	
	result.Result = parsedResult
	result.Success = true
	result.Duration = time.Since(startTime)
	result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
		Name:      toolName,
		Arguments: arguments,
	})
	
	// Determine consequence of this tool call
	consequence := e.determineToolConsequence(toolName, arguments, parsedResult)
	e.logger.Debug("  Consequence: %s", consequence)
	
	// Update execution context with latest state
	if execCtx != nil {
		e.updateExecutionContext(execCtx)
		execCtx.LastError = ""
	}
	
	e.logger.Info("✓ Subtask '%s' completed in %v", rule.Name, result.Duration)

	return result, nil
}

// buildExecutionContext builds execution context from current state
func (e *Executor) buildExecutionContext(subtaskName string, context string) *ExecutionContext {
	// Create a copy of execution history for context
	historyCopy := make([]ToolCallResult, len(e.executionHistory))
	copy(historyCopy, e.executionHistory)
	
	return &ExecutionContext{
		PreviousToolCalls: historyCopy,
		CurrentState:       e.describeCurrentState(),
		ToolHistory:        e.getToolHistory(),
		LastError:          e.getLastError(),
		SubtaskName:        subtaskName,
	}
}

// updateExecutionContext updates the provided context with current execution state
func (e *Executor) updateExecutionContext(execCtx *ExecutionContext) {
	if execCtx == nil {
		return
	}
	// Update context with current execution history
	execCtx.PreviousToolCalls = make([]ToolCallResult, len(e.executionHistory))
	copy(execCtx.PreviousToolCalls, e.executionHistory)
	execCtx.CurrentState = e.describeCurrentState()
	execCtx.ToolHistory = e.getToolHistory()
}

// recordToolCall records a tool call in execution history
func (e *Executor) recordToolCall(toolName string, arguments map[string]interface{}, success bool, result interface{}, errorMsg, consequence string) {
	callResult := ToolCallResult{
		ToolName:    toolName,
		Arguments:   arguments,
		Success:     success,
		Result:      result,
		Error:       errorMsg,
		Consequence: consequence,
	}
	e.executionHistory = append(e.executionHistory, callResult)
	// Keep only last 50 tool calls to avoid memory issues
	if len(e.executionHistory) > 50 {
		e.executionHistory = e.executionHistory[len(e.executionHistory)-50:]
	}
}

// describeCurrentState describes the current state based on tool history
func (e *Executor) describeCurrentState() string {
	if len(e.executionHistory) == 0 {
		return "No tools have been executed yet"
	}
	
	lastCall := e.executionHistory[len(e.executionHistory)-1]
	if !lastCall.Success {
		return fmt.Sprintf("Last tool call (%s) failed: %s", lastCall.ToolName, lastCall.Error)
	}
	
	return fmt.Sprintf("Last successful tool: %s - %s", lastCall.ToolName, lastCall.Consequence)
}

// getToolHistory returns list of tools used
func (e *Executor) getToolHistory() []string {
	history := make([]string, len(e.executionHistory))
	for i, call := range e.executionHistory {
		history[i] = call.ToolName
	}
	return history
}

// getLastError returns the last error if any
func (e *Executor) getLastError() string {
	if len(e.executionHistory) == 0 {
		return ""
	}
	lastCall := e.executionHistory[len(e.executionHistory)-1]
	return lastCall.Error
}

// determineToolConsequence determines what a tool call accomplished
func (e *Executor) determineToolConsequence(toolName string, arguments map[string]interface{}, result interface{}) string {
	// Basic consequence determination based on tool name patterns
	if strings.Contains(toolName, "navigate") {
		if url, ok := arguments["url"].(string); ok {
			return fmt.Sprintf("Navigated to %s", url)
		}
		return "Navigation performed"
	}
	if strings.Contains(toolName, "click") {
		return "Element clicked"
	}
	if strings.Contains(toolName, "type") || strings.Contains(toolName, "fill") {
		return "Text entered into form field"
	}
	if strings.Contains(toolName, "wait") {
		return "Waited for condition"
	}
	if strings.Contains(toolName, "evaluate") || strings.Contains(toolName, "extract") {
		return "Data extracted from page"
	}
	if strings.Contains(toolName, "snapshot") {
		return "Page state captured"
	}
	return fmt.Sprintf("Tool %s executed successfully", toolName)
}

// needsFreshSnapshot determines if a browser tool needs a fresh snapshot before execution
// Interactive tools that use element references need fresh snapshots to ensure
// element references are up-to-date with current page state
func (e *Executor) needsFreshSnapshot(toolName string) bool {
	toolNameLower := strings.ToLower(toolName)
	// Tools that interact with elements need fresh snapshots
	interactiveTools := []string{
		"browser_type",
		"browser_click",
		"browser_fill_form",
		"browser_select_option",
		"browser_drag",
		"browser_hover",
	}
	for _, interactiveTool := range interactiveTools {
		if strings.Contains(toolNameLower, interactiveTool) {
			return true
		}
	}
	return false
}

// isRetryableError determines if an error is retryable
func (e *Executor) isRetryableError(err error) bool {
	errStr := err.Error()
	// Session/state errors might be retryable if we can re-establish state
	if strings.Contains(errStr, "Session not found") || strings.Contains(errStr, "404") {
		return true // Can retry by re-establishing state
	}
	if strings.Contains(errStr, "timeout") {
		return true // Timeouts are usually retryable
	}
	return true // Default to retryable for tool execution errors
}

// extractToolFromSteps extracts tool name and arguments from subtask steps
func (e *Executor) extractToolFromSteps(steps []string) (string, map[string]interface{}, error) {
	var toolName string
	var arguments map[string]interface{} = make(map[string]interface{})

	for _, step := range steps {
		if strings.HasPrefix(step, "TOOL:") {
			toolName = strings.TrimPrefix(step, "TOOL:")
			toolName = strings.TrimSpace(toolName)
		} else if strings.HasPrefix(step, "ARGS:") {
			argsJSON := strings.TrimPrefix(step, "ARGS:")
			argsJSON = strings.TrimSpace(argsJSON)
			if err := json.Unmarshal([]byte(argsJSON), &arguments); err != nil {
				return "", nil, fmt.Errorf("failed to parse tool arguments: %w", err)
			}
		}
	}

	if toolName == "" {
		// If no tool specified in steps, try to use LLM to determine tool
		// This handles backward compatibility and recursive breakdown
		return e.determineToolFromDescription(steps)
	}

	return toolName, arguments, nil
}

// determineToolFromDescription uses LLM to determine which tool to use
// This is used when tool is not explicitly specified (backward compatibility or recursive breakdown)
func (e *Executor) determineToolFromDescription(steps []string) (string, map[string]interface{}, error) {
	rule := SubtaskRule{
		Name:        "Determine Tool",
		Description: strings.Join(steps, "\n"),
		Steps:       steps,
	}
	ctx := e.buildExecutionContext("Determine Tool", "Tool not specified")
	return e.determineToolWithContext(rule, ctx)
}

// determineToolWithContext uses LLM with full context to determine which tool to use
func (e *Executor) determineToolWithContext(rule SubtaskRule, execCtx *ExecutionContext) (string, map[string]interface{}, error) {
	e.logger.Debug("Tool not explicitly specified, using LLM with context to determine tool")
	
	// Get available tools
	tools, err := e.mcpClient.ListTools()
	if err != nil {
		return "", nil, fmt.Errorf("failed to list tools: %w", err)
	}

	// Convert tools to interface slice
	toolsInterface := make([]interface{}, len(tools))
	for i, tool := range tools {
		toolMap := map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		}
		toolsInterface[i] = toolMap
	}

	// Build rich context string
	contextStr := e.buildContextString(execCtx, rule)

	// Use LLM to reason about which tool to use with full context
	reasoning, err := e.llmCoord.ReasonAboutSubtaskWithContext(rule.Description, contextStr, toolsInterface, execCtx)
	if err != nil {
		return "", nil, fmt.Errorf("LLM reasoning failed: %w", err)
	}

	if len(reasoning.ToolCalls) == 0 {
		return "", nil, fmt.Errorf("LLM did not suggest any tool calls")
	}

	if len(reasoning.ToolCalls) > 1 {
		e.logger.Warn("LLM suggested %d tools, but subtask should use only one. Using first tool.", len(reasoning.ToolCalls))
	}

	toolCall := reasoning.ToolCalls[0]
	return toolCall.Name, toolCall.Arguments, nil
}

// BuildContextString builds a rich context string for LLM (exported for use by loop manager)
func (e *Executor) BuildContextString(execCtx *ExecutionContext, rule SubtaskRule) string {
	return e.buildContextString(execCtx, rule)
}

// buildContextString builds a rich context string for LLM
func (e *Executor) buildContextString(execCtx *ExecutionContext, rule SubtaskRule) string {
	var context strings.Builder
	
	context.WriteString("Current Execution Context:\n")
	context.WriteString(fmt.Sprintf("- Subtask: %s\n", rule.Name))
	context.WriteString(fmt.Sprintf("- Description: %s\n", rule.Description))
	
	if execCtx != nil {
		context.WriteString(fmt.Sprintf("- Current State: %s\n", execCtx.CurrentState))
		
		if len(execCtx.PreviousToolCalls) > 0 {
			context.WriteString("\nPrevious Tool Calls:\n")
			// Show last 5 tool calls
			start := len(execCtx.PreviousToolCalls) - 5
			if start < 0 {
				start = 0
			}
			for i := start; i < len(execCtx.PreviousToolCalls); i++ {
				call := execCtx.PreviousToolCalls[i]
				status := "✓"
				if !call.Success {
					status = "✗"
				}
				context.WriteString(fmt.Sprintf("  %s %s: %s\n", status, call.ToolName, call.Consequence))
			}
		}
		
		if execCtx.LastError != "" {
			context.WriteString(fmt.Sprintf("\nLast Error: %s\n", execCtx.LastError))
		}
		
		if len(execCtx.ToolHistory) > 0 {
			context.WriteString(fmt.Sprintf("\nTool History: %s\n", strings.Join(execCtx.ToolHistory, " → ")))
		}
	}
	
	return context.String()
}

// CaptureState captures current browser/system state via MCP
func (e *Executor) CaptureState() (string, error) {
	// Use MCP to get current browser state
	// This includes URL, cookies, localStorage, sessionStorage, etc.
	e.logger.Debug("Capturing current state via MCP...")
	
	// Call browser_snapshot tool to get current state
	snapshotArgs := map[string]interface{}{
		"random_string": "state_capture",
	}
	e.injectSessionID("mcp_playwright_browser_snapshot", snapshotArgs)
	
	stateData, err := e.mcpClient.CallTool("mcp_playwright_browser_snapshot", snapshotArgs)
	if err != nil {
		// If snapshot fails, return empty state (non-critical)
		e.logger.Debug("Failed to capture state via MCP: %v", err)
		return "{}", nil
	}
	
	e.logger.Debug("State captured successfully")
	return string(stateData), nil
}

// RestoreState restores browser/system state from snapshot
func (e *Executor) RestoreState(stateJSON string) error {
	if stateJSON == "" || stateJSON == "{}" {
		e.logger.Debug("No state to restore")
		return nil
	}
	
	e.logger.Debug("Restoring previous state via MCP...")
	
	// Parse state data
	var stateData map[string]interface{}
	if err := json.Unmarshal([]byte(stateJSON), &stateData); err != nil {
		return fmt.Errorf("failed to parse state data: %w", err)
	}
	
	// If state contains URL, navigate back to it
	if url, ok := stateData["url"].(string); ok && url != "" {
		e.logger.Debug("Restoring URL via MCP: %s", url)
		// Create arguments map for navigation
		navArgs := map[string]interface{}{
			"url": url,
		}
		e.injectSessionID("mcp_playwright_browser_navigate", navArgs)
		
		if _, err := e.mcpClient.CallTool("mcp_playwright_browser_navigate", navArgs); err != nil {
			return fmt.Errorf("failed to navigate to URL: %w", err)
		}
		e.logger.Debug("URL restored successfully")
	}
	
	// Additional state restoration logic can be added here
	// (cookies, localStorage, form data, etc.)
	
	return nil
}

// ReestablishBrowserSession attempts to re-establish a lost browser session
// This is used when deadlock is detected due to session loss
func (e *Executor) ReestablishBrowserSession(stateJSON string) error {
	e.logger.Info("Attempting to re-establish browser session...")
	e.logger.Info("Current browser session ID: %s", e.browserSessionID)
	
	// Try to find a URL from state snapshot or execution history
	var targetURL string
	
	// First, try to get URL from state snapshot
	if stateJSON != "" && stateJSON != "{}" {
		var stateData map[string]interface{}
		if err := json.Unmarshal([]byte(stateJSON), &stateData); err == nil {
			if url, ok := stateData["url"].(string); ok && url != "" {
				targetURL = url
			}
		}
	}
	
	// If no URL in snapshot, try to find last successful navigation from history
	if targetURL == "" {
		for i := len(e.executionHistory) - 1; i >= 0; i-- {
			call := e.executionHistory[i]
			if call.Success {
				// Check if this was a navigation call
				if call.ToolName == "browser_navigate" || call.ToolName == "mcp_playwright_browser_navigate" {
					if url, ok := call.Arguments["url"].(string); ok && url != "" {
						targetURL = url
						e.logger.Debug("Found URL from navigation history: %s", targetURL)
						break
					}
				}
			}
		}
	}
	
	// If still no URL, use a default or skip
	if targetURL == "" {
		e.logger.Warn("No URL found to re-establish session - browser session may need manual intervention")
		return fmt.Errorf("no URL available to re-establish browser session")
	}
	
	// Try to navigate to re-establish the session
	e.logger.Info("Navigating to %s to re-establish browser session", targetURL)
	
	// Try different possible tool names for navigation
	navigationTools := []string{
		"browser_navigate",
		"mcp_playwright_browser_navigate",
		"navigate",
	}
	
	var lastErr error
	for _, toolName := range navigationTools {
		// Create arguments map for navigation
		navArgs := map[string]interface{}{
			"url": targetURL,
		}
		e.injectSessionID(toolName, navArgs)
		
		// Log the session_id being used for re-establishment
		if sessionID, ok := navArgs["session_id"].(string); ok {
			e.logger.Info("Re-establishing browser session with ID: %s, URL: %s", sessionID, targetURL)
		}
		
		_, err := e.mcpClient.CallTool(toolName, navArgs)
		if err == nil {
			e.logger.Info("Browser session re-established successfully by navigating to %s", targetURL)
			e.logger.Info("Session ID used: %s", e.browserSessionID)
			// Clear error patterns since we've recovered
			e.errorPatterns = make(map[string]int)
			return nil
		}
		lastErr = err
		e.logger.Debug("Failed to navigate with tool %s: %v", toolName, err)
	}
	
	return fmt.Errorf("failed to re-establish browser session: %w", lastErr)
}

