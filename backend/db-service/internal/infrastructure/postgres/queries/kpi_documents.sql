-- name: AttachKpiDocument :exec
INSERT INTO kpi_documents (kpi_id, document_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (kpi_id, document_id) DO UPDATE SET role = EXCLUDED.role;

-- name: ListKpiDocuments :many
SELECT sqlc.embed(d), kd.role, kd.created_at AS attached_at
FROM kpi_documents kd
JOIN documents d ON d.id = kd.document_id
WHERE kd.kpi_id = $1
ORDER BY kd.created_at ASC;

-- name: DetachKpiDocument :exec
DELETE FROM kpi_documents WHERE kpi_id = $1 AND document_id = $2;
