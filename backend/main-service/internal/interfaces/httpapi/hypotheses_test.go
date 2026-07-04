package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// supportedVerdict is a parseable verifier reply used by the LLM-driven endpoints.
const supportedVerdict = `{"verdict":"supported","confidence":0.9,"rationale":"ok",` +
	`"supporting":["alpha beta gamma delta"],"contradicting":[]}`

// Admin sees every owner's hypotheses (list + foreign detail); regular users
// stay strictly owner-scoped.
func TestHypotheses_AdminSeesAllOwners(t *testing.T) {
	h := newHarness(t)
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h-a", OwnerId: "alice", Title: "A", Statement: "s"})
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h-b", OwnerId: "bob", Title: "B", Statement: "s"})
	user := h.token(t, "alice", "user")
	admin := h.token(t, "root", "admin")

	if r := h.do(t, http.MethodGet, "/hypotheses", user, ""); r.code != http.StatusOK ||
		strings.Contains(r.body, "h-b") {
		t.Fatalf("user list = %d, must not contain foreign rows: %s", r.code, r.body)
	}
	if r := h.do(t, http.MethodGet, "/hypotheses", admin, ""); r.code != http.StatusOK ||
		!strings.Contains(r.body, "h-a") || !strings.Contains(r.body, "h-b") {
		t.Fatalf("admin list = %d: %s", r.code, r.body)
	}
	if r := h.do(t, http.MethodGet, "/hypotheses/h-b", admin, ""); r.code != http.StatusOK ||
		!strings.Contains(r.body, `"owner_id":"bob"`) {
		t.Fatalf("admin foreign detail = %d: %s", r.code, r.body)
	}
	if r := h.do(t, http.MethodGet, "/hypotheses/h-b", user, ""); r.code != http.StatusNotFound {
		t.Fatalf("user foreign detail = %d, want 404", r.code)
	}
	if r := h.do(t, http.MethodPut, "/hypotheses/h-b", admin, `{"title":"B2","statement":"s2"}`); r.code != http.StatusOK ||
		!strings.Contains(r.body, `"owner_id":"bob"`) || !strings.Contains(r.body, `"title":"B2"`) {
		t.Fatalf("admin foreign update = %d, owner must stay bob: %s", r.code, r.body)
	}
	if r := h.do(t, http.MethodPut, "/hypotheses/h-a", user, `{"title":"A2","statement":"s2"}`); r.code != http.StatusOK {
		t.Fatalf("own update = %d: %s", r.code, r.body)
	}
}

// ---- KPIs ----

// KPI create/list/get/update round-trip with owner scoping.
func TestKPIs_Lifecycle(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	r := h.do(t, http.MethodPost, "/kpis", tok, `{"title":"Hardness","metric":"HV"}`)
	if r.code != http.StatusCreated {
		t.Fatalf("create kpi = %d: %s", r.code, r.body)
	}
	_ = r.body
	id := "kpi-Hardness"

	if r := h.do(t, http.MethodGet, "/kpis", tok, ""); r.code != http.StatusOK {
		t.Fatalf("list kpis = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodGet, "/kpis/"+id, tok, ""); r.code != http.StatusOK {
		t.Fatalf("get kpi = %d", r.code)
	} else {
		_ = r.body
	}
	// Update (read-modify-write).
	if r := h.do(t, http.MethodPut, "/kpis/"+id, tok, `{"title":"Hardness v2","metric":"HV"}`); r.code != http.StatusOK {
		t.Fatalf("update kpi = %d: %s", r.code, r.body)
	} else {
		_ = r.body
	}
	// Foreign owner cannot read it.
	if r := h.do(t, http.MethodGet, "/kpis/"+id, h.token(t, "bob"), ""); r.code != http.StatusNotFound {
		t.Fatalf("foreign kpi = %d, want 404", r.code)
	}
}

// createKPI without a title is a 400.
func TestKPIs_CreateValidation(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodPost, "/kpis", h.token(t, "alice"), `{"metric":"HV"}`)
	if r.code != http.StatusBadRequest {
		t.Fatalf("missing title = %d, want 400", r.code)
	}
	_ = r.body
}

// ---- Clusters ----

