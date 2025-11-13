package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"chrome-agent/internal/session"

	_ "github.com/mattn/go-sqlite3"
)

// DB handles SQLite database operations
type DB struct {
	db *sql.DB
}

// TaskStatus represents task status
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusPaused     TaskStatus = "paused"
)

// SubtaskStatus represents subtask status
type SubtaskStatus string

const (
	SubtaskStatusPending    SubtaskStatus = "pending"
	SubtaskStatusInProgress SubtaskStatus = "in_progress"
	SubtaskStatusCompleted  SubtaskStatus = "completed"
	SubtaskStatusFailed     SubtaskStatus = "failed"
)

// LoopType represents loop type
type LoopType string

const (
	LoopTypeTime   LoopType = "time"
	LoopTypeQuota  LoopType = "quota"
)

// LoopStatus represents loop status
type LoopStatus string

const (
	LoopStatusActive   LoopStatus = "active"
	LoopStatusCompleted LoopStatus = "completed"
	LoopStatusPaused   LoopStatus = "paused"
)

// Task represents a task record
type Task struct {
	ID         int64
	Name       string
	Status     TaskStatus
	FailReason string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ResultJSON string
}

// Subtask represents a subtask record
type Subtask struct {
	ID           int64
	TaskID       int64
	Name         string
	Status       SubtaskStatus
	CycleNumber  int
	RetryCount   int
	MaxRetries   int
	ResultJSON   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Loop represents a loop record
type Loop struct {
	ID          int64
	TaskID      int64
	Type        LoopType
	TargetValue float64
	CurrentValue float64
	Status      LoopStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Exception represents an exception record
type Exception struct {
	ID                  int64
	SubtaskID           int64
	ErrorMessage        string
	RequiresIntervention bool
	ResolvedAt          *time.Time
	CreatedAt           time.Time
}

// StateSnapshot represents a browser/system state snapshot
type StateSnapshot struct {
	ID          int64
	SubtaskID   int64
	CycleNumber int
	SnapshotData string // JSON of browser state (URL, cookies, localStorage, etc.)
	CreatedAt   time.Time
}

// NewDB creates a new database connection
func NewDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	d := &DB{db: db}
	if err := d.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return d, nil
}

func (d *DB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		fail_reason TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		result_json TEXT
	);

	CREATE TABLE IF NOT EXISTS subtasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		cycle_number INTEGER NOT NULL DEFAULT 0,
		retry_count INTEGER NOT NULL DEFAULT 0,
		max_retries INTEGER NOT NULL DEFAULT 3,
		result_json TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (task_id) REFERENCES tasks(id)
	);

	CREATE TABLE IF NOT EXISTS loops (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		type TEXT NOT NULL,
		target_value REAL NOT NULL,
		current_value REAL NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (task_id) REFERENCES tasks(id)
	);

	CREATE TABLE IF NOT EXISTS exceptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		subtask_id INTEGER NOT NULL,
		error_message TEXT NOT NULL,
		requires_intervention INTEGER NOT NULL DEFAULT 0,
		resolved_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (subtask_id) REFERENCES subtasks(id)
	);

	CREATE TABLE IF NOT EXISTS state_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		subtask_id INTEGER NOT NULL,
		cycle_number INTEGER NOT NULL DEFAULT 0,
		snapshot_data TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (subtask_id) REFERENCES subtasks(id)
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		tool_type TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'active',
		last_successful_op DATETIME,
		last_failure_time DATETIME,
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		total_operations INTEGER NOT NULL DEFAULT 0,
		successful_operations INTEGER NOT NULL DEFAULT 0,
		metadata TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_subtasks_task_id ON subtasks(task_id);
	CREATE INDEX IF NOT EXISTS idx_loops_task_id ON loops(task_id);
	CREATE INDEX IF NOT EXISTS idx_exceptions_subtask_id ON exceptions(subtask_id);
	CREATE INDEX IF NOT EXISTS idx_state_snapshots_subtask_id ON state_snapshots(subtask_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_tool_type ON sessions(tool_type);
	CREATE INDEX IF NOT EXISTS idx_sessions_state ON sessions(state);
	`

	_, err := d.db.Exec(schema)
	return err
}

// Close closes the database connection
func (d *DB) Close() error {
	return d.db.Close()
}

// CreateTask creates a new task
func (d *DB) CreateTask(name string) (int64, error) {
	result, err := d.db.Exec(
		"INSERT INTO tasks (name, status) VALUES (?, ?)",
		name, TaskStatusPending,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateTaskStatus updates task status
func (d *DB) UpdateTaskStatus(taskID int64, status TaskStatus) error {
	_, err := d.db.Exec(
		"UPDATE tasks SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		status, taskID,
	)
	return err
}

// UpdateTaskStatusWithReason updates task status and fail reason
func (d *DB) UpdateTaskStatusWithReason(taskID int64, status TaskStatus, failReason string) error {
	_, err := d.db.Exec(
		"UPDATE tasks SET status = ?, fail_reason = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		status, failReason, taskID,
	)
	return err
}

// UpdateTaskResult updates task result
func (d *DB) UpdateTaskResult(taskID int64, result interface{}) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(
		"UPDATE tasks SET result_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		string(resultJSON), taskID,
	)
	return err
}

// GetTask retrieves a task by ID
func (d *DB) GetTask(taskID int64) (*Task, error) {
	var task Task
	var failReason sql.NullString
	err := d.db.QueryRow(
		"SELECT id, name, status, fail_reason, created_at, updated_at, result_json FROM tasks WHERE id = ?",
		taskID,
	).Scan(&task.ID, &task.Name, &task.Status, &failReason, &task.CreatedAt, &task.UpdatedAt, &task.ResultJSON)
	if err != nil {
		return nil, err
	}
	if failReason.Valid {
		task.FailReason = failReason.String
	}
	return &task, nil
}

// CreateSubtask creates a new subtask
func (d *DB) CreateSubtask(taskID int64, name string, cycleNumber int) (int64, error) {
	result, err := d.db.Exec(
		"INSERT INTO subtasks (task_id, name, status, cycle_number, retry_count, max_retries) VALUES (?, ?, ?, ?, 0, 3)",
		taskID, name, SubtaskStatusPending, cycleNumber,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateSubtaskStatus updates subtask status
func (d *DB) UpdateSubtaskStatus(subtaskID int64, status SubtaskStatus) error {
	_, err := d.db.Exec(
		"UPDATE subtasks SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		status, subtaskID,
	)
	return err
}

// UpdateSubtaskResult updates subtask result
func (d *DB) UpdateSubtaskResult(subtaskID int64, result interface{}) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(
		"UPDATE subtasks SET result_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		string(resultJSON), subtaskID,
	)
	return err
}

// GetSubtasksByTaskID retrieves all subtasks for a task
func (d *DB) GetSubtasksByTaskID(taskID int64) ([]*Subtask, error) {
	rows, err := d.db.Query(
		"SELECT id, task_id, name, status, cycle_number, retry_count, max_retries, result_json, created_at, updated_at FROM subtasks WHERE task_id = ? ORDER BY cycle_number, id",
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subtasks []*Subtask
	for rows.Next() {
		var subtask Subtask
		var resultJSON sql.NullString
		err := rows.Scan(&subtask.ID, &subtask.TaskID, &subtask.Name, &subtask.Status, &subtask.CycleNumber, &subtask.RetryCount, &subtask.MaxRetries, &resultJSON, &subtask.CreatedAt, &subtask.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if resultJSON.Valid {
			subtask.ResultJSON = resultJSON.String
		}
		subtasks = append(subtasks, &subtask)
	}
	return subtasks, rows.Err()
}

// CreateLoop creates a new loop
func (d *DB) CreateLoop(taskID int64, loopType LoopType, targetValue float64) (int64, error) {
	result, err := d.db.Exec(
		"INSERT INTO loops (task_id, type, target_value, current_value, status) VALUES (?, ?, ?, 0, ?)",
		taskID, loopType, targetValue, LoopStatusActive,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateLoopProgress updates loop current value
func (d *DB) UpdateLoopProgress(loopID int64, currentValue float64) error {
	_, err := d.db.Exec(
		"UPDATE loops SET current_value = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		currentValue, loopID,
	)
	return err
}

// UpdateLoopStatus updates loop status
func (d *DB) UpdateLoopStatus(loopID int64, status LoopStatus) error {
	_, err := d.db.Exec(
		"UPDATE loops SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		status, loopID,
	)
	return err
}

// GetLoopByTaskID retrieves active loop for a task
func (d *DB) GetLoopByTaskID(taskID int64) (*Loop, error) {
	var loop Loop
	err := d.db.QueryRow(
		"SELECT id, task_id, type, target_value, current_value, status, created_at, updated_at FROM loops WHERE task_id = ? AND status = 'active' LIMIT 1",
		taskID,
	).Scan(&loop.ID, &loop.TaskID, &loop.Type, &loop.TargetValue, &loop.CurrentValue, &loop.Status, &loop.CreatedAt, &loop.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &loop, nil
}

// CreateException creates a new exception record
func (d *DB) CreateException(subtaskID int64, errorMessage string, requiresIntervention bool) (int64, error) {
	intervention := 0
	if requiresIntervention {
		intervention = 1
	}
	result, err := d.db.Exec(
		"INSERT INTO exceptions (subtask_id, error_message, requires_intervention) VALUES (?, ?, ?)",
		subtaskID, errorMessage, intervention,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ResolveException marks an exception as resolved
func (d *DB) ResolveException(exceptionID int64) error {
	_, err := d.db.Exec(
		"UPDATE exceptions SET resolved_at = CURRENT_TIMESTAMP WHERE id = ?",
		exceptionID,
	)
	return err
}

// GetUnresolvedExceptions retrieves unresolved exceptions
func (d *DB) GetUnresolvedExceptions() ([]*Exception, error) {
	rows, err := d.db.Query(
		"SELECT id, subtask_id, error_message, requires_intervention, resolved_at, created_at FROM exceptions WHERE resolved_at IS NULL ORDER BY created_at",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var exceptions []*Exception
	for rows.Next() {
		var ex Exception
		var intervention int
		err := rows.Scan(&ex.ID, &ex.SubtaskID, &ex.ErrorMessage, &intervention, &ex.ResolvedAt, &ex.CreatedAt)
		if err != nil {
			return nil, err
		}
		ex.RequiresIntervention = intervention == 1
		exceptions = append(exceptions, &ex)
	}
	return exceptions, rows.Err()
}

// IncrementSubtaskRetry increments retry count for a subtask
func (d *DB) IncrementSubtaskRetry(subtaskID int64) error {
	_, err := d.db.Exec(
		"UPDATE subtasks SET retry_count = retry_count + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		subtaskID,
	)
	return err
}

// GetSubtask retrieves a subtask by ID
func (d *DB) GetSubtask(subtaskID int64) (*Subtask, error) {
	var subtask Subtask
	var resultJSON sql.NullString
	err := d.db.QueryRow(
		"SELECT id, task_id, name, status, cycle_number, retry_count, max_retries, result_json, created_at, updated_at FROM subtasks WHERE id = ?",
		subtaskID,
	).Scan(&subtask.ID, &subtask.TaskID, &subtask.Name, &subtask.Status, &subtask.CycleNumber, &subtask.RetryCount, &subtask.MaxRetries, &resultJSON, &subtask.CreatedAt, &subtask.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if resultJSON.Valid {
		subtask.ResultJSON = resultJSON.String
	}
	return &subtask, nil
}

// CreateStateSnapshot creates a state snapshot
func (d *DB) CreateStateSnapshot(subtaskID int64, cycleNumber int, snapshotData string) (int64, error) {
	result, err := d.db.Exec(
		"INSERT INTO state_snapshots (subtask_id, cycle_number, snapshot_data) VALUES (?, ?, ?)",
		subtaskID, cycleNumber, snapshotData,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetLatestStateSnapshot retrieves the latest state snapshot for a subtask
func (d *DB) GetLatestStateSnapshot(subtaskID int64) (*StateSnapshot, error) {
	var snapshot StateSnapshot
	err := d.db.QueryRow(
		"SELECT id, subtask_id, cycle_number, snapshot_data, created_at FROM state_snapshots WHERE subtask_id = ? ORDER BY created_at DESC LIMIT 1",
		subtaskID,
	).Scan(&snapshot.ID, &snapshot.SubtaskID, &snapshot.CycleNumber, &snapshot.SnapshotData, &snapshot.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// CreateSession creates a new session in the database
func (d *DB) CreateSession(sess *session.Session) error {
	metadataJSON, err := json.Marshal(sess.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	_, err = d.db.Exec(
		`INSERT INTO sessions (
			id, tool_type, state, last_successful_op, last_failure_time,
			consecutive_failures, total_operations, successful_operations,
			metadata, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.ToolType, string(sess.State),
		sess.LastSuccessfulOp, sess.LastFailureTime,
		sess.ConsecutiveFailures, sess.TotalOperations, sess.SuccessfulOperations,
		string(metadataJSON), sess.CreatedAt, sess.UpdatedAt,
	)
	return err
}

