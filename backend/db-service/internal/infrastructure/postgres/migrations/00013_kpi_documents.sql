-- +goose Up
-- kpi_documents links a goal to the uploaded documents that describe the
-- plant's own input data for that goal (reports, flowsheets, constraints).
CREATE TABLE IF NOT EXISTS kpi_documents (
    kpi_id      TEXT NOT NULL REFERENCES kpis (id) ON DELETE CASCADE,
    document_id TEXT NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    role        TEXT NOT NULL DEFAULT 'input' CHECK (role IN ('input', 'reference')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kpi_id, document_id)
);
CREATE INDEX IF NOT EXISTS idx_kpi_documents_document ON kpi_documents (document_id);

-- +goose Down
DROP TABLE IF EXISTS kpi_documents;
