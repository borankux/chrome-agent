package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// TokenUsage tracks token usage for a single LLM call
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CallType         string // "planning", "reasoning", etc.
}

// loadPrompt loads a prompt from a file in the prompts/ directory
func loadPrompt(filename string) (string, error) {
	// Get the project root directory (assuming we're in internal/llm/)
	// Try to find prompts directory relative to current working directory
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	// Try multiple possible locations for prompts directory
	possiblePaths := []string{
		filepath.Join(wd, "prompts", filename),
		filepath.Join(wd, "..", "prompts", filename),
		filepath.Join(wd, "..", "..", "prompts", filename),
		filepath.Join(wd, "..", "..", "..", "prompts", filename),
	}

	var promptPath string
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			promptPath = path
			break
		}
	}

	if promptPath == "" {
		return "", fmt.Errorf("prompt file not found: %s (searched in: %v)", filename, possiblePaths)
	}

	content, err := os.ReadFile(promptPath)
	if err != nil {
		return "", fmt.Errorf("failed to read prompt file %s: %w", filename, err)
	}

	return string(content), nil
}

// validatePrompt validates a prompt for formatting requirements and placeholder integrity
func validatePrompt(prompt string, promptName string, hasPlaceholders bool) error {
	// Check prompt is non-empty
	if len(strings.TrimSpace(prompt)) == 0 {
		return fmt.Errorf("prompt validation failed for %s: prompt is empty", promptName)
	}

	// Check minimum length
	if len(prompt) < 10 {
		return fmt.Errorf("prompt validation failed for %s: prompt too short (minimum 10 characters)", promptName)
	}

	// Check maximum length (100KB)
	const maxPromptSize = 100 * 1024
	if len(prompt) > maxPromptSize {
		return fmt.Errorf("prompt validation failed for %s: prompt too long (maximum %d bytes)", promptName, maxPromptSize)
	}

	// Validate placeholders if needed
	if hasPlaceholders {
		if err := validatePlaceholders(prompt, promptName); err != nil {
			return err
		}
	}

	return nil
}

// validatePlaceholders validates fmt.Sprintf placeholders in a prompt
func validatePlaceholders(prompt string, promptName string) error {
	// Pattern to match valid fmt.Sprintf format specifiers
	// Matches: %[flags][width][.precision]verb
	// Valid verbs: v, s, d, b, o, x, X, f, F, e, E, g, G, c, q, p, t, T, U
	// Also handles %% for literal percent sign
	validVerbPattern := regexp.MustCompile(`%%|%[+\-#0 ]?[0-9]*(\.[0-9]+)?[vTtbcdoqxXfFeEgGspU]`)

	// Find all % signs
	percentIndexes := []int{}
	for i, r := range prompt {
		if r == '%' {
			percentIndexes = append(percentIndexes, i)
		}
	}

	// Check each % sign
	for _, idx := range percentIndexes {
		// Get the remaining string from this position
		remaining := prompt[idx:]

		// Check if this % starts a valid format specifier
		matched := validVerbPattern.FindStringIndex(remaining)
		if matched == nil || matched[0] != 0 {
			// Find the next space, newline, or end of string to show context
			contextEnd := idx + 20
			if contextEnd > len(prompt) {
				contextEnd = len(prompt)
			}
			context := prompt[idx:contextEnd]
			return fmt.Errorf("prompt validation failed for %s: broken or invalid fmt.Sprintf placeholder at position %d: %q (valid format: %%[flags][width][.precision]verb)", promptName, idx, context)
		}
	}

	// Check for unmatched braces (if used)
	openBraces := strings.Count(prompt, "{")
	closeBraces := strings.Count(prompt, "}")
	if openBraces != closeBraces {
		return fmt.Errorf("prompt validation failed for %s: unmatched braces (open: %d, close: %d)", promptName, openBraces, closeBraces)
	}

	return nil
}

// Coordinator handles LLM reasoning and tool coordination
type Coordinator struct {
	client     *openai.Client
	model      string
	messages   []openai.ChatCompletionMessage
	tokenUsage []TokenUsage
	mu         sync.Mutex // Protects tokenUsage
}

