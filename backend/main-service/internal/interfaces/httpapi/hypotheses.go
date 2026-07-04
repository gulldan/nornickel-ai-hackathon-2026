package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/platform/httpx"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

// Board queue identifiers, shared by the filter (boardQueueMatch) and the counts
// projection (boardQueueCounts).
const (
	queueAll          = "all"
	queueNeedsVerify  = "needs_verify"
	queueNeedsTRL     = "needs_trl"
	queueReady        = "ready"
	queueRisk         = "risk"
	queueInsufficient = "insufficient"

	autoVerifyEnqueuePerBoard = 4
	autoTRLEnqueuePerBoard    = 4
	autoEnrichEnqueuePerBoard = 4

	// boardEvidenceConcurrency bounds the parallel per-hypothesis evidence
	// hydration reads against db-service on a board page load.
	boardEvidenceConcurrency = 8
)

// decodeResource reads a JSON resource body for the create/update endpoints.
// Unlike httpx.Decode it tolerates unknown and server-managed fields (id,
// owner_id, timestamps, evidence ids), so a client can PUT back a representation
// it just GET'd (read-modify-write) without a 400.
func decodeResource(r *http.Request, v any) error {
	return decodeResourceLimited(r, v, 1<<20)
}

// decodeResourceLimited decodes a JSON body with an explicit size cap. Most
// resource POSTs are a single small object (1 MiB is plenty), but bulk endpoints
// like /clusters/replace publish the whole board — hundreds of clusters carrying
// full membership lists — which scales with the corpus and needs a bigger cap.
func decodeResourceLimited(r *http.Request, v any, maxBytes int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBytes))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	return nil
}

// The Hypothesis Factory endpoints are owner-scoped: any signed-in user manages
// their own KPIs, clusters and hypotheses (no role gate). The owner id always
// comes from the verified JWT, never the request body.

// updateOwned is the shared shape of the owner-scoped PUT handlers: resolve the
// owner, decode I, apply the update, re-read the row and render it as V.
func updateOwned[I, P, V any](
	a *API, w http.ResponseWriter, r *http.Request,
	apply func(ctx context.Context, ownerID, id string, in I) error,
	get func(ctx context.Context, ownerID, id string) (P, error),
	view func(P) V,
) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in I
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	if err := apply(r.Context(), ownerID, id, in); err != nil {
		a.fail(w, err)
		return
	}
	out, err := get(r.Context(), ownerID, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view(out))
}

// ---- Hypotheses ----

// hypothesisActOwner resolves the effective owner for an action on
// /hypotheses/{id}: the caller for regular users; for a privileged caller
// (admin) — the record's real owner, so the admin acts on behalf of the owner
// and downstream owner-scoped calls (jobs, KPIs, retrieval) stay consistent.
func (a *API) hypothesisActOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	return a.actOwnerFor(w, r, r.PathValue("id"))
}

func (a *API) actOwnerFor(w http.ResponseWriter, r *http.Request, hypothesisID string) (string, bool) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return "", false
	}
	if !a.hypothesesPrivileged(r) {
		return ownerID, true
	}
	h, err := a.hypotheses.GetHypothesis(r.Context(), ownerID, true, hypothesisID)
	if err != nil {
		a.fail(w, err)
		return "", false
	}
	return h.GetOwnerId(), true
}

func groupHypothesesByOwner(hs []*commonv1.Hypothesis) map[string][]*commonv1.Hypothesis {
	out := map[string][]*commonv1.Hypothesis{}
	for _, h := range hs {
		if h == nil || h.GetOwnerId() == "" {
			continue
		}
		out[h.GetOwnerId()] = append(out[h.GetOwnerId()], h)
	}
	return out
}

