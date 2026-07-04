package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/db-service/internal/domain"

	commonv1 "github.com/example/db-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/db-service/internal/platform/genproto/db/v1"
)

// ---- conversions ----

func i32PtrToIntPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func intPtrToI32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

func kpiPB(k *domain.KPI) *commonv1.KPI {
	return &commonv1.KPI{
		Id: k.ID, OwnerId: k.OwnerID, Title: k.Title, Description: k.Description, Metric: k.Metric,
		Unit: k.Unit, Direction: k.Direction, Baseline: k.Baseline, Target: k.Target,
		FunctionArea: k.FunctionArea, Status: k.Status, Detail: string(k.Detail),
		CreatedAt: k.CreatedAt.Format(timeLayout), UpdatedAt: k.UpdatedAt.Format(timeLayout),
	}
}

func kpiFromPB(pb *commonv1.KPI) *domain.KPI {
	return &domain.KPI{
		ID: pb.GetId(), OwnerID: pb.GetOwnerId(), Title: pb.GetTitle(), Description: pb.GetDescription(),
		Metric: pb.GetMetric(), Unit: pb.GetUnit(), Direction: pb.GetDirection(), Baseline: pb.Baseline,
		Target: pb.Target, FunctionArea: pb.GetFunctionArea(), Status: pb.GetStatus(), Detail: []byte(pb.GetDetail()),
	}
}

func clusterPB(c *domain.Cluster) *commonv1.Cluster {
	return &commonv1.Cluster{
		Id: c.ID, OwnerId: c.OwnerID, Label: c.Label, Summary: c.Summary, Keywords: c.Keywords,
		Method: c.Method, ChunkCount: int32(c.ChunkCount), DocumentCount: int32(c.DocumentCount),
		Representatives: string(c.Representatives), Params: string(c.Params), Status: c.Status,
		CreatedAt: c.CreatedAt.Format(timeLayout), UpdatedAt: c.UpdatedAt.Format(timeLayout),
	}
}

func clusterFromPB(pb *commonv1.Cluster) *domain.Cluster {
	return &domain.Cluster{
		ID: pb.GetId(), OwnerID: pb.GetOwnerId(), Label: pb.GetLabel(), Summary: pb.GetSummary(),
		Keywords: pb.GetKeywords(), Method: pb.GetMethod(), ChunkCount: int(pb.GetChunkCount()),
		DocumentCount: int(pb.GetDocumentCount()), Representatives: []byte(pb.GetRepresentatives()),
		Params: []byte(pb.GetParams()), Status: pb.GetStatus(),
	}
}

func evidencePB(e *domain.Evidence) *commonv1.HypothesisEvidence {
	return &commonv1.HypothesisEvidence{
		Id: e.ID, HypothesisId: e.HypothesisID, DocumentId: e.DocumentID, ChunkId: e.ChunkID,
		Filename: e.Filename, Snippet: e.Snippet, Stance: e.Stance, Score: e.Score,
		Ord: int32(e.Ord), CreatedAt: e.CreatedAt.Format(timeLayout), Relation: e.Relation,
		PageStart: int32(e.PageStart), PageEnd: int32(e.PageEnd), SectionHeading: e.SectionHeading,
		Origin: e.Origin,
	}
}

func evidenceFromPB(pb *commonv1.HypothesisEvidence) *domain.Evidence {
	return &domain.Evidence{
		ID: pb.GetId(), HypothesisID: pb.GetHypothesisId(), DocumentID: pb.DocumentId, ChunkID: pb.GetChunkId(),
		Filename: pb.GetFilename(), Snippet: pb.GetSnippet(), Stance: pb.GetStance(), Score: pb.Score,
		Ord: int(pb.GetOrd()), Relation: pb.GetRelation(), PageStart: int(pb.GetPageStart()),
		PageEnd: int(pb.GetPageEnd()), SectionHeading: pb.GetSectionHeading(), Origin: pb.GetOrigin(),
	}
}

func revisionPB(r *domain.Revision) *commonv1.HypothesisRevision {
	return &commonv1.HypothesisRevision{
		Id: r.ID, HypothesisId: r.HypothesisID, RevisionNo: int32(r.RevisionNo), EditorId: r.EditorID,
		Action: r.Action, Summary: r.Summary, Patch: string(r.Patch), CreatedAt: r.CreatedAt.Format(timeLayout),
	}
}

