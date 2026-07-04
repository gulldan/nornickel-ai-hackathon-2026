package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/example/db-service/internal/domain"
	"github.com/example/db-service/internal/infrastructure/postgres/sqlcgen"
)

// ---- KPIRepo ----

// KPIRepo implements domain.KPIRepository over sqlc.
type KPIRepo struct{ q *sqlcgen.Queries }

// NewKPIRepo builds a KPIRepo.
func NewKPIRepo(db *DB) *KPIRepo { return &KPIRepo{q: sqlcgen.New(db.Pool)} }

func kpiFromRow(k sqlcgen.Kpi) *domain.KPI {
	return &domain.KPI{
		ID: k.ID, OwnerID: k.OwnerID, Title: k.Title, Description: k.Description, Metric: k.Metric,
		Unit: k.Unit, Direction: k.Direction, Baseline: k.Baseline, Target: k.Target,
		FunctionArea: k.FunctionArea, Status: k.Status, Detail: k.Detail,
		CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
	}
}

// Create inserts a KPI.
func (r *KPIRepo) Create(ctx context.Context, k *domain.KPI) error {
	err := r.q.CreateKPI(ctx, sqlcgen.CreateKPIParams{
		ID: k.ID, OwnerID: k.OwnerID, Title: k.Title, Description: k.Description, Metric: k.Metric,
		Unit: k.Unit, Direction: k.Direction, Baseline: k.Baseline, Target: k.Target,
		FunctionArea: k.FunctionArea, Status: k.Status, Detail: jsonbOr(k.Detail, "{}"),
		CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("insert kpi: %w", err)
	}
	return nil
}

// Get fetches a KPI by id or domain.ErrNotFound.
func (r *KPIRepo) Get(ctx context.Context, id string) (*domain.KPI, error) {
	k, err := r.q.GetKPI(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select kpi: %w", err)
	}
	return kpiFromRow(k), nil
}

// ListByOwner returns an owner's KPIs, newest first.
func (r *KPIRepo) ListByOwner(ctx context.Context, ownerID string) ([]*domain.KPI, error) {
	rows, err := r.q.ListKPIsByOwner(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list kpis: %w", err)
	}
	out := make([]*domain.KPI, 0, len(rows))
	for i := range rows {
		out = append(out, kpiFromRow(rows[i]))
	}
	return out, nil
}

// Update persists a KPI's mutable fields or returns domain.ErrNotFound.
func (r *KPIRepo) Update(ctx context.Context, k *domain.KPI) error {
	n, err := r.q.UpdateKPI(ctx, sqlcgen.UpdateKPIParams{
		ID: k.ID, Title: k.Title, Description: k.Description, Metric: k.Metric, Unit: k.Unit,
		Direction: k.Direction, Baseline: k.Baseline, Target: k.Target, FunctionArea: k.FunctionArea,
		Status: k.Status, Detail: jsonbOr(k.Detail, "{}"),
	})
	if err != nil {
		return fmt.Errorf("update kpi: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes a KPI or returns domain.ErrNotFound. Hypotheses that pointed
// at it keep living with kpi_id nulled by the FK (ON DELETE SET NULL).
func (r *KPIRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteKPI(ctx, id)
	if err != nil {
		return fmt.Errorf("delete kpi: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// AttachDocument links a document to a goal (upsert on the pair).
func (r *KPIRepo) AttachDocument(ctx context.Context, kpiID, documentID, role string) error {
	err := r.q.AttachKpiDocument(ctx, sqlcgen.AttachKpiDocumentParams{KpiID: kpiID, DocumentID: documentID, Role: role})
	if err != nil {
		return fmt.Errorf("attach kpi document: %w", err)
	}
	return nil
}

// ListDocuments returns a goal's attached documents, oldest link first.
func (r *KPIRepo) ListDocuments(ctx context.Context, kpiID string) ([]*domain.KPIDocumentLink, error) {
	rows, err := r.q.ListKpiDocuments(ctx, kpiID)
	if err != nil {
		return nil, fmt.Errorf("list kpi documents: %w", err)
	}
	out := make([]*domain.KPIDocumentLink, 0, len(rows))
	for i := range rows {
		out = append(out, &domain.KPIDocumentLink{
			Document:   documentFromRow(rows[i].Document),
			Role:       rows[i].Role,
			AttachedAt: rows[i].AttachedAt,
		})
	}
	return out, nil
}

// DetachDocument unlinks a document from a goal (no-op when absent).
func (r *KPIRepo) DetachDocument(ctx context.Context, kpiID, documentID string) error {
	err := r.q.DetachKpiDocument(ctx, sqlcgen.DetachKpiDocumentParams{KpiID: kpiID, DocumentID: documentID})
	if err != nil {
		return fmt.Errorf("detach kpi document: %w", err)
	}
	return nil
}

// ---- ClusterRepo ----

// ClusterRepo implements domain.ClusterRepository over sqlc.
type ClusterRepo struct{ q *sqlcgen.Queries }

// NewClusterRepo builds a ClusterRepo.
func NewClusterRepo(db *DB) *ClusterRepo { return &ClusterRepo{q: sqlcgen.New(db.Pool)} }

func clusterFromRow(c sqlcgen.Cluster) *domain.Cluster {
	return &domain.Cluster{
		ID: c.ID, OwnerID: c.OwnerID, Label: c.Label, Summary: c.Summary, Keywords: c.Keywords,
		Method: c.Method, ChunkCount: int(c.ChunkCount), DocumentCount: int(c.DocumentCount),
		Representatives: c.Representatives, Params: c.Params, Status: c.Status,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// Create inserts a cluster.
func (r *ClusterRepo) Create(ctx context.Context, c *domain.Cluster) error {
	err := r.q.CreateCluster(ctx, sqlcgen.CreateClusterParams{
		ID: c.ID, OwnerID: c.OwnerID, Label: c.Label, Summary: c.Summary, Keywords: orEmpty(c.Keywords),
		Method: c.Method, ChunkCount: int32(c.ChunkCount), DocumentCount: int32(c.DocumentCount),
		Representatives: jsonbOr(c.Representatives, "[]"), Params: jsonbOr(c.Params, "{}"), Status: c.Status,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("insert cluster: %w", err)
	}
	return nil
}

// Get fetches a cluster by id or domain.ErrNotFound.
func (r *ClusterRepo) Get(ctx context.Context, id string) (*domain.Cluster, error) {
	c, err := r.q.GetCluster(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select cluster: %w", err)
	}
	return clusterFromRow(c), nil
}

// ListByOwner returns an owner's clusters, newest first.
func (r *ClusterRepo) ListByOwner(ctx context.Context, ownerID string) ([]*domain.Cluster, error) {
	rows, err := r.q.ListClustersByOwner(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	out := make([]*domain.Cluster, 0, len(rows))
	for i := range rows {
		out = append(out, clusterFromRow(rows[i]))
	}
	return out, nil
}

// Update persists a cluster's mutable fields or returns domain.ErrNotFound.
func (r *ClusterRepo) Update(ctx context.Context, c *domain.Cluster) error {
	n, err := r.q.UpdateCluster(ctx, sqlcgen.UpdateClusterParams{
		ID: c.ID, Label: c.Label, Summary: c.Summary, Keywords: orEmpty(c.Keywords), Method: c.Method,
		ChunkCount: int32(c.ChunkCount), DocumentCount: int32(c.DocumentCount),
		Representatives: jsonbOr(c.Representatives, "[]"), Params: jsonbOr(c.Params, "{}"), Status: c.Status,
	})
	if err != nil {
		return fmt.Errorf("update cluster: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteByOwner removes all of an owner's clusters and returns the count.
// Hypotheses are retained: hypotheses.primary_cluster_id is FK ON DELETE SET NULL.
func (r *ClusterRepo) DeleteByOwner(ctx context.Context, ownerID string) (int64, error) {
	n, err := r.q.DeleteClustersByOwner(ctx, ownerID)
	if err != nil {
		return 0, fmt.Errorf("delete clusters: %w", err)
	}
	return n, nil
}

// ---- HypothesisRepo ----

// HypothesisRepo implements domain.HypothesisRepository over sqlc. It keeps the
// pool to open transactions (Create/Update commit a hypothesis with its evidence
// and revision atomically via Queries.WithTx).
type HypothesisRepo struct {
	db *DB
	q  *sqlcgen.Queries
}

// NewHypothesisRepo builds a HypothesisRepo.
func NewHypothesisRepo(db *DB) *HypothesisRepo {
	return &HypothesisRepo{db: db, q: sqlcgen.New(db.Pool)}
}

func hypothesisFromRow(h sqlcgen.Hypothesis) *domain.Hypothesis {
	return &domain.Hypothesis{
		ID: h.ID, OwnerID: h.OwnerID, RunID: h.RunID, Title: h.Title, Statement: h.Statement,
		Rationale: h.Rationale, Method: h.Method, Status: h.Status, KPIID: h.KpiID,
		PrimaryClusterID: h.PrimaryClusterID, TRL: i16ToIntPtr(h.Trl), NoveltyScore: h.NoveltyScore,
		RiskScore: h.RiskScore, ValueScore: h.ValueScore, ConfidenceScore: h.ConfidenceScore,
		CompositeScore: h.CompositeScore, Measurable: h.Measurable, Organization: h.Organization,
		FunctionArea: h.FunctionArea, SourceType: h.SourceType, Location: h.Location, Tags: h.Tags,
		Assessment: h.Assessment, Detail: h.Detail, Generation: h.Generation,
		CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}

func evidenceFromRow(e sqlcgen.HypothesisEvidence) *domain.Evidence {
	return &domain.Evidence{
		ID: e.ID, HypothesisID: e.HypothesisID, DocumentID: e.DocumentID, ChunkID: e.ChunkID,
		Filename: e.Filename, Snippet: e.Snippet, Stance: e.Stance, Score: e.Score,
		Ord: int(e.Ord), CreatedAt: e.CreatedAt,
		Relation: e.Relation, PageStart: int(e.PageStart), PageEnd: int(e.PageEnd),
		SectionHeading: e.SectionHeading, Origin: e.Origin,
	}
}

func revisionFromRow(rev sqlcgen.HypothesisRevision) *domain.Revision {
	return &domain.Revision{
		ID: rev.ID, HypothesisID: rev.HypothesisID, RevisionNo: int(rev.RevisionNo), EditorID: rev.EditorID,
		Action: rev.Action, Summary: rev.Summary, Patch: rev.Patch, CreatedAt: rev.CreatedAt,
	}
}

func insertHypothesisParams(h *domain.Hypothesis) sqlcgen.InsertHypothesisParams {
	return sqlcgen.InsertHypothesisParams{
		ID: h.ID, OwnerID: h.OwnerID, RunID: h.RunID, Title: h.Title, Statement: h.Statement,
		Rationale: h.Rationale, Method: h.Method, Status: h.Status, KpiID: h.KPIID,
		PrimaryClusterID: h.PrimaryClusterID, Trl: intToI16Ptr(h.TRL), NoveltyScore: h.NoveltyScore,
		RiskScore: h.RiskScore, ValueScore: h.ValueScore, ConfidenceScore: h.ConfidenceScore,
		CompositeScore: h.CompositeScore, Measurable: h.Measurable, Organization: h.Organization,
		FunctionArea: h.FunctionArea, SourceType: h.SourceType, Location: h.Location, Tags: orEmpty(h.Tags),
		Assessment: jsonbOr(h.Assessment, "{}"), Detail: jsonbOr(h.Detail, "{}"),
		Generation: jsonbOr(h.Generation, "{}"), CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}

func insertRevisionParams(rev *domain.Revision) sqlcgen.InsertRevisionParams {
	return sqlcgen.InsertRevisionParams{
		ID: rev.ID, HypothesisID: rev.HypothesisID, EditorID: rev.EditorID, Action: rev.Action,
		Summary: rev.Summary, Patch: jsonbOr(rev.Patch, "{}"), CreatedAt: rev.CreatedAt,
	}
}

// Create inserts a hypothesis with its Evidence and an optional initial revision
// in one transaction.
func (r *HypothesisRepo) Create(ctx context.Context, h *domain.Hypothesis, initial *domain.Revision) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create hypothesis: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once Commit has succeeded
	qtx := r.q.WithTx(tx)

	if ierr := qtx.InsertHypothesis(ctx, insertHypothesisParams(h)); ierr != nil {
		return fmt.Errorf("insert hypothesis: %w", ierr)
	}
	for _, e := range h.Evidence {
		if ierr := qtx.InsertEvidence(ctx, sqlcgen.InsertEvidenceParams{
			ID: e.ID, HypothesisID: e.HypothesisID, DocumentID: e.DocumentID, ChunkID: e.ChunkID,
			Filename: e.Filename, Snippet: e.Snippet, Stance: e.Stance, Score: e.Score,
			Ord: int32(e.Ord), CreatedAt: e.CreatedAt, Relation: e.Relation,
			PageStart: int32(e.PageStart), PageEnd: int32(e.PageEnd), SectionHeading: e.SectionHeading,
			Origin: e.Origin,
		}); ierr != nil {
			return fmt.Errorf("insert evidence: %w", ierr)
		}
	}
	if initial != nil {
		no, rerr := qtx.InsertRevision(ctx, insertRevisionParams(initial))
		if rerr != nil {
			return fmt.Errorf("insert revision: %w", rerr)
		}
		initial.RevisionNo = int(no)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return fmt.Errorf("commit create hypothesis: %w", cerr)
	}
	return nil
}

// Get returns a hypothesis with its Evidence loaded, or domain.ErrNotFound.
func (r *HypothesisRepo) Get(ctx context.Context, id string) (*domain.Hypothesis, error) {
	row, err := r.q.GetHypothesis(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select hypothesis: %w", err)
	}
	h := hypothesisFromRow(row)
	ev, eerr := r.ListEvidence(ctx, id)
	if eerr != nil {
		return nil, eerr
	}
	h.Evidence = ev
	return h, nil
}

// List returns the board projection (Evidence is not loaded) for a filter.
func (r *HypothesisRepo) List(ctx context.Context, f domain.HypothesisFilter) ([]*domain.Hypothesis, error) {
	rows, err := r.q.ListHypotheses(ctx, sqlcgen.ListHypothesesParams{
		OwnerID: f.OwnerID, Status: f.Status, KpiID: f.KPIID, ClusterID: f.ClusterID,
		FunctionArea: f.FunctionArea, SourceType: f.SourceType, Organization: f.Organization,
		MinTrl: int32(f.MinTRL), MaxTrl: int32(f.MaxTRL), Tags: orEmpty(f.Tags),
		DocumentIds: orEmpty(f.DocumentIDs),
		OrderBy:     f.OrderBy, Lim: int32(f.Limit), Off: int32(f.Offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list hypotheses: %w", err)
	}
	out := make([]*domain.Hypothesis, 0, len(rows))
	for i := range rows {
		out = append(out, hypothesisFromRow(rows[i]))
	}
	return out, nil
}

// Update persists the mutable columns and appends an optional revision in one
// transaction, or returns domain.ErrNotFound.
func (r *HypothesisRepo) Update(ctx context.Context, h *domain.Hypothesis, rev *domain.Revision) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update hypothesis: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once Commit has succeeded
	qtx := r.q.WithTx(tx)

	n, uerr := qtx.UpdateHypothesis(ctx, sqlcgen.UpdateHypothesisParams{
		ID: h.ID, Title: h.Title, Statement: h.Statement, Rationale: h.Rationale, Method: h.Method,
		Status: h.Status, KpiID: h.KPIID, PrimaryClusterID: h.PrimaryClusterID, Trl: intToI16Ptr(h.TRL),
		NoveltyScore: h.NoveltyScore, RiskScore: h.RiskScore, ValueScore: h.ValueScore,
		ConfidenceScore: h.ConfidenceScore, CompositeScore: h.CompositeScore, Measurable: h.Measurable,
		Organization: h.Organization, FunctionArea: h.FunctionArea, SourceType: h.SourceType,
		Location: h.Location, Tags: orEmpty(h.Tags), Assessment: jsonbOr(h.Assessment, "{}"),
		Detail: jsonbOr(h.Detail, "{}"), Generation: jsonbOr(h.Generation, "{}"),
	})
	if uerr != nil {
		return fmt.Errorf("update hypothesis: %w", uerr)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	// New evidence (empty ID — e.g. supporting/contradicting items added by the
	// verifier) is inserted; existing evidence (already has an ID) is left as-is,
	// so a plain status/score update never duplicates rows.
	for _, e := range h.Evidence {
		if e.ID != "" {
			continue
		}
		e.ID = uuid.NewString()
		e.HypothesisID = h.ID
		if e.Stance == "" {
			e.Stance = "context"
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		if ierr := qtx.InsertEvidence(ctx, sqlcgen.InsertEvidenceParams{
			ID: e.ID, HypothesisID: e.HypothesisID, DocumentID: e.DocumentID, ChunkID: e.ChunkID,
			Filename: e.Filename, Snippet: e.Snippet, Stance: e.Stance, Score: e.Score,
			Ord: int32(e.Ord), CreatedAt: e.CreatedAt, Relation: e.Relation,
			PageStart: int32(e.PageStart), PageEnd: int32(e.PageEnd), SectionHeading: e.SectionHeading,
			Origin: e.Origin,
		}); ierr != nil {
			return fmt.Errorf("insert evidence on update: %w", ierr)
		}
	}
	if rev != nil {
		no, rerr := qtx.InsertRevision(ctx, insertRevisionParams(rev))
		if rerr != nil {
			return fmt.Errorf("insert revision: %w", rerr)
		}
		rev.RevisionNo = int(no)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return fmt.Errorf("commit update hypothesis: %w", cerr)
	}
	return nil
}

// ListEvidence returns a hypothesis's evidence in display order.
func (r *HypothesisRepo) ListEvidence(ctx context.Context, hypothesisID string) ([]*domain.Evidence, error) {
	rows, err := r.q.ListEvidenceByHypothesis(ctx, hypothesisID)
	if err != nil {
		return nil, fmt.Errorf("list evidence: %w", err)
	}
	out := make([]*domain.Evidence, 0, len(rows))
	for i := range rows {
		out = append(out, evidenceFromRow(rows[i]))
	}
	return out, nil
}

// AddRevision appends an audit/edit entry, assigning the next revision number.
func (r *HypothesisRepo) AddRevision(ctx context.Context, rev *domain.Revision) error {
	no, err := r.q.InsertRevision(ctx, insertRevisionParams(rev))
	if err != nil {
		return fmt.Errorf("insert revision: %w", err)
	}
	rev.RevisionNo = int(no)
	return nil
}

// ListRevisions returns a hypothesis's revisions in chronological order.
func (r *HypothesisRepo) ListRevisions(ctx context.Context, hypothesisID string) ([]*domain.Revision, error) {
	rows, err := r.q.ListRevisionsByHypothesis(ctx, hypothesisID)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	out := make([]*domain.Revision, 0, len(rows))
	for i := range rows {
		out = append(out, revisionFromRow(rows[i]))
	}
	return out, nil
}