func (a *API) listHypotheses(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	q := r.URL.Query()
	req := &dbv1.ListHypothesesRequest{
		Status: q.Get("status"), KpiId: q.Get("kpi_id"), ClusterId: q.Get("cluster_id"),
		FunctionArea: q.Get("function_area"), SourceType: q.Get("source_type"), Organization: q.Get("organization"),
		MinTrl: int32(queryInt(r, "min_trl", 0)), MaxTrl: int32(queryInt(r, "max_trl", 0)),
		OrderBy: q.Get("order_by"), Limit: int32(queryInt(r, "limit", 0)), Offset: int32(queryInt(r, "offset", 0)),
	}
	if tags := strings.TrimSpace(q.Get("tags")); tags != "" {
		req.Tags = strings.Split(tags, ",")
	}
	req.DocumentIds = splitIDList(q.Get("document_ids"))
	hs, err := a.hypotheses.ListHypotheses(r.Context(), ownerID, a.hypothesesPrivileged(r), req)
	if err != nil {
		a.fail(w, err)
		return
	}
	if q.Get("view") == "ref" {
		httpx.JSON(w, http.StatusOK, newHypothesisRefViews(hs))
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisViews(hs))
}

func (a *API) hypothesisBoard(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	q := r.URL.Query()
	req := &dbv1.ListHypothesesRequest{
		Status: q.Get("status"), KpiId: q.Get("kpi_id"), ClusterId: q.Get("cluster_id"),
		FunctionArea: q.Get("function_area"), SourceType: q.Get("source_type"), Organization: q.Get("organization"),
		MinTrl: int32(queryInt(r, "min_trl", 0)), MaxTrl: int32(queryInt(r, "max_trl", 0)),
		OrderBy: q.Get("order_by"),
	}
	if tags := strings.TrimSpace(q.Get("tags")); tags != "" {
		req.Tags = strings.Split(tags, ",")
	}
	privileged := a.hypothesesPrivileged(r)
	hs, err := a.hypotheses.ListHypotheses(r.Context(), ownerID, privileged, req)
	if err != nil {
		a.fail(w, err)
		return
	}
	base := boardFilterHypotheses(hs, q.Get("q"), "")
	filtered := boardFilterHypotheses(base, "", q.Get("queue"))
	total := len(filtered)
	limit := queryInt(r, "limit", 60)
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	page := []*commonv1.Hypothesis{}
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		page = filtered[offset:end]
	}
	if a.hypJobs != nil {
		for owner, group := range groupHypothesesByOwner(page) {
			a.hypJobs.EnsureVerify(r.Context(), owner, group, autoVerifyEnqueuePerBoard)
			a.hypJobs.EnsureAssessTRL(r.Context(), owner, group, autoTRLEnqueuePerBoard)
			// After verify: EnsureEnrich only enqueues rows that already carry a verdict.
			a.hypJobs.EnsureEnrich(r.Context(), owner, group, autoEnrichEnqueuePerBoard)
		}
	}
	page = a.hydrateBoardEvidence(r.Context(), ownerID, privileged, page)
	httpx.JSON(w, http.StatusOK, newHypothesisBoardView(page, filtered, base, limit, offset, total))
}

func (a *API) hydrateBoardEvidence(
	ctx context.Context,
	ownerID string,
	privileged bool,
	page []*commonv1.Hypothesis,
) []*commonv1.Hypothesis {
	if len(page) == 0 {
		return page
	}
	out := make([]*commonv1.Hypothesis, len(page))
	sem := make(chan struct{}, boardEvidenceConcurrency)
	var wg sync.WaitGroup
	for i, h := range page {
		if h == nil || len(h.GetEvidence()) > 0 {
			out[i] = h
			continue
		}
		wg.Add(1)
		go func(i int, h *commonv1.Hypothesis) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ev, err := a.hypotheses.ListEvidence(ctx, ownerID, privileged, h.GetId())
			if err != nil || len(ev) == 0 {
				out[i] = h
				return
			}
			clone, _ := proto.Clone(h).(*commonv1.Hypothesis)
			clone.Evidence = ev
			out[i] = clone
		}(i, h)
	}
	wg.Wait()
	return out
}