// ToolCall represents a tool call decision from LLM
type ToolCall struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Reasoning string                 `json:"reasoning"`
}

// ReasoningResult contains LLM reasoning output
type ReasoningResult struct {
	ToolCalls  []ToolCall `json:"tool_calls"`
	Reasoning  string     `json:"reasoning"`
	NextStep   string     `json:"next_step"`
	Confidence float64    `json:"confidence"`
}

// NewCoordinator creates a new LLM coordinator
func NewCoordinator() (*Coordinator, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable not set")
	}

	client := openai.NewClient(apiKey)

	return &Coordinator{
		client:     client,
		model:      "gpt-4o", // Use latest model
		messages:   make([]openai.ChatCompletionMessage, 0),
		tokenUsage: make([]TokenUsage, 0),
	}, nil
}

// SetModel changes the model to use
func (c *Coordinator) SetModel(model string) {
	c.model = model
}

// AddSystemMessage adds a system message to the conversation
func (c *Coordinator) AddSystemMessage(content string) {
	c.messages = append(c.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: content,
	})
}

// AddUserMessage adds a user message to the conversation
func (c *Coordinator) AddUserMessage(content string) {
	c.messages = append(c.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: content,
	})
}

// AddAssistantMessage adds an assistant message to the conversation
func (c *Coordinator) AddAssistantMessage(content string) {
	c.messages = append(c.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: content,
	})
}

// BuildToolDefinitions converts MCP tools to OpenAI tool definitions
func BuildToolDefinitions(tools []interface{}) []openai.Tool {
	definitions := make([]openai.Tool, 0, len(tools))

	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := toolMap["name"].(string)
		description, _ := toolMap["description"].(string)
		inputSchema, _ := toolMap["inputSchema"].(map[string]interface{})

		if name == "" {
			continue
		}

		// Convert inputSchema to JSON schema format
		var schema map[string]interface{}
		if inputSchema != nil {
			schemaBytes, err := json.Marshal(inputSchema)
			if err == nil {
				if err := json.Unmarshal(schemaBytes, &schema); err != nil {
					schema = nil
				}
			}
		}

		def := openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        name,
				Description: description,
				Parameters:  schema,
			},
		}

		definitions = append(definitions, def)
	}

	return definitions
}

// ReasonAboutTask uses LLM to reason about a task and select tools
func (c *Coordinator) ReasonAboutTask(taskDescription string, availableTools []interface{}) (*ReasoningResult, error) {
	return c.ReasonAboutTaskWithContext(taskDescription, availableTools, nil)
}

// ReasonAboutTaskWithContext uses LLM to reason about a task with full execution context
func (c *Coordinator) ReasonAboutTaskWithContext(taskDescription string, availableTools []interface{}, execCtx interface{}) (*ReasoningResult, error) {
	systemPrompt, err := loadPrompt("reason_about_task_system.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to load system prompt: %w", err)
	}
	if err := validatePrompt(systemPrompt, "reason_about_task_system.txt", false); err != nil {
		return nil, fmt.Errorf("system prompt validation failed: %w", err)
	}

	toolsJSON, err := json.MarshalIndent(availableTools, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tools: %w", err)
	}

	userPromptTemplate, err := loadPrompt("reason_about_task_user.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to load user prompt: %w", err)
	}
	if err := validatePrompt(userPromptTemplate, "reason_about_task_user.txt", true); err != nil {
		return nil, fmt.Errorf("user prompt validation failed: %w", err)
	}

	userPrompt := fmt.Sprintf(userPromptTemplate, taskDescription, string(toolsJSON))

	// Build function definitions for OpenAI
	toolDefs := BuildToolDefinitions(availableTools)

	messages := append([]openai.ChatCompletionMessage{}, c.messages...)
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemPrompt,
	})
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userPrompt,
	})

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.7,
	}

	if len(toolDefs) > 0 {
		req.Tools = toolDefs
		req.ToolChoice = "auto"
	}

	ctx := context.Background()
	resp, err := c.callWithRetry(ctx, req, 3)
	if err != nil {
		return nil, fmt.Errorf("failed to call OpenAI API: %w", err)
	}

	// Record token usage
	if resp.Usage.TotalTokens > 0 {
		c.recordTokenUsage(resp.Usage, "reasoning")
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	choice := resp.Choices[0]
	content := choice.Message.Content

	// Try to parse as JSON first
	var result ReasoningResult
	if err := json.Unmarshal([]byte(content), &result); err == nil {
		return &result, nil
	}

	// If tool calls were made, extract them
	if len(choice.Message.ToolCalls) > 0 {
		result.Reasoning = content
		result.ToolCalls = make([]ToolCall, 0, len(choice.Message.ToolCalls))

		for _, tc := range choice.Message.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = make(map[string]interface{})
			}

			result.ToolCalls = append(result.ToolCalls, ToolCall{
				Name:      tc.Function.Name,
				Arguments: args,
				Reasoning: "Selected by LLM tool calling",
			})
		}

		return &result, nil
	}

	// Fallback: parse from text
	result.Reasoning = content
	result.NextStep = "Continue execution"
	result.Confidence = 0.5

	return &result, nil
}

