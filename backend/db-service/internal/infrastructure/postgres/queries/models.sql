-- name: ListModels :many
SELECT * FROM models ORDER BY id;

-- name: UpsertModel :exec
INSERT INTO models (id, name, role, backend) VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE
  SET name = EXCLUDED.name, role = EXCLUDED.role, backend = EXCLUDED.backend;
