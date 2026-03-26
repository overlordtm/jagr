-- +goose Up
CREATE TABLE IF NOT EXISTS memos (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(8)))),
    exercise_id TEXT NOT NULL,
    session_id  TEXT,
    host        TEXT,
    scope       TEXT NOT NULL CHECK(scope IN ('agent', 'host', 'exercise')),
    content     TEXT NOT NULL,
    memo_type   TEXT NOT NULL DEFAULT 'observation'
                CHECK(memo_type IN ('observation', 'finding_lead', 'correlation', 'sitrep', 'enrichment')),
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_memos_agent ON memos(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_memos_host ON memos(exercise_id, host, created_at);
CREATE INDEX IF NOT EXISTS idx_memos_exercise ON memos(exercise_id, scope, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_memos_exercise;
DROP INDEX IF EXISTS idx_memos_host;
DROP INDEX IF EXISTS idx_memos_agent;
DROP TABLE IF EXISTS memos;
