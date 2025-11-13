package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/sashabaranov/go-openai"
)

// TokenUsage tracks token usage for a single LLM call
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CallType         string // "planning", "reasoning", etc.
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
	systemPrompt := `You are an intelligent agent coordinator that helps execute tasks using tools that interact with external systems.

CRITICAL UNDERSTANDING:
- Tools interact with external systems (browsers, APIs, databases, etc.) that maintain STATE
- When you call a tool, it may CREATE, MODIFY, or QUERY state in an external system
- Related tool calls often benefit from SHARING STATE (e.g., browser session, API connection)
- Tool state can be LOST if not maintained properly (sessions expire, connections close)
- You must reason about tool CONSEQUENCES and STATE DEPENDENCIES

Your job is to:
1. Understand the task description and current execution context
2. Analyze what tools were called before and what state they created
3. Determine if you should REUSE existing tool state or CREATE new state
4. Select the appropriate tool(s) considering state management
5. Think about tool CONSEQUENCES - what happens when this tool is called?
6. Consider tool SEQUENCES - which tools depend on others?

TOOL STATE MANAGEMENT:
- If previous tools created state (e.g., opened browser, logged in), consider reusing it
- If tool state was lost (errors indicate state not found), you may need to re-establish state
- Group related operations that benefit from shared state
- Think abstractly: "What state does this tool need? Does it exist? Should I create it?"

Respond in JSON format with:
- reasoning: Your thought process including state management considerations
- tool_calls: Array of tool calls to make (each with name, arguments, reasoning)
- next_step: What should happen after these tool calls
- confidence: Your confidence level (0.0 to 1.0)`

	toolsJSON, err := json.MarshalIndent(availableTools, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tools: %w", err)
	}

	userPrompt := fmt.Sprintf(`Task: %s

Available Tools:
%s

Analyze this task considering:
1. What tools were called before (if any) and what state they created
2. Whether you should reuse existing tool state or create new state
3. The consequences of calling each tool - what does it do? What state does it create/modify?
4. Tool dependencies - which tools need other tools to run first?
5. State management - how should tool state be maintained across calls?

Think abstractly about tool interactions and their consequences.`, taskDescription, string(toolsJSON))

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
	resp, err := c.client.CreateChatCompletion(ctx, req)
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
	systemPrompt := `You are an intelligent task planning agent that breaks down high-level objectives into structured, executable tasks.

CRITICAL RULE: Each task must use exactly ONE MCP tool call. If a task requires multiple tools, break it into multiple sequential tasks.

UNDERSTANDING TOOL INTERACTIONS:
- Tools interact with external systems (browsers, APIs, databases, etc.) that maintain STATE
- When tools are called, they CREATE, MODIFY, or QUERY state in external systems
- Related tool calls often benefit from SHARING STATE (e.g., browser sessions, API connections)
- Think about tool CONSEQUENCES: What happens when this tool is called? What state does it create?
- Consider tool SEQUENCES: Which tools depend on others? Which tools create state that others need?
- Group related operations that benefit from shared state together in the plan

Your job is to:
1. Analyze the objective and understand what needs to be accomplished
2. Break it down into logical subtasks - each subtask uses exactly ONE tool from the available tools
3. Think about tool CONSEQUENCES and STATE DEPENDENCIES when ordering tasks
4. Group related tool calls that benefit from shared state (e.g., browser operations)
5. Identify if the task requires looping (time-based or quota-based)
6. Suggest exception handling rules for common failure scenarios
7. For each task, specify the exact tool name and arguments needed

You MUST respond with valid JSON in the following schema:
{
  "objective": "string - the main objective",
  "loop_condition": {
    "type": "time|quota|null - null if no loop needed",
    "target_value": number - duration in seconds for time, count for quota,
    "description": "string - human-readable description"
  },
  "tasks": [
    {
      "name": "string - short task name",
      "description": "string - detailed description of what this task does",
      "tool_name": "string - REQUIRED: the exact name of the MCP tool to call (must match available tools)",
      "arguments": { "key": "value" } - REQUIRED: arguments object for the tool call (can be empty {}),
      "steps": ["string"] - optional array of step descriptions,
      "requires_loop": boolean - whether this task should be repeated
    }
  ],
  "exception_rules": [
    {
      "pattern": "string - error pattern to match",
      "requires_intervention": boolean - whether human intervention needed,
      "retryable": boolean - whether this can be retried,
      "max_retries": number - maximum retry attempts
    }
  ]
}

Examples:
- For "search for 10 movie stars": 
  - loop_condition.type = "quota", target_value = 10
  - Each task should use one tool (e.g., browser_navigate, browser_click, etc.)
  - Group browser operations together - they share browser state
- For "run for 1 hour": 
  - loop_condition.type = "time", target_value = 3600
- For "open Google and search": 
  - Task 1: tool_name = "mcp_playwright_browser_navigate", arguments = {"url": "https://google.com"}
    (This creates browser state - subsequent browser tools can reuse it)
  - Task 2: tool_name = "mcp_playwright_browser_fill", arguments = {"selector": "input[name='q']", "text": "search term"}
    (This uses the browser state created by Task 1)

TOOL STATE THINKING:
- When planning, think: "What state does this tool create? Do later tools need that state?"
- Group tools that share state together (e.g., all browser operations in sequence)
- Consider tool consequences: navigation creates browser state, clicking uses browser state, etc.
- Order tasks so state-creating tools come before state-using tools

IMPORTANT: 
- Each task MUST have exactly one tool_name from the available tools list
- If a high-level action needs multiple tools, create multiple tasks
- Think about tool state and consequences when ordering tasks
- Return ONLY valid JSON, no markdown formatting, no code blocks.`

	toolsJSON, err := json.MarshalIndent(availableTools, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal tools: %w", err)
	}

	userPrompt := fmt.Sprintf(`Objective: %s

Available Tools:
%s

Analyze this objective and create a structured plan. Break it down into executable tasks where EACH TASK USES EXACTLY ONE TOOL from the available tools list above.

CRITICAL CONSIDERATIONS:
1. Tool State Management:
   - Think about what state each tool creates or needs
   - Group related tools that share state (e.g., browser operations)
   - Order tasks so state-creating tools come before state-using tools

2. Tool Consequences:
   - What happens when you call each tool?
   - What state does it create/modify/query?
   - How do tools depend on each other?

3. Task Ordering:
   - Order tasks logically based on state dependencies
   - Group operations that benefit from shared state
   - Consider tool sequences and their consequences

Remember:
- Each task must specify exactly one tool_name (must match a tool from the list above)
- Each task must include an arguments object (can be empty {} if no arguments needed)
- If the objective requires multiple tool calls, create multiple sequential tasks
- Use the exact tool names as shown in the available tools list
- Think abstractly about tool interactions and state management`, objective, string(toolsJSON))

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
	resp, err := c.client.CreateChatCompletion(ctx, req)
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
	systemPrompt := `You are an intelligent loop completion evaluator. Your job is to determine if a loop should continue or complete based on execution context and progress.

CRITICAL UNDERSTANDING:
- Loops can complete EARLY if the objective is achieved before reaching the target
- Loops should CONTINUE if progress is being made toward the objective
- Loops should COMPLETE if the objective is clearly achieved or if continuing would be wasteful
- Consider both mechanical progress (time/quota) AND actual objective achievement

You must respond with valid JSON:
{
  "should_complete": boolean - true if loop should complete, false if should continue,
  "reasoning": "string - detailed explanation of your decision",
  "objective_achieved": boolean - whether the actual objective has been achieved,
  "confidence": number - confidence level 0.0 to 1.0
}`

	userPrompt := fmt.Sprintf(`Evaluate loop completion:

Objective: %s
Loop Type: %s
Current Progress: %.2f
Target: %.2f
Cycle Number: %d

Execution Context:
%s

Based on this information, determine:
1. Has the objective been achieved? (even if progress < target)
2. Should the loop continue or complete?
3. What is your reasoning?

Consider:
- If objective is achieved early, complete the loop
- If making good progress toward objective, continue
- If stuck or making no progress, consider completing
- Balance between efficiency and thoroughness`, objective, loopType, progress, target, cycleNumber, executionContext)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	resp, err := c.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:       c.model,
			Messages:    messages,
			Temperature: 0.3, // Lower temperature for more consistent decisions
			ResponseFormat: &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			},
		},
	)
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
	systemPrompt := `You are an intelligent error recovery coordinator. When retry limits are exceeded, you must decide the best course of action.

Available actions:
- "skip": Skip this subtask and continue to next
- "end_cycle": End current cycle and move to next cycle
- "end_task": End the entire task
- "recover_state": Attempt to recover tool state and retry
- "adjust_approach": Suggest a different approach for this subtask

You must respond with valid JSON:
{
  "action": "string - one of: skip, end_cycle, end_task, recover_state, adjust_approach",
  "reasoning": "string - detailed explanation",
  "suggestion": "string - optional suggestion for recovery or adjustment"
}`

	userPrompt := fmt.Sprintf(`Retry limit exceeded - decide next action:

Subtask: %s
Tool: %s
Error: %s
Retries: %d/%d
Objective: %s

Execution Context:
%s

What should we do?
- Skip this subtask if it's not critical?
- End cycle if this subtask is blocking?
- End task if objective cannot be achieved?
- Recover state if tool state was lost?
- Adjust approach if a different method might work?`, subtaskName, toolName, errorMessage, retryCount, maxRetries, objective, executionContext)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	resp, err := c.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:       c.model,
			Messages:    messages,
			Temperature: 0.5,
			ResponseFormat: &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			},
		},
	)
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
	systemPrompt := `You are an intelligent deadlock detection and recovery coordinator. When the same error repeats multiple times, you must detect deadlock and suggest recovery.

Available actions:
- "recover_state": Re-establish tool state (e.g., reopen browser, reconnect)
- "skip": Skip this operation and try alternative approach
- "adjust_approach": Use a different method to achieve the same goal
- "end_cycle": End current cycle and start fresh
- "end_task": End task if deadlock cannot be resolved

You must respond with valid JSON:
{
  "action": "string - one of: recover_state, skip, adjust_approach, end_cycle, end_task",
  "reasoning": "string - detailed explanation",
  "recovery_steps": ["string"] - optional steps to recover
}`

	userPrompt := fmt.Sprintf(`Deadlock detected - same error repeating:

Subtask: %s
Tool: %s
Error: %s
Consecutive Failures: %d
Objective: %s

Execution Context:
%s

This error has repeated %d times. This indicates a deadlock situation.
What should we do to recover?
- Recover tool state if state was lost?
- Skip and try alternative approach?
- Adjust the approach entirely?
- End cycle and start fresh?
- End task if recovery is impossible?`, subtaskName, toolName, errorMessage, consecutiveFailures, objective, executionContext, consecutiveFailures)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}

	resp, err := c.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:       c.model,
			Messages:    messages,
			Temperature: 0.5,
			ResponseFormat: &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			},
		},
	)
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

