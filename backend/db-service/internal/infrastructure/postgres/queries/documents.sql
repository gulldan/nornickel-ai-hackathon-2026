-- name: CreateDocument :exec
INSERT INTO documents
  (id, owner_id, filename, mime_type, size, object_key, status, status_msg,
   chunk_count, created_at, updated_at, content_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetDocument :one
SELECT * FROM documents WHERE id = $1;

-- name: FindDocumentByHash :one
SELECT * FROM documents
WHERE content_hash = sqlc.arg('content_hash')
  AND (sqlc.arg('owner_id')::text = '' OR owner_id = sqlc.arg('owner_id'))
  AND status <> 'failed'
ORDER BY chunk_count DESC, created_at ASC
LIMIT 1;

-- name: ListDocumentsByOwner :many
SELECT * FROM documents
WHERE (sqlc.arg('owner_id')::text = '' OR owner_id = sqlc.arg('owner_id'))
ORDER BY created_at DESC;

-- name: UpdateDocumentStatus :exec
UPDATE documents
SET status = sqlc.arg('status'),
    status_msg = sqlc.arg('status_msg'),
    chunk_count = COALESCE(sqlc.narg('chunk_count'), chunk_count),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: UpdateDocumentTitle :exec
UPDATE documents
SET title = sqlc.arg('title'),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: UpdateDocumentKind :exec
UPDATE documents
SET kind = sqlc.arg('kind'),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: UpdateDocumentMeta :exec
UPDATE documents
SET author = sqlc.arg('author'),
    published_at = sqlc.arg('published_at'),
    source_ref = sqlc.arg('source_ref'),
    updated_at = now()
WHERE id = sqlc.arg('id');
