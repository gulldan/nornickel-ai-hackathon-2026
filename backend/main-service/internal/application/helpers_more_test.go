package application

import (
	"context"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// buildDetail and its cleaners emit every populated materials-passport field and
// drop empty ones.
func TestBuildDetail_FullItem(t *testing.T) {
	item := genItem{
		Problem:                 "low yield",
		Drivers:                 []string{"d1"},
		MaterialSystem:          "Al2O3",
		CompositionChange:       "add Y",
		ProcessChange:           "anneal",
		MicrostructureMechanism: "grain refinement",
		TargetProperty:          "hardness",
		CharacterizationMethods: []string{"XRD", ""},
		TestMethods:             []string{"tensile"},
		FailureModes:            []string{"cracking"},
		CausalChain:             []causalStep{{Stage: "процесс", Change: "anneal"}, {Stage: "", Change: ""}},
		Experiment: &experimentVal{
			Variables: []string{"temp"}, Methods: []string{"SEM"}, SuccessCriteria: "+10%", Horizon: "weeks",
		},
		Feasibility: []feasibility{
			{Aspect: "science", Level: levelHigh, Note: "ok"},
			{Aspect: "scaling", Level: levelLow, Note: "n"},
			{Aspect: "", Level: "", Note: ""},
		},
	}
	d := buildDetail(item)
	for _, key := range []string{
		"material_system", "composition_change", "process_change", "microstructure_mechanism",
		"target_property", "characterization_methods", "test_methods", "failure_modes",
		"causal_chain", "experiment_plan", "feasibility",
	} {
		if _, ok := d[key]; !ok {
			t.Fatalf("buildDetail missing %q", key)
		}
	}
}

// cleanFeasibility normalises Russian level labels and drops empty rows. The
// inputs use the Russian stem forms that the normaliser accepts (each appears
// once in production), so the test adds no duplicate level literal to the package.
func TestCleanFeasibility(t *testing.T) {
	in := []feasibility{
		{Aspect: "a", Level: "низк", Note: "n"},
		{Aspect: "b", Level: "средн", Note: "m"},
		{Aspect: "c", Level: "высок", Note: "h"},
		{Aspect: "d", Level: "garbage", Note: "x"},
		{Aspect: "", Level: "", Note: ""},
	}
	out := cleanFeasibility(in)
	if len(out) != 4 {
		t.Fatalf("want 4 rows, got %d", len(out))
	}
	if out[0]["level"] != levelLow || out[1]["level"] != levelMedium || out[2]["level"] != levelHigh {
		t.Fatalf("level normalisation failed: %v", out)
	}
	if out[3]["level"] != "" {
		t.Fatalf("unknown level must blank out, got %q", out[3]["level"])
	}
}

// cleanExperiment returns nil for an all-empty plan and a populated map otherwise.
func TestCleanExperiment(t *testing.T) {
	if cleanExperiment(nil) != nil {
		t.Fatal("nil plan must yield nil")
	}
	if cleanExperiment(&experimentVal{}) != nil {
		t.Fatal("empty plan must yield nil")
	}
	got := cleanExperiment(&experimentVal{
		ExperimentType:  "покрытие",
		Sections:        []experimentSection{{Title: "Нанесение", Purpose: "получить покрытие", Items: []string{"PVD"}}},
		Materials:       []string{"substrate"},
		ProcessParams:   []experimentParam{{Name: "температура", Range: "500 °C"}},
		Variables:       []string{"v"},
		SuccessCriteria: "c",
		EstimatedTime:   "weeks",
	})
	if got["variables"] == nil || got["success_criteria"] != "c" || got["experiment_type"] != "coating_corrosion" {
		t.Fatalf("populated plan must carry fields, got %v", got)
	}
	if _, ok := got["sections"]; !ok {
		t.Fatalf("new domain sections must be preserved, got %v", got)
	}
}

// evidenceArticleKey prefers document id, then filename, then chunk id.
func TestEvidenceArticleKey(t *testing.T) {
	if k := evidenceArticleKey(&commonv1.Source{DocumentId: "d1"}); k != "doc:d1" {
		t.Fatalf("doc key = %q", k)
	}
	if k := evidenceArticleKey(&commonv1.Source{Filename: "F.PDF"}); k != "file:f.pdf" {
		t.Fatalf("file key = %q", k)
	}
	if k := evidenceArticleKey(&commonv1.Source{ChunkId: "c1"}); k != "chunk:c1" {
		t.Fatalf("chunk key = %q", k)
	}
}

// noveltySourceKey mirrors the document/filename/chunk preference.
func TestNoveltySourceKey(t *testing.T) {
	if k := noveltySourceKey(&commonv1.Source{DocumentId: "d"}); k != "doc:d" {
		t.Fatalf("doc key = %q", k)
	}
	if k := noveltySourceKey(&commonv1.Source{Filename: " A.PDF "}); k != "file:a.pdf" {
		t.Fatalf("file key = %q", k)
	}
	if k := noveltySourceKey(&commonv1.Source{ChunkId: "c"}); k != "chunk:c" {
		t.Fatalf("chunk key = %q", k)
	}
	if k := noveltySourceKey(&commonv1.Source{}); k != "" {
		t.Fatalf("empty source must yield empty key, got %q", k)
	}
}

// normalizeStance maps every recognised label and rejects the rest.
func TestNormalizeStance(t *testing.T) {
	cases := map[string]string{
		"supports": stanceSupports, "подтверждает": stanceSupports,
		"contradicts": stanceContradicts, "опровергает": stanceContradicts,
		"context": stanceContext, "метод": stanceContext,
		"nonsense": "",
	}
	for in, want := range cases {
		if got := normalizeStance(in); got != want {
			t.Fatalf("normalizeStance(%q) = %q, want %q", in, got, want)
		}
	}
}

// snippetForStance truncates long snippets to the classifier budget.
func TestSnippetForStance(t *testing.T) {
	short := snippetForStance("brief")
	if short != "brief" {
		t.Fatalf("short snippet must pass through, got %q", short)
	}
	long := snippetForStance(strings.Repeat("я", stanceSnippetMax+50))
	if !strings.HasSuffix(long, "…") {
		t.Fatal("an over-budget snippet must be ellipsised")
	}
}

// causalStageType maps the Russian stage vocabulary to node types.
func TestCausalStageType(t *testing.T) {
	cases := map[string]string{
		"состав": nodeMaterial, "процесс": nodeProcess, "режим": nodeProcess,
		"микроструктура": nodeMechanism, "механизм": nodeMechanism,
		"KPI": nodeKPI, "показатель": nodeKPI, "свойство": nodeProperty, "иное": nodeProperty,
	}
	for in, want := range cases {
		if got := causalStageType(in); got != want {
			t.Fatalf("causalStageType(%q) = %q, want %q", in, got, want)
		}
	}
}

// shortID compacts a UUID to its first segment.
func TestShortID(t *testing.T) {
	if got := shortID("abcd1234- effff"); got != "abcd1234" {
		t.Fatalf("shortID UUID = %q", got)
	}
	if got := shortID("0123456789"); got != "01234567" {
		t.Fatalf("shortID long-no-dash = %q", got)
	}
	if got := shortID("short"); got != "short" {
		t.Fatalf("shortID short = %q", got)
	}
}

// adaptiveTopK widens for short queries and narrows for specific ones.
func TestAdaptiveTopK(t *testing.T) {
	if got := adaptiveTopK("одно", 8, 12); got != 12 {
		t.Fatalf("broad query must hit the ceiling, got %d", got)
	}
	mid := adaptiveTopK("первый второй третий четвёртый пятый шестой", 8, 12)
	if mid != 10 {
		t.Fatalf("moderate query must use the midpoint, got %d", mid)
	}
	specific := adaptiveTopK(strings.Repeat("слово", 1)+" "+
		"альфа бета гамма дельта эпсилон дзета эта тета йота каппа", 8, 12)
	if specific != 8 {
		t.Fatalf("specific query must use the base, got %d", specific)
	}
}

// withDeadline keeps an already-tighter caller deadline and otherwise applies its own.
func TestWithDeadline(t *testing.T) {
	tight, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ctx, c := withDeadline(tight, time.Hour)
	defer c()
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) > time.Minute {
		t.Fatal("a tighter caller deadline must be preserved")
	}

	own, c2 := withDeadline(context.Background(), 50*time.Millisecond)
	defer c2()
	if _, ok := own.Deadline(); !ok {
		t.Fatal("withDeadline must impose its own deadline when none exists")
	}
}

