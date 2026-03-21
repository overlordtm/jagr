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
	CREATE TABLE IF NOT EXISTS agents (
		id          TEXT PRIMARY KEY,
		hostname    TEXT UNIQUE NOT NULL,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id              TEXT PRIMARY KEY,
		agent_id        TEXT NOT NULL REFERENCES agents(id),
		created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		status          TEXT DEFAULT 'active',
		last_heartbeat  DATETIME
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
		cost_usd    REAL DEFAULT 0,
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

	// Migration: add last_heartbeat column if missing
	s.db.Exec(`ALTER TABLE sessions ADD COLUMN last_heartbeat DATETIME`)

	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Migration: add cost_usd column if missing (for existing databases)
	s.db.Exec(`ALTER TABLE messages ADD COLUMN cost_usd REAL DEFAULT 0`)

	return nil
}

// FindOrCreateAgentByHostname returns the agent for the given hostname,
// creating one if it doesn't exist (auto-enrollment).
func (s *Store) FindOrCreateAgentByHostname(hostname string) (*models.Agent, error) {
	agent := &models.Agent{}
	err := s.db.QueryRow(`
		SELECT id, hostname, created_at FROM agents WHERE hostname = ?
	`, hostname).Scan(&agent.ID, &agent.Hostname, &agent.CreatedAt)
	if err == nil {
		return agent, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// Auto-enroll
	agentID := uuid.New().String()
	agent = &models.Agent{
		ID:        agentID,
		Hostname:  hostname,
		CreatedAt: time.Now(),
	}

	_, err = s.db.Exec(
		`INSERT INTO agents (id, hostname) VALUES (?, ?)`,
		agent.ID, agent.Hostname,
	)
	return agent, err
}

func (s *Store) GetAgent(agentID string) (*models.Agent, error) {
	agent := &models.Agent{}
	err := s.db.QueryRow(`
		SELECT id, hostname, created_at FROM agents WHERE id = ?
	`, agentID).Scan(&agent.ID, &agent.Hostname, &agent.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return agent, nil
}

func (s *Store) GetAgents() ([]models.Agent, error) {
	rows, err := s.db.Query(`
		SELECT id, hostname, created_at FROM agents ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []models.Agent
	for rows.Next() {
		var a models.Agent
		err := rows.Scan(&a.ID, &a.Hostname, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *Store) DeleteAgentByHostname(hostname string) error {
	result, err := s.db.Exec(`DELETE FROM agents WHERE hostname = ?`, hostname)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
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
		`INSERT INTO sessions (id, agent_id, created_at, updated_at, status, last_heartbeat) VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.AgentID, sess.CreatedAt, sess.UpdatedAt, sess.Status, now,
	)
	return sess, err
}

func (s *Store) GetSession(sessionID string) (*models.Session, error) {
	var sess models.Session
	err := s.db.QueryRow(`SELECT id, agent_id, created_at, updated_at, status, last_heartbeat FROM sessions WHERE id = ?`, sessionID).Scan(
		&sess.ID, &sess.AgentID, &sess.CreatedAt, &sess.UpdatedAt, &sess.Status, &sess.LastHeartbeat,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sess, nil
}

func (s *Store) GetSessionForAgent(agentID string) (*models.Session, error) {
	var sess models.Session
	err := s.db.QueryRow(`
		SELECT id, agent_id, created_at, updated_at, status, last_heartbeat FROM sessions
		WHERE agent_id = ? AND status = 'active' ORDER BY created_at DESC LIMIT 1
	`, agentID).Scan(&sess.ID, &sess.AgentID, &sess.CreatedAt, &sess.UpdatedAt, &sess.Status, &sess.LastHeartbeat)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sess, nil
}

func (s *Store) GetSessions(agentID string) ([]models.Session, error) {
	rows, err := s.db.Query(`
		SELECT id, agent_id, created_at, updated_at, status, last_heartbeat FROM sessions WHERE agent_id = ?
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []models.Session
	for rows.Next() {
		var s models.Session
		err := rows.Scan(&s.ID, &s.AgentID, &s.CreatedAt, &s.UpdatedAt, &s.Status, &s.LastHeartbeat)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func (s *Store) CloseSession(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET status = 'closed' WHERE id = ?`, sessionID)
	return err
}

func (s *Store) UpdateSessionUpdatedAt(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	return err
}

// TouchHeartbeat updates the last_heartbeat timestamp for a session.
func (s *Store) TouchHeartbeat(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_heartbeat = CURRENT_TIMESTAMP WHERE id = ? AND status = 'active'`, sessionID)
	return err
}

// ReapStaleSessions marks active sessions as "inactive" if they have not
// received a heartbeat within the given threshold duration.
func (s *Store) ReapStaleSessions(threshold time.Duration) (int64, error) {
	cutoff := time.Now().Add(-threshold)
	result, err := s.db.Exec(`
		UPDATE sessions SET status = 'inactive'
		WHERE status = 'active' AND last_heartbeat IS NOT NULL AND last_heartbeat < ?
	`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CloseTimedOutSessions marks active/inactive sessions as "closed" if their
// last message (updated_at) is older than the given timeout duration.
func (s *Store) CloseTimedOutSessions(timeout time.Duration) (int64, error) {
	cutoff := time.Now().Add(-timeout)
	result, err := s.db.Exec(`
		UPDATE sessions SET status = 'closed'
		WHERE status IN ('active', 'inactive') AND updated_at < ?
	`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// HeartbeatSession finds the active session for an agent and touches its heartbeat.
// Returns the session ID or ErrNotFound.
func (s *Store) HeartbeatSession(agentID string) (string, error) {
	var sessionID string
	err := s.db.QueryRow(`
		SELECT id FROM sessions WHERE agent_id = ? AND status = 'active' ORDER BY created_at DESC LIMIT 1
	`, agentID).Scan(&sessionID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", ErrNotFound
		}
		return "", err
	}
	return sessionID, s.TouchHeartbeat(sessionID)
}

func (s *Store) GetMessageCount(sessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&count)
	return count, err
}

func (s *Store) CountMessages(sessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM messages WHERE session_id = ?
	`, sessionID).Scan(&count)
	return count, err
}

func (s *Store) AppendMessage(sessID string, msg *models.Message, model string, tokensIn, tokensOut int, costUSD float64, latencyMs int) error {
	toolCallsJSON, _ := json.Marshal(msg.ToolCalls)

	_, err := s.db.Exec(`
		INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, cost_usd, latency_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sessID, msg.Role, msg.Content, string(toolCallsJSON), msg.ToolCallID, model, tokensIn, tokensOut, costUSD, latencyMs)
	return err
}

func (s *Store) GetSessionMessages(sessionID string) ([]models.MessageLog, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, cost_usd, latency_ms, created_at
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
		err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCallsStr, &m.ToolCallID, &m.Model, &m.TokensIn, &m.TokensOut, &m.CostUSD, &m.LatencyMs, &m.CreatedAt)
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

func (s *Store) GetMessagesWithToolCalls(sessionID string) ([]models.MessageLog, error) {
	query := `
		SELECT id, session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, cost_usd, latency_ms, created_at
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
		err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCallsStr, &m.ToolCallID, &m.Model, &m.TokensIn, &m.TokensOut, &m.CostUSD, &m.LatencyMs, &m.CreatedAt)
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

func (s *Store) LogAudit(agentID, eventType string, payload any) error {
	payloadBytes, _ := json.Marshal(payload)

	_, err := s.db.Exec(`
		INSERT INTO audit_log (agent_id, event_type, payload, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, agentID, eventType, string(payloadBytes))
	return err
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

// DashboardStats holds aggregate statistics for the dashboard.
type DashboardStats struct {
	AgentCount     int     `json:"agent_count"`
	ActiveSessions int     `json:"active_sessions"`
	TotalMessages  int     `json:"total_messages"`
	TotalCost      float64 `json:"total_cost"`
}

func (s *Store) GetDashboardStats() (*DashboardStats, error) {
	stats := &DashboardStats{}

	s.db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&stats.AgentCount)
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE status = 'active'`).Scan(&stats.ActiveSessions)
	s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&stats.TotalMessages)
	s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM messages`).Scan(&stats.TotalCost)

	return stats, nil
}

func (s *Store) GetSessionCost(sessionID string) (float64, error) {
	var cost float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM messages WHERE session_id = ?`, sessionID).Scan(&cost)
	return cost, err
}

func (s *Store) Close() error {
	return s.db.Close()
}
