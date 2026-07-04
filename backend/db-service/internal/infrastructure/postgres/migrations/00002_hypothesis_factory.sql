-- Hypothesis Factory schema. Scalar columns are the board's filter/sort
-- projection; assessment/detail/generation JSONB hold the rich structure
-- (mirrors schemas/hypothesis/v1). Enums are TEXT + CHECK. Scores are in
-- [0,1] and NULL until scored (risk: higher = riskier); TRL is 1..9.

-- +goose Up
CREATE TABLE IF NOT EXISTS kpis (
    id            TEXT PRIMARY KEY,
    owner_id      TEXT NOT NULL,
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    metric        TEXT NOT NULL DEFAULT '',
    unit          TEXT NOT NULL DEFAULT '',
    direction     TEXT NOT NULL DEFAULT 'increase'
                  CHECK (direction IN ('increase', 'decrease', 'maintain')),
    baseline      DOUBLE PRECISION,
    target        DOUBLE PRECISION,
    function_area TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    detail        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_kpis_owner ON kpis (owner_id, created_at DESC);

CREATE TABLE IF NOT EXISTS clusters (
    id              TEXT PRIMARY KEY,
    owner_id        TEXT NOT NULL,
    label           TEXT NOT NULL,
    summary         TEXT NOT NULL DEFAULT '',
    keywords        TEXT[] NOT NULL DEFAULT '{}',
    method          TEXT NOT NULL DEFAULT '',
    chunk_count     INTEGER NOT NULL DEFAULT 0,
    document_count  INTEGER NOT NULL DEFAULT 0,
    representatives JSONB NOT NULL DEFAULT '[]'::jsonb,
    params          JSONB NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_clusters_owner ON clusters (owner_id, created_at DESC);

CREATE TABLE IF NOT EXISTS hypotheses (
    id                 TEXT PRIMARY KEY,
    owner_id           TEXT NOT NULL,
    run_id             TEXT NOT NULL DEFAULT '',
    title              TEXT NOT NULL,
    statement          TEXT NOT NULL,
    rationale          TEXT NOT NULL DEFAULT '',
    method             TEXT NOT NULL DEFAULT 'cluster_kpi'
                       CHECK (method IN ('cluster_kpi', 'transfer', 'gap', 'combination', 'manual')),
    status             TEXT NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft', 'generated', 'under_review', 'approved', 'rejected', 'archived')),
    kpi_id             TEXT REFERENCES kpis (id) ON DELETE SET NULL,
    primary_cluster_id TEXT REFERENCES clusters (id) ON DELETE SET NULL,
    trl                SMALLINT CHECK (trl IS NULL OR trl BETWEEN 1 AND 9),
    novelty_score      DOUBLE PRECISION CHECK (novelty_score    IS NULL OR novelty_score    BETWEEN 0 AND 1),
    risk_score         DOUBLE PRECISION CHECK (risk_score       IS NULL OR risk_score       BETWEEN 0 AND 1),
    value_score        DOUBLE PRECISION CHECK (value_score      IS NULL OR value_score      BETWEEN 0 AND 1),
    confidence_score   DOUBLE PRECISION CHECK (confidence_score IS NULL OR confidence_score BETWEEN 0 AND 1),
    composite_score    DOUBLE PRECISION,
    measurable         BOOLEAN NOT NULL DEFAULT TRUE,
    organization       TEXT NOT NULL DEFAULT '',
    function_area      TEXT NOT NULL DEFAULT '',
    source_type        TEXT NOT NULL DEFAULT '',
    location           TEXT NOT NULL DEFAULT '',
    tags               TEXT[] NOT NULL DEFAULT '{}',
    assessment         JSONB NOT NULL DEFAULT '{}'::jsonb,
    detail             JSONB NOT NULL DEFAULT '{}'::jsonb,
    generation         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_hypotheses_owner   ON hypotheses (owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_hypotheses_status  ON hypotheses (owner_id, status);
CREATE INDEX IF NOT EXISTS idx_hypotheses_trl     ON hypotheses (owner_id, trl);
CREATE INDEX IF NOT EXISTS idx_hypotheses_kpi     ON hypotheses (kpi_id);
CREATE INDEX IF NOT EXISTS idx_hypotheses_cluster ON hypotheses (primary_cluster_id);
CREATE INDEX IF NOT EXISTS idx_hypotheses_run     ON hypotheses (run_id) WHERE run_id <> '';
CREATE INDEX IF NOT EXISTS idx_hypotheses_rank    ON hypotheses (owner_id, composite_score DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hypotheses_tags    ON hypotheses USING GIN (tags);

CREATE TABLE IF NOT EXISTS hypothesis_evidence (
    id            TEXT PRIMARY KEY,
    hypothesis_id TEXT NOT NULL REFERENCES hypotheses (id) ON DELETE CASCADE,
    document_id   TEXT REFERENCES documents (id) ON DELETE SET NULL,
    chunk_id      TEXT NOT NULL DEFAULT '',
    filename      TEXT NOT NULL DEFAULT '',
    snippet       TEXT NOT NULL DEFAULT '',
    stance        TEXT NOT NULL DEFAULT 'supports'
                  CHECK (stance IN ('supports', 'contradicts', 'context')),
    score         DOUBLE PRECISION,
    ord           INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_evidence_hypothesis ON hypothesis_evidence (hypothesis_id, ord);
CREATE INDEX IF NOT EXISTS idx_evidence_document   ON hypothesis_evidence (document_id) WHERE document_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS hypothesis_revisions (
    id            TEXT PRIMARY KEY,
    hypothesis_id TEXT NOT NULL REFERENCES hypotheses (id) ON DELETE CASCADE,
    revision_no   INTEGER NOT NULL,
    editor_id     TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL
                  CHECK (action IN ('created', 'edited', 'status_changed', 'score_override', 'approved', 'rejected', 'commented')),
    summary       TEXT NOT NULL DEFAULT '',
    patch         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (hypothesis_id, revision_no)
);
CREATE INDEX IF NOT EXISTS idx_revisions_hypothesis ON hypothesis_revisions (hypothesis_id, revision_no);

-- +goose Down
DROP TABLE IF EXISTS hypothesis_revisions;
DROP TABLE IF EXISTS hypothesis_evidence;
DROP TABLE IF EXISTS hypotheses;
DROP TABLE IF EXISTS clusters;
DROP TABLE IF EXISTS kpis;
