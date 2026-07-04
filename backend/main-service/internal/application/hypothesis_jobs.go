package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

// Hypothesis job kinds and lifecycle statuses for the durable background queue.
const (
	HypothesisJobGenerate    = "generate"
	HypothesisJobVerify      = "verify"
	HypothesisJobEnrich      = "enrich"
	HypothesisJobAssessTRL   = "assess_trl"
	HypothesisJobCompetitors = "competitors"
	HypothesisJobRefine      = "refine"
	HypothesisJobTag         = "tag"

	HypothesisJobQueued    = "queued"
	HypothesisJobRunning   = "running"
	HypothesisJobSucceeded = "succeeded"
	HypothesisJobFailed    = "failed"

	hypothesisJobTTL        = 7 * 24 * time.Hour
	hypothesisJobListMax    = 200
	hypothesisJobGlobalMax  = 1000
	hypothesisJobClaimTTL   = 6 * time.Hour
	hypothesisJobStaleAfter = 30 * time.Minute
	hypothesisJobActiveMax  = 75 * time.Second
	hypothesisAutoVerifyTTL = 30 * time.Minute
	hypothesisAutoTRLTTL    = 30 * time.Minute
	hypothesisAutoEnrichTTL = 30 * time.Minute

	// backgroundJobConcurrency caps concurrently-executing non-generate jobs;
	// the rest wait as queued so interactive generation keeps LLM headroom.
	backgroundJobConcurrency = 2
)

// HypothesisJobStore is the small durable store surface needed for hypothesis
// jobs. It is intentionally satisfied by platform/valkey.Client.
type HypothesisJobStore interface {
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
	GetJSON(ctx context.Context, key string, dest any) (bool, error)
	SetNX(ctx context.Context, key, val string, ttl time.Duration) (bool, error)
	LPush(ctx context.Context, key string, values ...string) error
	LTrim(ctx context.Context, key string, start, stop int64) error
	LRange(ctx context.Context, key string, start, stop int64) ([]string, error)
}

// HypothesisJobInput is the durable input payload for one long-running
// hypothesis operation.
type HypothesisJobInput struct {
	HypothesisID   string   `json:"hypothesis_id,omitempty"`
	KPIID          string   `json:"kpi_id,omitempty"`
	KPITitle       string   `json:"kpi_title,omitempty"`
	KPIMetric      string   `json:"kpi_metric,omitempty"`
	KPIDescription string   `json:"kpi_description,omitempty"`
	Constraints    string   `json:"constraints,omitempty"`
	Count          int      `json:"count,omitempty"`
	DocumentIDs    []string `json:"document_ids,omitempty"`
}

// HypothesisJob is the user-visible durable job record. ResultIDs are populated
// with created/updated hypothesis ids when the job succeeds.
type HypothesisJob struct {
	ID          string             `json:"id"`
	OwnerID     string             `json:"owner_id"`
	Kind        string             `json:"kind"`
	Status      string             `json:"status"`
	Input       HypothesisJobInput `json:"input"`
	ResultIDs   []string           `json:"result_ids,omitempty"`
	Error       string             `json:"error,omitempty"`
	CreatedAt   string             `json:"created_at"`
	StartedAt   string             `json:"started_at,omitempty"`
	HeartbeatAt string             `json:"heartbeat_at,omitempty"`
	FinishedAt  string             `json:"finished_at,omitempty"`
}

// HypothesisJobService owns durable background execution for model-dependent
// hypothesis operations. It does not cancel jobs when the client disconnects.
type HypothesisJobService struct {
	hypotheses *HypothesisService
	store      HypothesisJobStore
	// bgSem throttles background jobs (verify/TRL/enrich auto-enqueued on board
	// views) so their LLM bursts never starve an interactive generate job of
	// upstream model throughput. Generate jobs bypass it.
	bgSem chan struct{}
}

