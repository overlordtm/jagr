-- +goose Up
CREATE TABLE IF NOT EXISTS eval_runs (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    config_yaml  TEXT NOT NULL,
    gt_yaml      TEXT NOT NULL,
    started_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE TABLE IF NOT EXISTS eval_sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    eval_run_id TEXT NOT NULL REFERENCES eval_runs(id),
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    variant_id  TEXT NOT NULL,
    repeat_num  INTEGER NOT NULL DEFAULT 1,
    recall      REAL,
    precision   REAL,
    f1          REAL,
    fp_rate     REAL,
    score_json  TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_eval_sessions_run ON eval_sessions(eval_run_id);

-- +goose Down
DROP INDEX IF EXISTS idx_eval_sessions_run;
DROP TABLE IF EXISTS eval_sessions;
DROP TABLE IF EXISTS eval_runs;
