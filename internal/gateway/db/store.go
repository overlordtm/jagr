package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	_ "github.com/mattn/go-sqlite3"
	
	"github.com/overlordtm/jagr/internal/gateway/models"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db  *sql.DB
	log *zap.Logger
}

func NewStore(dbPath string, log *zap.Logger) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db, log: log}

	if err := store.createSchema(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *Store) createSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS exercises (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		status      TEXT DEFAULT 'active'
	);

	CREATE TABLE IF NOT EXISTS agents (
		id          TEXT PRIMARY KEY,
		exercise_id TEXT NOT NULL REFERENCES exercises(id),
		api_key     TEXT UNIQUE NOT NULL,
		hostname    TEXT,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id          TEXT PRIMARY KEY,
		agent_id    TEXT NOT NULL REFERENCES agents(id),
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		status      TEXT DEFAULT 'active'
	);

	CREATE TABLE IF NOT EXISTS messages (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL REFERENCES sessions(id),
		role        TEXT NOT NULL,
		content     TEXT,
		tool_calls  TEXT,
		tool_call_id TEXT,
		model       TEXT,
		tokens_in   INTEGER,
		tokens_out  INTEGER,
		latency_ms  INTEGER,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS audit_log (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id    TEXT NOT NULL,
		event_type  TEXT NOT NULL,
		payload     TEXT NOT NULL,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);
	CREATE INDEX IF NOT EXISTS idx_audit_agent_time ON audit_log(agent_id, created_at);
	`

	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) CreateExercise(id, name string) (*models.Exercise, error) {
	ex := &models.Exercise{
		ID:        id,
		Name:      name,
		CreatedAt: time.Now(),
		Status:    "active",
	}

	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO exercises (id, name, status) VALUES (?, ?, ?)`,
		ex.ID, ex.Name, ex.Status,
	)
	return ex, err
}

func (s *Store) CreateAgent(exerciseID, apiKey, hostname string) (*models.Agent, error) {
	agentID := uuid.New().String()
	agent := &models.Agent{
		ID:         agentID,
		ExerciseID: exerciseID,
		APIKeyHash: apiKey,
		Hostname:   hostname,
		CreatedAt:  time.Now(),
	}

	_, err := s.db.Exec(
		`INSERT INTO agents (id, exercise_id, api_key, hostname) VALUES (?, ?, ?, ?)`,
		agent.ID, agent.ExerciseID, agent.APIKeyHash, agent.Hostname,
	)
	return agent, err
}

func (s *Store) CreateSession(agentID string) (*models.Session, error) {
	sessionID := uuid.New().String()
	now := time.Now()

	sess := &models.Session{
		ID:        sessionID,
		AgentID:   agentID,
		CreatedAt: now,
		UpdatedAt: now,
		Status:    "active",
	}

	_, err := s.db.Exec(
		`INSERT INTO sessions (id, agent_id, created_at, updated_at, status) VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.AgentID, sess.CreatedAt, sess.UpdatedAt, sess.Status,
	)
	return sess, err
}

func (s *Store) GetSession(sessionID string) (*models.Session, error) {
	var sess models.Session
	err := s.db.QueryRow(`SELECT id, agent_id, created_at, updated_at, status FROM sessions WHERE id = ?`, sessionID).Scan(
		&sess.ID, &sess.AgentID, &sess.CreatedAt, &sess.UpdatedAt, &sess.Status,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sess, nil
}

func (s *Store) GetMessageCount(sessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&count)
	return count, err
}

func (s *Store) AppendMessage(sessID string, msg *models.Message, model string, tokensIn, tokensOut, latencyMs int) error {
	toolCallsJSON, _ := json.Marshal(msg.ToolCalls)

	_, err := s.db.Exec(`
		INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, latency_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sessID, msg.Role, msg.Content, string(toolCallsJSON), msg.ToolCallID, model, tokensIn, tokensOut, latencyMs)
	return err
}

