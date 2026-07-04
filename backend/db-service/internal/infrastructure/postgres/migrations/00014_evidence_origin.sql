-- +goose Up
-- origin marks where an evidence fragment came from relative to the goal:
-- '' (legacy) | 'input' (goal's own documents) | 'knowledge' | 'web'.
ALTER TABLE hypothesis_evidence ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE hypothesis_evidence DROP COLUMN IF EXISTS origin;