func (a *API) createHypothesis(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in hypothesisInput
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Title == "" || in.Statement == "" {
		httpx.Error(w, http.StatusBadRequest, "title and statement are required")
		return
	}
	req := &dbv1.CreateHypothesisRequest{
		Hypothesis: in.toProto(""),
		Initial:    in.Revision.toProto(""),
	}
	h, err := a.hypotheses.CreateHypothesis(r.Context(), ownerID, req)
	if err != nil {
		a.fail(w, err)
		return
	}
	if a.hypJobs != nil {
		a.hypJobs.EnsureVerify(r.Context(), ownerID, []*commonv1.Hypothesis{h}, 1)
		a.hypJobs.EnsureAssessTRL(r.Context(), ownerID, []*commonv1.Hypothesis{h}, 1)
	}
	httpx.JSON(w, http.StatusCreated, newHypothesisView(h))
}

func boardFilterHypotheses(hs []*commonv1.Hypothesis, search, queue string) []*commonv1.Hypothesis {
	q := strings.ToLower(strings.TrimSpace(search))
	out := make([]*commonv1.Hypothesis, 0, len(hs))
	for _, h := range hs {
		if q != "" && !boardSearchMatch(h, q) {
			continue
		}
		if queue != "" && queue != queueAll && !boardQueueMatch(h, queue) {
			continue
		}
		out = append(out, h)
	}
	return out
}

func boardSearchMatch(h *commonv1.Hypothesis, q string) bool {
	text := strings.ToLower(strings.Join([]string{
		h.GetTitle(), h.GetStatement(), h.GetRationale(), h.GetLocation(), h.GetOrganization(),
		h.GetFunctionArea(), h.GetSourceType(), strings.Join(h.GetTags(), " "),
	}, " "))
	return strings.Contains(text, q)
}

// boardQueueMatch reports whether h belongs to queue. The work queues partition
// on the verify verdict so a hypothesis lands in exactly one of them — no
// overlaps (a supported hypothesis never shows under «Противоречия» just because
// its risk score is high). Closed rows belong to no work queue, only «Все».
func boardQueueMatch(h *commonv1.Hypothesis, queue string) bool {
	closed := h.GetStatus() == "rejected" || h.GetStatus() == "archived"
	switch queue {
	case queueAll:
		return true
	case queueNeedsTRL:
		return !closed && h.Trl == nil
	}
	if closed {
		return false
	}
	return boardPrimaryQueue(h.GetStatus(), boardVerdict(h.GetAssessment())) == queue
}

func boardPrimaryQueue(status, verdict string) string {
	if status == "approved" || verdict == "supported" {
		return queueReady
	}
	switch verdict {
	case "refuted", "mixed":
		return queueRisk
	case "insufficient":
		return queueInsufficient
	}
	return queueNeedsVerify
}

func boardVerdict(assessment string) string {
	if assessment == "" {
		return ""
	}
	var m struct {
		Check struct {
			Verdict string `json:"verdict"`
		} `json:"check"`
	}
	if err := json.Unmarshal([]byte(assessment), &m); err != nil {
		return ""
	}
	return m.Check.Verdict
}

