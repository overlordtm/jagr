-- +goose Up
ALTER TABLE messages ADD COLUMN tokens_thinking INTEGER DEFAULT 0;
ALTER TABLE messages ADD COLUMN reasoning_content TEXT DEFAULT '';

-- +goose Down
ALTER TABLE messages DROP COLUMN reasoning_content;
ALTER TABLE messages DROP COLUMN tokens_thinking;
