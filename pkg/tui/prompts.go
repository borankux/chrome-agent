package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// PromptModel handles interactive prompts for LLM decisions
type PromptModel struct {
	active      bool
	promptType  string // "retry_exhaustion", "deadlock", "loop_completion", "state_recovery"
	context     string
	options     []string
	selected    int
	callback    func(string)
	width       int
	height      int
}

func NewPromptModel() PromptModel {
	return PromptModel{
		active:     false,
		options:    make([]string, 0),
		selected:   0,
		width:      80,
		height:     10,
	}
}

func (m PromptModel) Update(msg interface{}) (PromptModel, tea.Cmd) {
	switch msg := msg.(type) {
	case LLMPromptMsg:
		m.active = true
		m.promptType = msg.Type
		m.context = msg.Context
		m.options = msg.Options
		m.selected = 0
		m.callback = msg.Callback
		return m, nil
		
	case tea.KeyMsg:
		if !m.active {
			return m, nil
		}
		
		switch msg.String() {
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
			return m, nil
		case "down", "j":
			if m.selected < len(m.options)-1 {
				m.selected++
			}
			return m, nil
		case "enter":
			if m.callback != nil && m.selected < len(m.options) {
				selectedOption := m.options[m.selected]
				m.callback(selectedOption)
			}
			m.active = false
			return m, nil
		case "esc":
			m.active = false
			return m, nil
		}
		
	case ResizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		
	case PromptDismissMsg:
		m.active = false
		return m, nil
	}
	
	return m, nil
}

func (m PromptModel) View() string {
	if !m.active {
		return ""
	}
	
	lines := []string{}
	
	// Title based on prompt type
	title := m.getTitle()
	lines = append(lines, warningStyle.Bold(true).Render(title))
	lines = append(lines, "")
	
	// Context
	if m.context != "" {
		contextLines := strings.Split(m.context, "\n")
		for _, line := range contextLines {
			if len(line) > m.width-4 {
				// Wrap long lines
				words := strings.Fields(line)
				currentLine := ""
				for _, word := range words {
					if len(currentLine)+len(word)+1 > m.width-4 {
						lines = append(lines, "  "+currentLine)
						currentLine = word
					} else {
						if currentLine != "" {
							currentLine += " "
						}
						currentLine += word
					}
				}
				if currentLine != "" {
					lines = append(lines, "  "+currentLine)
				}
			} else {
				lines = append(lines, "  "+line)
			}
		}
		lines = append(lines, "")
	}
	
	// Options
	lines = append(lines, infoStyle.Render("Select an option:"))
	for i, option := range m.options {
		prefix := "  "
		if i == m.selected {
			prefix = successStyle.Render("▶ ")
		} else {
			prefix = "  "
		}
		optionText := option
		if i == m.selected {
			optionText = successStyle.Render(optionText)
		}
		lines = append(lines, prefix+optionText)
	}
	
	lines = append(lines, "")
	lines = append(lines, subtleStyle.Render("↑/↓: Navigate | Enter: Select | Esc: Cancel"))
	
	content := strings.Join(lines, "\n")
	return panelStyle.
		Width(m.width - 4).
		Height(m.height).
		BorderForeground(warningColor).
		Render(content)
}

func (m PromptModel) getTitle() string {
	switch m.promptType {
	case "retry_exhaustion":
		return "⚠ Retry Limit Exceeded - What should we do?"
	case "deadlock":
		return "⚠ Deadlock Detected - How should we proceed?"
	case "loop_completion":
		return "❓ Loop Completion - Should we continue?"
	case "state_recovery":
		return "⚠ Tool State Lost - Recover or skip?"
	default:
		return "❓ Decision Required"
	}
}

// PromptDismissMsg dismisses the current prompt
type PromptDismissMsg struct{}