// ReasonAboutSubtask reasons about a specific subtask
func (c *Coordinator) ReasonAboutSubtask(subtaskDescription string, context string, availableTools []interface{}) (*ReasoningResult, error) {
	fullDescription := fmt.Sprintf(`Subtask: %s

Context: %s`, subtaskDescription, context)

	return c.ReasonAboutTask(fullDescription, availableTools)
}

// ReasonAboutSubtaskWithContext reasons about a subtask with full execution context
func (c *Coordinator) ReasonAboutSubtaskWithContext(subtaskDescription string, context string, availableTools []interface{}, execCtx interface{}) (*ReasoningResult, error) {
	// Import task package types for context
	type ExecutionContext struct {
		PreviousToolCalls []interface{}
		CurrentState      string
		ToolHistory       []string
		LastError         string
		CycleNumber       int
		SubtaskName       string
		Objective         string
	}

	// Build enhanced context with execution history
	var enhancedContext strings.Builder
	enhancedContext.WriteString(context)
	enhancedContext.WriteString("\n\n")
	enhancedContext.WriteString("IMPORTANT: Consider the execution history above when making decisions.\n")
	enhancedContext.WriteString("Think about:\n")
	enhancedContext.WriteString("- What tools were already called and what they accomplished\n")
	enhancedContext.WriteString("- Whether tool state needs to be maintained or re-established\n")
	enhancedContext.WriteString("- How this tool call relates to previous tool calls\n")
	enhancedContext.WriteString("- Whether related tools should reuse existing state\n")
	enhancedContext.WriteString("\nSESSION PERSISTENCE:\n")
	enhancedContext.WriteString("- Browser tools automatically share the same browser session across all calls\n")
	enhancedContext.WriteString("- Browser state (tabs, cookies, navigation) persists between tool calls\n")
	enhancedContext.WriteString("- You don't need to pass session_id - it's handled automatically\n")
	enhancedContext.WriteString("- This means if you navigated to a page earlier, subsequent browser tools can interact with that same page\n")

	fullDescription := fmt.Sprintf(`Subtask: %s

%s`, subtaskDescription, enhancedContext.String())

	return c.ReasonAboutTaskWithContext(fullDescription, availableTools, execCtx)
}

// GetContext returns current conversation context
func (c *Coordinator) GetContext() []openai.ChatCompletionMessage {
	return c.messages
}

// Reset clears the conversation history
func (c *Coordinator) Reset() {
	c.messages = make([]openai.ChatCompletionMessage, 0)
}

// callWithRetry wraps CreateChatCompletion with retry logic
// Retries up to maxRetries times with exponential backoff
func (c *Coordinator) callWithRetry(ctx context.Context, req openai.ChatCompletionRequest, maxRetries int) (*openai.ChatCompletionResponse, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			time.Sleep(backoff)
		}
		resp, err := c.client.CreateChatCompletion(ctx, req)
		if err == nil {
			return &resp, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("LLM call failed after %d retries: %w", maxRetries, lastErr)
}

