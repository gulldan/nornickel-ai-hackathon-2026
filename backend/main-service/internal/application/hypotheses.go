// Hypothesis Factory use cases: a thin BFF over db-service that enforces owner
// scoping at the edge (db-service stores owner_id but does not check the caller).

package application

import (
	"context"

	"github.com/example/main-service/internal/domain"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	"github.com/example/main-service/internal/platform/logger"
	"github.com/example/main-service/internal/platform/runtimecfg"
)

// HypothesisService exposes the Hypothesis Factory to the HTTP edge. It owns the
// catalog (persistence via db-service), the answerer (retrieval + LLM via
// llm-service) used by Generate, a materials reference (Materials Project) used by
// computed novelty, an owner-scoped knowledge-graph store fed by generation, and a
// chunk reader (llm-service) used by Stage-2 enrichment to read a source
// document's full text.
type HypothesisService struct {
	cat       domain.HypothesisCatalog
	answerer  domain.Answerer
	weights   ScoringWeightsStore   // nil ⇒ default scoring profile
	settings  RuntimeSettingsStore  // nil ⇒ default runtime settings
	materials MaterialsRef          // nil ⇒ novelty skips the Materials Project sanity check
	graph     GraphStore            // nil ⇒ KG enrichment + bridges are a safe no-op
	chunks    domain.ChunkReader    // nil ⇒ enrichment falls back to retrieval-only context
	pubs      PubSearcher           // nil ⇒ generation skips open-literature evidence
	pubIngest PubIngestor           // nil ⇒ found publications are not back-filled into the corpus
	ovr       *runtimecfg.Overrides // nil ⇒ generation flags come from env/defaults
}

// SetRuntimeOverrides wires the global runtime overrides (admin settings panel)
// consulted by the generation flags; a nil value keeps plain env reads.
func (s *HypothesisService) SetRuntimeOverrides(ovr *runtimecfg.Overrides) { s.ovr = ovr }

// NewHypothesisService wires the catalog (db-service), the answerer (llm-service),
// the per-owner scoring-weights store (a nil store falls back to the default
// ranking profile), the materials reference (a nil ref skips the Materials
// Project novelty nudge), the knowledge-graph store (a nil store makes graph
// enrichment and bridge generation safe no-ops) and the chunk reader (a nil
// reader makes Stage-2 enrichment fall back to retrieval-only context).
func NewHypothesisService(
	cat domain.HypothesisCatalog, answerer domain.Answerer,
	weights ScoringWeightsStore, settings RuntimeSettingsStore, materials MaterialsRef, graph GraphStore,
	chunks domain.ChunkReader,
) *HypothesisService {
	return &HypothesisService{
		cat: cat, answerer: answerer, weights: weights, settings: settings,
		materials: materials, graph: graph, chunks: chunks,
	}
}

// rankingWeights resolves the scoring weights for an owner, falling back to the
// default profile when no store is wired or no override is stored.
func (s *HypothesisService) rankingWeights(ctx context.Context, ownerID string) ScoringWeights {
	if s.weights == nil {
		return DefaultWeights()
	}
	w, err := s.weights.Get(ctx, ownerID)
	if err != nil || w == nil {
		return DefaultWeights()
	}
	return *w
}

// GetScoringWeights returns the owner's effective ranking weights (their stored
// override or the default profile) for the settings UI.
func (s *HypothesisService) GetScoringWeights(ctx context.Context, ownerID string) ScoringWeights {
	return s.rankingWeights(ctx, ownerID)
}

// SetScoringWeights persists an owner's ranking-weights override. It is a no-op
// (no error) when no store is wired, so the edge degrades gracefully.
func (s *HypothesisService) SetScoringWeights(ctx context.Context, ownerID string, w ScoringWeights) error {
	if s.weights == nil {
		return nil
	}
	return s.weights.Set(ctx, ownerID, w)
}

// runtimeSettings resolves the owner's hypothesis factory settings, falling back
// to defaults when no store is wired, no override exists, or the store errors.
func (s *HypothesisService) runtimeSettings(ctx context.Context, ownerID string) HypothesisRuntimeSettings {
	if s.settings == nil {
		return DefaultHypothesisRuntimeSettings()
	}
	settings, err := s.settings.Get(ctx, ownerID)
	if err != nil || settings == nil {
		return DefaultHypothesisRuntimeSettings()
	}
	return NormalizeHypothesisRuntimeSettings(*settings)
}

