package eval

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// RawPath strips a "file:" URI prefix from a DB path (e.g. from --db=file:jagr.db).
func RawPath(path string) string {
	return strings.TrimPrefix(path, "file:")
}

// OpenDB opens the Jagr SQLite database and ensures eval tables exist.
func OpenDB(path string) (*sql.DB, error) {
	// Normalise: strip any user-supplied file: prefix before adding our own.
	// Without "file:" go-sqlite3 treats "?" as part of the filename.
	db, err := sql.Open("sqlite3", "file:"+RawPath(path)+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := initEvalTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init eval tables: %w", err)
	}
	return db, nil
}

// initEvalTables creates the eval_runs and eval_sessions tables if they don't exist.
// This lets the eval tool work against an existing jagr.db that predates migration 00010.
func initEvalTables(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS eval_runs (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			config_yaml  TEXT NOT NULL,
			gt_yaml      TEXT NOT NULL,
			started_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME
		);

		CREATE TABLE IF NOT EXISTS eval_sessions (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			eval_run_id          TEXT NOT NULL REFERENCES eval_runs(id),
			session_id           TEXT NOT NULL,
			variant_id           TEXT NOT NULL,
			repeat_num           INTEGER NOT NULL DEFAULT 1,
			recall               REAL,
			precision            REAL,
			f1                   REAL,
			fp_rate              REAL,
			score_json           TEXT,
			system_overview_memo TEXT,
			created_at           DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_eval_sessions_run ON eval_sessions(eval_run_id);
	`)
	if err != nil {
		return err
	}
	// Add system_overview_memo to pre-existing tables (ALTER TABLE ignores the error if column exists).
	_, _ = db.Exec(`ALTER TABLE eval_sessions ADD COLUMN system_overview_memo TEXT`)
	return nil
}

// CreateEvalRun inserts a new eval_runs row and returns the assigned ID.
func CreateEvalRun(db *sql.DB, id, name, configYAML, gtYAML string) error {
	_, err := db.Exec(
		`INSERT INTO eval_runs (id, name, config_yaml, gt_yaml) VALUES (?, ?, ?, ?)`,
		id, name, configYAML, gtYAML,
	)
	return err
}

// CompleteEvalRun marks an eval run as completed.
func CompleteEvalRun(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE eval_runs SET completed_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// TagSession links a session to an eval run + variant.
func TagSession(db *sql.DB, evalRunID, sessionID, variantID string, repeatNum int) error {
	_, err := db.Exec(
		`INSERT INTO eval_sessions (eval_run_id, session_id, variant_id, repeat_num) VALUES (?, ?, ?, ?)`,
		evalRunID, sessionID, variantID, repeatNum,
	)
	return err
}

// SaveScore updates the eval_sessions row with scored results.
func SaveScore(db *sql.DB, score EvalScore) error {
	_, err := db.Exec(
		`UPDATE eval_sessions SET recall=?, precision=?, f1=?, fp_rate=?, score_json=?
		 WHERE eval_run_id=? AND session_id=? AND variant_id=? AND repeat_num=?`,
		score.Recall, score.Precision, score.F1, score.FPRate, score.ScoreJSON,
		score.EvalRunID, score.SessionID, score.VariantID, score.RepeatNum,
	)
	return err
}

// FindSessionForAgent polls for the most recent session created after `after` for a given hostname.
// Returns ("", nil) if not found yet.
func FindSessionForAgent(db *sql.DB, hostname string, after time.Time) (string, error) {
	row := db.QueryRow(`
		SELECT s.id FROM sessions s
		JOIN agents a ON s.agent_id = a.id
		WHERE a.hostname = ? AND s.created_at > ?
		ORDER BY s.created_at DESC
		LIMIT 1`,
		hostname, after.UTC().Format(time.RFC3339),
	)
	var id string
	if err := row.Scan(&id); err == sql.ErrNoRows {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return id, nil
}

// WaitForSessionClose polls until the session transitions out of 'active' status or timeout.
func WaitForSessionClose(db *sql.DB, sessionID string, timeout time.Duration) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		row := db.QueryRow(`SELECT status, updated_at FROM sessions WHERE id = ?`, sessionID)
		var status, updatedAt string
		if err := row.Scan(&status, &updatedAt); err != nil {
			return time.Time{}, err
		}
		if status != "active" {
			t, _ := time.Parse(time.RFC3339, updatedAt)
			return t, nil
		}
		time.Sleep(2 * time.Second)
	}
	return time.Time{}, fmt.Errorf("timeout waiting for session %s to close", sessionID)
}

// GetSessionMetrics aggregates token, cost, latency, and timing data for a session.
func GetSessionMetrics(db *sql.DB, sessionID string) (RunMetrics, error) {
	row := db.QueryRow(`
		SELECT
			COALESCE(SUM(tokens_in), 0),
			COALESCE(SUM(tokens_out), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(AVG(latency_ms), 0),
			s.created_at,
			s.updated_at
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE m.session_id = ?
		GROUP BY s.id`,
		sessionID,
	)

	var m RunMetrics
	var createdAt, updatedAt string
	if err := row.Scan(&m.TokensIn, &m.TokensOut, &m.TotalCostUSD, &m.AvgLatencyMs, &createdAt, &updatedAt); err != nil {
		return m, fmt.Errorf("metrics query: %w", err)
	}

	t1, _ := time.Parse(time.RFC3339, createdAt)
	t2, _ := time.Parse(time.RFC3339, updatedAt)
	m.DurationSec = t2.Sub(t1).Seconds()

	return m, nil
}

// GetSessionFindings returns all findings for a session.
func GetSessionFindings(db *sql.DB, sessionID string) ([]DBFinding, error) {
	rows, err := db.Query(`
		SELECT finding_id, type, severity, observable, COALESCE(analysis, '')
		FROM session_findings WHERE session_id = ?`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var findings []DBFinding
	for rows.Next() {
		var f DBFinding
		if err := rows.Scan(&f.FindingID, &f.Type, &f.Severity, &f.Observable, &f.Analysis); err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

// FetchSystemOverviewMemo returns the content of the system_overview memo written during sessionID,
// or ("", nil) if none exists.
func FetchSystemOverviewMemo(db *sql.DB, sessionID string) (string, error) {
	row := db.QueryRow(`
		SELECT content FROM memos
		WHERE session_id = ? AND memo_type = 'system_overview'
		ORDER BY created_at DESC LIMIT 1`, sessionID)
	var content string
	if err := row.Scan(&content); err == sql.ErrNoRows {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return content, nil
}

// DeleteSystemOverviewMemos removes all system_overview memos for a session so they don't
// leak into subsequent variant runs that share the same exercise_id / host.
func DeleteSystemOverviewMemos(db *sql.DB, sessionID string) error {
	_, err := db.Exec(`DELETE FROM memos WHERE session_id = ? AND memo_type = 'system_overview'`, sessionID)
	return err
}

// SaveSystemOverviewMemo stores the captured system_overview memo content in eval_sessions.
func SaveSystemOverviewMemo(db *sql.DB, evalRunID, sessionID, variantID string, repeatNum int, content string) error {
	_, err := db.Exec(
		`UPDATE eval_sessions SET system_overview_memo = ?
		 WHERE eval_run_id = ? AND session_id = ? AND variant_id = ? AND repeat_num = ?`,
		content, evalRunID, sessionID, variantID, repeatNum,
	)
	return err
}

// GetEvalResults loads all variant results for a completed eval run from the DB.
func GetEvalResults(db *sql.DB, evalRunID string) ([]VariantResult, error) {
	rows, err := db.Query(`
		SELECT es.session_id, es.variant_id, es.repeat_num,
		       es.recall, es.precision, es.f1, es.fp_rate, es.score_json,
		       COALESCE(es.system_overview_memo, '')
		FROM eval_sessions es
		WHERE es.eval_run_id = ?
		ORDER BY es.variant_id, es.repeat_num`,
		evalRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []VariantResult
	for rows.Next() {
		var r VariantResult
		var scoreJSON string
		var recall, precision, f1, fpRate sql.NullFloat64
		if err := rows.Scan(&r.SessionID, &r.VariantID, &r.RepeatNum,
			&recall, &precision, &f1, &fpRate, &scoreJSON, &r.SystemOverview); err != nil {
			return nil, err
		}
		r.Score = FindingScore{
			Recall:    recall.Float64,
			Precision: precision.Float64,
			F1:        f1.Float64,
			FPRate:    fpRate.Float64,
		}
		if scoreJSON != "" {
			_ = json.Unmarshal([]byte(scoreJSON), &r.Score)
		}
		m, err := GetSessionMetrics(db, r.SessionID)
		if err == nil {
			r.Metrics = m
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
