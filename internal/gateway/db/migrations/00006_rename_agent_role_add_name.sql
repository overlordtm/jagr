-- +goose Up
ALTER TABLE messages RENAME COLUMN sub_agent_role TO agent_role;
ALTER TABLE messages ADD COLUMN agent_name TEXT DEFAULT '';

-- +goose Down
ALTER TABLE messages RENAME COLUMN agent_role TO sub_agent_role;
ALTER TABLE messages DROP COLUMN agent_name;