// Cluster create/list/get/update/delete round-trip.
func TestClusters_Lifecycle(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	r := h.do(t, http.MethodPost, "/clusters", tok, `{"label":"Coatings"}`)
	if r.code != http.StatusCreated {
		t.Fatalf("create cluster = %d: %s", r.code, r.body)
	}
	_ = r.body
	id := "cl-Coatings"

	if r := h.do(t, http.MethodGet, "/clusters", tok, ""); r.code != http.StatusOK {
		t.Fatalf("list clusters = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodGet, "/clusters/"+id, tok, ""); r.code != http.StatusOK {
		t.Fatalf("get cluster = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodPut, "/clusters/"+id, tok, `{"label":"Coatings v2"}`); r.code != http.StatusOK {
		t.Fatalf("update cluster = %d: %s", r.code, r.body)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodDelete, "/clusters", tok, ""); r.code != http.StatusNoContent {
		t.Fatalf("delete clusters = %d, want 204", r.code)
	}
}

// createCluster without a label is a 400.
func TestClusters_CreateValidation(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodPost, "/clusters", h.token(t, "alice"), `{"summary":"x"}`)
	if r.code != http.StatusBadRequest {
		t.Fatalf("missing label = %d, want 400", r.code)
	}
	_ = r.body
}

// replaceClusters publishes a versioned set and listClusters then filters to it.
func TestClusters_ReplaceAndPublishVersionFilter(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	// Seed a stale cluster from a previous publish version.
	h.cat.clusters["old"] = &commonv1.Cluster{Id: "old", OwnerId: "alice", Label: "Old", Params: `{"publish_version":"stale"}`}

	body := `{"clusters":[{"label":"Fresh A"},{"label":"Fresh B"}]}`
	r := h.do(t, http.MethodPost, "/clusters/replace", tok, body)
	if r.code != http.StatusCreated {
		t.Fatalf("replace = %d: %s", r.code, r.body)
	}
	_ = r.body

	// listClusters must now show only the freshly-published version.
	list := h.do(t, http.MethodGet, "/clusters", tok, "").body
	if strings.Contains(list, "\"Old\"") {
		t.Fatalf("stale cluster must be filtered out, got %s", list)
	}
	if !strings.Contains(list, "Fresh A") {
		t.Fatalf("fresh clusters must be listed, got %s", list)
	}
}

// replaceClusters rejects an empty list.
func TestClusters_ReplaceEmpty(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodPost, "/clusters/replace", h.token(t, "alice"), `{"clusters":[]}`)
	if r.code != http.StatusBadRequest {
		t.Fatalf("empty replace = %d, want 400", r.code)
	}
	_ = r.body
}

// ---- Hypotheses ----

// createHypothesis validates title/statement and persists.
func TestHypotheses_Create(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	if r := h.do(t, http.MethodPost, "/hypotheses", tok, `{"title":"","statement":""}`); r.code != http.StatusBadRequest {
		t.Fatalf("missing fields = %d, want 400", r.code)
	}
	r := h.do(t, http.MethodPost, "/hypotheses", tok, `{"title":"H","statement":"S","value_score":0.7}`)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.code, r.body)
	}
	if !strings.Contains(r.body, "composite_score") {
		t.Fatal("response must include a composite score")
	}
}

// listHypotheses and the board projection return owner-scoped rows + aggregates.
func TestHypotheses_ListAndBoard(t *testing.T) {
	h := newHarness(t)
	docID := "doc-1"
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Title: "Coating idea", Status: "generated"})
	h.cat.evidence["h1"] = []*commonv1.HypothesisEvidence{
		{Id: "ev-1", HypothesisId: "h1", DocumentId: &docID, Stance: "supports"},
	}
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h2", OwnerId: "bob", Title: "Other"})
	tok := h.token(t, "alice")

	list := h.do(t, http.MethodGet, "/hypotheses?status=generated&tags=a,b", tok, "").body
	if !strings.Contains(list, "Coating idea") || strings.Contains(list, "Other") {
		t.Fatalf("list must be owner-scoped, got %s", list)
	}
	refs := h.do(t, http.MethodGet, "/hypotheses?view=ref", tok, "").body
	if !strings.Contains(refs, "Coating idea") || strings.Contains(refs, "statement") {
		t.Fatalf("ref view must carry only id/kpi_id/status/title/generation, got %s", refs)
	}
	board := h.do(t, http.MethodGet, "/hypotheses/board?q=coating&queue=needs_verify&limit=10", tok, "").body
	if !strings.Contains(board, "queue_counts") || !strings.Contains(board, "facets") {
		t.Fatalf("board must carry aggregates, got %s", board)
	}
	if !strings.Contains(board, `"evidence_count":1`) || !strings.Contains(board, `"evidence_document_count":1`) {
		t.Fatalf("board items must carry evidence counters, got %s", board)
	}
}

