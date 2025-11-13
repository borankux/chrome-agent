package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	// Colors
	primaryColor   = lipgloss.Color("6")
	successColor   = lipgloss.Color("2")
	errorColor     = lipgloss.Color("1")
	warningColor   = lipgloss.Color("3")
	infoColor      = lipgloss.Color("4")
	subtleColor    = lipgloss.Color("8")
	
	// Styles
	headerStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(primaryColor).
		Padding(0, 1)
	
	successStyle = lipgloss.NewStyle().
		Foreground(successColor).
		Bold(true)
	
	errorStyle = lipgloss.NewStyle().
		Foreground(errorColor).
		Bold(true)
	
	warningStyle = lipgloss.NewStyle().
		Foreground(warningColor)
	
	infoStyle = lipgloss.NewStyle().
		Foreground(infoColor)
	
	subtleStyle = lipgloss.NewStyle().
		Foreground(subtleColor)
	
	// Panel styles
	panelStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(subtleColor).
		Padding(0, 1)
	
	// Status badges
	successBadge = successStyle.Render("✓")
	errorBadge   = errorStyle.Render("✗")
	runningBadge = infoStyle.Render("⏳")
	
	// Progress bar
	progressBarFull  = "█"
	progressBarEmpty = "░"
)

// GetLevelStyle returns the style for a log level
func GetLevelStyle(level string) lipgloss.Style {
	switch level {
	case "ERROR":
		return errorStyle
	case "WARN":
		return warningStyle
	case "INFO":
		return infoStyle
	case "TOOL":
		return successStyle
	default:
		return subtleStyle
	}
}