// NewHypothesisJobService wires the durable hypothesis-job runner over a
// hypothesis service and a small Valkey-backed job store.
func NewHypothesisJobService(hypotheses *HypothesisService, store HypothesisJobStore) *HypothesisJobService {
	return &HypothesisJobService{
		hypotheses: hypotheses,
		store:      store,
		bgSem:      make(chan struct{}, backgroundJobConcurrency),
	}
}

func hypothesisJobKey(ownerID, id string) string {
	return "rag:hypothesis_jobs:" + ownerID + ":" + id
}

func hypothesisJobListKey(ownerID string) string {
	return "rag:hypothesis_jobs:" + ownerID + ":ids"
}

func hypothesisJobGlobalListKey() string {
	return "rag:hypothesis_jobs:global_ids"
}

func hypothesisJobClaimKey(id string) string {
	return "rag:hypothesis_jobs:claims:" + id
}

func hypothesisAutoVerifyKey(ownerID, hypothesisID string) string {
	return "rag:hypothesis_jobs:auto_verify:" + ownerID + ":" + hypothesisID
}

func hypothesisAutoTRLKey(ownerID, hypothesisID string) string {
	return "rag:hypothesis_jobs:auto_trl:" + ownerID + ":" + hypothesisID
}

func hypothesisAutoEnrichKey(ownerID, hypothesisID string) string {
	return "rag:hypothesis_jobs:auto_enrich:" + ownerID + ":" + hypothesisID
}

func hypothesisJobRef(ownerID, id string) string {
	return ownerID + "\t" + id
}

func parseHypothesisJobRef(ref string) (string, string, bool) {
	ownerID, id, ok := strings.Cut(ref, "\t")
	return ownerID, id, ok && ownerID != "" && id != ""
}

func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// Enqueue stores a durable job and starts local execution.
func (s *HypothesisJobService) Enqueue(
	ctx context.Context, ownerID, kind string, in HypothesisJobInput,
) (*HypothesisJob, error) {
	if s == nil || s.store == nil || s.hypotheses == nil {
		return nil, errors.New("hypothesis job service is not configured")
	}
	if ownerID == "" {
		return nil, ErrForbidden
	}
	if err := validateHypothesisJobInput(kind, in); err != nil {
		return nil, err
	}
	now := utcNow()
	job := &HypothesisJob{
		ID:        uuid.NewString(),
		OwnerID:   ownerID,
		Kind:      kind,
		Status:    HypothesisJobQueued,
		Input:     in,
		CreatedAt: now,
	}
	if err := s.save(ctx, job); err != nil {
		return nil, err
	}
	if err := s.store.LPush(ctx, hypothesisJobListKey(ownerID), job.ID); err != nil {
		return nil, err
	}
	_ = s.store.LTrim(ctx, hypothesisJobListKey(ownerID), 0, hypothesisJobListMax-1)
	if err := s.store.LPush(ctx, hypothesisJobGlobalListKey(), hypothesisJobRef(ownerID, job.ID)); err != nil {
		return nil, err
	}
	_ = s.store.LTrim(ctx, hypothesisJobGlobalListKey(), 0, hypothesisJobGlobalMax-1)
	// run mutates the job as it executes; hand the goroutine its own copy so the
	// record returned here (and rendered by the HTTP handler) is never written
	// concurrently. The durable store, keyed by job ID, remains the shared truth.
	runJob := *job
	go s.run(ctx, &runJob)
	return job, nil
}

// StartWorker periodically resumes durable jobs that were queued before this
// process started. The SetNX claim keeps multiple main-service replicas from
// executing the same queued job concurrently.
func (s *HypothesisJobService) StartWorker(ctx context.Context) {
	if s == nil || s.store == nil || s.hypotheses == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		s.resumeQueued(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.resumeQueued(ctx)
			}
		}
	}()
}

func (s *HypothesisJobService) resumeQueued(ctx context.Context) {
	refs, err := s.store.LRange(ctx, hypothesisJobGlobalListKey(), 0, hypothesisJobGlobalMax-1)
	if err != nil {
		return
	}
	for _, ref := range refs {
		ownerID, id, ok := parseHypothesisJobRef(ref)
		if !ok {
			continue
		}
		job, jerr := s.Get(ctx, ownerID, id)
		if jerr != nil || job.Status != HypothesisJobQueued {
			continue
		}
		go s.run(ctx, job)
	}
}

