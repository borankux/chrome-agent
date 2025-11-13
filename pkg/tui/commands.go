package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CommandModel handles keyboard shortcuts and commands
type CommandModel struct {
	showHelp    bool
	width       int
	height      int
}

func NewCommandModel() CommandModel {
	return CommandModel{
		showHelp: false,
		width:    80,
		height:   24,
	}
}

func (m CommandModel) Update(msg interface{}) (CommandModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		case "h":
			m.showHelp = !m.showHelp
			return m, nil
		}
	case ResizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m CommandModel) View() string {
	if !m.showHelp {
		return ""
	}
	
	commands := []struct {
		key     string
		action  string
	}{
		{"q, Ctrl+C", "Quit"},
		{"↑, ↓", "Scroll logs"},
		{"End", "Scroll to bottom of logs"},
		{"e", "Toggle error panel"},
		{"h, ?", "Show/hide help"},
		{"f", "Filter logs by level"},
		{"r", "Refresh view"},
	}
	
	lines := []string{headerStyle.Render("Keyboard Shortcuts")}
	lines = append(lines, "")
	
	maxKeyLen := 0
	for _, cmd := range commands {
		if len(cmd.key) > maxKeyLen {
			maxKeyLen = len(cmd.key)
		}
	}
	
	for _, cmd := range commands {
		key := subtleStyle.Render(fmt.Sprintf("%-*s", maxKeyLen+2, cmd.key))
		action := infoStyle.Render(cmd.action)
		lines = append(lines, fmt.Sprintf("  %s %s", key, action))
	}
	
	lines = append(lines, "")
	lines = append(lines, subtleStyle.Render("Press 'h' or '?' to close this help"))
	
	content := strings.Join(lines, "\n")
	
	// Center the help panel
	helpWidth := 50
	helpHeight := len(lines) + 2
	if helpWidth > m.width-4 {
		helpWidth = m.width - 4
	}
	
	helpPanel := panelStyle.
		Width(helpWidth).
		Height(helpHeight).
		BorderForeground(infoColor).
		Render(content)
	
	// Center vertically and horizontally
	verticalPadding := (m.height - helpHeight) / 2
	if verticalPadding < 0 {
		verticalPadding = 0
	}
	horizontalPadding := (m.width - helpWidth) / 2
	if horizontalPadding < 0 {
		horizontalPadding = 0
	}
	
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		helpPanel,
	)
}

// HandleCommand processes keyboard commands and returns appropriate messages
func HandleCommand(key string, model *Model) (tea.Model, tea.Cmd) {
	switch key {
	case "f":
		// Toggle log filter (cycle through levels)
		// This would need to be implemented in LogsModel
		return model, nil
	case "r":
		// Refresh view - no-op for now, could trigger a refresh
		return model, nil
	case "e":
		// Toggle error panel - handled in dashboard
		return model, nil
	}
	return model, nil
}

