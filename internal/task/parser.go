package task

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"chrome-agent/internal/llm"
	"chrome-agent/internal/mcp"
	"chrome-agent/pkg/logger"
)

// TaskSpec represents a parsed task specification
type TaskSpec struct {
	Objective      string
	SubtaskRules   []SubtaskRule
	LoopCondition  *LoopCondition
	ExceptionRules []ExceptionRule
}

// SubtaskRule defines how to execute a subtask
type SubtaskRule struct {
	Name        string
	Description string
	Steps       []string
}

// LoopCondition defines loop requirements
type LoopCondition struct {
	Type        string  // "time" or "quota"
	TargetValue float64 // duration in seconds or quota count
	Description string
}

// ExceptionRule defines exception handling
type ExceptionRule struct {
	Pattern           string
	RequiresIntervention bool
	Retryable         bool
	MaxRetries        int
}

// ParseTaskFile reads and parses task.txt
func ParseTaskFile(path string) (*TaskSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read task file: %w", err)
	}

	return ParseTask(string(data))
}

// ParseTask parses task specification from text
func ParseTask(text string) (*TaskSpec, error) {
	spec := &TaskSpec{
		SubtaskRules:   make([]SubtaskRule, 0),
		ExceptionRules: make([]ExceptionRule, 0),
	}

	lines := strings.Split(text, "\n")
	var currentSection string
	var currentRule *SubtaskRule

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Detect sections
		if strings.HasPrefix(strings.ToLower(line), "objective:") {
			spec.Objective = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "objective:"))
			currentSection = "objective"
			continue
		}

		if strings.HasPrefix(strings.ToLower(line), "loop:") {
			currentSection = "loop"
			loopText := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "loop:"))
			condition, err := parseLoopCondition(loopText)
			if err != nil {
				return nil, fmt.Errorf("line %d: failed to parse loop condition: %w", i+1, err)
			}
			spec.LoopCondition = condition
			continue
		}

		if strings.HasPrefix(strings.ToLower(line), "subtask:") {
			if currentRule != nil {
				spec.SubtaskRules = append(spec.SubtaskRules, *currentRule)
			}
			currentRule = &SubtaskRule{
				Name:        strings.TrimSpace(strings.TrimPrefix(line, "subtask:")),
				Description: "",
				Steps:       make([]string, 0),
			}
			currentSection = "subtask"
			continue
		}

		if strings.HasPrefix(strings.ToLower(line), "exception:") {
			currentSection = "exception"
			rule, err := parseExceptionRule(strings.TrimSpace(strings.TrimPrefix(line, "exception:")))
			if err != nil {
				return nil, fmt.Errorf("line %d: failed to parse exception rule: %w", i+1, err)
			}
			spec.ExceptionRules = append(spec.ExceptionRules, rule)
			continue
		}

		// Process content based on current section
		switch currentSection {
		case "objective":
			if spec.Objective == "" {
				spec.Objective = line
			} else {
				spec.Objective += " " + line
			}
		case "subtask":
			if currentRule != nil {
				if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*") {
					step := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "*"))
					currentRule.Steps = append(currentRule.Steps, step)
				} else if currentRule.Description == "" {
					currentRule.Description = line
				} else {
					currentRule.Description += " " + line
				}
			}
		}
	}

	// Add last rule if exists
	if currentRule != nil {
		spec.SubtaskRules = append(spec.SubtaskRules, *currentRule)
	}

	// If no objective found, use first non-empty line
	if spec.Objective == "" {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				spec.Objective = line
				break
			}
		}
	}

	return spec, nil
}

