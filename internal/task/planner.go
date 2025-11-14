package task

import (
	"encoding/json"
	"fmt"
	"strings"

	"chrome-agent/internal/llm"
	"chrome-agent/internal/mcp"
	"chrome-agent/pkg/logger"
)

// PlanResult represents the structured plan returned by LLM
type PlanResult struct {
	Objective      string                 `json:"objective"`
	LoopCondition  *PlanLoopCondition     `json:"loop_condition,omitempty"`
	Tasks          []PlanTask             `json:"tasks"`
	ExceptionRules []PlanExceptionRule    `json:"exception_rules,omitempty"`
}

// PlanLoopCondition represents loop condition in plan
type PlanLoopCondition struct {
	Type        string  `json:"type"`         // "time", "quota", or null/empty
	TargetValue float64 `json:"target_value"` // duration in seconds or quota count
	Description string  `json:"description"`
}

// PlanTask represents a task in the plan
type PlanTask struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	ToolName     string                 `json:"tool_name"`     // The specific MCP tool to call
	Arguments    map[string]interface{} `json:"arguments"`     // Arguments for the tool call
	Steps        []string               `json:"steps,omitempty"` // Optional step descriptions
	RequiresLoop bool                   `json:"requires_loop,omitempty"`
}

// PlanExceptionRule represents exception rule in plan
type PlanExceptionRule struct {
	Pattern             string `json:"pattern"`
	RequiresIntervention bool  `json:"requires_intervention"`
	Retryable           bool   `json:"retryable"`
	MaxRetries          int    `json:"max_retries"`
}

// Planner handles LLM-based task planning
type Planner struct {
	llmCoord   *llm.Coordinator
	mcpClient  *mcp.Client
	logger     *logger.Logger
	lastPlan   *PlanResult // Store last plan result for todo generation
}

// NewPlanner creates a new planner
func NewPlanner(llmCoord *llm.Coordinator, mcpClient *mcp.Client, log *logger.Logger) *Planner {
	return &Planner{
		llmCoord:  llmCoord,
		mcpClient: mcpClient,
		logger:    log,
	}
}

// PlanTask uses LLM to plan a task from a free-form objective
func (p *Planner) PlanTask(objective string) (*TaskSpec, error) {
	p.logger.Info("Planning task using LLM...")
	p.logger.Debug("Objective: %s", objective)

	// Get available tools from MCP
	p.logger.Debug("Checking MCP tool availability...")
	tools, err := p.mcpClient.ListTools()
	if err != nil {
		return nil, fmt.Errorf("failed to list tools for planning: %w", err)
	}

	if len(tools) == 0 {
		return nil, fmt.Errorf("MCP server has no available tools - cannot proceed with planning")
	}

	p.logger.Info("Found %d available tools from MCP server", len(tools))
	p.logger.Debug("Available tools:")
	for i, tool := range tools {
		p.logger.Debug("  %d. %s: %s", i+1, tool.Name, tool.Description)
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

	// Call LLM to plan the task
	planJSON, err := p.llmCoord.PlanTask(objective, toolsInterface)
	if err != nil {
		return nil, fmt.Errorf("LLM planning failed: %w", err)
	}

	// Parse the plan JSON
	var planResult PlanResult
	if err := json.Unmarshal([]byte(planJSON), &planResult); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w", err)
	}

	// Validate plan
	if planResult.Objective == "" {
		return nil, fmt.Errorf("LLM plan missing objective")
	}
	if len(planResult.Tasks) == 0 {
		return nil, fmt.Errorf("LLM plan contains no tasks")
	}

	// Validate that each task has a tool_name
	for i, task := range planResult.Tasks {
		if task.ToolName == "" {
			return nil, fmt.Errorf("task %d (%s) is missing tool_name - each task must specify exactly one MCP tool", i+1, task.Name)
		}
	}

	// Store plan result for todo generation
	p.lastPlan = &planResult

	// Convert plan result to TaskSpec
	spec := p.convertPlanToTaskSpec(&planResult)
	
	p.logger.Info("Planning completed successfully")
	p.logger.Info("Generated %d subtasks:", len(spec.SubtaskRules))
	for i, rule := range spec.SubtaskRules {
		p.logger.Info("  %d. %s: %s", i+1, rule.Name, rule.Description)
		if len(rule.Steps) > 0 {
			for j, step := range rule.Steps {
				p.logger.Debug("     Step %d: %s", j+1, step)
			}
		}
	}
	if spec.LoopCondition != nil {
		p.logger.Info("Loop condition detected: %s (type: %s, target: %.0f)", 
			spec.LoopCondition.Description, 
			spec.LoopCondition.Type, 
			spec.LoopCondition.TargetValue)
	}
	if len(spec.ExceptionRules) > 0 {
		p.logger.Info("Exception rules: %d", len(spec.ExceptionRules))
	}

	return spec, nil
}

