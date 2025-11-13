package task

import (
	"fmt"
	"time"

	"chrome-agent/internal/state"
)

// Manager handles task lifecycle management
type Manager struct {
	db      *state.DB
	taskID  int64
	spec    *TaskSpec
	loopID  int64
}

// NewManager creates a new task manager
func NewManager(db *state.DB, spec *TaskSpec) (*Manager, error) {
	taskID, err := db.CreateTask(spec.Objective)
	if err != nil {
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	m := &Manager{
		db:     db,
		taskID: taskID,
		spec:   spec,
	}

	// Create loop if condition exists
	if spec.LoopCondition != nil {
		loopType := state.LoopTypeTime
		if spec.LoopCondition.Type == "quota" {
			loopType = state.LoopTypeQuota
		}

		loopID, err := db.CreateLoop(taskID, loopType, spec.LoopCondition.TargetValue)
		if err != nil {
			return nil, fmt.Errorf("failed to create loop: %w", err)
		}
		m.loopID = loopID
	}

	return m, nil
}

// GetTaskID returns the task ID
func (m *Manager) GetTaskID() int64 {
	return m.taskID
}

// GetSpec returns the task specification
func (m *Manager) GetSpec() *TaskSpec {
	return m.spec
}

// GetLoop returns the active loop if exists
func (m *Manager) GetLoop() (*state.Loop, error) {
	if m.loopID == 0 {
		return nil, nil
	}
	return m.db.GetLoopByTaskID(m.taskID)
}

// CreateSubtask creates a new subtask for current cycle
func (m *Manager) CreateSubtask(name string, cycleNumber int) (int64, error) {
	return m.db.CreateSubtask(m.taskID, name, cycleNumber)
}

// UpdateSubtaskStatus updates subtask status
func (m *Manager) UpdateSubtaskStatus(subtaskID int64, status state.SubtaskStatus) error {
	return m.db.UpdateSubtaskStatus(subtaskID, status)
}

// UpdateSubtaskResult updates subtask result
func (m *Manager) UpdateSubtaskResult(subtaskID int64, result interface{}) error {
	return m.db.UpdateSubtaskResult(subtaskID, result)
}

// UpdateTaskStatus updates task status
func (m *Manager) UpdateTaskStatus(status state.TaskStatus) error {
	return m.db.UpdateTaskStatus(m.taskID, status)
}

// UpdateTaskStatusWithReason updates task status with fail reason
func (m *Manager) UpdateTaskStatusWithReason(status state.TaskStatus, failReason string) error {
	return m.db.UpdateTaskStatusWithReason(m.taskID, status, failReason)
}

// UpdateTaskResult updates task result
func (m *Manager) UpdateTaskResult(result interface{}) error {
	return m.db.UpdateTaskResult(m.taskID, result)
}

// UpdateLoopProgress updates loop progress
func (m *Manager) UpdateLoopProgress(currentValue float64) error {
	if m.loopID == 0 {
		return nil
	}
	return m.db.UpdateLoopProgress(m.loopID, currentValue)
}

// CompleteLoop marks loop as completed
func (m *Manager) CompleteLoop() error {
	if m.loopID == 0 {
		return nil
	}
	return m.db.UpdateLoopStatus(m.loopID, state.LoopStatusCompleted)
}

// PauseLoop pauses the loop
func (m *Manager) PauseLoop() error {
	if m.loopID == 0 {
		return nil
	}
	return m.db.UpdateLoopStatus(m.loopID, state.LoopStatusPaused)
}

// ResumeLoop resumes the loop
func (m *Manager) ResumeLoop() error {
	if m.loopID == 0 {
		return nil
	}
	return m.db.UpdateLoopStatus(m.loopID, state.LoopStatusActive)
}

// GetTask retrieves current task
func (m *Manager) GetTask() (*state.Task, error) {
	return m.db.GetTask(m.taskID)
}

// GetSubtasks retrieves all subtasks
func (m *Manager) GetSubtasks() ([]*state.Subtask, error) {
	return m.db.GetSubtasksByTaskID(m.taskID)
}

// IsLoopable checks if task has a loop condition
func (m *Manager) IsLoopable() bool {
	return m.spec.LoopCondition != nil
}

// CheckLoopCondition checks if loop condition is met
func (m *Manager) CheckLoopCondition() (bool, error) {
	if !m.IsLoopable() {
		return true, nil // No loop, condition always met
	}

	loop, err := m.GetLoop()
	if err != nil {
		return false, err
	}
	if loop == nil {
		return true, nil
	}

	switch loop.Type {
	case state.LoopTypeTime:
		// Check if elapsed time >= target
		elapsed := time.Since(loop.CreatedAt).Seconds()
		return elapsed >= loop.TargetValue, nil
	case state.LoopTypeQuota:
		// Check if current value >= target
		return loop.CurrentValue >= loop.TargetValue, nil
	default:
		return true, nil
	}
}

// GetElapsedTime returns elapsed time for time-based loops
func (m *Manager) GetElapsedTime() (time.Duration, error) {
	loop, err := m.GetLoop()
	if err != nil {
		return 0, err
	}
	if loop == nil || loop.Type != state.LoopTypeTime {
		return 0, nil
	}
	return time.Since(loop.CreatedAt), nil
}

// IncrementSubtaskRetry increments retry count for a subtask
func (m *Manager) IncrementSubtaskRetry(subtaskID int64) error {
	return m.db.IncrementSubtaskRetry(subtaskID)
}

// GetSubtask retrieves a subtask by ID
func (m *Manager) GetSubtask(subtaskID int64) (*state.Subtask, error) {
	return m.db.GetSubtask(subtaskID)
}

// CreateStateSnapshot creates a state snapshot
func (m *Manager) CreateStateSnapshot(subtaskID int64, cycleNumber int, snapshotData string) (int64, error) {
	return m.db.CreateStateSnapshot(subtaskID, cycleNumber, snapshotData)
}

// GetLatestStateSnapshot retrieves the latest state snapshot for a subtask
func (m *Manager) GetLatestStateSnapshot(subtaskID int64) (*state.StateSnapshot, error) {
	return m.db.GetLatestStateSnapshot(subtaskID)
}

