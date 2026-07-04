package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// seedHypothesis stores a minimal owned hypothesis and returns the catalog + service.
func seedHypothesis(a *scriptedAnswerer, h *commonv1.Hypothesis) (*fakeCatalog, *HypothesisService) {
	cat := newCatalog()
	cat.putHypothesis(h)
	return cat, newHypService(cat, a)
}

// ---- Verify ----

// Verify runs the two retrieval passes, persists evidence + verdict and re-reads.
func TestVerify_PersistsVerdictAndEvidence(t *testing.T) {
	main := reply(`{"verdict":"supported","confidence":0.8,"rationale":"ok",`+
		`"supporting":["alpha beta gamma delta"],"contradicting":[]}`,
		&commonv1.Source{ChunkId: "c1", Filename: "p.pdf", Snippet: "alpha beta gamma delta epsilon"})
	counter := reply(`{"verdict":"insufficient","confidence":0.4,"rationale":"none",`+
		`"supporting":[],"contradicting":["zeta eta theta iota"]}`,
		&commonv1.Source{ChunkId: "c2", Filename: "q.pdf", Snippet: "zeta eta theta iota kappa"})
	a := newAnswerer(main, counter)
	cat, svc := seedHypothesis(a, &commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "alpha beta gamma delta"})

	got, err := svc.Verify(context.Background(), "alice", "h1")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.ConfidenceScore == nil {
		t.Fatal("Verify must set a confidence (belief) score")
	}
	if !strings.Contains(got.GetAssessment(), "\"check\"") {
		t.Fatal("assessment.check must be written")
	}
	if cat.updateCalls == 0 {
		t.Fatal("Verify must persist via UpdateHypothesis")
	}
	if a.callCount() < 2 {
		t.Fatalf("Verify must run main + counter passes, got %d", a.callCount())
	}
}

// Verify is forbidden for a foreign hypothesis.
func TestVerify_Forbidden(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(reply("{}")), &commonv1.Hypothesis{Id: "h1", OwnerId: "alice"})
	if _, err := svc.Verify(context.Background(), "bob", "h1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign verify must be forbidden, got %v", err)
	}
}

// A main-pass LLM error fails Verify.
func TestVerify_MainPassError(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(failReply(errors.New("llm down"))),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	if _, err := svc.Verify(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("main-pass error must surface")
	}
}

// Unparseable verifier output fails Verify.
func TestVerify_ParseError(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(reply("not json at all")),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	if _, err := svc.Verify(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("unparseable verdict must surface")
	}
}

// ---- AssessTRL ----

// AssessTRL fills the questionnaire, gates the level and persists assessment.trl.
func TestAssessTRL_GatesAndPersists(t *testing.T) {
	// Levels 1-3 met, 4 not ⇒ gated TRL 3.
	answer := `{"levels":[{"level":1,"met":true,"note":""},{"level":2,"met":true,"note":""},` +
		`{"level":3,"met":true,"note":""},{"level":4,"met":false,"note":"no prototype"},` +
		`{"level":5,"met":false},{"level":6,"met":false},{"level":7,"met":false},` +
		`{"level":8,"met":false},{"level":9,"met":false}]}`
	cat, svc := seedHypothesis(newAnswerer(reply(answer)),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	got, err := svc.AssessTRL(context.Background(), "alice", "h1")
	if err != nil {
		t.Fatalf("AssessTRL: %v", err)
	}
	if got.GetTrl() != 3 {
		t.Fatalf("gated TRL = %d, want 3", got.GetTrl())
	}
	if !strings.Contains(cat.hypotheses["h1"].GetAssessment(), "ugt-gating") {
		t.Fatal("assessment.trl must record the gating method")
	}
}

// AssessTRL floors a fully-unmet questionnaire at level 1.
func TestAssessTRL_FloorsAtOne(t *testing.T) {
	answer := `{"levels":[{"level":1,"met":false}]}`
	_, svc := seedHypothesis(newAnswerer(reply(answer)),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	got, err := svc.AssessTRL(context.Background(), "alice", "h1")
	if err != nil {
		t.Fatalf("AssessTRL: %v", err)
	}
	if got.GetTrl() != 1 {
		t.Fatalf("a formulated hypothesis is at least УГТ 1, got %d", got.GetTrl())
	}
}

// AssessTRL surfaces an unparseable anketa.
func TestAssessTRL_ParseError(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(reply("garbage")),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	if _, err := svc.AssessTRL(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("unparseable anketa must surface")
	}
}

// TRLRubric returns the embedded JSON.
func TestTRLRubric(t *testing.T) {
	svc := newHypService(newCatalog(), nil)
	if len(svc.TRLRubric()) == 0 {
		t.Fatal("TRLRubric must be non-empty")
	}
}

// ---- TagHypothesis ----

// TagHypothesis validates codes against the taxonomy and stores classification.
func TestTagHypothesis_StoresClassification(t *testing.T) {
	// Use a clearly-bogus code so validate() drops it, plus a real research_type.
	answer := `{"research_type":"практическое","grnti":[{"code":"99.99.99","name":"fake"}],` +
		`"vak":[],"asjc":[]}`
	cat, svc := seedHypothesis(newAnswerer(reply(answer)),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Title: "t", Statement: "s", Measurable: true})
	got, err := svc.TagHypothesis(context.Background(), "alice", "h1")
	if err != nil {
		t.Fatalf("TagHypothesis: %v", err)
	}
	if !strings.Contains(cat.hypotheses["h1"].GetDetail(), "classification") {
		t.Fatal("detail.classification must be written")
	}
	// research_type tag must be present.
	found := false
	for _, tag := range got.GetTags() {
		if tag == researchTagPractical {
			found = true
		}
	}
	if !found {
		t.Fatalf("research tag must be set, got %v", got.GetTags())
	}
}

// TagHypothesis surfaces a parse error.
func TestTagHypothesis_ParseError(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(reply("nope")),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Title: "t"})
	if _, err := svc.TagHypothesis(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("parse error must surface")
	}
}