// Списочные ответы урезаны: борд не тянет detail и тяжёлые ветки assessment,
// generation усечён до скаляров вида, список кластеров — без representatives
// и members/itc.components/signals.
func TestListResponsesArePruned(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	h.cat.putHypothesis(&commonv1.Hypothesis{
		Id: "h1", OwnerId: "alice", Title: "H", Status: "generated",
		Assessment: `{"trl":{"level":4,"levels":[{"note":"big"}]},` +
			`"check":{"verdict":"supported","supporting":["x"],"refuting":["y"]},` +
			`"ranking":{"score":71,"factors":[{"note":"f"}]}}`,
		Detail: `{"plan":"big"}`, Generation: `{"prompt":"big","kind":"bridge"}`,
	})
	h.cat.clusters["c1"] = &commonv1.Cluster{
		Id: "c1", OwnerId: "alice", Label: "C",
		Representatives: `[{"snippet":"long"}]`,
		Params:          `{"members":[1,2],"itc":{"score":6.5,"components":{"a":1},"signals":{"b":2}}}`,
	}

	board := h.do(t, http.MethodGet, "/hypotheses/board", tok, "").body
	if strings.Contains(board, `"levels"`) || strings.Contains(board, `"supporting"`) ||
		strings.Contains(board, `"refuting"`) || strings.Contains(board, `"factors"`) {
		t.Fatalf("board assessment must be pruned, got %s", board)
	}
	if !strings.Contains(board, `"verdict":"supported"`) || !strings.Contains(board, `"score":71`) {
		t.Fatalf("board must keep assessment scalars, got %s", board)
	}
	if !strings.Contains(board, `"detail":null`) || strings.Contains(board, `"prompt"`) ||
		!strings.Contains(board, `"generation":{"kind":"bridge"}`) {
		t.Fatalf("board must drop detail and keep only generation scalars, got %s", board)
	}

	refs := h.do(t, http.MethodGet, "/hypotheses?view=ref", tok, "").body
	if !strings.Contains(refs, `"generation":{"kind":"bridge"}`) || strings.Contains(refs, `"prompt"`) {
		t.Fatalf("ref view must carry truncated generation, got %s", refs)
	}

	list := h.do(t, http.MethodGet, "/clusters", tok, "").body
	if strings.Contains(list, `"members"`) || strings.Contains(list, `"components"`) ||
		strings.Contains(list, `"signals"`) || strings.Contains(list, "snippet") {
		t.Fatalf("cluster list must be pruned, got %s", list)
	}
	if !strings.Contains(list, `"representatives":[]`) || !strings.Contains(list, `"score":6.5`) {
		t.Fatalf("cluster list must keep itc scalars, got %s", list)
	}
	// Ручка деталей продолжает отдавать полный кластер.
	full := h.do(t, http.MethodGet, "/clusters/c1", tok, "").body
	if !strings.Contains(full, "snippet") || !strings.Contains(full, `"members"`) {
		t.Fatalf("cluster detail must stay full, got %s", full)
	}
}