// recordTokenUsage records token usage from an OpenAI response
func (c *Coordinator) recordTokenUsage(usage openai.Usage, callType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tokenUsage := TokenUsage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CallType:         callType,
	}

	c.tokenUsage = append(c.tokenUsage, tokenUsage)

	// Log token usage for this call
	fmt.Printf("[LLM] %s call: %d prompt + %d completion = %d total tokens\n",
		callType, tokenUsage.PromptTokens, tokenUsage.CompletionTokens, tokenUsage.TotalTokens)
}

// GetTokenStatistics returns token usage statistics
func (c *Coordinator) GetTokenStatistics() (totalPrompt, totalCompletion, totalTokens int, callCount int, breakdown []TokenUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	totalPrompt = 0
	totalCompletion = 0
	totalTokens = 0
	callCount = len(c.tokenUsage)

	for _, usage := range c.tokenUsage {
		totalPrompt += usage.PromptTokens
		totalCompletion += usage.CompletionTokens
		totalTokens += usage.TotalTokens
	}

	// Create a copy of breakdown for safe access
	breakdown = make([]TokenUsage, len(c.tokenUsage))
	copy(breakdown, c.tokenUsage)

	return totalPrompt, totalCompletion, totalTokens, callCount, breakdown
}

// PlanTask uses LLM to plan a task from a free-form objective
// Returns JSON string with structured plan
func (c *Coordinator) PlanTask(objective string, availableTools []interface{}) (string, error) {
	systemPrompt, err := loadPrompt("plan_task_system.txt")
	if err != nil {
		return "", fmt.Errorf("failed to load system prompt: %w", err)
	}
	if err := validatePrompt(systemPrompt, "plan_task_system.txt", false); err != nil {
		return "", fmt.Errorf("system prompt validation failed: %w", err)
	}

	toolsJSON, err := json.MarshalIndent(availableTools, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal tools: %w", err)
	}

	userPromptTemplate, err := loadPrompt("plan_task_user.txt")
	if err != nil {
		return "", fmt.Errorf("failed to load user prompt: %w", err)
	}
	if err := validatePrompt(userPromptTemplate, "plan_task_user.txt", true); err != nil {
		return "", fmt.Errorf("user prompt validation failed: %w", err)
	}

	userPrompt := fmt.Sprintf(userPromptTemplate, objective, string(toolsJSON))

	messages := append([]openai.ChatCompletionMessage{}, c.messages...)
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemPrompt,
	})
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userPrompt,
	})

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.3, // Lower temperature for more structured output
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}

	ctx := context.Background()
	resp, err := c.callWithRetry(ctx, req, 3)
	if err != nil {
		return "", fmt.Errorf("failed to call OpenAI API: %w", err)
	}

	// Record token usage
	if resp.Usage.TotalTokens > 0 {
		c.recordTokenUsage(resp.Usage, "planning")
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	content := resp.Choices[0].Message.Content

	// Clean up the content - remove markdown code blocks if present
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	// Validate it's valid JSON
	var testJSON map[string]interface{}
	if err := json.Unmarshal([]byte(content), &testJSON); err != nil {
		return "", fmt.Errorf("LLM returned invalid JSON: %w", err)
	}

	return content, nil
}

