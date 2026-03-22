-- +goose Up
-- Deduplicate existing records by keeping the one with the highest ID for each role in a session
DELETE FROM session_agent_configs
WHERE id NOT IN (
    SELECT MAX(id)
    FROM session_agent_configs
    GROUP BY session_id, role
);

-- Add the new columns
ALTER TABLE session_agent_configs ADD COLUMN actual_model TEXT DEFAULT '';
ALTER TABLE session_agent_configs ADD COLUMN provider TEXT DEFAULT '';

-- Add a unique constraint (index) to support UPSERT
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_configs_unique ON session_agent_configs(session_id, role);

-- +goose Down
DROP INDEX IF EXISTS idx_agent_configs_unique;
