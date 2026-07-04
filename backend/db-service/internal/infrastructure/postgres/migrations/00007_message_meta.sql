-- Adds the raw-JSON provenance envelope of a chat message ({model, cached,
-- trace}, written for assistant turns). '{}' means "no provenance" — user
-- messages and legacy rows. Added at the end of the table via ALTER so sqlc
-- places it last in the generated row struct.

-- +goose Up
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS meta JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE chat_messages DROP COLUMN IF EXISTS meta;
