-- +goose Up
ALTER TABLE session_findings ADD COLUMN status TEXT NOT NULL DEFAULT 'preliminary';

-- +goose Down