// Board queue counts use the owner's runtime thresholds, not hardcoded UI defaults.
func TestHypotheses_BoardQueuePartition(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	// supported ⇒ Готовые, even with low TRL/score and a high risk score — it
	// must NOT also appear under Противоречия.
	h.cat.putHypothesis(&commonv1.Hypothesis{
		Id: "supported", OwnerId: "alice", Title: "Supported", Status: "generated",
		Assessment: `{"check":{"verdict":"supported"}}`, Trl: i32(2), CompositeScore: f64(0.20), RiskScore: f64(0.95),
	})
	// mixed ⇒ Противоречия (real contradictions).
	h.cat.putHypothesis(&commonv1.Hypothesis{
		Id: "mixed", OwnerId: "alice", Title: "Mixed", Status: "generated",
		Assessment: `{"check":{"verdict":"mixed"}}`,
	})
	// insufficient ⇒ Недостаточно данных, even with a high risk score — never Противоречия.
	h.cat.putHypothesis(&commonv1.Hypothesis{
		Id: "insufficient", OwnerId: "alice", Title: "Insufficient", Status: "generated",
		Assessment: `{"check":{"verdict":"insufficient"}}`, RiskScore: f64(0.95),
	})
	// empty verdict ⇒ На проверке.
	h.cat.putHypothesis(&commonv1.Hypothesis{
		Id: "pending", OwnerId: "alice", Title: "Pending", Status: "generated",
	})

	body := h.do(t, http.MethodGet, "/hypotheses/board", tok, "").body
	for _, want := range []string{`"all":4`, `"ready":1`, `"risk":1`, `"insufficient":1`, `"needs_verify":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("queue must partition on verdict (want %s), got %s", want, body)
		}
	}
}

// getHypothesis / updateHypothesis / revisions / evidence round-trip.
func TestHypotheses_GetUpdateRevisionsEvidence(t *testing.T) {
	h := newHarness(t)
	docID := "d1"
	h.cat.putHypothesis(&commonv1.Hypothesis{
		Id: "h1", OwnerId: "alice", Title: "H", Statement: "S",
		Evidence: []*commonv1.HypothesisEvidence{{Id: "e1", DocumentId: &docID, Stance: "supports"}},
	})
	h.cat.evidence["h1"] = []*commonv1.HypothesisEvidence{{Id: "e1", DocumentId: &docID, Stance: "supports"}}
	tok := h.token(t, "alice")

	if r := h.do(t, http.MethodGet, "/hypotheses/h1", tok, ""); r.code != http.StatusOK {
		t.Fatalf("get = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodPut, "/hypotheses/h1", tok,
		`{"title":"H2","statement":"S2","value_score":0.5}`); r.code != http.StatusOK {
		t.Fatalf("update = %d: %s", r.code, r.body)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodGet, "/hypotheses/h1/revisions", tok, ""); r.code != http.StatusOK {
		t.Fatalf("list revisions = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodPost, "/hypotheses/h1/revisions", tok,
		`{"action":"reviewed","summary":"ok"}`); r.code != http.StatusCreated {
		t.Fatalf("add revision = %d: %s", r.code, r.body)
	} else {
		_ = r.body
	}
	// A revision with no action is a 400.
	if r := h.do(t, http.MethodPost, "/hypotheses/h1/revisions", tok, `{"summary":"x"}`); r.code != http.StatusBadRequest {
		t.Fatalf("missing action = %d, want 400", r.code)
	}
	if r := h.do(t, http.MethodGet, "/hypotheses/h1/evidence", tok, ""); r.code != http.StatusOK {
		t.Fatalf("list evidence = %d", r.code)
	} else {
		_ = r.body
	}
}

// The LLM-driven endpoints (verify/assess-trl/competitors/refine/experiment/tag) each
// persist via the catalog and return the updated hypothesis.
func TestHypotheses_LLMEndpoints(t *testing.T) {
	cases := []struct {
		path   string
		answer string
	}{
		{"/hypotheses/h1/verify", supportedVerdict},
		{"/hypotheses/h1/assess-trl", `{"levels":[{"level":1,"met":false}]}`},
		{"/hypotheses/h1/competitors", `{"summary":"s","competitors":[]}`},
		{"/hypotheses/h1/refine", supportedVerdict},
		{"/hypotheses/h1/experiment", `{"materials":["m"],"success_criteria":["c"]}`},
		{"/hypotheses/h1/tag", `{"research_type":"практическое","grnti":[],"vak":[],"asjc":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			h := newHarness(t)
			h.answerer.push(&commonv1.RagResponse{
				Answer: tc.answer, Model: "m",
				Sources: []*commonv1.Source{{ChunkId: "c1", Snippet: "alpha beta gamma delta epsilon"}},
			}, nil)
			h.cat.putHypothesis(&commonv1.Hypothesis{
				Id: "h1", OwnerId: "alice", Title: "H", Statement: "alpha beta gamma delta",
			})
			r := h.do(t, http.MethodPost, tc.path, h.token(t, "alice"), "")
			if r.code != http.StatusOK {
				t.Fatalf("%s = %d: %s", tc.path, r.code, r.body)
			}
			_ = r.body
		})
	}
}