func (s *Store) GetSessionMessages(sessionID string) ([]models.MessageLog, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, latency_ms, created_at
		FROM messages WHERE session_id = ? ORDER BY created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []models.MessageLog
	for rows.Next() {
		var m models.MessageLog
		var toolCallsStr sql.NullString
		err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCallsStr, &m.ToolCallID, &m.Model, &m.TokensIn, &m.TokensOut, &m.LatencyMs, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		if toolCallsStr.Valid {
			m.ToolCalls = toolCallsStr.String
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *Store) LogAudit(agentID, eventType string, payload any) error {
	payloadBytes, _ := json.Marshal(payload)

	_, err := s.db.Exec(`
		INSERT INTO audit_log (agent_id, event_type, payload, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, agentID, eventType, string(payloadBytes))
	return err
}

func (s *Store) UpdateSessionUpdatedAt(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) FindAgentByAPIKey(apiKey string) (*models.Agent, error) {
	agent := &models.Agent{}
	err := s.db.QueryRow(`
		SELECT id, exercise_id, api_key, hostname, created_at FROM agents WHERE api_key = ?
	`, apiKey).Scan(&agent.ID, &agent.ExerciseID, &agent.APIKeyHash, &agent.Hostname, &agent.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return agent, nil
}

func (s *Store) UpdateAgentHostname(agentID, hostname string) error {
	_, err := s.db.Exec(`UPDATE agents SET hostname = ? WHERE id = ?`, hostname, agentID)
	return err
}

func (s *Store) CloseSession(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET status = 'closed' WHERE id = ?`, sessionID)
	return err
}

func (s *Store) GetExercises(status string) ([]models.Exercise, error) {
	query := `SELECT id, name, created_at, status FROM exercises`
	if status != "" {
		query += ` WHERE status = ?`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var exercises []models.Exercise
	for rows.Next() {
		var ex models.Exercise
		err := rows.Scan(&ex.ID, &ex.Name, &ex.CreatedAt, &ex.Status)
		if err != nil {
			return nil, err
		}
		exercises = append(exercises, ex)
	}
	return exercises, rows.Err()
}

func (s *Store) GetAgents(exerciseID string) ([]models.Agent, error) {
	rows, err := s.db.Query(`
		SELECT id, exercise_id, api_key, hostname, created_at FROM agents WHERE exercise_id = ?
	`, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []models.Agent
	for rows.Next() {
		var a models.Agent
		err := rows.Scan(&a.ID, &a.ExerciseID, &a.APIKeyHash, &a.Hostname, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *Store) GetSessions(agentID string) ([]models.Session, error) {
	rows, err := s.db.Query(`
		SELECT id, agent_id, created_at, updated_at, status FROM sessions WHERE agent_id = ?
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []models.Session
	for rows.Next() {
		var s models.Session
		err := rows.Scan(&s.ID, &s.AgentID, &s.CreatedAt, &s.UpdatedAt, &s.Status)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func (s *Store) GetAuditLog(agentID string, start, end time.Time, eventType string) ([]models.AuditLog, error) {
	query := `
		SELECT id, agent_id, event_type, payload, created_at FROM audit_log WHERE agent_id = ?
	`
	args := []any{agentID}

	if !start.IsZero() {
		query += ` AND created_at >= ?`
		args = append(args, start)
	}
	if !end.IsZero() {
		query += ` AND created_at <= ?`
		args = append(args, end)
	}
	if eventType != "" {
		query += ` AND event_type = ?`
		args = append(args, eventType)
	}
	query += ` ORDER BY created_at DESC LIMIT 1000`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.AuditLog
	for rows.Next() {
		var l models.AuditLog
		err := rows.Scan(&l.ID, &l.AgentID, &l.EventType, &l.Payload, &l.CreatedAt)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func (s *Store) GetAgent(agentID string) (*models.Agent, error) {
	agent := &models.Agent{}
	err := s.db.QueryRow(`
		SELECT id, exercise_id, api_key, hostname, created_at FROM agents WHERE id = ?
	`, agentID).Scan(&agent.ID, &agent.ExerciseID, &agent.APIKeyHash, &agent.Hostname, &agent.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return agent, nil
}

func (s *Store) CountMessages(sessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM messages WHERE session_id = ?
	`, sessionID).Scan(&count)
	return count, err
}

func (s *Store) GetMessagesWithToolCalls(sessionID string) ([]models.MessageLog, error) {
	query := `
		SELECT id, session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, latency_ms, created_at
		FROM messages WHERE session_id = ? AND (tool_calls IS NOT NULL OR role = 'tool')
		ORDER BY created_at ASC
	`
	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []models.MessageLog
	for rows.Next() {
		var m models.MessageLog
		var toolCallsStr sql.NullString
		err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCallsStr, &m.ToolCallID, &m.Model, &m.TokensIn, &m.TokensOut, &m.LatencyMs, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		if toolCallsStr.Valid {
			m.ToolCalls = toolCallsStr.String
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *Store) GetEventsForSession(sessionID string) ([]models.MessageLog, error) {
	return s.GetSessionMessages(sessionID)
}

func (s *Store) GetSessionForAgent(agentID string) (*models.Session, error) {
	var sess models.Session
	err := s.db.QueryRow(`
		SELECT id, agent_id, created_at, updated_at, status FROM sessions 
		WHERE agent_id = ? AND status = 'active' ORDER BY created_at DESC LIMIT 1
	`, agentID).Scan(&sess.ID, &sess.AgentID, &sess.CreatedAt, &sess.UpdatedAt, &sess.Status)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sess, nil
}
