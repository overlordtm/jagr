package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
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

	// SQLite performance tuning
	pragmas := []string{
		"PRAGMA journal_mode=WAL",          // write-ahead logging: concurrent reads during writes
		"PRAGMA synchronous=NORMAL",        // safe with WAL, much faster than FULL
		"PRAGMA cache_size=-64000",         // 64MB page cache (negative = KiB)
		"PRAGMA busy_timeout=5000",         // wait up to 5s on lock instead of failing immediately
		"PRAGMA foreign_keys=ON",           // enforce FK constraints
		"PRAGMA temp_store=MEMORY",         // keep temp tables in memory
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("setting %s: %w", p, err)
		}
	}

	store := &Store{db: db, log: log}

	if err := store.migrate(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *Store) migrate() error {
	goose.SetBaseFS(Migrations)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}

	return goose.Up(s.db, "migrations")
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
	err := s.db.QueryRow(`SELECT id, agent_id, created_at, updated_at, status, last_heartbeat, error FROM sessions WHERE id = ?`, sessionID).Scan(
		&sess.ID, &sess.AgentID, &sess.CreatedAt, &sess.UpdatedAt, &sess.Status, &sess.LastHeartbeat, &sess.Error,
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
		SELECT id, agent_id, created_at, updated_at, status, last_heartbeat, error FROM sessions
		WHERE agent_id = ? AND status = 'active' ORDER BY created_at DESC LIMIT 1
	`, agentID).Scan(&sess.ID, &sess.AgentID, &sess.CreatedAt, &sess.UpdatedAt, &sess.Status, &sess.LastHeartbeat, &sess.Error)
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
		SELECT id, agent_id, created_at, updated_at, status, last_heartbeat, error FROM sessions WHERE agent_id = ?
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []models.Session
	for rows.Next() {
		var s models.Session
		err := rows.Scan(&s.ID, &s.AgentID, &s.CreatedAt, &s.UpdatedAt, &s.Status, &s.LastHeartbeat, &s.Error)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func (s *Store) CloseSession(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET status = 'completed', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	return err
}

func (s *Store) CloseSessionWithError(sessionID, errMsg string) error {
	_, err := s.db.Exec(`UPDATE sessions SET status = 'error', error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, errMsg, sessionID)
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