// GetRuntimeSettings returns the owner's effective factory settings.
func (s *HypothesisService) GetRuntimeSettings(ctx context.Context, ownerID string) HypothesisRuntimeSettings {
	return s.runtimeSettings(ctx, ownerID)
}

// SetRuntimeSettings persists an owner's factory settings override.
func (s *HypothesisService) SetRuntimeSettings(ctx context.Context, ownerID string, settings HypothesisRuntimeSettings) error {
	if s.settings == nil {
		return nil
	}
	return s.settings.Set(ctx, ownerID, NormalizeHypothesisRuntimeSettings(settings))
}

// applyRanking recomputes a hypothesis's transparent composite using its owner's
// weights, keeping composite_score and assessment.ranking in sync.
func (s *HypothesisService) applyRanking(ctx context.Context, h *commonv1.Hypothesis) {
	applyRankingWith(h, s.rankingWeights(ctx, h.GetOwnerId()))
}

// ---- KPIs ----

// CreateKPI inserts a KPI owned by ownerID.
func (s *HypothesisService) CreateKPI(
	ctx context.Context, ownerID string, req *dbv1.CreateKPIRequest,
) (*commonv1.KPI, error) {
	req.OwnerId = ownerID
	return s.cat.CreateKPI(ctx, req)
}

// ListKPIs returns the owner's KPIs.
func (s *HypothesisService) ListKPIs(ctx context.Context, ownerID string) ([]*commonv1.KPI, error) {
	return s.cat.ListKPIs(ctx, ownerID)
}

// GetKPI returns a KPI the caller owns, or ErrForbidden.
func (s *HypothesisService) GetKPI(ctx context.Context, ownerID, id string) (*commonv1.KPI, error) {
	k, err := s.cat.GetKPI(ctx, id)
	if err != nil {
		return nil, err
	}
	if k.GetOwnerId() != ownerID {
		return nil, ErrForbidden
	}
	return k, nil
}

// UpdateKPI persists a KPI the caller owns (owner_id is immutable). Archiving or
// reactivating a goal cascades onto its hypotheses so the board never shows
// hypotheses for a goal that is no longer active.
func (s *HypothesisService) UpdateKPI(ctx context.Context, ownerID string, kpi *commonv1.KPI) error {
	prev, err := s.GetKPI(ctx, ownerID, kpi.GetId())
	if err != nil {
		return err
	}
	kpi.OwnerId = ownerID
	if uerr := s.cat.UpdateKPI(ctx, kpi); uerr != nil {
		return uerr
	}
	if st := kpi.GetStatus(); st != "" && st != prev.GetStatus() {
		s.cascadeKPIStatus(ctx, ownerID, kpi.GetId(), st)
	}
	return nil
}

// DeleteKPI removes a goal the caller owns and archives its hypotheses first, so
// they leave the board instead of surviving as orphans once the FK nulls their
// kpi_id.
func (s *HypothesisService) DeleteKPI(ctx context.Context, ownerID, id string) error {
	if _, err := s.GetKPI(ctx, ownerID, id); err != nil {
		return err
	}
	s.cascadeKPIStatus(ctx, ownerID, id, statusArchived)
	return s.cat.DeleteKPI(ctx, id)
}

const kpiStatusActive = "active"

