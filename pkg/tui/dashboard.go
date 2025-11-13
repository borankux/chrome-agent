package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model represents the main TUI model
type Model struct {
	header    HeaderModel
	progress  ProgressModel
	execution ExecutionModel
	logs      LogsModel
	stats     StatsModel
	
	width     int
	height    int
	taskState *TaskState
	statsData *Stats
	
	// Channels for receiving updates
	logChan    chan *LogEntry
	updateChan chan interface{}
	quitChan   chan struct{}
}

// NewModel creates a new TUI model
func NewModel() *Model {
	return &Model{
		header:    NewHeaderModel(),
		progress:  NewProgressModel(),
		execution: NewExecutionModel(),
		logs:      NewLogsModel(),
		stats:     NewStatsModel(),
		width:     80,
		height:    24,
		taskState: &TaskState{},
		statsData: &Stats{},
		logChan:   make(chan *LogEntry, 100),
		updateChan: make(chan interface{}, 100),
		quitChan:   make(chan struct{}),
	}
}

// Init initializes the TUI
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.listenForUpdates(),
		m.listenForLogs(),
	)
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		resizeMsg := ResizeMsg{Width: msg.Width, Height: msg.Height}
		m.header, _ = m.header.Update(resizeMsg)
		m.progress, _ = m.progress.Update(resizeMsg)
		m.execution, _ = m.execution.Update(resizeMsg)
		m.stats, _ = m.stats.Update(resizeMsg)
		m.logs, _ = m.logs.Update(resizeMsg)
		return m, nil
		
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up":
			m.logs = m.logs.ScrollUp()
			return m, nil
		case "down":
			m.logs = m.logs.ScrollDown()
			return m, nil
		case "end":
			m.logs = m.logs.ScrollToBottom()
			return m, nil
		}
		
	case LogMsg:
		m.logs, _ = m.logs.Update(msg)
		return m, m.listenForLogs()
		
	case TaskUpdateMsg:
		m.taskState = msg.State
		m.header, _ = m.header.Update(msg)
		m.progress, _ = m.progress.Update(msg)
		m.execution, _ = m.execution.Update(msg)
		return m, m.listenForUpdates()
		
	case ExecutionUpdateMsg:
		m.execution, _ = m.execution.Update(msg)
		return m, nil
		
	case StatsUpdateMsg:
		m.statsData = msg.Stats
		m.stats, _ = m.stats.Update(msg)
		return m, nil
		
	case error:
		return m, tea.Quit
	}
	
	return m, nil
}

// View renders the TUI
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing..."
	}
	
	// Calculate panel heights
	headerHeight := 1
	progressHeight := 1
	executionHeight := 4
	statsHeight := 1
	logsHeight := m.height - headerHeight - progressHeight - executionHeight - statsHeight - 2
	
	if logsHeight < 5 {
		logsHeight = 5
	}
	
	// Update logs height
	m.logs.height = logsHeight
	
	// Build view
	header := m.header.View()
	progress := m.progress.View()
	execution := m.execution.View()
	logs := m.logs.View()
	stats := m.stats.View()
	
	// Combine all panels
	panels := []string{header}
	if progress != "" {
		panels = append(panels, progress)
	}
	if execution != "" {
		panels = append(panels, execution)
	}
	panels = append(panels, logs)
	if stats != "" {
		panels = append(panels, stats)
	}
	
	return lipgloss.JoinVertical(lipgloss.Left, panels...)
}

// Listen for updates from channels
func (m Model) listenForUpdates() tea.Cmd {
	return func() tea.Msg {
		select {
		case update := <-m.updateChan:
			return update
		case <-m.quitChan:
			return tea.Quit()
		}
	}
}

func (m Model) listenForLogs() tea.Cmd {
	return func() tea.Msg {
		select {
		case logEntry := <-m.logChan:
			return LogMsg{Entry: logEntry}
		case <-m.quitChan:
			return tea.Quit()
		}
	}
}

// SendLog sends a log entry to the TUI (implements logger.TUIBackend)
func (m *Model) SendLog(level, prefix, message string) {
	entry := &LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Prefix:    prefix,
		Message:   message,
	}
	select {
	case m.logChan <- entry:
	default:
		// Channel full, drop log
	}
}

// SendUpdate sends an update to the TUI
func (m *Model) SendUpdate(update interface{}) {
	select {
	case m.updateChan <- update:
	default:
		// Channel full, drop update
	}
}

// Start starts the TUI program
func Start() (*Model, error) {
	model := NewModel()
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithoutSignalHandler())
	
	// Run TUI in a goroutine
	go func() {
		if _, err := p.Run(); err != nil {
			// Error already handled by tea.Program
			return
		}
	}()
	
	// Give TUI a moment to initialize
	time.Sleep(100 * time.Millisecond)
	
	return model, nil
}

// Stop stops the TUI
func (m *Model) Stop() {
	close(m.quitChan)
}

