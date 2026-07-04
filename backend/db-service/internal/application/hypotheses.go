package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/example/db-service/internal/domain"
)

// trlMin and trlMax bound a hypothesis's Technology Readiness Level (1..9). A nil
// TRL is allowed (unset); a set value outside the range is a validation error
// rather than something to silently clamp on the int16 column.
const (
	trlMin = 1
	trlMax = 9
)

// validateTRL rejects an out-of-range TRL so the int->int16 narrowing in the
// repository never silently truncates a bad value. nil (unset) is valid.
func validateTRL(trl *int) error {
	if trl == nil {
		return nil
	}
	if *trl < trlMin || *trl > trlMax {
		return fmt.Errorf("trl must be between %d and %d: %w", trlMin, trlMax, domain.ErrInvalidArgument)
	}
	return nil
}

// ---- KPIs ----

// CreateKPI inserts a KPI, assigning its id and defaulting direction/status.
func (s *Service) CreateKPI(ctx context.Context, k *domain.KPI) (*domain.KPI, error) {
	if k.OwnerID == "" || k.Title == "" {
		return nil, fmt.Errorf("owner_id and title are required: %w", domain.ErrInvalidArgument)
	}
	k.ID = uuid.NewString()
	if k.Direction == "" {
		k.Direction = domain.DirectionIncrease
	}
	if k.Status == "" {
		k.Status = domain.StatusActive
	}
	k.CreatedAt = now()
	k.UpdatedAt = now()
	if err := s.kpis.Create(ctx, k); err != nil {
		return nil, err
	}
	return k, nil
}

// GetKPI fetches a KPI by id.
func (s *Service) GetKPI(ctx context.Context, id string) (*domain.KPI, error) {
	return s.kpis.Get(ctx, id)
}

// ListKPIs returns an owner's KPIs.
func (s *Service) ListKPIs(ctx context.Context, ownerID string) ([]*domain.KPI, error) {
	return s.kpis.ListByOwner(ctx, ownerID)
}

// UpdateKPI persists a full KPI (read-modify-write).
func (s *Service) UpdateKPI(ctx context.Context, k *domain.KPI) error {
	if k.ID == "" {
		return fmt.Errorf("id is required: %w", domain.ErrInvalidArgument)
	}
	if k.Direction == "" {
		k.Direction = domain.DirectionIncrease
	}
	if k.Status == "" {
		k.Status = domain.StatusActive
	}
	return s.kpis.Update(ctx, k)
}

// DeleteKPI removes a KPI; linked hypotheses keep living with kpi_id nulled.
func (s *Service) DeleteKPI(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("id is required: %w", domain.ErrInvalidArgument)
	}
	return s.kpis.Delete(ctx, id)
}

// AttachKPIDocument links a document to a goal after checking both exist.
func (s *Service) AttachKPIDocument(ctx context.Context, kpiID, documentID, role string) error {
	if kpiID == "" || documentID == "" {
		return fmt.Errorf("kpi_id and document_id are required: %w", domain.ErrInvalidArgument)
	}
	if role == "" {
		role = domain.KPIDocumentRoleInput
	}
	if _, err := s.kpis.Get(ctx, kpiID); err != nil {
		return err
	}
	if _, err := s.docs.Get(ctx, documentID); err != nil {
		return err
	}
	return s.kpis.AttachDocument(ctx, kpiID, documentID, role)
}

// ListKPIDocuments returns a goal's attached documents.
func (s *Service) ListKPIDocuments(ctx context.Context, kpiID string) ([]*domain.KPIDocumentLink, error) {
	if kpiID == "" {
		return nil, fmt.Errorf("kpi_id is required: %w", domain.ErrInvalidArgument)
	}
	return s.kpis.ListDocuments(ctx, kpiID)
}

// DetachKPIDocument unlinks a document from a goal.
func (s *Service) DetachKPIDocument(ctx context.Context, kpiID, documentID string) error {
	if kpiID == "" || documentID == "" {
		return fmt.Errorf("kpi_id and document_id are required: %w", domain.ErrInvalidArgument)
	}
	return s.kpis.DetachDocument(ctx, kpiID, documentID)
}

// ---- Clusters ----

