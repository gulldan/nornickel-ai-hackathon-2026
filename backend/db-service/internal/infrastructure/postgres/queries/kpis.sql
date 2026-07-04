-- name: CreateKPI :exec
INSERT INTO kpis
  (id, owner_id, title, description, metric, unit, direction, baseline, target,
   function_area, status, detail, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: GetKPI :one
SELECT * FROM kpis WHERE id = $1;

-- name: ListKPIsByOwner :many
SELECT * FROM kpis WHERE owner_id = $1 ORDER BY created_at DESC;

-- name: UpdateKPI :execrows
UPDATE kpis
SET title = $2, description = $3, metric = $4, unit = $5, direction = $6,
    baseline = $7, target = $8, function_area = $9, status = $10, detail = $11,
    updated_at = now()
WHERE id = $1;

-- name: DeleteKPI :execrows
DELETE FROM kpis WHERE id = $1;