// ITCRubric returns the embedded JSON.
func TestITCRubric(t *testing.T) {
	svc := newHypService(newCatalog(), nil)
	if len(svc.ITCRubric()) == 0 {
		t.Fatal("ITCRubric must be non-empty")
	}
}

// ---- AnalyzeCompetitors ----

// AnalyzeCompetitors stores the competitor analysis under detail.competitors.
func TestAnalyzeCompetitors_StoresDetail(t *testing.T) {
	answer := `{"summary":"crowded","competitors":[{"name":"Acme","approach":"x",` +
		`"strengths":["a"],"weaknesses":["b"],"maturity":"TRL5","source":"doc.pdf"}]}`
	cat, svc := seedHypothesis(newAnswerer(reply(answer)),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Title: "t", Statement: "s"})
	if _, err := svc.AnalyzeCompetitors(context.Background(), "alice", "h1"); err != nil {
		t.Fatalf("AnalyzeCompetitors: %v", err)
	}
	if !strings.Contains(cat.hypotheses["h1"].GetDetail(), "competitors") {
		t.Fatal("detail.competitors must be written")
	}
}

// AnalyzeCompetitors surfaces a parse error.
func TestAnalyzeCompetitors_ParseError(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(reply("nope")),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Title: "t"})
	if _, err := svc.AnalyzeCompetitors(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("parse error must surface")
	}
}

// ---- PlanExperiment ----

// PlanExperiment merges a plan into detail.experiment_plan.
func TestPlanExperiment_StoresPlan(t *testing.T) {
	answer := `{"materials":["powder"],"process_parameters":[{"name":"temp","range":"600-800C"}],` +
		`"characterization_methods":["XRD"],"test_methods":["tensile"],"controls":["baseline"],` +
		`"success_criteria":["+10%"],"estimated_cost":"medium","estimated_time":"weeks","risks":["cracking"]}`
	cat, svc := seedHypothesis(newAnswerer(reply(answer)),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	if _, err := svc.PlanExperiment(context.Background(), "alice", "h1"); err != nil {
		t.Fatalf("PlanExperiment: %v", err)
	}
	if !strings.Contains(cat.hypotheses["h1"].GetDetail(), "experiment_plan") {
		t.Fatal("detail.experiment_plan must be written")
	}
}

// PlanExperiment surfaces a parse error and an LLM error.
func TestPlanExperiment_Errors(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(reply("nope")),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	if _, err := svc.PlanExperiment(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("parse error must surface")
	}
	_, svc2 := seedHypothesis(newAnswerer(failReply(errors.New("llm down"))),
		&commonv1.Hypothesis{Id: "h2", OwnerId: "alice", Statement: "s"})
	if _, err := svc2.PlanExperiment(context.Background(), "alice", "h2"); err == nil {
		t.Fatal("LLM error must surface")
	}
}

// ---- StoreITC ----

// StoreITC links the theme and stores the breakdown; the payload composite is
// ignored because composite_score is always computed by the rank-v1 scorer.
func TestStoreITC_StoresAndLinks(t *testing.T) {
	cat, svc := seedHypothesis(newAnswerer(reply("{}")), &commonv1.Hypothesis{Id: "h1", OwnerId: "alice"})
	composite := 0.73
	in := ITCInput{ClusterID: "cl1", ITC: []byte(`{"score":7}`), Composite: &composite}
	got, err := svc.StoreITC(context.Background(), "alice", "h1", in)
	if err != nil {
		t.Fatalf("StoreITC: %v", err)
	}
	if got.GetPrimaryClusterId() != "cl1" {
		t.Fatalf("theme link = %q, want cl1", got.GetPrimaryClusterId())
	}
	if got.CompositeScore != nil {
		t.Fatalf("composite = %.2f, want untouched (rank-v1 owns it)", got.GetCompositeScore())
	}
	if !strings.Contains(cat.hypotheses["h1"].GetAssessment(), "itc") {
		t.Fatal("assessment.itc must be written")
	}
}

// StoreITC is forbidden for a foreign hypothesis.
func TestStoreITC_Forbidden(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(reply("{}")), &commonv1.Hypothesis{Id: "h1", OwnerId: "alice"})
	if _, err := svc.StoreITC(context.Background(), "bob", "h1", ITCInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign StoreITC must be forbidden, got %v", err)
	}
}

