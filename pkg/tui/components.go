package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// HeaderModel displays task header
type HeaderModel struct {
	state *TaskState
	width int
}

func NewHeaderModel() HeaderModel {
	return HeaderModel{
		state: &TaskState{},
		width: 80,
	}
}

func (m HeaderModel) Update(msg interface{}) (HeaderModel, interface{}) {
	switch msg := msg.(type) {
	case TaskUpdateMsg:
		m.state = msg.State
	case ResizeMsg:
		m.width = msg.Width
	}
	return m, nil
}

func (m HeaderModel) View() string {
	if m.state == nil {
		return headerStyle.Render("Chrome Agent - Initializing...")
	}
	
	status := m.state.Status
	statusStyle := infoStyle
	switch status {
	case "completed":
		statusStyle = successStyle
	case "failed":
		statusStyle = errorStyle
	case "paused":
		statusStyle = warningStyle
	}
	
	title := fmt.Sprintf("Chrome Agent - Task: %s", m.state.Objective)
	statusText := statusStyle.Render(fmt.Sprintf("[%s]", strings.ToUpper(status)))
	elapsed := fmt.Sprintf("Time: %s", formatDuration(m.state.ElapsedTime))
	
	return headerStyle.Width(m.width - 2).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			title,
			strings.Repeat(" ", max(0, m.width-len(title)-len(statusText)-len(elapsed)-10)),
			statusText,
			" ",
			elapsed,
		),
	)
}

// ProgressModel displays loop progress
type ProgressModel struct {
	state *TaskState
	width int
}

func NewProgressModel() ProgressModel {
	return ProgressModel{
		state: &TaskState{},
		width: 80,
	}
}

func (m ProgressModel) Update(msg interface{}) (ProgressModel, interface{}) {
	switch msg := msg.(type) {
	case TaskUpdateMsg:
		m.state = msg.State
	case ResizeMsg:
		m.width = msg.Width
	}
	return m, nil
}

func (m ProgressModel) View() string {
	if m.state == nil || !m.state.IsLoopable {
		return ""
	}
	
	var progressBar string
	var progressText string
	
	if m.state.LoopType == "time" {
		progress := m.state.LoopProgress / m.state.LoopTarget
		if progress > 1.0 {
			progress = 1.0
		}
		progressBar = renderProgressBar(progress, 30)
		progressText = fmt.Sprintf("%.0f%%", progress*100)
	} else if m.state.LoopType == "quota" {
		progress := m.state.LoopProgress / m.state.LoopTarget
		if progress > 1.0 {
			progress = 1.0
		}
		progressBar = renderProgressBar(progress, 30)
		progressText = fmt.Sprintf("%.0f / %.0f", m.state.LoopProgress, m.state.LoopTarget)
	}
	
	cycleText := fmt.Sprintf("Cycle: %d", m.state.CycleNumber)
	
	return panelStyle.Width(m.width - 2).Render(
		fmt.Sprintf("Progress: %s %s | %s", progressBar, progressText, cycleText),
	)
}

// ExecutionModel displays current execution status
type ExecutionModel struct {
	state *TaskState
	width int
}

func NewExecutionModel() ExecutionModel {
	return ExecutionModel{
		state: &TaskState{},
		width: 80,
	}
}

func (m ExecutionModel) Update(msg interface{}) (ExecutionModel, interface{}) {
	switch msg := msg.(type) {
	case TaskUpdateMsg, ExecutionUpdateMsg:
		if taskMsg, ok := msg.(TaskUpdateMsg); ok {
			m.state = taskMsg.State
		} else if execMsg, ok := msg.(ExecutionUpdateMsg); ok {
			if m.state == nil {
				m.state = &TaskState{}
			}
			m.state.CurrentSubtask = execMsg.Subtask
			m.state.CurrentTool = execMsg.Tool
			m.state.ToolStatus = execMsg.Status
		}
	case ResizeMsg:
		m.width = msg.Width
	}
	return m, nil
}

func (m ExecutionModel) View() string {
	if m.state == nil || m.state.CurrentSubtask == "" {
		return ""
	}
	
	var statusBadge string
	switch m.state.ToolStatus {
	case "success":
		statusBadge = successBadge + " Success"
	case "failed":
		statusBadge = errorBadge + " Failed"
	case "running":
		statusBadge = runningBadge + " Running"
	default:
		statusBadge = infoStyle.Render("Pending")
	}
	
	lines := []string{
		fmt.Sprintf("▶ Executing: %s", m.state.CurrentSubtask),
	}
	
	if m.state.CurrentTool != "" {
		lines = append(lines, fmt.Sprintf("  Tool: %s", m.state.CurrentTool))
	}
	lines = append(lines, fmt.Sprintf("  Status: %s", statusBadge))
	
	return panelStyle.Width(m.width - 2).Render(strings.Join(lines, "\n"))
}

// StatsModel displays execution statistics
type StatsModel struct {
	stats *Stats
	width int
}

func NewStatsModel() StatsModel {
	return StatsModel{
		stats: &Stats{},
		width: 80,
	}
}

func (m StatsModel) Update(msg interface{}) (StatsModel, interface{}) {
	switch msg := msg.(type) {
	case StatsUpdateMsg:
		m.stats = msg.Stats
	case ResizeMsg:
		m.width = msg.Width
	}
	return m, nil
}

func (m StatsModel) View() string {
	if m.stats == nil {
		return ""
	}
	
	text := fmt.Sprintf("Stats: Tokens: %d | Tools: %d | Success: %d | Failed: %d | LLM Calls: %d",
		m.stats.TotalTokens,
		m.stats.ToolCalls,
		m.stats.SuccessCount,
		m.stats.FailureCount,
		m.stats.LLMCalls,
	)
	
	return panelStyle.Width(m.width - 2).Render(text)
}

// Helper functions
func renderProgressBar(progress float64, width int) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	
	filled := int(progress * float64(width))
	empty := width - filled
	
	bar := strings.Repeat(progressBarFull, filled) + strings.Repeat(progressBarEmpty, empty)
	return successStyle.Render(bar[:filled]) + subtleStyle.Render(bar[filled:])
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

