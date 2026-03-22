-- +goose Up
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
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT NOT NULL REFERENCES sessions(id),
    role            TEXT NOT NULL,
    content         TEXT,
    tool_calls      TEXT,
    tool_call_id    TEXT,
    model           TEXT,
    tokens_in       INTEGER,
    tokens_out      INTEGER,
    cost_usd        REAL DEFAULT 0,
    latency_ms      INTEGER,
    sub_agent_role  TEXT DEFAULT '',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    payload     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS session_findings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    finding_id  TEXT NOT NULL,
    type        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    observable  TEXT NOT NULL,
    analysis    TEXT,
    evidence    TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS session_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    content     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS session_agent_configs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    role        TEXT NOT NULL,
    model       TEXT NOT NULL,
    temperature REAL DEFAULT 0,
    top_p       REAL DEFAULT 0,
    top_k       INTEGER DEFAULT 0,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_agent_time ON audit_log(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_findings_session ON session_findings(session_id);
CREATE INDEX IF NOT EXISTS idx_reports_session ON session_reports(session_id);
CREATE INDEX IF NOT EXISTS idx_agent_configs_session ON session_agent_configs(session_id);

-- +goose Down
DROP INDEX IF EXISTS idx_agent_configs_session;
DROP INDEX IF EXISTS idx_reports_session;
DROP INDEX IF EXISTS idx_findings_session;
DROP INDEX IF EXISTS idx_audit_agent_time;
DROP INDEX IF EXISTS idx_messages_session;
DROP TABLE IF EXISTS session_agent_configs;
DROP TABLE IF EXISTS session_reports;
DROP TABLE IF EXISTS session_findings;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS agents;
