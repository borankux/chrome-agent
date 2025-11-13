package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"chrome-agent/internal/llm"
	"chrome-agent/internal/mcp"
	"chrome-agent/pkg/logger"
)

// Executor executes subtasks using LLM and MCP
type Executor struct {
	mcpClient      *mcp.Client
	llmCoord       *llm.Coordinator
	logger         *logger.Logger
	executionHistory []ToolCallResult // Track tool execution history
	errorPatterns   map[string]int     // Track consecutive errors for deadlock detection
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
func NewExecutor(mcpClient *mcp.Client, llmCoord *llm.Coordinator, log *logger.Logger) *Executor {
	return &Executor{
		mcpClient:      mcpClient,
		llmCoord:       llmCoord,
		logger:         log,
		executionHistory: make([]ToolCallResult, 0),
		errorPatterns:   make(map[string]int),
	}
}

// GetLLMCoordinator returns the LLM coordinator
func (e *Executor) GetLLMCoordinator() *llm.Coordinator {
	return e.llmCoord
}

// ValidateToolAccessible checks if a tool exists and is accessible
func (e *Executor) ValidateToolAccessible(toolName string) error {
	e.logger.Debug("Checking MCP tool availability: %s", toolName)
	
	tools, err := e.mcpClient.ListTools()
	if err != nil {
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

	// Execute single tool call
	e.logger.Tool("Executing: %s", toolName)
	e.logger.Debug("  Arguments: %v", arguments)

	toolResult, err := e.mcpClient.CallTool(toolName, arguments)
	if err != nil {
		// Error is already abstracted by MCP client, but add more context if needed
		abstractError := err.Error()
		e.logger.Error("  ✗ Tool execution failed")
		e.logger.Error("    Tool: %s", toolName)
		e.logger.Error("    Error: %s", abstractError)
		
		// Track error pattern for deadlock detection
		errorKey := fmt.Sprintf("%s:%s", toolName, abstractError)
		e.errorPatterns[errorKey]++
		consecutiveFailures := e.errorPatterns[errorKey]
		
		// Check for deadlock (same error 3+ times)
		isDeadlock := consecutiveFailures >= 3
		if isDeadlock {
			e.logger.Warn("  ⚠ Deadlock detected: Same error repeated %d times", consecutiveFailures)
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
	
	// Success - reset error pattern for this tool
	errorKey := fmt.Sprintf("%s:", toolName)
	for key := range e.errorPatterns {
		if strings.HasPrefix(key, errorKey) {
			delete(e.errorPatterns, key)
		}
	}

	// Parse result
	var parsedResult interface{}
	if err := json.Unmarshal(toolResult, &parsedResult); err != nil {
		parsedResult = string(toolResult)
	}

	// Determine consequence of this tool call
	consequence := e.determineToolConsequence(toolName, arguments, parsedResult)

	// Record successful tool call in history
	e.recordToolCall(toolName, arguments, true, parsedResult, "", consequence)

	result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
		Name:      toolName,
		Arguments: arguments,
	})
	result.Success = true
	result.Result = parsedResult
	result.Duration = time.Since(startTime)
	
	e.logger.Info("  ✓ Tool completed: %s", toolName)
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
func (e *Executor) buildExecutionContext(subtaskName, context string) *ExecutionContext {
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
	stateData, err := e.mcpClient.CallTool("mcp_playwright_browser_snapshot", map[string]interface{}{
		"random_string": "state_capture",
	})
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
		if _, err := e.mcpClient.CallTool("mcp_playwright_browser_navigate", map[string]interface{}{
			"url": url,
		}); err != nil {
			return fmt.Errorf("failed to navigate to URL: %w", err)
		}
		e.logger.Debug("URL restored successfully")
	}
	
	// Additional state restoration logic can be added here
	// (cookies, localStorage, form data, etc.)
	
	return nil
}