// EvaluateLoopCompletion evaluates whether a loop should continue or complete using LLM
func (c *Coordinator) EvaluateLoopCompletion(executionContext string, loopType string, progress float64, target float64, cycleNumber int, objective string) (bool, string, error) {
	systemPrompt, err := loadPrompt("evaluate_loop_completion_system.txt")
	if err != nil {
		return false, "", fmt.Errorf("failed to load system prompt: %w", err)
	}
	if err := validatePrompt(systemPrompt, "evaluate_loop_completion_system.txt", false); err != nil {
		return false, "", fmt.Errorf("system prompt validation failed: %w", err)
	}

	userPromptTemplate, err := loadPrompt("evaluate_loop_completion_user.txt")
	if err != nil {
		return false, "", fmt.Errorf("failed to load user prompt: %w", err)
	}
	if err := validatePrompt(userPromptTemplate, "evaluate_loop_completion_user.txt", true); err != nil {
		return false, "", fmt.Errorf("user prompt validation failed: %w", err)
	}

	userPrompt := fmt.Sprintf(userPromptTemplate, objective, loopType, progress, target, cycleNumber, executionContext)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.3, // Lower temperature for more consistent decisions
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}
	resp, err := c.callWithRetry(context.Background(), req, 3)
	if err != nil {
		return false, "", fmt.Errorf("LLM evaluation failed: %w", err)
	}

	// Record token usage
	c.recordTokenUsage(resp.Usage, "loop_completion_evaluation")

	content := resp.Choices[0].Message.Content
	// Clean up markdown code blocks if present
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	var result struct {
		ShouldComplete    bool    `json:"should_complete"`
		Reasoning         string  `json:"reasoning"`
		ObjectiveAchieved bool    `json:"objective_achieved"`
		Confidence        float64 `json:"confidence"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return false, "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return result.ShouldComplete, result.Reasoning, nil
}

// EvaluateRetryExhaustion evaluates what to do when retries are exhausted
func (c *Coordinator) EvaluateRetryExhaustion(subtaskName string, toolName string, errorMessage string, retryCount int, maxRetries int, executionContext string, objective string) (string, string, error) {
	systemPrompt, err := loadPrompt("evaluate_retry_exhaustion_system.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load system prompt: %w", err)
	}
	if err := validatePrompt(systemPrompt, "evaluate_retry_exhaustion_system.txt", false); err != nil {
		return "", "", fmt.Errorf("system prompt validation failed: %w", err)
	}

	userPromptTemplate, err := loadPrompt("evaluate_retry_exhaustion_user.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load user prompt: %w", err)
	}
	if err := validatePrompt(userPromptTemplate, "evaluate_retry_exhaustion_user.txt", true); err != nil {
		return "", "", fmt.Errorf("user prompt validation failed: %w", err)
	}

	userPrompt := fmt.Sprintf(userPromptTemplate, subtaskName, toolName, errorMessage, retryCount, maxRetries, objective, executionContext)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.5,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}
	resp, err := c.callWithRetry(context.Background(), req, 3)
	if err != nil {
		return "skip", "", fmt.Errorf("LLM evaluation failed: %w", err)
	}

	// Record token usage
	c.recordTokenUsage(resp.Usage, "retry_exhaustion_evaluation")

	content := resp.Choices[0].Message.Content
	// Clean up markdown code blocks if present
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	var result struct {
		Action     string `json:"action"`
		Reasoning  string `json:"reasoning"`
		Suggestion string `json:"suggestion"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return "skip", "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return result.Action, result.Reasoning, nil
}

