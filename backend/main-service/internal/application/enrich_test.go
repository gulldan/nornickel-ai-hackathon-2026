package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	llmv1 "github.com/example/main-service/internal/platform/genproto/llm/v1"
)

// enrichService wires a HypothesisService with a chunk reader for Stage-2 tests.
func enrichService(cat *fakeCatalog, a *scriptedAnswerer, chunks *fakeChunks) *HypothesisService {
	return NewHypothesisService(cat, a, nil, nil, nil, nil, chunks)
}

// A full, rich enrich reply that leaves a hypothesis clearly above the gate.
const enrichRich = `{"causal_chain":[{"stage":"процесс","change":"старение"},{"stage":"свойство","change":"прочность"}],` +
	`"experiment_plan":{"experiment_type":"new_alloy","materials":["Ni"],"success_criteria":"рост предела текучести на 10%",` +
	`"estimated_cost":"medium","estimated_time":"weeks"},` +
	`"feasibility":[{"aspect":"научная реализуемость","level":"medium","note":"ок"}],` +
	`"microstructure_mechanism":"упрочнение выделениями","composition_change":"добавить Nb","process_change":"режим старения",` +
	`"characterization_methods":["SEM"],"test_methods":["tensile test"],"failure_modes":["перестаривание"]}`

// EnrichHypothesis reads the source document's full text, fills the rich passport
// fields, records the enrichment marker and recomputes the composite.
func TestEnrichHypothesis_FillsDetailFromFullText(t *testing.T) {
	docID := "d1"
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "h1", OwnerId: "alice", Title: "t", Statement: "aging raises yield strength", Status: "generated",
		Evidence: []*commonv1.HypothesisEvidence{{ChunkId: "c1", DocumentId: &docID, Snippet: "s"}},
	})
	chunks := &fakeChunks{chunks: map[string][]*llmv1.DocumentChunk{
		docID: {{Index: 0, Text: "Aging of Ni superalloys forms gamma-prime precipitates raising yield strength."}},
	}}
	a := newAnswerer(reply(enrichRich))
	svc := enrichService(cat, a, chunks)

	h, err := svc.EnrichHypothesis(context.Background(), "alice", "h1")
	if err != nil {
		t.Fatalf("EnrichHypothesis: %v", err)
	}
	if h.GetStatus() != "generated" {
		t.Fatalf("a good hypothesis must keep its status, got %q", h.GetStatus())
	}
	detail := cat.hypotheses["h1"].GetDetail()
	for _, want := range []string{"causal_chain", "experiment_plan", "microstructure_mechanism", "feasibility", "enrichment"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail must contain %q, got %s", want, detail)
		}
	}
	if !enrichmentDone(cat.hypotheses["h1"].GetAssessment()) {
		t.Fatal("assessment must carry the enrichment marker for the enqueue gate")
	}
	if cat.hypotheses["h1"].CompositeScore == nil {
		t.Fatal("ranking must be recomputed after enrichment")
	}
	if !strings.Contains(a.requests[0].GetPrompt(), "gamma-prime precipitates") {
		t.Fatal("enrich prompt must embed the full source text, not just chunks")
	}
}

// Enrichment fills only empty fields: generation / experiment-planner output is
// never clobbered.
func TestEnrichHypothesis_DoesNotClobberExistingDetail(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "h1", OwnerId: "alice", Statement: "s", Status: "generated",
		Detail:   `{"material_system":"Al2O3","experiment_plan":{"success_criteria":"already"}}`,
		Evidence: []*commonv1.HypothesisEvidence{{ChunkId: "c1"}},
	})
	res := `{"experiment_plan":{"success_criteria":"NEW"},"microstructure_mechanism":"mech"}`
	svc := enrichService(cat, newAnswerer(reply(res)), &fakeChunks{})

	if _, err := svc.EnrichHypothesis(context.Background(), "alice", "h1"); err != nil {
		t.Fatalf("EnrichHypothesis: %v", err)
	}
	detail := cat.hypotheses["h1"].GetDetail()
	if strings.Contains(detail, "NEW") {
		t.Fatalf("existing experiment_plan must not be clobbered, got %s", detail)
	}
	if !strings.Contains(detail, "already") || !strings.Contains(detail, "Al2O3") {
		t.Fatalf("existing detail fields must survive, got %s", detail)
	}
	if !strings.Contains(detail, "mech") {
		t.Fatalf("new mechanism must be added, got %s", detail)
	}
}

// The quality gate archives a still thin, ungrounded hypothesis off the board.
func TestEnrichHypothesis_ArchivesThinUngrounded(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "junk", OwnerId: "alice", Statement: "vague idea about things", Status: "generated",
	})
	svc := enrichService(cat, newAnswerer(reply("{}")), &fakeChunks{})

	h, err := svc.EnrichHypothesis(context.Background(), "alice", "junk")
	if err != nil {
		t.Fatalf("EnrichHypothesis: %v", err)
	}
	if h.GetStatus() != "archived" {
		t.Fatalf("thin, ungrounded hypothesis must be archived, got %q", h.GetStatus())
	}
}

// The quality gate never reverts a human decision: an approved hypothesis that
// would otherwise be culled (thin, ungrounded) keeps its status.
func TestEnrichHypothesis_KeepsApproved(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "appr", OwnerId: "alice", Statement: "vague idea about things", Status: statusApproved,
	})
	svc := enrichService(cat, newAnswerer(reply("{}")), &fakeChunks{})

	h, err := svc.EnrichHypothesis(context.Background(), "alice", "appr")
	if err != nil {
		t.Fatalf("EnrichHypothesis: %v", err)
	}
	if h.GetStatus() != statusApproved {
		t.Fatalf("an approved hypothesis must not be auto-archived, got %q", h.GetStatus())
	}
}