// A foreign owner gets 404 on the LLM-driven endpoints (ownership check first).
func TestHypotheses_VerifyForeign(t *testing.T) {
	h := newHarness(t)
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	r := h.do(t, http.MethodPost, "/hypotheses/h1/verify", h.token(t, "bob"), "")
	if r.code != http.StatusNotFound {
		t.Fatalf("foreign verify = %d, want 404", r.code)
	}
	_ = r.body
}

// storeHypothesisITC records the index and links the theme.
func TestHypotheses_StoreITC(t *testing.T) {
	h := newHarness(t)
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice"})
	body := `{"cluster_id":"cl1","itc":{"score":7},"composite_score":0.7}`
	r := h.do(t, http.MethodPost, "/hypotheses/h1/itc", h.token(t, "alice"), body)
	if r.code != http.StatusOK {
		t.Fatalf("store itc = %d: %s", r.code, r.body)
	}
	_ = r.body
}

// The TRL and ITC rubrics are served as JSON.
func TestRubrics(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	if r := h.do(t, http.MethodGet, "/trl/rubric", tok, ""); r.code != http.StatusOK || len(r.body) == 0 {
		t.Fatal("trl rubric must be non-empty JSON")
	}
	if r := h.do(t, http.MethodGet, "/itc/rubric", tok, ""); r.code != http.StatusOK || len(r.body) == 0 {
		t.Fatal("itc rubric must be non-empty JSON")
	}
}

// generateHypotheses runs the KPI → hypotheses loop (inline KPI title).
func TestHypotheses_Generate(t *testing.T) {
	h := newHarness(t)
	h.answerer.push(&commonv1.RagResponse{
		Answer:  `[{"title":"A","statement":"sa","rationale":"ra"}]`,
		Model:   "m",
		Sources: []*commonv1.Source{{DocumentId: "d1", Filename: "p.pdf", Snippet: "grounded fact"}},
	}, nil)
	h.answerer.push(&commonv1.RagResponse{Answer: "[]", Model: "m"}, nil)
	r := h.do(t, http.MethodPost, "/hypotheses/generate", h.token(t, "alice"), `{"kpi_title":"Hardness","count":1}`)
	if r.code != http.StatusCreated {
		t.Fatalf("generate = %d: %s", r.code, r.body)
	}
	_ = r.body
}

// generateHypotheses without kpi_id or kpi_title is a 400.
func TestHypotheses_GenerateValidation(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodPost, "/hypotheses/generate", h.token(t, "alice"), `{"count":1}`)
	if r.code != http.StatusBadRequest {
		t.Fatalf("missing kpi = %d, want 400", r.code)
	}
	_ = r.body
}

// hypothesisGraph returns the citation graph.
func TestHypotheses_Graph(t *testing.T) {
	h := newHarness(t)
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Title: "H"})
	r := h.do(t, http.MethodGet, "/hypotheses/graph", h.token(t, "alice"), "")
	if r.code != http.StatusOK || !strings.Contains(r.body, "nodes") {
		t.Fatal("graph must return nodes/edges")
	}
}

// graphHypotheses returns candidate bridges (empty for a fresh portfolio).
func TestHypotheses_GraphHypotheses(t *testing.T) {
	h := newHarness(t)
	h.cat.kpis["kpi1"] = &commonv1.KPI{Id: "kpi1", OwnerId: "alice", Title: "T"}
	r := h.do(t, http.MethodPost, "/kpis/kpi1/graph-hypotheses", h.token(t, "alice"), "")
	if r.code != http.StatusOK || !strings.Contains(r.body, "candidates") {
		t.Fatal("graph-hypotheses must return a candidates list")
	}
}

// ---- Scoring weights ----