// EvaluateDeadlock evaluates what to do when a deadlock is detected
func (c *Coordinator) EvaluateDeadlock(subtaskName string, toolName string, errorMessage string, consecutiveFailures int, executionContext string, objective string) (string, string, error) {
	systemPrompt, err := loadPrompt("evaluate_deadlock_system.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load system prompt: %w", err)
	}
	if err := validatePrompt(systemPrompt, "evaluate_deadlock_system.txt", false); err != nil {
		return "", "", fmt.Errorf("system prompt validation failed: %w", err)
	}

	userPromptTemplate, err := loadPrompt("evaluate_deadlock_user.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load user prompt: %w", err)
	}
	if err := validatePrompt(userPromptTemplate, "evaluate_deadlock_user.txt", true); err != nil {
		return "", "", fmt.Errorf("user prompt validation failed: %w", err)
	}

	userPrompt := fmt.Sprintf(userPromptTemplate, subtaskName, toolName, errorMessage, consecutiveFailures, objective, executionContext, consecutiveFailures)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.5,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}
	resp, err := c.callWithRetry(context.Background(), req, 3)
	if err != nil {
		return "skip", "", fmt.Errorf("LLM evaluation failed: %w", err)
	}

	// Record token usage
	c.recordTokenUsage(resp.Usage, "deadlock_evaluation")

	content := resp.Choices[0].Message.Content
	// Clean up markdown code blocks if present
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	var result struct {
		Action        string   `json:"action"`
		Reasoning     string   `json:"reasoning"`
		RecoverySteps []string `json:"recovery_steps"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return "skip", "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	reasoning := result.Reasoning
	if len(result.RecoverySteps) > 0 {
		reasoning += "\nRecovery steps: " + strings.Join(result.RecoverySteps, ", ")
	}

	return result.Action, reasoning, nil
}

// EvaluateSessionLifecycle evaluates what to do with a session when errors occur
func (c *Coordinator) EvaluateSessionLifecycle(
	toolType string,
	sessionHealth string,
	errorMessage string,
	consecutiveFailures int,
	totalOperations int,
	successRate float64,
	executionContext string,
	objective string,
) (string, string, error) {
	systemPrompt, err := loadPrompt("evaluate_session_lifecycle_system.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load system prompt: %w", err)
	}
	if err := validatePrompt(systemPrompt, "evaluate_session_lifecycle_system.txt", false); err != nil {
		return "", "", fmt.Errorf("system prompt validation failed: %w", err)
	}

	userPromptTemplate, err := loadPrompt("evaluate_session_lifecycle_user.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load user prompt: %w", err)
	}
	if err := validatePrompt(userPromptTemplate, "evaluate_session_lifecycle_user.txt", true); err != nil {
		return "", "", fmt.Errorf("user prompt validation failed: %w", err)
	}

	userPrompt := fmt.Sprintf(userPromptTemplate,
		toolType, sessionHealth, errorMessage, consecutiveFailures, totalOperations, successRate*100, objective, executionContext)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.3, // Lower temperature for consistent decisions
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}
	resp, err := c.callWithRetry(context.Background(), req, 3)
	if err != nil {
		return "keep_alive", "", fmt.Errorf("LLM evaluation failed: %w", err)
	}

	// Record token usage
	c.recordTokenUsage(resp.Usage, "session_lifecycle_evaluation")

	content := resp.Choices[0].Message.Content
	// Clean up markdown code blocks if present
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	var result struct {
		Action         string `json:"action"`
		Reasoning      string `json:"reasoning"`
		ShouldPreserve bool   `json:"should_preserve"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return "keep_alive", "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return result.Action, result.Reasoning, nil
}

// EvaluateMCPHealth evaluates MCP server health and decides what to do
func (c *Coordinator) EvaluateMCPHealth(
	mcpHealth string,
	consecutiveFailures int,
	reconnectAttempts int,
	timeSinceLastSuccess time.Duration,
	executionContext string,
	objective string,
	taskProgress string,
) (string, string, error) {
	systemPrompt, err := loadPrompt("evaluate_mcp_health_system.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load system prompt: %w", err)
	}
	if err := validatePrompt(systemPrompt, "evaluate_mcp_health_system.txt", false); err != nil {
		return "", "", fmt.Errorf("system prompt validation failed: %w", err)
	}

	userPromptTemplate, err := loadPrompt("evaluate_mcp_health_user.txt")
	if err != nil {
		return "", "", fmt.Errorf("failed to load user prompt: %w", err)
	}
	if err := validatePrompt(userPromptTemplate, "evaluate_mcp_health_user.txt", true); err != nil {
		return "", "", fmt.Errorf("user prompt validation failed: %w", err)
	}

	userPrompt := fmt.Sprintf(userPromptTemplate,
		mcpHealth, consecutiveFailures, reconnectAttempts, timeSinceLastSuccess, objective, taskProgress, executionContext)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.3, // Lower temperature for consistent decisions
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}
	resp, err := c.callWithRetry(context.Background(), req, 3)
	if err != nil {
		return "continue", "", fmt.Errorf("LLM evaluation failed: %w", err)
	}

	// Record token usage
	c.recordTokenUsage(resp.Usage, "mcp_health_evaluation")

	content := resp.Choices[0].Message.Content
	// Clean up markdown code blocks if present
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	var result struct {
		Action    string `json:"action"`
		Reasoning string `json:"reasoning"`
		MCPStatus string `json:"mcp_status"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return "continue", "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return result.Action, result.Reasoning, nil
}