// A duplicate statement is archived deterministically: the higher-id twin
// archives itself while the lowest id survives — so two identical statements
// enriched concurrently can never archive each other (both would otherwise see
// the other still live). Both are grounded and enrich richly, so duplicate is
// the only gate that can fire.
func TestEnrichHypothesis_ArchivesDuplicate(t *testing.T) {
	docID := "d1"
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "dup-a", OwnerId: "alice", Statement: "Adding X raises hardness.", Status: "generated",
		Evidence: []*commonv1.HypothesisEvidence{{ChunkId: "c", DocumentId: &docID}},
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "dup-b", OwnerId: "alice", Statement: "adding x raises hardness", Status: "generated",
		Evidence: []*commonv1.HypothesisEvidence{{ChunkId: "c", DocumentId: &docID}},
	})
	svc := enrichService(cat, newAnswerer(reply(enrichRich)), &fakeChunks{})

	// Lowest id survives even while its higher-id twin is still live.
	lo, err := svc.EnrichHypothesis(context.Background(), "alice", "dup-a")
	if err != nil {
		t.Fatalf("EnrichHypothesis dup-a: %v", err)
	}
	if lo.GetStatus() == statusArchived {
		t.Fatalf("the lowest-id twin must survive, got archived")
	}
	// The higher-id twin archives itself against the surviving lower id.
	hi, err := svc.EnrichHypothesis(context.Background(), "alice", "dup-b")
	if err != nil {
		t.Fatalf("EnrichHypothesis dup-b: %v", err)
	}
	if hi.GetStatus() != statusArchived {
		t.Fatalf("the higher-id duplicate must be archived, got %q", hi.GetStatus())
	}
}

// Enrichment surfaces parse, LLM and ownership errors honestly.
func TestEnrichHypothesis_Errors(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	if _, err := enrichService(cat, newAnswerer(reply("nope")), &fakeChunks{}).
		EnrichHypothesis(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("a parse failure must surface")
	}
	if _, err := enrichService(cat, newAnswerer(failReply(errors.New("llm down"))), &fakeChunks{}).
		EnrichHypothesis(context.Background(), "alice", "h1"); err == nil {
		t.Fatal("an LLM failure must surface")
	}
	if _, err := enrichService(cat, newAnswerer(reply("{}")), &fakeChunks{}).
		EnrichHypothesis(context.Background(), "bob", "h1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a foreign enrich must be forbidden, got %v", err)
	}
}

// Breadth (RAG_GEN_BREADTH) forces the light schema for any count; disabling it
// restores the legacy count>3 threshold.
func TestUseLightGenSchema_BreadthToggle(t *testing.T) {
	ctx := context.Background()
	t.Setenv("RAG_GEN_BREADTH", "false")
	if useLightGenSchema(ctx, nil, 2) {
		t.Fatal("no breadth + small count must use the full schema")
	}
	if !useLightGenSchema(ctx, nil, 5) {
		t.Fatal("count>3 must use the light schema even without breadth")
	}
	t.Setenv("RAG_GEN_BREADTH", "true")
	if !useLightGenSchema(ctx, nil, 2) {
		t.Fatal("breadth must use the light schema for any count")
	}
}

// The breadth prompt omits the heavy experiment_plan schema; the legacy prompt keeps it.
func TestBuildGenPrompt_BreadthOmitsHeavySchema(t *testing.T) {
	kpi := &commonv1.KPI{Title: "T"}
	if strings.Contains(buildGenPrompt(kpi, 2, "", true), `"experiment_plan"`) {
		t.Fatal("breadth prompt must use the light schema (no experiment_plan)")
	}
	if !strings.Contains(buildGenPrompt(kpi, 2, "", false), `"experiment_plan"`) {
		t.Fatal("legacy small-count prompt must include the full schema")
	}
}

func TestQualityGate(t *testing.T) {
	if _, arch := qualityGate(0.9, true, true, true, true, 0.35); !arch {
		t.Fatal("a duplicate must be archived")
	}
	if _, arch := qualityGate(0.1, false, false, true, false, 0.35); !arch {
		t.Fatal("thin with no mechanism/plan must be archived")
	}
	if _, arch := qualityGate(0.1, false, false, false, false, 0.35); !arch {
		t.Fatal("ungrounded and thin must be archived")
	}
	if _, arch := qualityGate(0.9, true, true, true, false, 0.35); arch {
		t.Fatal("rich, grounded, unique must be kept")
	}
	if _, arch := qualityGate(0.2, true, false, true, false, 0.35); arch {
		t.Fatal("a mechanism keeps a low-schema hypothesis on the board")
	}
}

func TestNormalizedStatementAndEnrichmentDone(t *testing.T) {
	if normalizedStatement("Adding X, raises  hardness!") != normalizedStatement("adding x raises hardness") {
		t.Fatal("normalization must ignore case, punctuation and spacing")
	}
	if normalizedStatement("!!!") != "" {
		t.Fatal("punctuation-only statement must normalize to empty")
	}
	if enrichmentDone("") || enrichmentDone(`{"check":{"verdict":"supported"}}`) {
		t.Fatal("no marker must read as not enriched")
	}
	if !enrichmentDone(`{"enrichment":{"enriched_at":"2026-01-01T00:00:00Z"}}`) {
		t.Fatal("an enrichment marker must read as enriched")
	}
}
