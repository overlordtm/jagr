-- +goose Up
-- SQLite does not support ALTER TABLE ... MODIFY CONSTRAINT, so we recreate the table.
CREATE TABLE IF NOT EXISTS memos_new (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(8)))),
    exercise_id TEXT NOT NULL,
    session_id  TEXT,
    host        TEXT,
    scope       TEXT NOT NULL CHECK(scope IN ('agent', 'host', 'exercise')),
    content     TEXT NOT NULL,
    memo_type   TEXT NOT NULL DEFAULT 'observation'
                CHECK(memo_type IN ('observation', 'finding_lead', 'correlation', 'sitrep', 'enrichment', 'system_overview')),
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    agent_name  TEXT
);

INSERT INTO memos_new SELECT id, exercise_id, session_id, host, scope, content, memo_type, created_at, agent_name FROM memos;

DROP INDEX IF EXISTS idx_memos_agent;
DROP INDEX IF EXISTS idx_memos_host;
DROP INDEX IF EXISTS idx_memos_exercise;
DROP TABLE memos;

ALTER TABLE memos_new RENAME TO memos;

CREATE INDEX IF NOT EXISTS idx_memos_agent ON memos(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_memos_host ON memos(exercise_id, host, created_at);
CREATE INDEX IF NOT EXISTS idx_memos_exercise ON memos(exercise_id, scope, created_at);

-- +goose Down
CREATE TABLE IF NOT EXISTS memos_old (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(8)))),
    exercise_id TEXT NOT NULL,
    session_id  TEXT,
    host        TEXT,
    scope       TEXT NOT NULL CHECK(scope IN ('agent', 'host', 'exercise')),
    content     TEXT NOT NULL,
    memo_type   TEXT NOT NULL DEFAULT 'observation'
                CHECK(memo_type IN ('observation', 'finding_lead', 'correlation', 'sitrep', 'enrichment')),
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    agent_name  TEXT
);

INSERT INTO memos_old SELECT id, exercise_id, session_id, host, scope, content, memo_type, created_at, agent_name FROM memos WHERE memo_type != 'system_overview';

DROP INDEX IF EXISTS idx_memos_agent;
DROP INDEX IF EXISTS idx_memos_host;
DROP INDEX IF EXISTS idx_memos_exercise;
DROP TABLE memos;

ALTER TABLE memos_old RENAME TO memos;

CREATE INDEX IF NOT EXISTS idx_memos_agent ON memos(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_memos_host ON memos(exercise_id, host, created_at);
CREATE INDEX IF NOT EXISTS idx_memos_exercise ON memos(exercise_id, scope, created_at);