// cascadeKPIStatus mirrors a goal's lifecycle onto its hypotheses: an archived
// or deleted goal archives its open hypotheses; a reactivated goal restores the
// ones it had archived. Best-effort — a per-hypothesis failure is logged so the
// KPI change itself still succeeds.
func (s *HypothesisService) cascadeKPIStatus(ctx context.Context, ownerID, kpiID, kpiStatus string) {
	var target string
	switch kpiStatus {
	case statusArchived:
		target = statusArchived
	case kpiStatusActive:
		target = statusGenerated
	default:
		return
	}
	refs, err := s.cat.ListHypotheses(ctx, &dbv1.ListHypothesesRequest{OwnerId: ownerID, KpiId: kpiID})
	if err != nil {
		logger.From(ctx).Warn().Err(err).Str("kpi_id", kpiID).Msg("kpi cascade: list hypotheses failed")
		return
	}
	for _, ref := range refs {
		if !cascadeFlips(ref.GetStatus(), target) {
			continue
		}
		h, gerr := s.cat.GetHypothesis(ctx, ref.GetId())
		if gerr != nil {
			logger.From(ctx).Warn().Err(gerr).Str("hypothesis_id", ref.GetId()).Msg("kpi cascade: get failed")
			continue
		}
		h.Status = target
		rev := &commonv1.HypothesisRevision{
			HypothesisId: h.GetId(), Action: "status_changed", EditorId: editorSystem,
			Summary: cascadeSummary(target),
		}
		if uerr := s.cat.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: h, Revision: rev}); uerr != nil {
			logger.From(ctx).Warn().Err(uerr).Str("hypothesis_id", h.GetId()).Msg("kpi cascade: update failed")
		}
	}
}

func cascadeFlips(current, target string) bool {
	switch target {
	case statusArchived:
		return current == "draft" || current == statusGenerated || current == "under_review"
	case statusGenerated:
		return current == statusArchived
	}
	return false
}

func cascadeSummary(target string) string {
	if target == statusArchived {
		return "Архивация вместе с целью"
	}
	return "Восстановление вместе с целью"
}

// AttachKPIDocuments links documents to a goal the caller owns as its input data.
func (s *HypothesisService) AttachKPIDocuments(ctx context.Context, ownerID, kpiID string, documentIDs []string) error {
	if _, err := s.GetKPI(ctx, ownerID, kpiID); err != nil {
		return err
	}
	for _, docID := range documentIDs {
		if docID == "" {
			continue
		}
		if err := s.cat.AttachKPIDocument(ctx, kpiID, docID, "input"); err != nil {
			return err
		}
	}
	return nil
}

// ListKPIDocuments returns the documents attached to a goal the caller owns.
func (s *HypothesisService) ListKPIDocuments(ctx context.Context, ownerID, kpiID string) ([]*dbv1.KpiDocumentLink, error) {
	if _, err := s.GetKPI(ctx, ownerID, kpiID); err != nil {
		return nil, err
	}
	return s.cat.ListKPIDocuments(ctx, kpiID)
}

// DetachKPIDocument unlinks a document from a goal the caller owns.
func (s *HypothesisService) DetachKPIDocument(ctx context.Context, ownerID, kpiID, documentID string) error {
	if _, err := s.GetKPI(ctx, ownerID, kpiID); err != nil {
		return err
	}
	return s.cat.DetachKPIDocument(ctx, kpiID, documentID)
}

// ---- Clusters ----

// CreateCluster inserts a cluster owned by ownerID.
func (s *HypothesisService) CreateCluster(
	ctx context.Context, ownerID string, req *dbv1.CreateClusterRequest,
) (*commonv1.Cluster, error) {
	req.OwnerId = ownerID
	return s.cat.CreateCluster(ctx, req)
}

// ListClusters returns the owner's clusters.
func (s *HypothesisService) ListClusters(ctx context.Context, ownerID string) ([]*commonv1.Cluster, error) {
	return s.cat.ListClusters(ctx, ownerID)
}

// GetCluster returns a cluster the caller owns, or ErrForbidden.
func (s *HypothesisService) GetCluster(ctx context.Context, ownerID, id string) (*commonv1.Cluster, error) {
	c, err := s.cat.GetCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.GetOwnerId() != ownerID {
		return nil, ErrForbidden
	}
	return c, nil
}

// UpdateCluster persists a cluster the caller owns.
func (s *HypothesisService) UpdateCluster(ctx context.Context, ownerID string, cluster *commonv1.Cluster) error {
	if _, err := s.GetCluster(ctx, ownerID, cluster.GetId()); err != nil {
		return err
	}
	cluster.OwnerId = ownerID
	return s.cat.UpdateCluster(ctx, cluster)
}

