package tui

import (
	"fmt"
	"strings"
)

// LogsModel displays scrollable logs
type LogsModel struct {
	entries    []*LogEntry
	maxEntries int
	scrollPos  int
	width      int
	height     int
	filterLevel string // "ALL", "DEBUG", "INFO", "WARN", "ERROR"
	autoScroll  bool
}

func NewLogsModel() LogsModel {
	return LogsModel{
		entries:     make([]*LogEntry, 0),
		maxEntries:  1000,
		scrollPos:   0,
		width:       80,
		height:      10,
		filterLevel: "ALL",
		autoScroll:  true,
	}
}

func (m LogsModel) Update(msg interface{}) (LogsModel, interface{}) {
	switch msg := msg.(type) {
	case LogMsg:
		m.entries = append(m.entries, msg.Entry)
		if len(m.entries) > m.maxEntries {
			m.entries = m.entries[len(m.entries)-m.maxEntries:]
		}
		if m.autoScroll {
			m.scrollPos = len(m.entries) - m.height
			if m.scrollPos < 0 {
				m.scrollPos = 0
			}
		}
	case ResizeMsg:
		m.width = msg.Width
		m.height = msg.Height / 4 // Logs take about 1/4 of screen
		if m.height < 5 {
			m.height = 5
		}
	}
	return m, nil
}

func (m LogsModel) View() string {
	filtered := m.getFilteredEntries()
	visible := m.getVisibleEntries(filtered)
	
	lines := []string{"Logs:"}
	
	for _, entry := range visible {
		lines = append(lines, m.formatLogEntry(entry))
	}
	
	// Add scroll indicator
	if len(filtered) > len(visible) {
		if m.scrollPos > 0 {
			lines = append([]string{subtleStyle.Render("↑ More logs above")}, lines...)
		}
		if m.scrollPos+len(visible) < len(filtered) {
			lines = append(lines, subtleStyle.Render("↓ More logs below"))
		}
	}
	
	content := strings.Join(lines, "\n")
	return panelStyle.Width(m.width - 2).Height(m.height).Render(content)
}

func (m LogsModel) getFilteredEntries() []*LogEntry {
	if m.filterLevel == "ALL" {
		return m.entries
	}
	
	filtered := make([]*LogEntry, 0)
	for _, entry := range m.entries {
		if entry.Level == m.filterLevel {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (m LogsModel) getVisibleEntries(entries []*LogEntry) []*LogEntry {
	start := m.scrollPos
	end := start + m.height - 2 // Reserve space for header and scroll indicator
	
	if start < 0 {
		start = 0
	}
	if end > len(entries) {
		end = len(entries)
	}
	if start >= len(entries) {
		return []*LogEntry{}
	}
	
	return entries[start:end]
}

func (m LogsModel) formatLogEntry(entry *LogEntry) string {
	timestamp := entry.Timestamp.Format("15:04:05")
	levelStyle := GetLevelStyle(entry.Level)
	levelText := levelStyle.Render(fmt.Sprintf("%-5s", entry.Level))
	
	prefix := ""
	if entry.Prefix != "" {
		prefix = subtleStyle.Render(fmt.Sprintf("[%s] ", entry.Prefix))
	}
	
	return fmt.Sprintf("[%s] %s%s %s",
		subtleStyle.Render(timestamp),
		prefix,
		levelText,
		entry.Message,
	)
}

func (m LogsModel) ScrollUp() LogsModel {
	if m.scrollPos > 0 {
		m.scrollPos--
		m.autoScroll = false
	}
	return m
}

func (m LogsModel) ScrollDown() LogsModel {
	filtered := m.getFilteredEntries()
	if m.scrollPos < len(filtered)-m.height+2 {
		m.scrollPos++
		m.autoScroll = false
	}
	return m
}

func (m LogsModel) ScrollToBottom() LogsModel {
	filtered := m.getFilteredEntries()
	m.scrollPos = len(filtered) - m.height + 2
	if m.scrollPos < 0 {
		m.scrollPos = 0
	}
	m.autoScroll = true
	return m
}