func parseLoopCondition(text string) (*LoopCondition, error) {
	condition := &LoopCondition{}

	// Try to parse time-based loop (e.g., "for 1 hour", "for 30 minutes")
	timePattern := regexp.MustCompile(`(?i)for\s+(\d+)\s*(hour|hours|minute|minutes|min|hr|h|m)`)
	matches := timePattern.FindStringSubmatch(text)
	if len(matches) > 0 {
		value, err := strconv.ParseFloat(matches[1], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid time value: %s", matches[1])
		}

		unit := strings.ToLower(matches[2])
		var seconds float64
		switch {
		case strings.HasPrefix(unit, "h"):
			seconds = value * 3600
		case strings.HasPrefix(unit, "m"):
			seconds = value * 60
		default:
			seconds = value * 60
		}

		condition.Type = "time"
		condition.TargetValue = seconds
		condition.Description = text
		return condition, nil
	}

	// Try to parse quota-based loop (e.g., "find 10 items", "collect 5 creators")
	quotaPattern := regexp.MustCompile(`(?i)(find|collect|get|gather)\s+(\d+)\s+`)
	matches = quotaPattern.FindStringSubmatch(text)
	if len(matches) > 0 {
		value, err := strconv.ParseFloat(matches[2], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid quota value: %s", matches[2])
		}

		condition.Type = "quota"
		condition.TargetValue = value
		condition.Description = text
		return condition, nil
	}

	// Default: try to extract number as quota
	numPattern := regexp.MustCompile(`\d+`)
	matches = numPattern.FindStringSubmatch(text)
	if len(matches) > 0 {
		value, err := strconv.ParseFloat(matches[0], 64)
		if err == nil {
			condition.Type = "quota"
			condition.TargetValue = value
			condition.Description = text
			return condition, nil
		}
	}

	return nil, fmt.Errorf("could not parse loop condition: %s", text)
}

func parseExceptionRule(text string) (ExceptionRule, error) {
	rule := ExceptionRule{
		RequiresIntervention: false,
		Retryable:            true,
		MaxRetries:           3,
	}

	parts := strings.Split(text, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			rule.Pattern = part
			continue
		}

		key := strings.TrimSpace(strings.ToLower(kv[0]))
		value := strings.TrimSpace(kv[1])

		switch key {
		case "pattern":
			rule.Pattern = value
		case "intervention":
			rule.RequiresIntervention = strings.ToLower(value) == "true" || value == "1"
		case "retryable":
			rule.Retryable = strings.ToLower(value) == "true" || value == "1"
		case "maxretries":
			if v, err := strconv.Atoi(value); err == nil {
				rule.MaxRetries = v
			}
		}
	}

	if rule.Pattern == "" {
		rule.Pattern = text
	}

	return rule, nil
}

// FormatDuration formats seconds as human-readable duration
func FormatDuration(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%.0f seconds", seconds)
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1f minutes", seconds/60)
	}
	return fmt.Sprintf("%.1f hours", seconds/3600)
}

// ParseTaskOrPlan parses task file and uses LLM planning if no subtasks are found
func ParseTaskOrPlan(path string, llmCoord *llm.Coordinator, mcpClient *mcp.Client, log *logger.Logger) (*TaskSpec, error) {
	// First, try to parse the task file
	taskSpec, err := ParseTaskFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse task file: %w", err)
	}

	// If subtasks are already defined, use the parsed spec
	if len(taskSpec.SubtaskRules) > 0 {
		log.Info("Found %d manually defined subtasks, using parsed task specification", len(taskSpec.SubtaskRules))
		return taskSpec, nil
	}

	// If no subtasks found but we have an objective, use LLM planning
	if taskSpec.Objective == "" {
		return nil, fmt.Errorf("no objective found in task file and no subtasks defined")
	}

	log.Info("No subtasks found in task file, using LLM to plan task from objective")
	
	// Create planner and plan the task
	planner := NewPlanner(llmCoord, mcpClient, log)
	plannedSpec, err := planner.PlanTask(taskSpec.Objective)
	if err != nil {
		log.Warn("LLM planning failed: %v, falling back to parsed task specification", err)
		// Return the original spec (may have loop condition or exception rules)
		return taskSpec, nil
	}

	// Merge any manually defined loop conditions or exception rules from the parsed file
	if taskSpec.LoopCondition != nil && plannedSpec.LoopCondition == nil {
		plannedSpec.LoopCondition = taskSpec.LoopCondition
		log.Info("Using manually defined loop condition from task file")
	}

	if len(taskSpec.ExceptionRules) > 0 {
		// Merge exception rules (manual ones take precedence)
		plannedSpec.ExceptionRules = append(taskSpec.ExceptionRules, plannedSpec.ExceptionRules...)
		log.Info("Merged %d manually defined exception rules", len(taskSpec.ExceptionRules))
	}

	return plannedSpec, nil
}