// DeleteAllClusters removes every cluster the caller owns. Automated
// reclustering now uses versioned publish through the HTTP API; this method
// remains for operator cleanup. Owner scoping is authoritative.
func (s *HypothesisService) DeleteAllClusters(ctx context.Context, ownerID string) error {
	return s.cat.DeleteClusters(ctx, ownerID)
}

// ---- Hypotheses ----

// CreateHypothesis inserts a hypothesis owned by ownerID (with its evidence and
// optional initial revision).
func (s *HypothesisService) CreateHypothesis(
	ctx context.Context, ownerID string, req *dbv1.CreateHypothesisRequest,
) (*commonv1.Hypothesis, error) {
	if req.GetHypothesis() != nil {
		req.Hypothesis.OwnerId = ownerID
		s.applyRanking(ctx, req.Hypothesis)
	}
	return s.cat.CreateHypothesis(ctx, req)
}

// ListHypotheses returns the board projection for the owner's filter. The
// caller's owner id is authoritative; a privileged caller (admin) lists every
// owner's hypotheses.
func (s *HypothesisService) ListHypotheses(
	ctx context.Context, ownerID string, privileged bool, req *dbv1.ListHypothesesRequest,
) ([]*commonv1.Hypothesis, error) {
	req.OwnerId = ownerID
	if privileged {
		req.OwnerId = ""
	}
	return s.cat.ListHypotheses(ctx, req)
}

// GetHypothesis returns a hypothesis the caller owns (with evidence), or
// ErrForbidden. A privileged caller (admin) may read any hypothesis.
func (s *HypothesisService) GetHypothesis(
	ctx context.Context, ownerID string, privileged bool, id string,
) (*commonv1.Hypothesis, error) {
	h, err := s.cat.GetHypothesis(ctx, id)
	if err != nil {
		return nil, err
	}
	if !privileged && h.GetOwnerId() != ownerID {
		return nil, ErrForbidden
	}
	return h, nil
}

// UpdateHypothesis persists a hypothesis the caller owns and an optional revision.
func (s *HypothesisService) UpdateHypothesis(
	ctx context.Context, ownerID string, req *dbv1.UpdateHypothesisRequest,
) error {
	if _, err := s.GetHypothesis(ctx, ownerID, false, req.GetHypothesis().GetId()); err != nil {
		return err
	}
	req.Hypothesis.OwnerId = ownerID
	// A PUT may change scores, TRL, evidence, KPI binding or measurability. Keep
	// composite_score and assessment.ranking in sync instead of trusting a stale
	// client-provided composite.
	s.applyRanking(ctx, req.Hypothesis)
	return s.cat.UpdateHypothesis(ctx, req)
}

// AddRevision appends an audit/edit entry to a hypothesis the caller owns.
func (s *HypothesisService) AddRevision(
	ctx context.Context, ownerID string, rev *commonv1.HypothesisRevision,
) (*commonv1.HypothesisRevision, error) {
	if _, err := s.GetHypothesis(ctx, ownerID, false, rev.GetHypothesisId()); err != nil {
		return nil, err
	}
	return s.cat.AddHypothesisRevision(ctx, rev)
}

// ListRevisions returns the revisions of a hypothesis the caller owns; a
// privileged caller (admin) may read any hypothesis's revisions.
func (s *HypothesisService) ListRevisions(
	ctx context.Context, ownerID string, privileged bool, hypothesisID string,
) ([]*commonv1.HypothesisRevision, error) {
	if _, err := s.GetHypothesis(ctx, ownerID, privileged, hypothesisID); err != nil {
		return nil, err
	}
	return s.cat.ListHypothesisRevisions(ctx, hypothesisID)
}

// ListEvidence returns the evidence of a hypothesis the caller owns; a
// privileged caller (admin) may read any hypothesis's evidence.
func (s *HypothesisService) ListEvidence(
	ctx context.Context, ownerID string, privileged bool, hypothesisID string,
) ([]*commonv1.HypothesisEvidence, error) {
	if _, err := s.GetHypothesis(ctx, ownerID, privileged, hypothesisID); err != nil {
		return nil, err
	}
	return s.cat.ListHypothesisEvidence(ctx, hypothesisID)
}