// GetSession retrieves a session by ID
func (d *DB) GetSession(sessionID string) (*session.Session, error) {
	row := d.db.QueryRow(
		`SELECT id, tool_type, state, last_successful_op, last_failure_time,
			consecutive_failures, total_operations, successful_operations,
			metadata, created_at, updated_at
		FROM sessions WHERE id = ?`,
		sessionID,
	)

	var sess session.Session
	var metadataJSON string
	var lastSuccessOp, lastFailureTime sql.NullTime

	err := row.Scan(
		&sess.ID, &sess.ToolType, &sess.State,
		&lastSuccessOp, &lastFailureTime,
		&sess.ConsecutiveFailures, &sess.TotalOperations, &sess.SuccessfulOperations,
		&metadataJSON, &sess.CreatedAt, &sess.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session %s not found", sessionID)
		}
		return nil, err
	}

	if lastSuccessOp.Valid {
		sess.LastSuccessfulOp = lastSuccessOp.Time
	}
	if lastFailureTime.Valid {
		sess.LastFailureTime = lastFailureTime.Time
	}

	if metadataJSON != "" {
		if err := json.Unmarshal([]byte(metadataJSON), &sess.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}

	return &sess, nil
}

// UpdateSession updates a session in the database
func (d *DB) UpdateSession(sess *session.Session) error {
	metadataJSON, err := json.Marshal(sess.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	_, err = d.db.Exec(
		`UPDATE sessions SET
			state = ?, last_successful_op = ?, last_failure_time = ?,
			consecutive_failures = ?, total_operations = ?, successful_operations = ?,
			metadata = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		string(sess.State), sess.LastSuccessfulOp, sess.LastFailureTime,
		sess.ConsecutiveFailures, sess.TotalOperations, sess.SuccessfulOperations,
		string(metadataJSON), sess.ID,
	)
	return err
}

// GetActiveSessions returns all active sessions
func (d *DB) GetActiveSessions() ([]*session.Session, error) {
	rows, err := d.db.Query(
		`SELECT id, tool_type, state, last_successful_op, last_failure_time,
			consecutive_failures, total_operations, successful_operations,
			metadata, created_at, updated_at
		FROM sessions WHERE state IN ('active', 'recovering')`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*session.Session
	for rows.Next() {
		var sess session.Session
		var metadataJSON string
		var lastSuccessOp, lastFailureTime sql.NullTime

		err := rows.Scan(
			&sess.ID, &sess.ToolType, &sess.State,
			&lastSuccessOp, &lastFailureTime,
			&sess.ConsecutiveFailures, &sess.TotalOperations, &sess.SuccessfulOperations,
			&metadataJSON, &sess.CreatedAt, &sess.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if lastSuccessOp.Valid {
			sess.LastSuccessfulOp = lastSuccessOp.Time
		}
		if lastFailureTime.Valid {
			sess.LastFailureTime = lastFailureTime.Time
		}

		if metadataJSON != "" {
			if err := json.Unmarshal([]byte(metadataJSON), &sess.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		}

		sessions = append(sessions, &sess)
	}

	return sessions, rows.Err()
}

// GetSessionsByToolType returns all sessions for a specific tool type
func (d *DB) GetSessionsByToolType(toolType string) ([]*session.Session, error) {
	rows, err := d.db.Query(
		`SELECT id, tool_type, state, last_successful_op, last_failure_time,
			consecutive_failures, total_operations, successful_operations,
			metadata, created_at, updated_at
		FROM sessions WHERE tool_type = ?`,
		toolType,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*session.Session
	for rows.Next() {
		var sess session.Session
		var metadataJSON string
		var lastSuccessOp, lastFailureTime sql.NullTime

		err := rows.Scan(
			&sess.ID, &sess.ToolType, &sess.State,
			&lastSuccessOp, &lastFailureTime,
			&sess.ConsecutiveFailures, &sess.TotalOperations, &sess.SuccessfulOperations,
			&metadataJSON, &sess.CreatedAt, &sess.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if lastSuccessOp.Valid {
			sess.LastSuccessfulOp = lastSuccessOp.Time
		}
		if lastFailureTime.Valid {
			sess.LastFailureTime = lastFailureTime.Time
		}

		if metadataJSON != "" {
			if err := json.Unmarshal([]byte(metadataJSON), &sess.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		}

		sessions = append(sessions, &sess)
	}

	return sessions, rows.Err()
}

// CloseSession marks a session as closed
func (d *DB) CloseSession(sessionID string) error {
	_, err := d.db.Exec(
		"UPDATE sessions SET state = 'closed', updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		sessionID,
	)
	return err
}