func (s *Store) GetMessageCountByRole(sessionID, subAgentRole string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ? AND sub_agent_role = ?`, sessionID, subAgentRole).Scan(&count)
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
		INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, cost_usd, latency_ms, sub_agent_role)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sessID, msg.Role, msg.Content, string(toolCallsJSON), msg.ToolCallID, model, tokensIn, tokensOut, costUSD, latencyMs, msg.SubAgentRole)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessID)
	return err
}

func (s *Store) GetSessionMessages(sessionID string) ([]models.MessageLog, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, cost_usd, latency_ms, sub_agent_role, created_at
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
		var subAgentRole sql.NullString
		err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCallsStr, &m.ToolCallID, &m.Model, &m.TokensIn, &m.TokensOut, &m.CostUSD, &m.LatencyMs, &subAgentRole, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		if toolCallsStr.Valid {
			m.ToolCalls = toolCallsStr.String
		}
		if subAgentRole.Valid {
			m.SubAgentRole = subAgentRole.String
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *Store) GetMessagesWithToolCalls(sessionID string) ([]models.MessageLog, error) {
	query := `
		SELECT id, session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, cost_usd, latency_ms, sub_agent_role, created_at
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
		var subAgentRole sql.NullString
		err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCallsStr, &m.ToolCallID, &m.Model, &m.TokensIn, &m.TokensOut, &m.CostUSD, &m.LatencyMs, &subAgentRole, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		if toolCallsStr.Valid {
			m.ToolCalls = toolCallsStr.String
		}
		if subAgentRole.Valid {
			m.SubAgentRole = subAgentRole.String
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *Store) GetSessionMessagesPaginated(sessionID string, limit, offset int) ([]models.MessageLog, int, error) {
	var total int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(`
		SELECT id, session_id, role, content, tool_calls, tool_call_id, model, tokens_in, tokens_out, cost_usd, latency_ms, sub_agent_role, created_at
		FROM messages WHERE session_id = ? ORDER BY created_at ASC LIMIT ? OFFSET ?
	`, sessionID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var messages []models.MessageLog
	for rows.Next() {
		var m models.MessageLog
		var toolCallsStr sql.NullString
		var subAgentRole sql.NullString
		err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCallsStr, &m.ToolCallID, &m.Model, &m.TokensIn, &m.TokensOut, &m.CostUSD, &m.LatencyMs, &subAgentRole, &m.CreatedAt)
		if err != nil {
			return nil, 0, err
		}
		if toolCallsStr.Valid {
			m.ToolCalls = toolCallsStr.String
		}
		if subAgentRole.Valid {
			m.SubAgentRole = subAgentRole.String
		}
		messages = append(messages, m)
	}
	return messages, total, rows.Err()
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

// --- Findings ---

func (s *Store) AddFinding(sessionID string, f *models.SessionFinding) error {
	status := f.Status
	if status == "" {
		status = "preliminary"
	}
	_, err := s.db.Exec(`
		INSERT INTO session_findings (session_id, finding_id, type, severity, observable, analysis, evidence, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, sessionID, f.FindingID, f.Type, f.Severity, f.Observable, f.Analysis, f.Evidence, status)
	return err
}

func (s *Store) UpdateFindingStatus(sessionID, findingID, status string) error {
	_, err := s.db.Exec(`
		UPDATE session_findings SET status = ? WHERE session_id = ? AND finding_id = ?
	`, status, sessionID, findingID)
	return err
}

func (s *Store) GetSessionFindings(sessionID string) ([]models.SessionFinding, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, finding_id, type, severity, observable, analysis, evidence, status, created_at
		FROM session_findings WHERE session_id = ? ORDER BY created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var findings []models.SessionFinding
	for rows.Next() {
		var f models.SessionFinding
		if err := rows.Scan(&f.ID, &f.SessionID, &f.FindingID, &f.Type, &f.Severity, &f.Observable, &f.Analysis, &f.Evidence, &f.Status, &f.CreatedAt); err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

func (s *Store) GetFindingsForAgent(agentID string) ([]models.SessionFinding, error) {
	rows, err := s.db.Query(`
		SELECT f.id, f.session_id, f.finding_id, f.type, f.severity, f.observable, f.analysis, f.evidence, f.status, f.created_at
		FROM session_findings f
		JOIN sessions s ON f.session_id = s.id
		WHERE s.agent_id = ?
		ORDER BY f.created_at DESC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var findings []models.SessionFinding
	for rows.Next() {
		var f models.SessionFinding
		if err := rows.Scan(&f.ID, &f.SessionID, &f.FindingID, &f.Type, &f.Severity, &f.Observable, &f.Analysis, &f.Evidence, &f.Status, &f.CreatedAt); err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

func (s *Store) GetSessionFindingCount(sessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM session_findings WHERE session_id = ?`, sessionID).Scan(&count)
	return count, err
}

// --- Reports ---

func (s *Store) AddReport(sessionID string, content string) error {
	_, err := s.db.Exec(`
		INSERT INTO session_reports (session_id, content) VALUES (?, ?)
	`, sessionID, content)
	return err
}

func (s *Store) GetSessionReport(sessionID string) (*models.SessionReport, error) {
	var r models.SessionReport
	err := s.db.QueryRow(`
		SELECT id, session_id, content, created_at FROM session_reports WHERE session_id = ? ORDER BY created_at DESC LIMIT 1
	`, sessionID).Scan(&r.ID, &r.SessionID, &r.Content, &r.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (s *Store) GetReportsForAgent(agentID string) ([]models.SessionReport, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.session_id, r.content, r.created_at
		FROM session_reports r
		JOIN sessions s ON r.session_id = s.id
		WHERE s.agent_id = ?
		ORDER BY r.created_at DESC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reports []models.SessionReport
	for rows.Next() {
		var r models.SessionReport
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Content, &r.CreatedAt); err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return reports, rows.Err()
}

func (s *Store) HasReport(sessionID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM session_reports WHERE session_id = ?`, sessionID).Scan(&count)
	return count > 0, err
}

// --- Agent Configs ---

func (s *Store) AddAgentConfig(sessionID string, c *models.SessionAgentConfig) error {
	_, err := s.db.Exec(`
		INSERT INTO session_agent_configs (session_id, role, model, temperature, top_p, top_k)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, role) DO UPDATE SET
			model = excluded.model,
			temperature = excluded.temperature,
			top_p = excluded.top_p,
			top_k = excluded.top_k
	`, sessionID, c.Role, c.Model, c.Temperature, c.TopP, c.TopK)
	return err
}

func (s *Store) UpdateAgentConfigUpstream(sessionID, role, modelAlias, actualModel, provider string) error {
	_, err := s.db.Exec(`
		INSERT INTO session_agent_configs (session_id, role, model, actual_model, provider)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id, role) DO UPDATE SET
			actual_model = excluded.actual_model,
			provider = excluded.provider
	`, sessionID, role, modelAlias, actualModel, provider)
	return err
}

func (s *Store) GetSessionAgentConfigs(sessionID string) ([]models.SessionAgentConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, role, model, actual_model, provider, temperature, top_p, top_k, created_at
		FROM session_agent_configs WHERE session_id = ? ORDER BY role ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []models.SessionAgentConfig
	for rows.Next() {
		var c models.SessionAgentConfig
		if err := rows.Scan(&c.ID, &c.SessionID, &c.Role, &c.Model, &c.ActualModel, &c.Provider, &c.Temperature, &c.TopP, &c.TopK, &c.CreatedAt); err != nil {
			return nil, err
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

func (s *Store) GetSessionUpstreamModel(sessionID string) (string, error) {
	var model string
	err := s.db.QueryRow(`
		SELECT actual_model FROM session_agent_configs
		WHERE session_id = ? AND actual_model != ''
		LIMIT 1
	`, sessionID).Scan(&model)
	if err != nil {
		return "", err
	}
	return model, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
