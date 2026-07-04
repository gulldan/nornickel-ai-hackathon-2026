-- name: CreateCluster :exec
INSERT INTO clusters
  (id, owner_id, label, summary, keywords, method, chunk_count, document_count,
   representatives, params, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetCluster :one
SELECT * FROM clusters WHERE id = $1;

-- name: ListClustersByOwner :many
SELECT * FROM clusters WHERE owner_id = $1 ORDER BY created_at DESC;

-- name: UpdateCluster :execrows
UPDATE clusters
SET label = $2, summary = $3, keywords = $4, method = $5, chunk_count = $6,
    document_count = $7, representatives = $8, params = $9, status = $10,
    updated_at = now()
WHERE id = $1;

-- name: DeleteClustersByOwner :execrows
DELETE FROM clusters WHERE owner_id = $1;