// revisionFromPB returns nil for a nil message, so optional request revisions
// pass straight through as "no revision".
func revisionFromPB(pb *commonv1.HypothesisRevision) *domain.Revision {
	if pb == nil {
		return nil
	}
	return &domain.Revision{
		ID: pb.GetId(), HypothesisID: pb.GetHypothesisId(), RevisionNo: int(pb.GetRevisionNo()),
		EditorID: pb.GetEditorId(), Action: pb.GetAction(), Summary: pb.GetSummary(), Patch: []byte(pb.GetPatch()),
	}
}

func hypothesisPB(h *domain.Hypothesis) *commonv1.Hypothesis {
	pb := &commonv1.Hypothesis{
		Id: h.ID, OwnerId: h.OwnerID, RunId: h.RunID, Title: h.Title, Statement: h.Statement,
		Rationale: h.Rationale, Method: h.Method, Status: h.Status, KpiId: h.KPIID,
		PrimaryClusterId: h.PrimaryClusterID, Trl: intPtrToI32Ptr(h.TRL), NoveltyScore: h.NoveltyScore,
		RiskScore: h.RiskScore, ValueScore: h.ValueScore, ConfidenceScore: h.ConfidenceScore,
		CompositeScore: h.CompositeScore, Measurable: h.Measurable, Organization: h.Organization,
		FunctionArea: h.FunctionArea, SourceType: h.SourceType, Location: h.Location, Tags: h.Tags,
		Assessment: string(h.Assessment), Detail: string(h.Detail), Generation: string(h.Generation),
		CreatedAt: h.CreatedAt.Format(timeLayout), UpdatedAt: h.UpdatedAt.Format(timeLayout),
	}
	for _, e := range h.Evidence {
		pb.Evidence = append(pb.Evidence, evidencePB(e))
	}
	return pb
}

func hypothesisFromPB(pb *commonv1.Hypothesis) *domain.Hypothesis {
	h := &domain.Hypothesis{
		ID: pb.GetId(), OwnerID: pb.GetOwnerId(), RunID: pb.GetRunId(), Title: pb.GetTitle(),
		Statement: pb.GetStatement(), Rationale: pb.GetRationale(), Method: pb.GetMethod(), Status: pb.GetStatus(),
		KPIID: pb.KpiId, PrimaryClusterID: pb.PrimaryClusterId, TRL: i32PtrToIntPtr(pb.Trl),
		NoveltyScore: pb.NoveltyScore, RiskScore: pb.RiskScore, ValueScore: pb.ValueScore,
		ConfidenceScore: pb.ConfidenceScore, CompositeScore: pb.CompositeScore, Measurable: pb.GetMeasurable(),
		Organization: pb.GetOrganization(), FunctionArea: pb.GetFunctionArea(), SourceType: pb.GetSourceType(),
		Location: pb.GetLocation(), Tags: pb.GetTags(), Assessment: []byte(pb.GetAssessment()),
		Detail: []byte(pb.GetDetail()), Generation: []byte(pb.GetGeneration()),
	}
	for _, e := range pb.GetEvidence() {
		h.Evidence = append(h.Evidence, evidenceFromPB(e))
	}
	return h
}

// ---- KPI handlers ----

