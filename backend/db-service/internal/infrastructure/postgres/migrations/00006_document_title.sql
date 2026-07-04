-- Adds the real article title extracted from a document's text (LLM, at
-- indexing time, and by the one-shot backfill). Empty string means "not yet
-- extracted" — the UI falls back to the filename. Added at the end of the table
-- via ALTER so sqlc places it last in the generated row struct.

-- +goose Up
ALTER TABLE documents ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE documents DROP COLUMN IF EXISTS title;
