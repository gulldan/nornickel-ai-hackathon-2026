package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	llmv1 "github.com/example/main-service/internal/platform/genproto/llm/v1"
)

// genArray is a minimal valid generation reply with one usable hypothesis.
const genArray = `[{"title":"Improve coating","statement":"Adding X raises hardness",` +
	`"rationale":"based on doc","trl":4,"novelty":0.6,"risk":0.4,"value":0.7,"confidence":0.6,` +
	`"measurable":true,"material_system":"Al2O3","process_change":"anneal","target_property":"hardness",` +
	`"causal_chain":[{"stage":"процесс","change":"anneal"},{"stage":"свойство","change":"hardness"}]}]`

// Generate retrieves, parses, scores and persists hypotheses, mining graph edges.
func TestGenerate_PersistsHypotheses(t *testing.T) {
	gen := reply(genArray, &commonv1.Source{DocumentId: "d1", Filename: "p.pdf", Snippet: "X raises hardness in Al2O3", Score: 0.9})
	// Subsequent passes (judge, counter-evidence, novelty, stance) reuse the last reply,
	// which is harmless empty/parse-tolerant output for those best-effort steps.
	a := newAnswerer(gen, reply("[]"))
	cat := newCatalog()
	graph := newGraph()
	svc := NewHypothesisService(cat, a, nil, nil, nil, graph, nil)

	kpi := &commonv1.KPI{Id: "kpi1", OwnerId: "alice", Title: "Hardness", Metric: "HV"}
	created, err := svc.Generate(context.Background(), "alice", kpi, 1, "без кобальта, бюджет до 1 млн")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("want 1 hypothesis, got %d", len(created))
	}
	if created[0].GetOwnerId() != "alice" || created[0].CompositeScore == nil {
		t.Fatalf("created hypothesis must be owned and ranked: %+v", created[0])
	}
	// The researcher's hard constraints must reach the LLM prompt and stay in provenance.
	if !strings.Contains(a.requests[0].GetPrompt(), "без кобальта") {
		t.Fatal("constraints must be woven into the generation prompt")
	}
	if !strings.Contains(created[0].GetGeneration(), "без кобальта") {
		t.Fatalf("constraints must be recorded in generation provenance: %s", created[0].GetGeneration())
	}
	if !hasString(created[0].GetTags(), "on_demand") {
		t.Fatalf("on-demand generation must be filterable by tag, got %v", created[0].GetTags())
	}
	// Graph enrichment mined edges from the structured fields.
	if len(graph.edges["alice"]) == 0 {
		t.Fatal("Generate should enrich the knowledge graph")
	}
}

// Documents that are themselves ready-made hypotheses ("leaked answers") must be
// excluded from generation retrieval and fed to the prompt only as ideas to avoid.
func TestGenerate_ExcludesAnswerDocs(t *testing.T) {
	gen := reply(genArray, &commonv1.Source{DocumentId: "d1", Filename: "p.pdf", Snippet: "X raises hardness", Score: 0.9})
	a := newAnswerer(gen, reply("[]"))
	cat := newCatalog()
	cat.docs = []*commonv1.Document{
		{Id: "ans1", OwnerId: "alice", Filename: "Гипотезы КГМК.docx", Kind: "hypotheses"},
		{Id: "d1", OwnerId: "alice", Filename: "p.pdf"},
	}
	chunks := &fakeChunks{chunks: map[string][]*llmv1.DocumentChunk{
		"ans1": {{Index: 0, Text: "Гипотезы по результатам мозгового штурма: 1. Магнитная сепарация."}},
	}}
	svc := NewHypothesisService(cat, a, nil, nil, nil, newGraph(), chunks)

	kpi := &commonv1.KPI{Id: "kpi1", OwnerId: "alice", Title: "Hardness", Metric: "HV"}
	if _, err := svc.Generate(context.Background(), "alice", kpi, 1, ""); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasString(a.requests[0].GetExcludeDocumentIds(), "ans1") {
		t.Fatalf("answer doc must be excluded from retrieval, got %v", a.requests[0].GetExcludeDocumentIds())
	}
	if !strings.Contains(a.requests[0].GetPrompt(), "Уже предложенные идеи") ||
		!strings.Contains(a.requests[0].GetPrompt(), "Магнитная сепарация") {
		t.Fatal("answer-doc content must appear as prior-art to avoid, not as evidence")
	}
}

