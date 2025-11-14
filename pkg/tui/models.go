package tui

import (
	"time"
)

// TaskState represents the current state of task execution
type TaskState struct {
	Objective      string
	Status        string // "running", "completed", "failed", "paused"
	ElapsedTime   time.Duration
	IsLoopable    bool
	LoopType      string // "time", "quota", ""
	LoopProgress  float64
	LoopTarget    float64
	CycleNumber   int
	CurrentSubtask string
	CurrentTool   string
	ToolStatus    string // "success", "failed", "running"
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time
	Level     string // "DEBUG", "INFO", "WARN", "ERROR", "TOOL"
	Prefix    string
	Message   string
}

// Stats represents execution statistics
type Stats struct {
	TotalTokens    int
	PromptTokens   int
	CompletionTokens int
	ToolCalls      int
	SuccessCount   int
	FailureCount   int
	LLMCalls       int
}

// ErrorInfo represents error information
type ErrorInfo struct {
	Message     string
	Tool        string
	Subtask     string
	RetryCount  int
	MaxRetries  int
	IsDeadlock  bool
	Timestamp   time.Time
}

// Update messages for Bubble Tea
type (
	TaskUpdateMsg struct {
		State *TaskState
	}
	
	LogMsg struct {
		Entry *LogEntry
	}
	
	ProgressUpdateMsg struct {
		Progress float64
		Cycle    int
	}
	
	ErrorMsg struct {
		Error *ErrorInfo
	}
	
	StatsUpdateMsg struct {
		Stats *Stats
	}
	
	ExecutionUpdateMsg struct {
		Subtask string
		Tool    string
		Status  string
	}
	
	LLMPromptMsg struct {
		Type        string // "retry_exhaustion", "deadlock", "loop_completion", "state_recovery"
		Context     string
		Options     []string
		Callback    func(string) // Callback for user decision
	}
	
	ResizeMsg struct {
		Width  int
		Height int
	}
	
	TodoUpdateMsg struct {
		Todos []*TodoItem
	}
)

// TodoItem represents a single todo item
type TodoItem struct {
	ID          int
	Name        string
	Description string
	Status      string // "pending", "in_progress", "completed", "failed"
	ToolName    string
}

// TodoList represents a collection of todos
type TodoList struct {
	Items []*TodoItem
}

