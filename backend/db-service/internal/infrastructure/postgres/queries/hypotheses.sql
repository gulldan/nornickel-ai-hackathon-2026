-- name: InsertHypothesis :exec
INSERT INTO hypotheses
  (id, owner_id, run_id, title, statement, rationale, method, status, kpi_id,
   primary_cluster_id, trl, novelty_score, risk_score, value_score, confidence_score,
   composite_score, measurable, organization, function_area, source_type, location,
   tags, assessment, detail, generation, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
        $18, $19, $20, $21, $22, $23, $24, $25, $26, $27);

-- name: GetHypothesis :one
SELECT * FROM hypotheses WHERE id = $1;

-- name: UpdateHypothesis :execrows
UPDATE hypotheses
SET title = $2, statement = $3, rationale = $4, method = $5, status = $6,
    kpi_id = $7, primary_cluster_id = $8, trl = $9, novelty_score = $10,
    risk_score = $11, value_score = $12, confidence_score = $13, composite_score = $14,
    measurable = $15, organization = $16, function_area = $17, source_type = $18,
    location = $19, tags = $20, assessment = $21, detail = $22, generation = $23,
    updated_at = now()
WHERE id = $1;

-- name: ListHypotheses :many
SELECT * FROM hypotheses
WHERE (sqlc.arg('owner_id')::text = '' OR owner_id = sqlc.arg('owner_id'))
  AND (sqlc.arg('status')::text = '' OR status = sqlc.arg('status'))
  AND (sqlc.arg('kpi_id')::text = '' OR kpi_id = sqlc.arg('kpi_id'))
  AND (sqlc.arg('cluster_id')::text = '' OR primary_cluster_id = sqlc.arg('cluster_id'))
  AND (sqlc.arg('function_area')::text = '' OR function_area = sqlc.arg('function_area'))
  AND (sqlc.arg('source_type')::text = '' OR source_type = sqlc.arg('source_type'))
  AND (sqlc.arg('organization')::text = '' OR organization = sqlc.arg('organization'))
  AND (sqlc.arg('min_trl')::int = 0 OR trl >= sqlc.arg('min_trl')::int)
  AND (sqlc.arg('max_trl')::int = 0 OR trl <= sqlc.arg('max_trl')::int)
  AND (cardinality(sqlc.arg('tags')::text[]) = 0 OR tags @> sqlc.arg('tags')::text[])
  AND (cardinality(sqlc.arg('document_ids')::text[]) = 0 OR EXISTS (
    SELECT 1 FROM hypothesis_evidence e
    WHERE e.hypothesis_id = hypotheses.id
      AND e.document_id = ANY(sqlc.arg('document_ids')::text[])))
ORDER BY
  CASE WHEN sqlc.arg('order_by')::text = 'created_at' THEN extract(epoch FROM created_at) END DESC NULLS LAST,
  CASE WHEN sqlc.arg('order_by')::text = 'trl' THEN trl::double precision END DESC NULLS LAST,
  CASE WHEN sqlc.arg('order_by')::text = 'novelty' THEN novelty_score END DESC NULLS LAST,
  CASE WHEN sqlc.arg('order_by')::text = 'value' THEN value_score END DESC NULLS LAST,
  CASE WHEN sqlc.arg('order_by')::text NOT IN ('created_at', 'trl', 'novelty', 'value')
       THEN composite_score END DESC NULLS LAST,
  created_at DESC
LIMIT CASE WHEN sqlc.arg('lim')::int <= 0 THEN NULL ELSE sqlc.arg('lim')::int END
OFFSET sqlc.arg('off')::int;

-- name: InsertEvidence :exec
INSERT INTO hypothesis_evidence
  (id, hypothesis_id, document_id, chunk_id, filename, snippet, stance, score, ord, created_at,
   relation, page_start, page_end, section_heading, origin)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: ListEvidenceByHypothesis :many
SELECT * FROM hypothesis_evidence WHERE hypothesis_id = $1 ORDER BY ord ASC;

-- name: InsertRevision :one
INSERT INTO hypothesis_revisions
  (id, hypothesis_id, revision_no, editor_id, action, summary, patch, created_at)
VALUES ($1, $2,
        (SELECT COALESCE(MAX(revision_no), 0) + 1 FROM hypothesis_revisions WHERE hypothesis_id = $2),
        $3, $4, $5, $6, $7)
RETURNING revision_no;

-- name: ListRevisionsByHypothesis :many
SELECT * FROM hypothesis_revisions WHERE hypothesis_id = $1 ORDER BY revision_no ASC;