// Generate falls back to evidence-derived drafts when the model output is a stub.
func TestGenerate_FallbackOnStub(t *testing.T) {
	stub := answer{resp: &commonv1.RagResponse{
		Answer:  "[stub-llm] no backend",
		Model:   "stub",
		Sources: []*commonv1.Source{{DocumentId: "d1", Filename: "paper.pdf", Snippet: "some grounded finding about coatings"}},
	}}
	a := newAnswerer(stub)
	cat := newCatalog()
	svc := newHypService(cat, a)
	created, err := svc.Generate(context.Background(), "alice", &commonv1.KPI{Id: "k", Title: "T"}, 2, "")
	if err != nil {
		t.Fatalf("Generate (fallback): %v", err)
	}
	if len(created) == 0 {
		t.Fatal("stub backend must still yield fallback drafts")
	}
	if strings.HasPrefix(strings.ToLower(created[0].GetTitle()), "проверить ") {
		t.Fatalf("fallback title must be a hypothesis, got %q", created[0].GetTitle())
	}
	if !strings.Contains(strings.ToLower(created[0].GetStatement()), "если ") {
		t.Fatalf("fallback statement must be testable, got %q", created[0].GetStatement())
	}
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestGenerate_FallbackDedupesSameHypothesis(t *testing.T) {
	stub := answer{resp: &commonv1.RagResponse{
		Answer: "[stub-llm] no backend",
		Model:  "stub",
		Sources: []*commonv1.Source{
			{DocumentId: "d1", Filename: "coating-a.pdf", Snippet: "protective coating reduces corrosion rate"},
			{DocumentId: "d2", Filename: "coating-b.pdf", Snippet: "surface coating lowers corrosion rate"},
			{DocumentId: "d3", Filename: "coating-c.pdf", Snippet: "coating improves corrosion resistance"},
		},
	}}
	a := newAnswerer(stub)
	svc := newHypService(newCatalog(), a)
	kpi := &commonv1.KPI{Id: "k", Title: "Снизить скорость коррозии защитных покрытий", Metric: "скорость коррозии"}

	created, err := svc.Generate(context.Background(), "alice", kpi, 5, "")
	if err != nil {
		t.Fatalf("Generate (fallback): %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("fallback must merge duplicate hypothesis titles, got %d", len(created))
	}
	if got := created[0].GetTitle(); strings.HasPrefix(strings.ToLower(got), "проверить ") {
		t.Fatalf("fallback title must be a hypothesis, got %q", got)
	}
}

// Imperative task-like titles are rejected as invalid model output and retried.
func TestGenerate_RetriesInstructionLikeTitle(t *testing.T) {
	bad := `[{"title":"Проверить подход из «coating.pdf» для KPI",` +
		`"statement":"Если нанести покрытие X, то коррозионная стойкость возрастёт.",` +
		`"rationale":"based on doc"}]`
	good := `[{"title":"Покрытие X повысит коррозионную стойкость",` +
		`"statement":"Если нанести покрытие X, то коррозионная стойкость возрастёт.",` +
		`"rationale":"based on doc"}]`
	a := newAnswerer(
		reply(bad, &commonv1.Source{DocumentId: "d1", Filename: "coating.pdf", Snippet: "coating improves corrosion resistance"}),
		reply(good, &commonv1.Source{DocumentId: "d1", Filename: "coating.pdf", Snippet: "coating improves corrosion resistance"}),
		reply("[]"),
	)
	svc := newHypService(newCatalog(), a)
	kpi := &commonv1.KPI{Id: "k", Title: "Повысить коррозионную стойкость"}
	created, err := svc.Generate(context.Background(), "alice", kpi, 1, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("want 1 hypothesis, got %d", len(created))
	}
	if got := created[0].GetTitle(); strings.HasPrefix(strings.ToLower(got), "проверить ") {
		t.Fatalf("instruction-like title must not be persisted, got %q", got)
	}
	if a.callCount() < 2 {
		t.Fatalf("invalid title must trigger a retry, got %d calls", a.callCount())
	}
}

func TestGenerate_RetriesNoisyDemoTitle(t *testing.T) {
	bad := `[{"title":"Если применить изменение режима термообработки, то деньги улучшится",` +
		`"statement":"Если применить изменение режима термообработки, то деньги улучшится.",` +
		`"rationale":"bad legacy demo row"}]`
	good := `[{"title":"Термообработка Ni-сплава повысит предел текучести при 800 °C",` +
		`"statement":"Если подобрать режим старения Ni-сплава, то предел текучести при 800 °C возрастёт.",` +
		`"rationale":"контекст связывает старение и прочность никелевых сплавов"}]`
	a := newAnswerer(
		reply(bad, &commonv1.Source{DocumentId: "d1", Filename: "nickel.pdf", Snippet: "aging improves nickel alloy strength"}),
		reply(good, &commonv1.Source{DocumentId: "d1", Filename: "nickel.pdf", Snippet: "aging improves nickel alloy strength"}),
		reply("[]"),
	)
	svc := newHypService(newCatalog(), a)
	kpi := &commonv1.KPI{Id: "k", Title: "Повысить жаропрочность Ni-сплава"}
	created, err := svc.Generate(context.Background(), "alice", kpi, 1, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("want 1 hypothesis, got %d", len(created))
	}
	if got := strings.ToLower(created[0].GetTitle()); strings.Contains(got, "деньги") {
		t.Fatalf("noisy title must not be persisted, got %q", created[0].GetTitle())
	}
	if a.callCount() < 2 {
		t.Fatalf("noisy title must trigger a retry, got %d calls", a.callCount())
	}
}

// Generate also falls back to cluster representatives when the LLM call fails
// before it can return sources. This is the post-reindex path: documents and
// clusters are ready, but the external generation model may be unavailable.
func TestGenerate_FallbackFromClustersOnLLMFailure(t *testing.T) {
	cat := newCatalog()
	cat.clusters["cl1"] = &commonv1.Cluster{
		Id: "cl1", OwnerId: "alice", Label: "Жаропрочные никелевые сплавы",
		Summary:       "Кластер про никелевые суперсплавы и старение.",
		DocumentCount: 3,
		Representatives: `[
			{"document_id":"d1","filename":"nickel_alloy.pdf",
				"snippet":"Nb microalloying and aging improve high-temperature yield strength in Ni alloys."},
			{"document_id":"d2","filename":"coating.pdf","snippet":"Thermal barrier coatings reduce oxidation at elevated temperature."}
		]`,
	}
	a := newAnswerer(failReply(errors.New("llm down")))
	svc := newHypService(cat, a)
	kpi := &commonv1.KPI{Id: "k", OwnerId: "alice", Title: "Повысить жаропрочность Ni-сплава", Metric: "предел текучести"}

	created, err := svc.Generate(context.Background(), "alice", kpi, 2, "")
	if err != nil {
		t.Fatalf("Generate cluster fallback: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("cluster fallback must create KPI hypotheses")
	}
	if created[0].GetKpiId() != "k" {
		t.Fatalf("created hypothesis must be KPI-linked, got %q", created[0].GetKpiId())
	}
	if len(cat.evidence[created[0].GetId()]) == 0 {
		t.Fatal("cluster fallback must persist representative evidence")
	}
	if a.callCount() != genLLMAttempts {
		t.Fatalf("fallback persistence must not run extra LLM passes, got %d calls", a.callCount())
	}
}

func TestClusterFallbackSources_UsesDomainHintsOverClusterSize(t *testing.T) {
	cat := newCatalog()
	cat.clusters["large"] = &commonv1.Cluster{
		Id: "large", OwnerId: "alice", Label: "Широкий кластер",
		DocumentCount: 100,
		Keywords:      []string{"semiconductors perovskites", "solar cells"},
		Representatives: `[
			{"document_id":"p1","filename":"perovskite.pdf","snippet":"Perovskite solar cells improve light harvesting stability."}
		]`,
	}
	cat.clusters["small"] = &commonv1.Cluster{
		Id: "small", OwnerId: "alice", Label: "Сплавы",
		DocumentCount: 3,
		Keywords:      []string{"high-entropy alloys", "nickel superalloys"},
		Representatives: `[
			{"document_id":"n1","filename":"nickel_superalloy.pdf",
				"snippet":"Nickel superalloy aging improves high temperature yield strength."}
		]`,
	}
	svc := newHypService(cat, nil)
	kpi := &commonv1.KPI{
		Title:        "Повысить жаропрочность Ni-сплава при 800 °C",
		Metric:       "предел текучести при 800 °C",
		Description:  "Искать режимы термообработки жаропрочных сплавов.",
		FunctionArea: "Жаропрочные сплавы",
	}

	got := svc.clusterFallbackSources(context.Background(), "alice", kpi, 2)
	if len(got) == 0 {
		t.Fatal("expected fallback sources")
	}
	if got[0].GetFilename() != "nickel_superalloy.pdf" {
		t.Fatalf("domain-relevant source must outrank larger unrelated cluster, got %q", got[0].GetFilename())
	}
}

func TestFallbackGenItems_DropsNoisyMoneyKPI(t *testing.T) {
	kpi := &commonv1.KPI{
		Id:     "bad-kpi",
		Title:  "Понизить максимально стоимость выплавик деталей из никеля",
		Metric: "деньги",
	}
	items := fallbackGenItems(kpi, []*commonv1.Source{{
		DocumentId: "d1",
		Filename:   "ntrs_patent_19700023972_High_temperature_nickel_base_alloy_Patent.txt",
		Snippet:    "High temperature nickel base alloy patent.",
	}}, 1)
	if len(items) != 0 {
		t.Fatalf("bad money KPI must not yield fallback hypotheses, got %+v", items)
	}
}

// Generate returns an error when every attempt fails and no fallback is possible.
func TestGenerate_AllAttemptsFail(t *testing.T) {
	a := newAnswerer(failReply(errors.New("llm down")))
	svc := newHypService(newCatalog(), a)
	if _, err := svc.Generate(context.Background(), "alice", &commonv1.KPI{Id: "k", Title: "T"}, 1, ""); err == nil {
		t.Fatal("a total LLM failure must surface")
	}
}

// Generate caps the count and clamps an out-of-range request to the default.
func TestGenerate_CountClamped(t *testing.T) {
	// Two usable items; request 0 ⇒ defaults to 5, so both survive.
	two := `[{"title":"A","statement":"sa","rationale":"ra"},{"title":"B","statement":"sb","rationale":"rb"}]`
	a := newAnswerer(reply(two), reply("[]"))
	svc := newHypService(newCatalog(), a)
	created, err := svc.Generate(context.Background(), "alice", &commonv1.KPI{Id: "k", Title: "T"}, 0, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("want 2 created, got %d", len(created))
	}
}

// A CreateHypothesis failure aborts persistence.
func TestGenerate_CreateError(t *testing.T) {
	cat := newCatalog()
	cat.createHypErr = errors.New("db down")
	a := newAnswerer(reply(genArray), reply("[]"))
	svc := newHypService(cat, a)
	if _, err := svc.Generate(context.Background(), "alice", &commonv1.KPI{Id: "k", Title: "T"}, 1, ""); err == nil {
		t.Fatal("create failure must surface")
	}
}

// feedbackNote weaves rejected/approved titles into the prompt addendum.
func TestFeedbackNote(t *testing.T) {
	cat := newCatalog()
	cat.list = []*commonv1.Hypothesis{{Id: "h1", OwnerId: "alice", Title: "Rejected idea", Status: "rejected"}}
	svc := newHypService(cat, nil)
	note := svc.feedbackNote(context.Background(), "alice", &commonv1.KPI{Id: "kpi1"})
	if note == "" {
		t.Fatal("feedback note should be non-empty when history exists")
	}
	// A KPI with no id yields no feedback.
	if got := svc.feedbackNote(context.Background(), "alice", &commonv1.KPI{}); got != "" {
		t.Fatalf("blank KPI id must yield no feedback, got %q", got)
	}
}
