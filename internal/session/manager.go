package session

import (
	"fmt"
	"time"
)

// State represents the state of a session
type State string

const (
	StateActive     State = "active"
	StateLost       State = "lost"
	StateRecovering State = "recovering"
	StateClosed     State = "closed"
)

// Session represents an abstract session for any tool that maintains state
type Session struct {
	ID                    string
	ToolType              string              // e.g., "browser", "api", "database"
	State                 State
	LastSuccessfulOp      time.Time
	LastFailureTime       time.Time
	ConsecutiveFailures   int
	TotalOperations       int
	SuccessfulOperations  int
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Metadata              map[string]interface{} // Tool-specific metadata (e.g., URL for browser)
}

// Manager manages sessions in a tool-agnostic way
type Manager struct {
	Sessions map[string]*Session // sessionID -> Session (exported for executor access)
}

// NewManager creates a new session manager
func NewManager() *Manager {
	return &Manager{
		Sessions: make(map[string]*Session),
	}
}

// CreateSession creates a new session
func (m *Manager) CreateSession(toolType string, metadata map[string]interface{}) *Session {
	sessionID := fmt.Sprintf("%s_%d", toolType, time.Now().UnixNano())
	session := &Session{
		ID:                   sessionID,
		ToolType:             toolType,
		State:                StateActive,
		LastSuccessfulOp:     time.Now(),
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
		Metadata:             metadata,
		TotalOperations:      0,
		SuccessfulOperations: 0,
		ConsecutiveFailures:  0,
	}
	m.Sessions[sessionID] = session
	return session
}

// GetSession retrieves a session by ID
func (m *Manager) GetSession(sessionID string) (*Session, bool) {
	session, exists := m.Sessions[sessionID]
	return session, exists
}

// GetActiveSessions returns all active sessions
func (m *Manager) GetActiveSessions() []*Session {
	var active []*Session
	for _, session := range m.Sessions {
		if session.State == StateActive || session.State == StateRecovering {
			active = append(active, session)
		}
	}
	return active
}

// GetSessionsByToolType returns all sessions for a specific tool type
func (m *Manager) GetSessionsByToolType(toolType string) []*Session {
	var sessions []*Session
	for _, session := range m.Sessions {
		if session.ToolType == toolType {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

// RecordSuccess records a successful operation for a session
func (m *Manager) RecordSuccess(sessionID string) error {
	session, exists := m.Sessions[sessionID]
	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	session.LastSuccessfulOp = time.Now()
	session.ConsecutiveFailures = 0
	session.TotalOperations++
	session.SuccessfulOperations++
	session.UpdatedAt = time.Now()

	// If recovering, mark as active
	if session.State == StateRecovering {
		session.State = StateActive
	}

	return nil
}

// RecordFailure records a failed operation for a session
func (m *Manager) RecordFailure(sessionID string) error {
	session, exists := m.Sessions[sessionID]
	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	session.LastFailureTime = time.Now()
	session.ConsecutiveFailures++
	session.TotalOperations++
	session.UpdatedAt = time.Now()

	// If too many consecutive failures, mark as lost
	if session.ConsecutiveFailures >= 3 {
		session.State = StateLost
	}

	return nil
}

// UpdateState updates the state of a session
func (m *Manager) UpdateState(sessionID string, state State) error {
	session, exists := m.Sessions[sessionID]
	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	session.State = state
	session.UpdatedAt = time.Now()
	return nil
}

// UpdateMetadata updates metadata for a session
func (m *Manager) UpdateMetadata(sessionID string, metadata map[string]interface{}) error {
	session, exists := m.Sessions[sessionID]
	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if session.Metadata == nil {
		session.Metadata = make(map[string]interface{})
	}
	for k, v := range metadata {
		session.Metadata[k] = v
	}
	session.UpdatedAt = time.Now()
	return nil
}

// CloseSession closes a session
func (m *Manager) CloseSession(sessionID string) error {
	session, exists := m.Sessions[sessionID]
	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	session.State = StateClosed
	session.UpdatedAt = time.Now()
	return nil
}

// GetHealth returns health metrics for a session
func (s *Session) GetHealth() HealthMetrics {
	successRate := 0.0
	if s.TotalOperations > 0 {
		successRate = float64(s.SuccessfulOperations) / float64(s.TotalOperations)
	}

	timeSinceLastSuccess := time.Since(s.LastSuccessfulOp)
	timeSinceLastFailure := time.Since(s.LastFailureTime)

	return HealthMetrics{
		SuccessRate:          successRate,
		ConsecutiveFailures:   s.ConsecutiveFailures,
		TimeSinceLastSuccess: timeSinceLastSuccess,
		TimeSinceLastFailure: timeSinceLastFailure,
		TotalOperations:      s.TotalOperations,
		IsHealthy:            s.State == StateActive && s.ConsecutiveFailures < 3 && successRate > 0.5,
	}
}

// HealthMetrics contains health information for a session
type HealthMetrics struct {
	SuccessRate          float64
	ConsecutiveFailures  int
	TimeSinceLastSuccess time.Duration
	TimeSinceLastFailure time.Duration
	TotalOperations      int
	IsHealthy            bool
}

// FindOrCreateSessionForTool finds an active session for a tool type or creates a new one
func (m *Manager) FindOrCreateSessionForTool(toolType string, metadata map[string]interface{}) *Session {
	// First, try to find an active session for this tool type
	sessions := m.GetSessionsByToolType(toolType)
	for _, session := range sessions {
		if session.State == StateActive {
			// Found an active session, return it
			return session
		}
	}

	// No active session found, create a new one
	return m.CreateSession(toolType, metadata)
}