// CreateCluster inserts a cluster, assigning its id and defaulting status.
func (s *Service) CreateCluster(ctx context.Context, c *domain.Cluster) (*domain.Cluster, error) {
	if c.OwnerID == "" || c.Label == "" {
		return nil, fmt.Errorf("owner_id and label are required: %w", domain.ErrInvalidArgument)
	}
	c.ID = uuid.NewString()
	if c.Status == "" {
		c.Status = domain.StatusActive
	}
	c.CreatedAt = now()
	c.UpdatedAt = now()
	if err := s.clusters.Create(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// GetCluster fetches a cluster by id.
func (s *Service) GetCluster(ctx context.Context, id string) (*domain.Cluster, error) {
	return s.clusters.Get(ctx, id)
}

// ListClusters returns an owner's clusters.
func (s *Service) ListClusters(ctx context.Context, ownerID string) ([]*domain.Cluster, error) {
	return s.clusters.ListByOwner(ctx, ownerID)
}

// UpdateCluster persists a full cluster (read-modify-write).
func (s *Service) UpdateCluster(ctx context.Context, c *domain.Cluster) error {
	if c.ID == "" {
		return fmt.Errorf("id is required: %w", domain.ErrInvalidArgument)
	}
	if c.Status == "" {
		c.Status = domain.StatusActive
	}
	return s.clusters.Update(ctx, c)
}

// DeleteClusters removes all of an owner's clusters and returns the count.
func (s *Service) DeleteClusters(ctx context.Context, ownerID string) (int64, error) {
	if ownerID == "" {
		return 0, fmt.Errorf("owner_id is required: %w", domain.ErrInvalidArgument)
	}
	return s.clusters.DeleteByOwner(ctx, ownerID)
}

// ---- Hypotheses ----

// CreateHypothesis assigns ids and timestamps for the hypothesis, its evidence
// and the optional initial revision, then persists them atomically.
func (s *Service) CreateHypothesis(
	ctx context.Context, h *domain.Hypothesis, initial *domain.Revision,
) (*domain.Hypothesis, error) {
	if h.OwnerID == "" || h.Title == "" || h.Statement == "" {
		return nil, fmt.Errorf("owner_id, title and statement are required: %w", domain.ErrInvalidArgument)
	}
	if err := validateTRL(h.TRL); err != nil {
		return nil, err
	}
	h.ID = uuid.NewString()
	if h.Method == "" {
		h.Method = domain.MethodClusterKPI
	}
	if h.Status == "" {
		h.Status = domain.HypothesisGenerated
	}
	h.CreatedAt = now()
	h.UpdatedAt = now()
	for _, e := range h.Evidence {
		e.ID = uuid.NewString()
		e.HypothesisID = h.ID
		if e.Stance == "" {
			e.Stance = domain.StanceSupports
		}
		e.CreatedAt = now()
	}
	if initial != nil {
		initial.ID = uuid.NewString()
		initial.HypothesisID = h.ID
		if initial.Action == "" {
			initial.Action = domain.ActionCreated
		}
		initial.CreatedAt = now()
	}
	if err := s.hypotheses.Create(ctx, h, initial); err != nil {
		return nil, err
	}
	return h, nil
}

// GetHypothesis fetches a hypothesis (with evidence) by id.
func (s *Service) GetHypothesis(ctx context.Context, id string) (*domain.Hypothesis, error) {
	return s.hypotheses.Get(ctx, id)
}

// ListHypotheses returns the board projection for a filter. An empty owner id
// means all owners (privileged admin listing at the edge).
func (s *Service) ListHypotheses(
	ctx context.Context, f domain.HypothesisFilter,
) ([]*domain.Hypothesis, error) {
	return s.hypotheses.List(ctx, f)
}

// UpdateHypothesis persists a full hypothesis and an optional audit revision.
func (s *Service) UpdateHypothesis(ctx context.Context, h *domain.Hypothesis, rev *domain.Revision) error {
	if h.ID == "" {
		return fmt.Errorf("id is required: %w", domain.ErrInvalidArgument)
	}
	if err := validateTRL(h.TRL); err != nil {
		return err
	}
	if h.Method == "" {
		h.Method = domain.MethodClusterKPI
	}
	if h.Status == "" {
		h.Status = domain.HypothesisGenerated
	}
	h.UpdatedAt = now()
	if rev != nil {
		rev.ID = uuid.NewString()
		rev.HypothesisID = h.ID
		if rev.Action == "" {
			rev.Action = domain.ActionEdited
		}
		rev.CreatedAt = now()
	}
	return s.hypotheses.Update(ctx, h, rev)
}

// AddHypothesisRevision appends an audit/edit entry, assigning its id.
func (s *Service) AddHypothesisRevision(ctx context.Context, rev *domain.Revision) (*domain.Revision, error) {
	if rev.HypothesisID == "" || rev.Action == "" {
		return nil, fmt.Errorf("hypothesis_id and action are required: %w", domain.ErrInvalidArgument)
	}
	rev.ID = uuid.NewString()
	rev.CreatedAt = now()
	if err := s.hypotheses.AddRevision(ctx, rev); err != nil {
		return nil, err
	}
	return rev, nil
}

// ListHypothesisRevisions returns a hypothesis's revisions in order.
func (s *Service) ListHypothesisRevisions(ctx context.Context, hypothesisID string) ([]*domain.Revision, error) {
	return s.hypotheses.ListRevisions(ctx, hypothesisID)
}

// ListHypothesisEvidence returns a hypothesis's evidence in display order.
func (s *Service) ListHypothesisEvidence(ctx context.Context, hypothesisID string) ([]*domain.Evidence, error) {
	return s.hypotheses.ListEvidence(ctx, hypothesisID)
}