// CreateKPI creates a KPI.
func (s *Server) CreateKPI(ctx context.Context, req *dbv1.CreateKPIRequest) (*commonv1.KPI, error) {
	k, err := s.svc.CreateKPI(ctx, &domain.KPI{
		OwnerID: req.GetOwnerId(), Title: req.GetTitle(), Description: req.GetDescription(),
		Metric: req.GetMetric(), Unit: req.GetUnit(), Direction: req.GetDirection(), Baseline: req.Baseline,
		Target: req.Target, FunctionArea: req.GetFunctionArea(), Detail: []byte(req.GetDetail()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return kpiPB(k), nil
}

// GetKPI fetches a KPI by id.
func (s *Server) GetKPI(ctx context.Context, req *dbv1.GetKPIRequest) (*commonv1.KPI, error) {
	k, err := s.svc.GetKPI(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return kpiPB(k), nil
}

// ListKPIs lists an owner's KPIs.
func (s *Server) ListKPIs(ctx context.Context, req *dbv1.ListKPIsRequest) (*dbv1.ListKPIsResponse, error) {
	ks, err := s.svc.ListKPIs(ctx, req.GetOwnerId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.KPI, 0, len(ks))
	for _, k := range ks {
		out = append(out, kpiPB(k))
	}
	return &dbv1.ListKPIsResponse{Kpis: out}, nil
}

// UpdateKPI persists a full KPI.
func (s *Server) UpdateKPI(ctx context.Context, req *dbv1.UpdateKPIRequest) (*dbv1.UpdateKPIResponse, error) {
	if err := s.svc.UpdateKPI(ctx, kpiFromPB(req.GetKpi())); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.UpdateKPIResponse{}, nil
}

// DeleteKPI removes a KPI by id.
func (s *Server) DeleteKPI(ctx context.Context, req *dbv1.DeleteKPIRequest) (*dbv1.DeleteKPIResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.svc.DeleteKPI(ctx, req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.DeleteKPIResponse{}, nil
}

// AttachKpiDocument links a document to a goal.
func (s *Server) AttachKpiDocument(
	ctx context.Context, req *dbv1.AttachKpiDocumentRequest,
) (*dbv1.AttachKpiDocumentResponse, error) {
	if err := s.svc.AttachKPIDocument(ctx, req.GetKpiId(), req.GetDocumentId(), req.GetRole()); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.AttachKpiDocumentResponse{}, nil
}

// ListKpiDocuments returns a goal's attached documents.
func (s *Server) ListKpiDocuments(
	ctx context.Context, req *dbv1.ListKpiDocumentsRequest,
) (*dbv1.ListKpiDocumentsResponse, error) {
	links, err := s.svc.ListKPIDocuments(ctx, req.GetKpiId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*dbv1.KpiDocumentLink, 0, len(links))
	for _, l := range links {
		out = append(out, &dbv1.KpiDocumentLink{
			Document:   documentPB(l.Document),
			Role:       l.Role,
			AttachedAt: l.AttachedAt.Format(timeLayout),
		})
	}
	return &dbv1.ListKpiDocumentsResponse{Links: out}, nil
}

// DetachKpiDocument unlinks a document from a goal.
func (s *Server) DetachKpiDocument(
	ctx context.Context, req *dbv1.DetachKpiDocumentRequest,
) (*dbv1.DetachKpiDocumentResponse, error) {
	if err := s.svc.DetachKPIDocument(ctx, req.GetKpiId(), req.GetDocumentId()); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.DetachKpiDocumentResponse{}, nil
}

// ---- Cluster handlers ----

// CreateCluster creates a cluster.
func (s *Server) CreateCluster(ctx context.Context, req *dbv1.CreateClusterRequest) (*commonv1.Cluster, error) {
	c, err := s.svc.CreateCluster(ctx, &domain.Cluster{
		OwnerID: req.GetOwnerId(), Label: req.GetLabel(), Summary: req.GetSummary(), Keywords: req.GetKeywords(),
		Method: req.GetMethod(), ChunkCount: int(req.GetChunkCount()), DocumentCount: int(req.GetDocumentCount()),
		Representatives: []byte(req.GetRepresentatives()), Params: []byte(req.GetParams()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return clusterPB(c), nil
}

// GetCluster fetches a cluster by id.
func (s *Server) GetCluster(ctx context.Context, req *dbv1.GetClusterRequest) (*commonv1.Cluster, error) {
	c, err := s.svc.GetCluster(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return clusterPB(c), nil
}

// ListClusters lists an owner's clusters.
func (s *Server) ListClusters(
	ctx context.Context, req *dbv1.ListClustersRequest,
) (*dbv1.ListClustersResponse, error) {
	cs, err := s.svc.ListClusters(ctx, req.GetOwnerId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.Cluster, 0, len(cs))
	for _, c := range cs {
		out = append(out, clusterPB(c))
	}
	return &dbv1.ListClustersResponse{Clusters: out}, nil
}

// UpdateCluster persists a full cluster.
func (s *Server) UpdateCluster(
	ctx context.Context, req *dbv1.UpdateClusterRequest,
) (*dbv1.UpdateClusterResponse, error) {
	if err := s.svc.UpdateCluster(ctx, clusterFromPB(req.GetCluster())); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.UpdateClusterResponse{}, nil
}

// DeleteClusters removes all of an owner's clusters.
func (s *Server) DeleteClusters(
	ctx context.Context, req *dbv1.DeleteClustersRequest,
) (*dbv1.DeleteClustersResponse, error) {
	n, err := s.svc.DeleteClusters(ctx, req.GetOwnerId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.DeleteClustersResponse{Deleted: n}, nil
}

// ---- Hypothesis handlers ----

// CreateHypothesis creates a hypothesis with its evidence and optional initial revision.
func (s *Server) CreateHypothesis(
	ctx context.Context, req *dbv1.CreateHypothesisRequest,
) (*commonv1.Hypothesis, error) {
	h, err := s.svc.CreateHypothesis(ctx, hypothesisFromPB(req.GetHypothesis()), revisionFromPB(req.GetInitial()))
	if err != nil {
		return nil, toStatus(err)
	}
	return hypothesisPB(h), nil
}

// GetHypothesis fetches a hypothesis (with evidence) by id.
func (s *Server) GetHypothesis(
	ctx context.Context, req *dbv1.GetHypothesisRequest,
) (*commonv1.Hypothesis, error) {
	h, err := s.svc.GetHypothesis(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return hypothesisPB(h), nil
}

// ListHypotheses returns the board projection for a filter.
func (s *Server) ListHypotheses(
	ctx context.Context, req *dbv1.ListHypothesesRequest,
) (*dbv1.ListHypothesesResponse, error) {
	hs, err := s.svc.ListHypotheses(ctx, domain.HypothesisFilter{
		OwnerID: req.GetOwnerId(), Status: req.GetStatus(), KPIID: req.GetKpiId(), ClusterID: req.GetClusterId(),
		FunctionArea: req.GetFunctionArea(), SourceType: req.GetSourceType(), Organization: req.GetOrganization(),
		MinTRL: int(req.GetMinTrl()), MaxTRL: int(req.GetMaxTrl()), Tags: req.GetTags(),
		DocumentIDs: req.GetDocumentIds(),
		OrderBy:     req.GetOrderBy(), Limit: int(req.GetLimit()), Offset: int(req.GetOffset()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.Hypothesis, 0, len(hs))
	for _, h := range hs {
		out = append(out, hypothesisPB(h))
	}
	return &dbv1.ListHypothesesResponse{Hypotheses: out}, nil
}

// UpdateHypothesis persists a full hypothesis and an optional audit revision.
func (s *Server) UpdateHypothesis(
	ctx context.Context, req *dbv1.UpdateHypothesisRequest,
) (*dbv1.UpdateHypothesisResponse, error) {
	err := s.svc.UpdateHypothesis(ctx, hypothesisFromPB(req.GetHypothesis()), revisionFromPB(req.GetRevision()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.UpdateHypothesisResponse{}, nil
}

// AddHypothesisRevision appends an audit/edit entry.
func (s *Server) AddHypothesisRevision(
	ctx context.Context, req *dbv1.AddHypothesisRevisionRequest,
) (*commonv1.HypothesisRevision, error) {
	rev, err := s.svc.AddHypothesisRevision(ctx, revisionFromPB(req.GetRevision()))
	if err != nil {
		return nil, toStatus(err)
	}
	return revisionPB(rev), nil
}

// ListHypothesisRevisions lists a hypothesis's revisions.
func (s *Server) ListHypothesisRevisions(
	ctx context.Context, req *dbv1.ListHypothesisRevisionsRequest,
) (*dbv1.ListHypothesisRevisionsResponse, error) {
	revs, err := s.svc.ListHypothesisRevisions(ctx, req.GetHypothesisId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.HypothesisRevision, 0, len(revs))
	for _, r := range revs {
		out = append(out, revisionPB(r))
	}
	return &dbv1.ListHypothesisRevisionsResponse{Revisions: out}, nil
}

// ListHypothesisEvidence lists a hypothesis's evidence.
func (s *Server) ListHypothesisEvidence(
	ctx context.Context, req *dbv1.ListHypothesisEvidenceRequest,
) (*dbv1.ListHypothesisEvidenceResponse, error) {
	ev, err := s.svc.ListHypothesisEvidence(ctx, req.GetHypothesisId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.HypothesisEvidence, 0, len(ev))
	for _, e := range ev {
		out = append(out, evidencePB(e))
	}
	return &dbv1.ListHypothesisEvidenceResponse{Evidence: out}, nil
}
