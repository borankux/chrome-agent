package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ErrorModel displays error information
type ErrorModel struct {
	errors      []*ErrorInfo
	maxErrors   int
	width       int
	height      int
	expanded    map[int]bool // Track which errors are expanded
	showPanel   bool
}

func NewErrorModel() ErrorModel {
	return ErrorModel{
		errors:    make([]*ErrorInfo, 0),
		maxErrors: 50,
		width:     80,
		height:    8,
		expanded:  make(map[int]bool),
		showPanel: false,
	}
}

func (m ErrorModel) Update(msg interface{}) (ErrorModel, interface{}) {
	switch msg := msg.(type) {
	case ErrorMsg:
		m.errors = append(m.errors, msg.Error)
		if len(m.errors) > m.maxErrors {
			m.errors = m.errors[len(m.errors)-m.maxErrors:]
		}
		m.showPanel = true
		// Auto-expand latest error
		m.expanded[len(m.errors)-1] = true
	case ResizeMsg:
		m.width = msg.Width
		m.height = msg.Height / 6 // Errors take about 1/6 of screen
		if m.height < 3 {
			m.height = 3
		}
	case tea.KeyMsg:
		if msg.String() == "e" {
			// Toggle error panel visibility
			m.showPanel = !m.showPanel
		}
	}
	return m, nil
}

func (m ErrorModel) View() string {
	if !m.showPanel || len(m.errors) == 0 {
		return ""
	}
	
	// Group errors by type
	grouped := m.groupErrors()
	
	lines := []string{"Errors:"}
	
	visibleCount := 0
	maxVisible := m.height - 2
	
	for i, group := range grouped {
		if visibleCount >= maxVisible {
			remaining := len(grouped) - i
			lines = append(lines, subtleStyle.Render(fmt.Sprintf("... and %d more error groups", remaining)))
			break
		}
		
		errorCount := len(group.Errors)
		latestError := group.Errors[len(group.Errors)-1]
		
		// Error header
		icon := errorBadge
		if latestError.IsDeadlock {
			icon = warningStyle.Render("⚠")
		}
		
		header := fmt.Sprintf("%s %s (%d occurrence%s)",
			icon,
			latestError.Message,
			errorCount,
			map[bool]string{true: "s", false: ""}[errorCount > 1],
		)
		
		lines = append(lines, errorStyle.Render(header))
		
		// Error details if expanded
		if m.expanded[i] {
			details := []string{
				fmt.Sprintf("  Tool: %s", latestError.Tool),
				fmt.Sprintf("  Subtask: %s", latestError.Subtask),
				fmt.Sprintf("  Retries: %d/%d", latestError.RetryCount, latestError.MaxRetries),
				fmt.Sprintf("  Time: %s", latestError.Timestamp.Format("15:04:05")),
			}
			if latestError.IsDeadlock {
				details = append(details, warningStyle.Render("  ⚠ Deadlock detected - same error repeating"))
			}
			lines = append(lines, details...)
		}
		
		visibleCount++
	}
	
	content := strings.Join(lines, "\n")
	return panelStyle.Width(m.width - 2).Height(m.height).Render(content)
}

// ErrorGroup represents a group of similar errors
type ErrorGroup struct {
	Message string
	Errors  []*ErrorInfo
}

func (m ErrorModel) groupErrors() []ErrorGroup {
	groups := make(map[string]*ErrorGroup)
	
	for _, err := range m.errors {
		key := m.getErrorKey(err)
		if group, exists := groups[key]; exists {
			group.Errors = append(group.Errors, err)
		} else {
			groups[key] = &ErrorGroup{
				Message: err.Message,
				Errors:  []*ErrorInfo{err},
			}
		}
	}
	
	// Convert to slice and sort by most recent
	result := make([]ErrorGroup, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	
	// Sort by latest timestamp (most recent first)
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			latestI := result[i].Errors[len(result[i].Errors)-1].Timestamp
			latestJ := result[j].Errors[len(result[j].Errors)-1].Timestamp
			if latestJ.After(latestI) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	
	return result
}

func (m ErrorModel) getErrorKey(err *ErrorInfo) string {
	// Group by error message and tool
	return fmt.Sprintf("%s:%s", err.Message, err.Tool)
}

func (m ErrorModel) ToggleExpand(index int) ErrorModel {
	m.expanded[index] = !m.expanded[index]
	return m
}

func (m ErrorModel) Clear() ErrorModel {
	m.errors = make([]*ErrorInfo, 0)
	m.expanded = make(map[int]bool)
	m.showPanel = false
	return m
}

