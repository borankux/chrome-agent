package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sashabaranov/go-openai"
)

// Coordinator handles LLM reasoning and tool coordination
type Coordinator struct {
	client   *openai.Client
	model    string
	messages []openai.ChatCompletionMessage
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
		client:   client,
		model:    "gpt-4o", // Use latest model
		messages: make([]openai.ChatCompletionMessage, 0),
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
	systemPrompt := `You are an intelligent agent coordinator that helps break down tasks and select appropriate tools.

Your job is to:
1. Understand the task description
2. Identify which tools from the available tools list should be used
3. Determine the sequence of tool calls needed
4. Provide reasoning for your decisions

Respond in JSON format with:
- reasoning: Your thought process
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

Analyze this task and determine which tools to use and in what order.`, taskDescription, string(toolsJSON))

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

// GetContext returns current conversation context
func (c *Coordinator) GetContext() []openai.ChatCompletionMessage {
	return c.messages
}

// Reset clears the conversation history
func (c *Coordinator) Reset() {
	c.messages = make([]openai.ChatCompletionMessage, 0)
}