func validateHypothesisJobInput(kind string, in HypothesisJobInput) error {
	switch kind {
	case HypothesisJobGenerate:
		if in.KPIID == "" && in.KPITitle == "" {
			return fmt.Errorf("kpi_id or kpi_title is required: %w", ErrInvalidArgument)
		}
	case HypothesisJobVerify, HypothesisJobEnrich, HypothesisJobAssessTRL,
		HypothesisJobCompetitors, HypothesisJobRefine, HypothesisJobTag:
		if in.HypothesisID == "" {
			return fmt.Errorf("hypothesis_id is required: %w", ErrInvalidArgument)
		}
	default:
		return fmt.Errorf("unsupported hypothesis job kind %q: %w", kind, ErrInvalidArgument)
	}
	return nil
}

func (s *HypothesisJobService) save(ctx context.Context, job *HypothesisJob) error {
	if err := s.store.SetJSON(ctx, hypothesisJobKey(job.OwnerID, job.ID), job, hypothesisJobTTL); err != nil {
		return fmt.Errorf("save hypothesis job: %w", err)
	}
	return nil
}

// Get returns a job owned by ownerID. Running jobs with stale heartbeats are
// reported as failed so the UI never shows zombie work forever after a crash.
func (s *HypothesisJobService) Get(ctx context.Context, ownerID, id string) (*HypothesisJob, error) {
	var job HypothesisJob
	ok, err := s.store.GetJSON(ctx, hypothesisJobKey(ownerID, id), &job)
	if err != nil {
		return nil, err
	}
	if !ok || job.OwnerID != ownerID {
		return nil, ErrNotFound
	}
	return normalizeHypothesisJob(&job), nil
}

// GetAny returns a job by id regardless of its owner — the privileged (admin)
// polling path for jobs enqueued on behalf of another owner.
func (s *HypothesisJobService) GetAny(ctx context.Context, id string) (*HypothesisJob, error) {
	if s == nil || s.store == nil {
		return nil, ErrNotFound
	}
	refs, err := s.store.LRange(ctx, hypothesisJobGlobalListKey(), 0, hypothesisJobGlobalMax-1)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		ownerID, jobID, ok := parseHypothesisJobRef(ref)
		if !ok || jobID != id {
			continue
		}
		return s.Get(ctx, ownerID, jobID)
	}
	return nil, ErrNotFound
}

// List returns recent jobs for an owner, newest first.
func (s *HypothesisJobService) List(ctx context.Context, ownerID string, limit int) ([]*HypothesisJob, error) {
	if limit <= 0 || limit > hypothesisJobListMax {
		limit = 50
	}
	ids, err := s.store.LRange(ctx, hypothesisJobListKey(ownerID), 0, int64(limit-1))
	if err != nil {
		return nil, err
	}
	out := make([]*HypothesisJob, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		job, jerr := s.Get(ctx, ownerID, id)
		if jerr != nil {
			continue
		}
		out = append(out, job)
	}
	return out, nil
}

// ListRecentAll returns recent jobs across all owners, newest first — the
// admin-facing view for duration stats. Best-effort: unreadable refs are skipped.
func (s *HypothesisJobService) ListRecentAll(ctx context.Context, limit int) []*HypothesisJob {
	if s == nil || s.store == nil {
		return nil
	}
	if limit <= 0 || limit > hypothesisJobGlobalMax {
		limit = hypothesisJobListMax
	}
	refs, err := s.store.LRange(ctx, hypothesisJobGlobalListKey(), 0, int64(limit-1))
	if err != nil {
		return nil
	}
	out := make([]*HypothesisJob, 0, len(refs))
	for _, ref := range refs {
		ownerID, id, ok := parseHypothesisJobRef(ref)
		if !ok {
			continue
		}
		job, jerr := s.Get(ctx, ownerID, id)
		if jerr != nil {
			continue
		}
		out = append(out, job)
	}
	return out
}

