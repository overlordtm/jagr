-- +goose Up
ALTER TABLE memos ADD COLUMN agent_name TEXT;

-- +goose Down
-- SQLite does not support ALTER TABLE DROP COLUMN easily, 
-- but for migrations we can just leave it or recreate the table if needed.
-- Since this is an addition, we can just leave it for now.