// ---- Refine ----

// A strong verdict short-circuits Refine (no revision pass).
func TestRefine_StrongVerdictReturnsEarly(t *testing.T) {
	supported := reply(`{"verdict":"supported","confidence":0.9,"rationale":"",`+
		`"supporting":["a b c d"],"contradicting":[]}`,
		&commonv1.Source{ChunkId: "c1", Snippet: "a b c d e"})
	// Verify runs main+counter (2 passes); a strong verdict ⇒ no revise pass.
	a := newAnswerer(supported, supported)
	_, svc := seedHypothesis(a, &commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "alpha beta gamma delta"})
	if _, err := svc.Refine(context.Background(), "alice", "h1"); err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if a.callCount() != 2 {
		t.Fatalf("strong verdict must skip revision, got %d passes", a.callCount())
	}
}

// A weak verdict triggers a revision and re-verification.
func TestRefine_WeakVerdictRevises(t *testing.T) {
	weak := reply(`{"verdict":"insufficient","confidence":0.4,"rationale":"",` +
		`"supporting":[],"contradicting":["lim"]}`)
	revised := reply(`{"statement":"a sharper, grounded statement"}`)
	// passes: verify(main, counter) → revise → verify(main, counter).
	a := newAnswerer(weak, weak, revised, weak, weak)
	cat, svc := seedHypothesis(a, &commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "vague"})
	if _, err := svc.Refine(context.Background(), "alice", "h1"); err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if cat.hypotheses["h1"].GetStatement() != "a sharper, grounded statement" {
		t.Fatalf("Refine must apply the revised statement, got %q", cat.hypotheses["h1"].GetStatement())
	}
}