// HypothesisJobStat is a per-kind duration aggregate over finished durable jobs.
type HypothesisJobStat struct {
	Kind   string  `json:"kind"`
	Count  int     `json:"count"`
	P50Sec float64 `json:"p50_sec"`
	MaxSec float64 `json:"max_sec"`
}

// SummarizeHypothesisJobs aggregates full job durations (StartedAt→FinishedAt)
// of succeeded jobs finished within window before now, grouped by kind. It backs
// the «первичный набор гипотез за минуты» requirement on the admin perf panel.
func SummarizeHypothesisJobs(jobs []*HypothesisJob, now time.Time, window time.Duration) []HypothesisJobStat {
	durs := map[string][]float64{}
	for _, job := range jobs {
		if job == nil || job.Status != HypothesisJobSucceeded {
			continue
		}
		started, serr := time.Parse(time.RFC3339Nano, job.StartedAt)
		finished, ferr := time.Parse(time.RFC3339Nano, job.FinishedAt)
		if serr != nil || ferr != nil || finished.Before(started) || now.Sub(finished) > window {
			continue
		}
		durs[job.Kind] = append(durs[job.Kind], finished.Sub(started).Seconds())
	}
	out := make([]HypothesisJobStat, 0, len(durs))
	for _, kind := range slices.Sorted(maps.Keys(durs)) {
		d := durs[kind]
		slices.Sort(d)
		out = append(out, HypothesisJobStat{
			Kind:   kind,
			Count:  len(d),
			P50Sec: roundSec(d[(len(d)-1)/2]),
			MaxSec: roundSec(d[len(d)-1]),
		})
	}
	return out
}