// Scoring weights: get defaults, update with normalisation, reject out-of-range.
func TestScoringWeights(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	if r := h.do(t, http.MethodGet, "/hypotheses/scoring-weights", tok, ""); r.code != http.StatusOK {
		t.Fatalf("get weights = %d", r.code)
	} else if !strings.Contains(r.body, "kpi_fit") {
		t.Fatal("weights must include kpi_fit")
	}
	// A valid update is normalised and stored.
	if r := h.do(t, http.MethodPut, "/hypotheses/scoring-weights", tok,
		`{"kpi_fit":0.5,"evidence":0.5,"novelty":0,"value":0,"risk_inv":0,"trl_fit":0}`); r.code != http.StatusOK {
		t.Fatalf("update weights = %d: %s", r.code, r.body)
	} else {
		_ = r.body
	}
	// Out-of-range weight is a 400.
	if r := h.do(t, http.MethodPut, "/hypotheses/scoring-weights", tok,
		`{"kpi_fit":2,"evidence":0,"novelty":0,"value":0,"risk_inv":0,"trl_fit":0}`); r.code != http.StatusBadRequest {
		t.Fatalf("out-of-range = %d, want 400", r.code)
	}
	// All-zero weights are a 400.
	if r := h.do(t, http.MethodPut, "/hypotheses/scoring-weights", tok,
		`{"kpi_fit":0,"evidence":0,"novelty":0,"value":0,"risk_inv":0,"trl_fit":0}`); r.code != http.StatusBadRequest {
		t.Fatalf("all-zero = %d, want 400", r.code)
	}
}

// Runtime settings: get defaults, update with clamping and persist per owner.
func TestHypothesisRuntimeSettings(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	if r := h.do(t, http.MethodGet, "/hypotheses/runtime-settings", tok, ""); r.code != http.StatusOK {
		t.Fatalf("get settings = %d", r.code)
	} else if !strings.Contains(r.body, "default_generate_count") {
		t.Fatalf("settings must include generation count, got %s", r.body)
	}
	body := `{"default_generate_count":25,"cluster_generate_count":4,"direction_generate_count":4,` +
		`"generation_timeout_sec":240,"ready_trl_min":5,"ready_score_min":65,` +
		`"risk_score_min":75,"graph_direction_limit":30,"deep_postprocess_enabled":true}`
	if r := h.do(t, http.MethodPut, "/hypotheses/runtime-settings", tok, body); r.code != http.StatusOK {
		t.Fatalf("update settings = %d: %s", r.code, r.body)
	} else if !strings.Contains(r.body, `"default_generate_count":10`) ||
		!strings.Contains(r.body, `"graph_direction_limit":20`) ||
		!strings.Contains(r.body, `"generation_timeout_sec":180`) {
		t.Fatalf("settings must be clamped, got %s", r.body)
	}
	if r := h.do(t, http.MethodGet, "/hypotheses/runtime-settings", tok, ""); r.code != http.StatusOK {
		t.Fatalf("get saved settings = %d", r.code)
	} else if !strings.Contains(r.body, `"cluster_generate_count":4`) ||
		!strings.Contains(r.body, `"deep_postprocess_enabled":true`) {
		t.Fatalf("saved settings missing custom value, got %s", r.body)
	}
}

// ---- Hypothesis jobs ----

// Hypothesis jobs: enqueue, list, get.
func TestHypothesisJobs(t *testing.T) {
	h := newHarness(t)
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "alpha beta gamma delta"})
	h.answerer.push(&commonv1.RagResponse{
		Answer: supportedVerdict, Model: "m",
		Sources: []*commonv1.Source{{ChunkId: "c1", Snippet: "alpha beta gamma delta x"}},
	}, nil)
	// Keep the detached job runner parked until the enqueue response is rendered:
	// the runner and the handler share the returned *HypothesisJob, so releasing it
	// only after the POST returns avoids a read/write race on that object.
	gate := make(chan struct{})
	h.jobStore.claimGate = gate
	tok := h.token(t, "alice")

	r := h.do(t, http.MethodPost, "/hypothesis-jobs", tok, `{"kind":"verify","hypothesis_id":"h1"}`)
	if r.code != http.StatusAccepted {
		t.Fatalf("enqueue = %d: %s", r.code, r.body)
	}
	id := extractJSONString(t, r.body, "id")
	close(gate) // the response is rendered; let the runner proceed

	if r := h.do(t, http.MethodGet, "/hypothesis-jobs", tok, ""); r.code != http.StatusOK {
		t.Fatalf("list jobs = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodGet, "/hypothesis-jobs/"+id, tok, ""); r.code != http.StatusOK {
		t.Fatalf("get job = %d", r.code)
	} else {
		_ = r.body
	}
	// An invalid job kind is a 400.
	if r := h.do(t, http.MethodPost, "/hypothesis-jobs", tok, `{"kind":"bogus"}`); r.code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d, want 400", r.code)
	}
}
