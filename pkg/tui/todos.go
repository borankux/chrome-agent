package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// TodoModel displays the todo list
type TodoModel struct {
	todos []*TodoItem
	width int
	height int
	scrollOffset int
}

// NewTodoModel creates a new todo model
func NewTodoModel() TodoModel {
	return TodoModel{
		todos: make([]*TodoItem, 0),
		width: 80,
		height: 10,
		scrollOffset: 0,
	}
}

// Update handles messages for the todo model
func (m TodoModel) Update(msg interface{}) (TodoModel, interface{}) {
	switch msg := msg.(type) {
	case TodoUpdateMsg:
		m.todos = msg.Todos
		// Reset scroll when todos are updated
		m.scrollOffset = 0
	case ResizeMsg:
		m.width = msg.Width
	}
	return m, nil
}

// View renders the todo list
func (m TodoModel) View() string {
	if len(m.todos) == 0 {
		return ""
	}

	lines := []string{}
	lines = append(lines, todoHeaderStyle.Render("Todo List"))
	
	// Calculate visible todos based on scroll offset
	maxVisible := m.height - 2 // Reserve space for header and border
	if maxVisible < 1 {
		maxVisible = 1
	}
	
	startIdx := m.scrollOffset
	endIdx := startIdx + maxVisible
	if endIdx > len(m.todos) {
		endIdx = len(m.todos)
	}
	
	for i := startIdx; i < endIdx; i++ {
		todo := m.todos[i]
		lines = append(lines, m.renderTodoItem(i+1, todo))
	}
	
	// Show scroll indicator if needed
	if len(m.todos) > maxVisible {
		if m.scrollOffset > 0 {
			lines = append([]string{lines[0], subtleStyle.Render("↑ More above...")}, lines[1:]...)
		}
		if endIdx < len(m.todos) {
			lines = append(lines, subtleStyle.Render("↓ More below..."))
		}
	}
	
	content := strings.Join(lines, "\n")
	return panelStyle.Width(m.width - 2).Height(m.height).Render(content)
}

// renderTodoItem renders a single todo item with color coding
func (m TodoModel) renderTodoItem(index int, todo *TodoItem) string {
	var statusIcon string
	var statusStyle lipgloss.Style
	
	switch todo.Status {
	case "pending":
		statusIcon = "[ ]"
		statusStyle = subtleStyle
	case "in_progress":
		statusIcon = "[→]"
		statusStyle = infoStyle
	case "completed":
		statusIcon = "[✓]"
		statusStyle = successStyle
	case "failed":
		statusIcon = "[✗]"
		statusStyle = errorStyle
	default:
		statusIcon = "[ ]"
		statusStyle = subtleStyle
	}
	
	// Format: "  [✓] 1. Task Name"
	icon := statusStyle.Render(statusIcon)
	taskNum := fmt.Sprintf("%d.", index)
	taskName := todo.Name
	
	// Truncate task name if too long
	maxNameLen := m.width - 15 // Reserve space for icon, number, and padding
	if len(taskName) > maxNameLen {
		taskName = taskName[:maxNameLen-3] + "..."
	}
	
	line := fmt.Sprintf("  %s %s %s", icon, taskNum, taskName)
	
	// Add tool name if available
	if todo.ToolName != "" {
		toolInfo := fmt.Sprintf(" (%s)", todo.ToolName)
		if len(line)+len(toolInfo) <= m.width-4 {
			line += subtleStyle.Render(toolInfo)
		}
	}
	
	return statusStyle.Render(line)
}

// ScrollUp scrolls the todo list up
func (m TodoModel) ScrollUp() TodoModel {
	if m.scrollOffset > 0 {
		m.scrollOffset--
	}
	return m
}

// ScrollDown scrolls the todo list down
func (m TodoModel) ScrollDown() TodoModel {
	maxVisible := m.height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	if m.scrollOffset+maxVisible < len(m.todos) {
		m.scrollOffset++
	}
	return m
}

// ScrollToTop scrolls to the top
func (m TodoModel) ScrollToTop() TodoModel {
	m.scrollOffset = 0
	return m
}

// ScrollToBottom scrolls to the bottom
func (m TodoModel) ScrollToBottom() TodoModel {
	maxVisible := m.height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	if len(m.todos) > maxVisible {
		m.scrollOffset = len(m.todos) - maxVisible
	} else {
		m.scrollOffset = 0
	}
	return m
}

// Count returns the number of todos
func (m TodoModel) Count() int {
	return len(m.todos)
}

// todoHeaderStyle is the style for the todo header
var todoHeaderStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(infoColor).
	Padding(0, 1)