// Refine surfaces a verify failure.
func TestRefine_VerifyError(t *testing.T) {
	_, svc := seedHypothesis(newAnswerer(failReply(errors.New("down"))),
		&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	if _, err := svc.Refine(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("verify error must surface")
	}
}

// weakVerdict classifies verdicts correctly.
func TestWeakVerdict(t *testing.T) {
	for _, v := range []string{verdictRefuted, verdictMixed, verdictInsufficient, ""} {
		if !weakVerdict(v) {
			t.Fatalf("%q should be weak", v)
		}
	}
	if weakVerdict(verdictSupported) {
		t.Fatal("supported is strong")
	}
}

// ---- classifyEvidenceStances ----

// Stance classification labels fragments in place and ignores out-of-range indices.
func TestClassifyEvidenceStances(t *testing.T) {
	a := newAnswerer(reply(`[{"i":1,"stance":"supports"},{"i":2,"stance":"contradicts"},{"i":99,"stance":"supports"}]`))
	svc := newHypService(newCatalog(), a)
	ev := []*commonv1.HypothesisEvidence{
		{ChunkId: "c1", Snippet: "frag one", Stance: stanceContext},
		{ChunkId: "c2", Snippet: "frag two", Stance: stanceContext},
	}
	svc.classifyEvidenceStances(context.Background(), "alice", "stmt", ev)
	if ev[0].GetStance() != stanceSupports || ev[1].GetStance() != stanceContradicts {
		t.Fatalf("stances not applied: %q / %q", ev[0].GetStance(), ev[1].GetStance())
	}
}

// A classifier outage leaves the prior (context) stance untouched.
func TestClassifyEvidenceStances_OutageKeepsContext(t *testing.T) {
	svc := newHypService(newCatalog(), newAnswerer(failReply(errors.New("down"))))
	ev := []*commonv1.HypothesisEvidence{{ChunkId: "c1", Snippet: "f", Stance: stanceContext}}
	svc.classifyEvidenceStances(context.Background(), "alice", "stmt", ev)
	if ev[0].GetStance() != stanceContext {
		t.Fatalf("outage must not change stance, got %q", ev[0].GetStance())
	}
	// Empty evidence is a no-op.
	svc.classifyEvidenceStances(context.Background(), "alice", "stmt", nil)
}

// counterEvidenceSources returns the retrieved sources, or nil on error.
func TestCounterEvidenceSources(t *testing.T) {
	a := newAnswerer(reply("", src("d1", "limitation"), src("d2", "contradiction")))
	svc := newHypService(newCatalog(), a)
	got := svc.counterEvidenceSources(context.Background(), "alice", "statement", 5, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 counter sources, got %d", len(got))
	}
	svc2 := newHypService(newCatalog(), newAnswerer(failReply(errors.New("down"))))
	if got := svc2.counterEvidenceSources(context.Background(), "alice", "s", 5, nil); got != nil {
		t.Fatalf("error must yield nil, got %v", got)
	}
}

// ---- Graph ----

// Graph builds hypothesis + document nodes and stance-typed edges.
func TestGraph_BuildsCitationGraph(t *testing.T) {
	cat := newCatalog()
	cat.list = []*commonv1.Hypothesis{
		{Id: "h1", OwnerId: "alice", Title: "H1", Status: "generated"},
		{Id: "h2", OwnerId: "alice", Title: "H2", Status: "approved"},
	}
	docID := "d1"
	cat.evidence["h1"] = []*commonv1.HypothesisEvidence{
		{DocumentId: &docID, Filename: "shared.pdf", Stance: stanceSupports, ChunkId: "c1"},
	}
	cat.evidence["h2"] = []*commonv1.HypothesisEvidence{
		{DocumentId: &docID, Filename: "shared.pdf", Stance: stanceContradicts, ChunkId: "c2"},
		{Filename: "byname.pdf", Stance: stanceContext, ChunkId: "c3"},
	}
	svc := newHypService(cat, nil)
	g, err := svc.Graph(context.Background(), "alice")
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	// 2 hypotheses + 2 document nodes (d1 shared, file:byname.pdf).
	if len(g.Nodes) != 4 {
		t.Fatalf("want 4 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 3 {
		t.Fatalf("want 3 edges, got %d", len(g.Edges))
	}
}

// Graph surfaces a list error.
func TestGraph_ListError(t *testing.T) {
	cat := newCatalog()
	cat.listHypErr = errors.New("db down")
	svc := newHypService(cat, nil)
	if _, err := svc.Graph(context.Background(), "alice"); err == nil {
		t.Fatal("list error must surface")
	}
}

// ---- GraphHypotheses ----

// A nil graph store yields an empty (non-nil) bridge list.
func TestGraphHypotheses_NilStore(t *testing.T) {
	svc := newHypService(newCatalog(), nil)
	got, err := svc.GraphHypotheses(context.Background(), "alice", "kpi1")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("nil store must yield empty slice, got %v / %v", got, err)
	}
}

// With a populated graph, GraphHypotheses finds cross-hypothesis bridges.
func TestGraphHypotheses_FindsBridges(t *testing.T) {
	cat := newCatalog()
	cat.kpis["kpi1"] = &commonv1.KPI{Id: "kpi1", OwnerId: "alice", Title: "Strength", Metric: "MPa"}
	graph := newGraph()
	graph.edges["alice"] = []KGEdge{
		{
			Relation: relAffectsProperty, FromType: nodeProcess, FromName: "anneal",
			ToType: nodeProperty, ToName: "hardness", HypothesisID: "hA",
		},
		{Relation: relSupportsKPI, FromName: "hardness", ToName: "kpi:kpi1", HypothesisID: "hB", KPIID: "kpi1"},
	}
	svc := NewHypothesisService(cat, nil, nil, nil, nil, graph, nil)
	got, err := svc.GraphHypotheses(context.Background(), "alice", "kpi1")
	if err != nil {
		t.Fatalf("GraphHypotheses: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one bridge candidate")
	}
	if !strings.Contains(got[0].KPI, "Strength") {
		t.Fatalf("bridge should name the KPI, got %q", got[0].KPI)
	}
}

// An empty graph yields no bridges.
func TestGraphHypotheses_EmptyGraph(t *testing.T) {
	svc := NewHypothesisService(newCatalog(), nil, nil, nil, nil, newGraph(), nil)
	got, err := svc.GraphHypotheses(context.Background(), "alice", "kpi1")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty graph must yield no bridges, got %v / %v", got, err)
	}
}