// GetTodos generates todo items from the last plan
func (p *Planner) GetTodos() []*PlanTodo {
	if p.lastPlan == nil {
		return nil
	}
	return GenerateTodosFromPlan(p.lastPlan)
}

// GenerateTodosFromPlan generates todo items from a plan result
func GenerateTodosFromPlan(plan *PlanResult) []*PlanTodo {
	todos := make([]*PlanTodo, 0, len(plan.Tasks))
	for i, task := range plan.Tasks {
		todo := &PlanTodo{
			ID:          i + 1,
			Name:        task.Name,
			Description: task.Description,
			Status:      "pending",
			ToolName:    task.ToolName,
		}
		todos = append(todos, todo)
	}
	return todos
}

// PlanTodo represents a todo item generated from a plan (before conversion to TUI format)
type PlanTodo struct {
	ID          int
	Name        string
	Description string
	Status      string // "pending", "in_progress", "completed", "failed"
	ToolName    string
}

// convertPlanToTaskSpec converts PlanResult to TaskSpec
func (p *Planner) convertPlanToTaskSpec(plan *PlanResult) *TaskSpec {
	spec := &TaskSpec{
		Objective:      plan.Objective,
		SubtaskRules:   make([]SubtaskRule, 0, len(plan.Tasks)),
		ExceptionRules: make([]ExceptionRule, 0),
	}

	// Convert loop condition
	if plan.LoopCondition != nil && plan.LoopCondition.Type != "" && 
		plan.LoopCondition.Type != "null" && strings.ToLower(plan.LoopCondition.Type) != "null" {
		spec.LoopCondition = &LoopCondition{
			Type:        plan.LoopCondition.Type,
			TargetValue: plan.LoopCondition.TargetValue,
			Description: plan.LoopCondition.Description,
		}
	}

	// Convert tasks to subtask rules
	for _, task := range plan.Tasks {
		// Build description that includes tool information
		description := task.Description
		if task.ToolName != "" {
			description = fmt.Sprintf("Tool: %s\n%s", task.ToolName, description)
		}
		
		// Store tool name and arguments in steps for executor to use
		steps := make([]string, 0)
		if task.ToolName != "" {
			steps = append(steps, fmt.Sprintf("TOOL:%s", task.ToolName))
			if len(task.Arguments) > 0 {
				argsJSON, _ := json.Marshal(task.Arguments)
				steps = append(steps, fmt.Sprintf("ARGS:%s", string(argsJSON)))
			}
		}
		// Add any additional step descriptions
		steps = append(steps, task.Steps...)
		
		rule := SubtaskRule{
			Name:        task.Name,
			Description: description,
			Steps:       steps,
		}
		spec.SubtaskRules = append(spec.SubtaskRules, rule)
	}

	// Convert exception rules
	for _, er := range plan.ExceptionRules {
		rule := ExceptionRule{
			Pattern:             er.Pattern,
			RequiresIntervention: er.RequiresIntervention,
			Retryable:           er.Retryable,
			MaxRetries:          er.MaxRetries,
		}
		if rule.MaxRetries == 0 {
			rule.MaxRetries = 3 // Default
		}
		spec.ExceptionRules = append(spec.ExceptionRules, rule)
	}

	return spec
}

