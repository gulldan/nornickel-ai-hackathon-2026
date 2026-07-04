-- Evidence provenance for grounded citations: a per-fragment relation comment
-- (how the snippet relates to the hypothesis, with numbers) and the source
-- location (page span + section) so the UI can jump to the exact place in the
-- original document. All default-empty/zero, so existing rows stay valid.

-- +goose Up
ALTER TABLE hypothesis_evidence ADD COLUMN IF NOT EXISTS relation        TEXT    NOT NULL DEFAULT '';
ALTER TABLE hypothesis_evidence ADD COLUMN IF NOT EXISTS page_start      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hypothesis_evidence ADD COLUMN IF NOT EXISTS page_end        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hypothesis_evidence ADD COLUMN IF NOT EXISTS section_heading TEXT    NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE hypothesis_evidence DROP COLUMN IF EXISTS section_heading;
ALTER TABLE hypothesis_evidence DROP COLUMN IF EXISTS page_end;
ALTER TABLE hypothesis_evidence DROP COLUMN IF EXISTS page_start;
ALTER TABLE hypothesis_evidence DROP COLUMN IF EXISTS relation;
