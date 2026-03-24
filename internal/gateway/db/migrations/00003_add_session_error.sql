-- +goose Up
ALTER TABLE sessions ADD COLUMN error TEXT DEFAULT '';

-- +goose Down
