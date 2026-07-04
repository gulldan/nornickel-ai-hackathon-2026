-- Owner-scope content-hash dedup. The lookup now matches (owner_id, content_hash)
-- so a tenant's upload can only link to its own document; replace the global
-- single-column hash index with a composite one supporting that lookup. This is
-- not a UNIQUE constraint: re-finalize/retries with the same (owner, hash) must
-- still insert.

-- +goose Up
DROP INDEX IF EXISTS idx_documents_hash;
CREATE INDEX IF NOT EXISTS idx_documents_owner_hash
    ON documents (owner_id, content_hash) WHERE content_hash <> '';

-- +goose Down
DROP INDEX IF EXISTS idx_documents_owner_hash;
CREATE INDEX IF NOT EXISTS idx_documents_hash ON documents (content_hash) WHERE content_hash <> '';