// generateHypotheses runs the KPI → hypotheses loop. The KPI is referenced by id
// or created inline from kpi_title; count caps how many to generate.
func (a *API) generateHypotheses(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in struct {
		KPIID          string   `json:"kpi_id"`
		KPITitle       string   `json:"kpi_title"`
		KPIMetric      string   `json:"kpi_metric"`
		KPIDescription string   `json:"kpi_description"`
		Constraints    string   `json:"constraints"`
		Count          int      `json:"count"`
		DocumentIDs    []string `json:"document_ids"`
	}
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	var kpi *commonv1.KPI
	var err error
	switch {
	case in.KPIID != "":
		kpi, err = a.hypotheses.GetKPI(r.Context(), ownerID, in.KPIID)
	case strings.TrimSpace(in.KPITitle) != "":
		// Metric/description broaden retrievalQuery so cluster-based generation
		// retrieves richer evidence than the bare label would.
		kpi, err = a.hypotheses.CreateKPI(r.Context(), ownerID, &dbv1.CreateKPIRequest{
			Title: in.KPITitle, Metric: in.KPIMetric, Description: in.KPIDescription,
		})
	default:
		httpx.Error(w, http.StatusBadRequest, "kpi_id or kpi_title is required")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	created, gerr := a.hypotheses.GenerateFromDocuments(r.Context(), ownerID, kpi, in.Count, in.Constraints, in.DocumentIDs)
	if gerr != nil {
		a.fail(w, gerr)
		return
	}
	a.triggerHypothesisITC(r.Context(), len(created))
	httpx.JSON(w, http.StatusCreated, newHypothesisViews(created))
}

func (a *API) triggerHypothesisITC(ctx context.Context, count int) {
	if a.metrics == nil || count == 0 {
		return
	}
	triggeredAt := time.Now().UTC().Format(time.RFC3339Nano)
	value := fmt.Sprintf("hypotheses-generate:%d:%s", count, triggeredAt)
	_ = a.metrics.Set(ctx, itcTriggerKey, value, 0)
}

// verifyHypothesis checks a hypothesis against the corpus (confirm / refute) and
// returns it with the verdict recorded under assessment.check.
func (a *API) verifyHypothesis(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	h, err := a.hypotheses.Verify(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	// Собрать view до постановки фоновых джобов: они могут мутировать h
	// конкурентно (с ин-процесс каталогом указатель общий).
	view := newHypothesisView(h)
	if a.hypJobs != nil {
		a.hypJobs.EnsureAssessTRL(r.Context(), ownerID, []*commonv1.Hypothesis{h}, 1)
		a.hypJobs.EnsureEnrich(r.Context(), ownerID, []*commonv1.Hypothesis{h}, 1)
	}
	httpx.JSON(w, http.StatusOK, view)
}

// enrichHypothesis runs Stage-2 enrichment (fill the rich passport fields from the
// full source text) then the quality gate; returns the updated hypothesis.
func (a *API) enrichHypothesis(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	h, err := a.hypotheses.EnrichHypothesis(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

// assessHypothesisTRL computes the TRL level (ГОСТ Р 58048-2017) for a
// hypothesis and stores it under assessment.trl; returns the updated hypothesis.
func (a *API) assessHypothesisTRL(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	h, err := a.hypotheses.AssessTRL(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

// analyzeCompetitors finds competing approaches for a hypothesis from the corpus
// and stores them under detail.competitors; returns the updated hypothesis.
func (a *API) analyzeCompetitors(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	h, err := a.hypotheses.AnalyzeCompetitors(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

// tagHypothesis classifies a hypothesis into ГРНТИ / ВАК / Scopus ASJC scientific
// specialties and stores the tags; returns the updated hypothesis.
func (a *API) tagHypothesis(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	h, err := a.hypotheses.TagHypothesis(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

// planExperiment turns a hypothesis into a concrete experiment plan (materials,
// process parameters, methods, controls, success criteria, cost/time, risks),
// stored under detail.experiment_plan; returns the updated hypothesis.
func (a *API) planExperiment(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	h, err := a.hypotheses.PlanExperiment(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

// refineHypothesis runs the multi-agent loop (verify → revise if weak → re-verify)
// over a hypothesis and returns the refined result.
func (a *API) refineHypothesis(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	h, err := a.hypotheses.Refine(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

// trlRubric serves the TRL rubric (levels + criteria) so the UI can explain the score.
func (a *API) trlRubric(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(a.hypotheses.TRLRubric())
}

// storeHypothesisITC records a deterministically-computed ITC (Technology Index)
// on a hypothesis and links it to its theme (primary_cluster_id); returns the
// updated hypothesis.
func (a *API) storeHypothesisITC(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	var in application.ITCInput
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	h, err := a.hypotheses.StoreITC(r.Context(), ownerID, r.PathValue("id"), in)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

// itcRubric serves the ITC methodology rubric (components, bands, TechScore) so
// the UI can explain how the index is computed.
func (a *API) itcRubric(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(a.hypotheses.ITCRubric())
}

// ---- Scoring weights ----

// getScoringWeights returns the owner's transparent-ranking weights — their
// saved override, or the default profile when none is stored.
func (a *API) getScoringWeights(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	httpx.JSON(w, http.StatusOK, a.hypotheses.GetScoringWeights(r.Context(), ownerID))
}

// updateScoringWeights validates and persists the owner's ranking weights. Each
// weight must be within [0, 1] and at least one must be positive; the saved set
// is normalised to sum to 1 so the explained composite stays comparable.
func (a *API) updateScoringWeights(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in application.ScoringWeights
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg, valid := validateScoringWeights(in); !valid {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	saved := normalizeScoringWeights(in)
	if err := a.hypotheses.SetScoringWeights(r.Context(), ownerID, saved); err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

// scoringWeightValues lists the six ranking weights for uniform validation and
// normalisation, avoiding per-field repetition.
func scoringWeightValues(w application.ScoringWeights) []float64 {
	return []float64{w.KPIFit, w.Evidence, w.Novelty, w.Value, w.RiskInv, w.TRLFit}
}

// validateScoringWeights enforces the [0, 1] range and a positive total; the
// message is user-facing (Russian, matching the rest of the edge).
func validateScoringWeights(w application.ScoringWeights) (string, bool) {
	sum := 0.0
	for _, v := range scoringWeightValues(w) {
		if v < 0 || v > 1 {
			return "каждый вес должен быть в диапазоне от 0 до 1", false
		}
		sum += v
	}
	if sum <= 0 {
		return "хотя бы один вес должен быть больше нуля", false
	}
	return "", true
}

// normalizeScoringWeights rescales the weights to sum to 1, keeping the owner's
// relative emphasis while leaving the composite score on the default scale.
func normalizeScoringWeights(w application.ScoringWeights) application.ScoringWeights {
	sum := 0.0
	for _, v := range scoringWeightValues(w) {
		sum += v
	}
	if sum == 0 {
		return w
	}
	return application.ScoringWeights{
		KPIFit:   w.KPIFit / sum,
		Evidence: w.Evidence / sum,
		Novelty:  w.Novelty / sum,
		Value:    w.Value / sum,
		RiskInv:  w.RiskInv / sum,
		TRLFit:   w.TRLFit / sum,
	}
}

// ---- Runtime settings ----

func (a *API) getHypothesisRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	httpx.JSON(w, http.StatusOK, a.hypotheses.GetRuntimeSettings(r.Context(), ownerID))
}

func (a *API) updateHypothesisRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in application.HypothesisRuntimeSettings
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	saved := application.NormalizeHypothesisRuntimeSettings(in)
	if err := a.hypotheses.SetRuntimeSettings(r.Context(), ownerID, saved); err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) getHypothesis(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	h, err := a.hypotheses.GetHypothesis(r.Context(), ownerID, a.hypothesesPrivileged(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if a.hypJobs != nil {
		owner := h.GetOwnerId()
		a.hypJobs.EnsureVerify(r.Context(), owner, []*commonv1.Hypothesis{h}, 1)
		a.hypJobs.EnsureAssessTRL(r.Context(), owner, []*commonv1.Hypothesis{h}, 1)
		a.hypJobs.EnsureEnrich(r.Context(), owner, []*commonv1.Hypothesis{h}, 1)
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

func (a *API) updateHypothesis(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var in hypothesisInput
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	req := &dbv1.UpdateHypothesisRequest{
		Hypothesis: in.toProto(id),
		Revision:   in.Revision.toProto(id),
	}
	if err := a.hypotheses.UpdateHypothesis(r.Context(), ownerID, req); err != nil {
		a.fail(w, err)
		return
	}
	h, err := a.hypotheses.GetHypothesis(r.Context(), ownerID, false, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisView(h))
}

func (a *API) listHypothesisRevisions(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	revs, err := a.hypotheses.ListRevisions(r.Context(), ownerID, a.hypothesesPrivileged(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newRevisionViews(revs))
}

func (a *API) addHypothesisRevision(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := a.hypothesisActOwner(w, r)
	if !ok {
		return
	}
	var in revisionInput
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Action == "" {
		httpx.Error(w, http.StatusBadRequest, "action is required")
		return
	}
	rev, err := a.hypotheses.AddRevision(r.Context(), ownerID, in.toProto(r.PathValue("id")))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, newRevisionView(rev))
}

// hypothesisGraph returns the owner's evidence graph — hypotheses and the source
// documents they cite, with edges classed by evidence stance (supports /
// contradicts / context) — for the "check against the knowledge base" graph view.
// This is separate from the typed knowledge graph used for bridge directions.
func (a *API) hypothesisGraph(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	g, err := a.hypotheses.Graph(r.Context(), ownerID)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newGraphView(g))
}

func (a *API) listHypothesisEvidence(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	ev, err := a.hypotheses.ListEvidence(r.Context(), ownerID, a.hypothesesPrivileged(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newEvidenceViews(ev))
}

// ---- Durable hypothesis jobs ----

func (a *API) createHypothesisJob(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if a.hypJobs == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "hypothesis job service is not configured")
		return
	}
	var in hypothesisJobInput
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.HypothesisID != "" {
		if ownerID, ok = a.actOwnerFor(w, r, in.HypothesisID); !ok {
			return
		}
	}
	job, err := a.hypJobs.Enqueue(r.Context(), ownerID, in.Kind, in.toApplicationInput())
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, newHypothesisJobView(job))
}

func (a *API) listHypothesisJobs(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if a.hypJobs == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "hypothesis job service is not configured")
		return
	}
	jobs, err := a.hypJobs.List(r.Context(), ownerID, queryInt(r, "limit", 50))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisJobViews(jobs))
}

func (a *API) getHypothesisJob(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if a.hypJobs == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "hypothesis job service is not configured")
		return
	}
	job, err := a.hypJobs.Get(r.Context(), ownerID, r.PathValue("id"))
	if err != nil && a.hypothesesPrivileged(r) {
		job, err = a.hypJobs.GetAny(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newHypothesisJobView(job))
}

// graphHypotheses synthesises cross-hypothesis "bridge" research directions for a
// KPI from the owner's knowledge graph (a process→property link from one source
// recombined with a property→KPI link from another). Returns a bounded JSON list
// of candidate directions; an empty/small graph yields an empty list, never an
// error. These are drafts and are not persisted by this endpoint.
func (a *API) graphHypotheses(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	bridges, err := a.hypotheses.GraphHypotheses(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"candidates": bridges})
}

// ---- KPIs ----

func (a *API) listKPIs(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	ks, err := a.hypotheses.ListKPIs(r.Context(), ownerID)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newKPIViews(ks))
}

func (a *API) createKPI(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in kpiInput
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Title == "" {
		httpx.Error(w, http.StatusBadRequest, "title is required")
		return
	}
	k, err := a.hypotheses.CreateKPI(r.Context(), ownerID, in.toCreateRequest())
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, newKPIView(k))
}

func (a *API) getKPI(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	k, err := a.hypotheses.GetKPI(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newKPIView(k))
}

func (a *API) updateKPI(w http.ResponseWriter, r *http.Request) {
	updateOwned(a, w, r,
		func(ctx context.Context, ownerID, id string, in kpiInput) error {
			return a.hypotheses.UpdateKPI(ctx, ownerID, in.toProto(id))
		},
		a.hypotheses.GetKPI, newKPIView)
}

func (a *API) deleteKPI(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if err := a.hypotheses.DeleteKPI(r.Context(), ownerID, r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type attachKPIDocumentsRequest struct {
	DocumentIDs []string `json:"document_ids"`
}

func (a *API) attachKPIDocuments(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in attachKPIDocumentsRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.DocumentIDs) == 0 {
		httpx.Error(w, http.StatusBadRequest, "document_ids is required")
		return
	}
	if err := a.hypotheses.AttachKPIDocuments(r.Context(), ownerID, r.PathValue("id"), in.DocumentIDs); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listKPIDocuments(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	links, err := a.hypotheses.ListKPIDocuments(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]kpiDocumentView, 0, len(links))
	for _, l := range links {
		out = append(out, newKPIDocumentView(l))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"documents": out})
}

func (a *API) detachKPIDocument(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if err := a.hypotheses.DetachKPIDocument(r.Context(), ownerID, r.PathValue("id"), r.PathValue("docId")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Clusters ----

func (a *API) listClusters(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	cs, err := a.hypotheses.ListClusters(r.Context(), ownerID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if version, ok, verr := a.currentClusterPublishVersion(r.Context(), ownerID); verr != nil {
		a.fail(w, verr)
		return
	} else if ok {
		cs = filterClustersByPublishVersion(cs, version)
	}
	httpx.JSON(w, http.StatusOK, newClusterListViews(cs))
}

func (a *API) currentClusterPublishVersion(ctx context.Context, ownerID string) (string, bool, error) {
	if a.metrics == nil {
		return "", false, nil
	}
	version, ok, err := a.metrics.Get(ctx, clusterPublishVersionKey(ownerID))
	if err != nil {
		return "", false, err
	}
	if !ok || strings.TrimSpace(version) == "" {
		return "", false, nil
	}
	return version, true, nil
}

func clusterPublishVersionKey(ownerID string) string {
	return clusterPublishVersionPrefix + ownerID
}

func filterClustersByPublishVersion(cs []*commonv1.Cluster, version string) []*commonv1.Cluster {
	out := make([]*commonv1.Cluster, 0, len(cs))
	for _, c := range cs {
		if clusterPublishVersion(c) == version {
			out = append(out, c)
		}
	}
	return out
}

func clusterPublishVersion(c *commonv1.Cluster) string {
	params := c.GetParams()
	if params == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(params), &m); err != nil {
		return ""
	}
	switch v := m["publish_version"].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return ""
	}
}

func (a *API) createCluster(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var in clusterInput
	if err := decodeResource(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Label == "" {
		httpx.Error(w, http.StatusBadRequest, "label is required")
		return
	}
	c, err := a.hypotheses.CreateCluster(r.Context(), ownerID, in.toCreateRequest())
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, newClusterView(c))
}

func (a *API) replaceClusters(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if a.metrics == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "cluster publishing store is not configured")
		return
	}
	var in struct {
		Clusters []clusterInput `json:"clusters"`
	}
	// The whole board is posted at once — full membership + representatives for
	// hundreds of clusters — so allow far more than the single-resource 1 MiB.
	if err := decodeResourceLimited(r, &in, 64<<20); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.Clusters) == 0 {
		httpx.Error(w, http.StatusBadRequest, "clusters are required")
		return
	}
	now := time.Now().UTC()
	version := strconv.FormatInt(now.UnixNano(), 10)
	created := make([]*commonv1.Cluster, 0, len(in.Clusters))
	for i := range in.Clusters {
		if strings.TrimSpace(in.Clusters[i].Label) == "" {
			httpx.Error(w, http.StatusBadRequest, "cluster label is required")
			return
		}
		in.Clusters[i].Params = mergeClusterParams(in.Clusters[i].Params, map[string]any{
			"publish_version": version,
			"published_at":    now.Format(time.RFC3339Nano),
		})
		c, err := a.hypotheses.CreateCluster(r.Context(), ownerID, in.Clusters[i].toCreateRequest())
		if err != nil {
			a.fail(w, err)
			return
		}
		created = append(created, c)
	}
	if err := a.metrics.Set(r.Context(), clusterPublishVersionKey(ownerID), version, 0); err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, newClusterViews(created))
}

func mergeClusterParams(raw json.RawMessage, values map[string]any) json.RawMessage {
	m := map[string]any{}
	if len(raw) > 0 && json.Valid(raw) {
		_ = json.Unmarshal(raw, &m)
	}
	for k, v := range values {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func (a *API) getCluster(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	c, err := a.hypotheses.GetCluster(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newClusterView(c))
}

func (a *API) updateCluster(w http.ResponseWriter, r *http.Request) {
	updateOwned(a, w, r,
		func(ctx context.Context, ownerID, id string, in clusterInput) error {
			return a.hypotheses.UpdateCluster(ctx, ownerID, in.toProto(id))
		},
		a.hypotheses.GetCluster, newClusterView)
}

// deleteClusters removes every cluster the caller owns. It remains for operator
// cleanup; automated reclustering publishes versioned sets via replaceClusters.
func (a *API) deleteClusters(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if err := a.hypotheses.DeleteAllClusters(r.Context(), ownerID); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