func roundSec(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func (s *HypothesisJobService) activeKindCount(ctx context.Context, ownerID, kind string) int {
	jobs, err := s.List(ctx, ownerID, hypothesisJobListMax)
	if err != nil {
		return 0
	}
	active := 0
	for _, job := range jobs {
		if job == nil || job.Kind != kind {
			continue
		}
		if hypothesisJobCountsAsActive(job) {
			active++
		}
	}
	return active
}

func hypothesisJobCountsAsActive(job *HypothesisJob) bool {
	if job.Status == HypothesisJobQueued {
		return true
	}
	if job.Status != HypothesisJobRunning {
		return false
	}
	heartbeat := job.HeartbeatAt
	if heartbeat == "" {
		heartbeat = job.StartedAt
	}
	t, err := time.Parse(time.RFC3339Nano, heartbeat)
	return err == nil && time.Since(t) <= hypothesisJobActiveMax
}

// ensureAutoJobs is the shared engine behind the auto-trigger helpers: it caps
// how many jobs of kind may start (accounting for already-active ones), then
// enqueues one per eligible hypothesis behind a short-lived idempotency marker.
func (s *HypothesisJobService) ensureAutoJobs(
	ctx context.Context,
	ownerID string,
	hs []*commonv1.Hypothesis,
	limit int,
	kind string,
	eligible func(*commonv1.Hypothesis, string) bool,
	markerKey func(ownerID, hypothesisID string) string,
	markerTTL time.Duration,
) int {
	if s == nil || s.store == nil || s.hypotheses == nil || ownerID == "" || limit == 0 {
		return 0
	}
	if limit < 0 {
		limit = len(hs)
	}
	capacity := limit
	if limit > 0 {
		capacity -= s.activeKindCount(ctx, ownerID, kind)
		if capacity <= 0 {
			return 0
		}
	}
	started := 0
	for _, h := range hs {
		if !eligible(h, ownerID) {
			continue
		}
		ok, err := s.store.SetNX(ctx, markerKey(ownerID, h.GetId()), "1", markerTTL)
		if err != nil || !ok {
			continue
		}
		if _, err := s.Enqueue(ctx, ownerID, kind, HypothesisJobInput{HypothesisID: h.GetId()}); err != nil {
			continue
		}
		started++
		if capacity > 0 && started >= capacity {
			break
		}
	}
	return started
}

// EnsureVerify starts automatic evidence checking for hypotheses that do not yet
// have a corpus verdict. It is idempotent over a short TTL so passive UI
// refreshes do not flood retrieval/LLM with duplicate jobs.
func (s *HypothesisJobService) EnsureVerify(
	ctx context.Context,
	ownerID string,
	hs []*commonv1.Hypothesis,
	limit int,
) int {
	return s.ensureAutoJobs(
		ctx, ownerID, hs, limit,
		HypothesisJobVerify, verifyEligible, hypothesisAutoVerifyKey, hypothesisAutoVerifyTTL,
	)
}

// EnsureAssessTRL starts automatic readiness scoring for hypotheses that do not
// yet have a УГТ value. It is idempotent over a short TTL so passive UI refreshes
// do not flood the LLM with duplicate jobs.
func (s *HypothesisJobService) EnsureAssessTRL(
	ctx context.Context,
	ownerID string,
	hs []*commonv1.Hypothesis,
	limit int,
) int {
	return s.ensureAutoJobs(
		ctx, ownerID, hs, limit,
		HypothesisJobAssessTRL, assessTRLEligible, hypothesisAutoTRLKey, hypothesisAutoTRLTTL,
	)
}

// EnsureEnrich starts Stage-2 enrichment for verified hypotheses that have not
// been enriched yet. It runs strictly AFTER verification (a corpus verdict must
// exist) and is idempotent over a short TTL so passive UI refreshes do not flood
// the LLM with duplicate jobs.
func (s *HypothesisJobService) EnsureEnrich(
	ctx context.Context,
	ownerID string,
	hs []*commonv1.Hypothesis,
	limit int,
) int {
	return s.ensureAutoJobs(
		ctx, ownerID, hs, limit,
		HypothesisJobEnrich, enrichEligible, hypothesisAutoEnrichKey, hypothesisAutoEnrichTTL,
	)
}

// enrichEligible reports whether a hypothesis should be auto-enriched: owned,
// open, already verified (Stage-2 runs after verify, never before) and not yet
// enriched (marker read from the board row's assessment).
func enrichEligible(h *commonv1.Hypothesis, ownerID string) bool {
	if h == nil || h.GetOwnerId() != ownerID || h.GetId() == "" {
		return false
	}
	if h.GetStatus() == statusRejected || h.GetStatus() == statusArchived {
		return false
	}
	return verdictFromAssessment(h.GetAssessment()) != "" && !enrichmentDone(h.GetAssessment())
}

// verifyEligible reports whether a hypothesis should be auto-verified: owned,
// open (not rejected/archived) and still lacking a corpus verdict.
func verifyEligible(h *commonv1.Hypothesis, ownerID string) bool {
	if h == nil || h.GetOwnerId() != ownerID || h.GetId() == "" {
		return false
	}
	if h.GetStatus() == statusRejected || h.GetStatus() == statusArchived {
		return false
	}
	return verdictFromAssessment(h.GetAssessment()) == ""
}

// assessTRLEligible reports whether a hypothesis should be auto-scored for УГТ:
// owned, open, not yet scored and already carrying a corpus verdict (TRL scoring
// runs strictly after verification).
func assessTRLEligible(h *commonv1.Hypothesis, ownerID string) bool {
	if h == nil || h.Trl != nil || h.GetOwnerId() != ownerID || h.GetId() == "" {
		return false
	}
	if h.GetStatus() == statusRejected || h.GetStatus() == statusArchived {
		return false
	}
	return verdictFromAssessment(h.GetAssessment()) != ""
}

func normalizeHypothesisJob(job *HypothesisJob) *HypothesisJob {
	if job.Status != HypothesisJobRunning {
		return job
	}
	heartbeat := job.HeartbeatAt
	if heartbeat == "" {
		heartbeat = job.StartedAt
	}
	t, err := time.Parse(time.RFC3339Nano, heartbeat)
	if err == nil && time.Since(t) > hypothesisJobStaleAfter {
		cp := *job
		cp.Status = HypothesisJobFailed
		cp.Error = "job heartbeat expired; retry the operation"
		cp.FinishedAt = utcNow()
		return &cp
	}
	return job
}

func (s *HypothesisJobService) run(parent context.Context, job *HypothesisJob) {
	// Durable jobs must outlive the originating request/worker tick, so detach
	// cancellation while preserving trace/logger values from the parent context.
	ctx := context.WithoutCancel(parent)
	claimed, err := s.store.SetNX(ctx, hypothesisJobClaimKey(job.ID), job.OwnerID, hypothesisJobClaimTTL)
	if err != nil || !claimed {
		return
	}
	if job.Kind != HypothesisJobGenerate {
		s.bgSem <- struct{}{}
		defer func() { <-s.bgSem }()
	}
	now := utcNow()
	job.Status = HypothesisJobRunning
	job.StartedAt = now
	job.HeartbeatAt = now
	_ = s.save(ctx, job)
	stopHeartbeat := s.startHeartbeat(ctx, job)

	ids, err := s.execute(ctx, job.OwnerID, job.Kind, job.Input)
	stopHeartbeat()
	job.FinishedAt = utcNow()
	job.HeartbeatAt = job.FinishedAt
	if err != nil {
		job.Status = HypothesisJobFailed
		job.Error = err.Error()
	} else {
		job.Status = HypothesisJobSucceeded
		job.ResultIDs = ids
	}
	_ = s.save(ctx, job)
}

func (s *HypothesisJobService) startHeartbeat(ctx context.Context, job *HypothesisJob) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	snapshot := *job
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				cp := snapshot
				cp.HeartbeatAt = utcNow()
				_ = s.save(ctx, &cp)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (s *HypothesisJobService) execute(
	ctx context.Context, ownerID, kind string, in HypothesisJobInput,
) ([]string, error) {
	switch kind {
	case HypothesisJobGenerate:
		kpi, err := s.jobKPI(ctx, ownerID, in)
		if err != nil {
			return nil, err
		}
		created, err := s.hypotheses.GenerateFromDocuments(ctx, ownerID, kpi, in.Count, in.Constraints, in.DocumentIDs)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(created))
		for _, h := range created {
			ids = append(ids, h.GetId())
		}
		return ids, nil
	case HypothesisJobVerify:
		return singleHypothesisID(s.hypotheses.Verify(ctx, ownerID, in.HypothesisID))
	case HypothesisJobEnrich:
		return singleHypothesisID(s.hypotheses.EnrichHypothesis(ctx, ownerID, in.HypothesisID))
	case HypothesisJobAssessTRL:
		return singleHypothesisID(s.hypotheses.AssessTRL(ctx, ownerID, in.HypothesisID))
	case HypothesisJobCompetitors:
		return singleHypothesisID(s.hypotheses.AnalyzeCompetitors(ctx, ownerID, in.HypothesisID))
	case HypothesisJobRefine:
		return singleHypothesisID(s.hypotheses.Refine(ctx, ownerID, in.HypothesisID))
	case HypothesisJobTag:
		return singleHypothesisID(s.hypotheses.TagHypothesis(ctx, ownerID, in.HypothesisID))
	default:
		return nil, fmt.Errorf("unsupported hypothesis job kind %q: %w", kind, ErrInvalidArgument)
	}
}

func (s *HypothesisJobService) jobKPI(
	ctx context.Context, ownerID string, in HypothesisJobInput,
) (*commonv1.KPI, error) {
	if in.KPIID != "" {
		return s.hypotheses.GetKPI(ctx, ownerID, in.KPIID)
	}
	return s.hypotheses.CreateKPI(ctx, ownerID, &dbv1.CreateKPIRequest{
		Title:       in.KPITitle,
		Metric:      in.KPIMetric,
		Description: in.KPIDescription,
	})
}

func singleHypothesisID(h *commonv1.Hypothesis, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	return []string{h.GetId()}, nil
}
