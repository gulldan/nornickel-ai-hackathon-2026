-- Core RAG schema: users, documents, chats/messages, models. Ids are TEXT (UUID
-- strings from Go) to keep the adapter free of PG extensions. Applied by goose
-- on startup; sqlc reads these files as the schema.
--
-- The IF NOT EXISTS guards let goose baseline older local databases where these
-- core tables were created before goose version tracking was introduced.

-- +goose Up
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    roles         TEXT[] NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS documents (
    id           TEXT PRIMARY KEY,
    owner_id     TEXT NOT NULL,
    filename     TEXT NOT NULL DEFAULT '',
    mime_type    TEXT NOT NULL DEFAULT '',
    size         BIGINT NOT NULL DEFAULT 0,
    object_key   TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'uploaded',
    status_msg   TEXT NOT NULL DEFAULT '',
    chunk_count  INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- BLAKE3-256 hex of the file bytes ("" = unknown); the dedup lookup key.
    content_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_documents_owner ON documents (owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_documents_hash ON documents (content_hash) WHERE content_hash <> '';

CREATE TABLE IF NOT EXISTS chats (
    id         TEXT PRIMARY KEY,
    owner_id   TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT 'New chat',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_chats_owner ON chats (owner_id, created_at DESC);

CREATE TABLE IF NOT EXISTS chat_messages (
    id         TEXT PRIMARY KEY,
    chat_id    TEXT NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    sources    JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_messages_chat ON chat_messages (chat_id, created_at ASC);

CREATE TABLE IF NOT EXISTS models (
    id      TEXT PRIMARY KEY,
    name    TEXT NOT NULL,
    role    TEXT NOT NULL,
    backend TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS chat_messages;
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS models;
DROP TABLE IF EXISTS users;