// TRLRubricJSON exposes the embedded rubric as a package-level convenience.
func TestTRLRubricJSON(t *testing.T) {
	if len(TRLRubricJSON()) == 0 {
		t.Fatal("TRLRubricJSON must be non-empty")
	}
}

// judgeScores returns calibrated scores keyed by item index and tolerates noise.
func TestJudgeScores(t *testing.T) {
	a := newAnswerer(reply(`[{"idx":0,"novelty":0.7,"value":0.8,"risk":0.3,"confidence":0.6},` +
		`{"idx":99,"novelty":0.1}]`))
	svc := newHypService(newCatalog(), a)
	items := []genItem{{Title: "A", Statement: "s"}}
	got := svc.judgeScores(context.Background(), "alice", &commonv1.KPI{Title: "T"}, items, nil)
	if len(got) != 1 {
		t.Fatalf("out-of-range verdicts must be dropped, got %d", len(got))
	}
	if got[0].Novelty == nil || *got[0].Novelty != 0.7 {
		t.Fatalf("calibrated novelty not applied: %v", got[0])
	}
	// An LLM error yields nil so self-scores stand.
	bad := newHypService(newCatalog(), newAnswerer(failReply(context.DeadlineExceeded)))
	if got := bad.judgeScores(context.Background(), "alice", &commonv1.KPI{}, items, nil); got != nil {
		t.Fatalf("judge error must yield nil, got %v", got)
	}
}
