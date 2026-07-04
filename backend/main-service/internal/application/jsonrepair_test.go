package application

import (
	"encoding/json"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

func TestRepairJSONBackslashes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// \c is not a valid JSON escape → the lone backslash gets doubled.
		{"latex chem", `{"r":"\ce{H2O}"}`, `{"r":"\\ce{H2O}"}`},
		{"latex sigma", `{"r":"\sigma=\alpha"}`, `{"r":"\\sigma=\\alpha"}`},
		// Valid escapes (\n \" \\ \u) must be preserved exactly.
		{"valid escapes kept", `{"r":"a\nb \"q\" \\ A"}`, `{"r":"a\nb \"q\" \\ A"}`},
		{"already escaped", `{"r":"\\ce{CO2}"}`, `{"r":"\\ce{CO2}"}`},
		{"no backslash", `{"r":"plain text"}`, `{"r":"plain text"}`},
	}
	for _, c := range cases {
		if got := repairJSONBackslashes(c.in); got != c.want {
			t.Errorf("%s: repairJSONBackslashes(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestRepairedJSONUnmarshals(t *testing.T) {
	// Unescaped LaTeX backslashes make this invalid JSON; the repaired form parses.
	bad := `[{"title":"T","statement":"S","rationale":"$E=mc^2$, \ce{H2O}"}]`
	var items []genItem
	if err := json.Unmarshal([]byte(bad), &items); err == nil {
		t.Fatal("expected raw output with unescaped LaTeX to be invalid JSON")
	}
	if err := json.Unmarshal([]byte(repairJSONBackslashes(bad)), &items); err != nil {
		t.Fatalf("repaired JSON should parse: %v", err)
	}
	if len(items) != 1 || items[0].Rationale != `$E=mc^2$, \ce{H2O}` {
		t.Fatalf("unexpected parse result: %+v", items)
	}
}

func TestParseGenItemsRecoversLatex(t *testing.T) {
	answer := `prose before [{"title":"T","statement":"S","rationale":"\ce{CO2} + \pu{5 g}"}] trailing`
	items, err := parseGenItems(answer)
	if err != nil {
		t.Fatalf("parseGenItems: %v", err)
	}
	if len(items) != 1 || items[0].Rationale != `\ce{CO2} + \pu{5 g}` {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestParseGenItemsAcceptsWrappedAndSingleObjects(t *testing.T) {
	wrapped := "```json\n" +
		`{"hypotheses":[{"title":"T","statement":"S","rationale":"R"}]}` +
		"\n```"
	items, err := parseGenItems(wrapped)
	if err != nil || len(items) != 1 || items[0].Title != "T" {
		t.Fatalf("wrapped output should parse, items=%+v err=%v", items, err)
	}
	single := `{"title":"One","statement":"S","rationale":"R"}`
	items, err = parseGenItems(single)
	if err != nil || len(items) != 1 || items[0].Title != "One" {
		t.Fatalf("single object should parse, items=%+v err=%v", items, err)
	}
}

func TestParseGenItemsRecoversCompletedObjectsFromTruncatedArray(t *testing.T) {
	answer := "```json\n[" +
		`{"title":"One","statement":"S1","rationale":"R1","causal_chain":[{"stage":"KPI","change":"x"}]},` +
		`{"title":"Two","statement":"S2","rationale":"R2"},` +
		`{"title":"Broken","statement":"S3","rationale":"`
	items, err := parseGenItems(answer)
	if err != nil {
		t.Fatalf("truncated array should yield completed items: %v", err)
	}
	if len(items) != 2 || items[0].Title != "One" || items[1].Title != "Two" {
		t.Fatalf("unexpected partial items: %+v", items)
	}
}

func TestGenerationPromptsUseValidJSONExamples(t *testing.T) {
	genPrompt := buildGenPrompt(&commonv1.KPI{Title: "Повысить точность прогноза"}, 3, "", false)
	judgePrompt := buildJudgePrompt([]genItem{{
		Title:     "Модель X снизит ошибку прогноза",
		Statement: "Если применить модель X, то ошибка прогноза снизится.",
	}})
	for name, prompt := range map[string]string{"generator": genPrompt, "judge": judgePrompt} {
		if strings.Contains(prompt, "0..1") || strings.Contains(prompt, "1-9") {
			t.Fatalf("%s prompt must not contain invalid JSON numeric ranges", name)
		}
		if !strings.Contains(prompt, `"novelty":0.5`) || !strings.Contains(prompt, `"confidence":0.6`) {
			t.Fatalf("%s prompt should show concrete JSON numbers", name)
		}
	}
}

func TestFallbackGenItemsUsesEvidence(t *testing.T) {
	kpi := &commonv1.KPI{
		Title:        "Повысить точность сегментации КТ",
		Metric:       "Dice",
		FunctionArea: "Медицинская визуализация",
	}
	sources := []*commonv1.Source{
		{
			DocumentId: "doc-1",
			Filename:   "001_SAM_CT_segmentation.pdf",
			Snippet:    "Метод SAM адаптируется для сегментации КТ и сравнивается с U-Net. Это релевантный фрагмент.",
			Score:      0.91,
		},
		{
			DocumentId: "doc-1",
			Filename:   "001_SAM_CT_segmentation.pdf",
			Snippet:    "Повтор того же документа не должен создавать вторую fallback-гипотезу.",
			Score:      0.88,
		},
		{
			DocumentId: "doc-2",
			Filename:   "002_Low_cost_inference.pdf",
			Snippet:    "Экономная модель снижает стоимость инференса на медицинских изображениях.",
			Score:      0.76,
		},
	}

	items := fallbackGenItems(kpi, sources, 3)
	if len(items) != 2 {
		t.Fatalf("fallbackGenItems() len = %d, want 2", len(items))
	}
	if items[0].Title == "" || items[0].Statement == "" || items[0].Rationale == "" {
		t.Fatalf("fallback item must be usable: %+v", items[0])
	}
	if items[0].Function != kpi.FunctionArea || items[0].SourceType != "literature" {
		t.Fatalf("unexpected metadata: %+v", items[0])
	}
	if items[0].TRL == nil || *items[0].TRL != 3 {
		t.Fatalf("unexpected TRL: %+v", items[0].TRL)
	}
}

func TestFirstSentenceIsRuneSafe(t *testing.T) {
	got := firstSentence("Точность сегментации повышается при адаптации модели.", 12)
	if got == "" || got == "Точность сег�" {
		t.Fatalf("firstSentence produced broken unicode: %q", got)
	}
}

func TestSelectEvidenceGroupsChunksByArticle(t *testing.T) {
	item := genItem{
		Statement: "Проверить SAM сегментацию КТ и экономный inference",
		Rationale: "SAM segmentation CT inference",
	}
	sources := []*commonv1.Source{
		{DocumentId: "doc-1", ChunkId: "c1", Filename: "sam.pdf", Snippet: "SAM segmentation CT", Score: 0.95},
		{DocumentId: "doc-1", ChunkId: "c2", Filename: "sam.pdf", Snippet: "SAM CT inference", Score: 0.90},
		{DocumentId: "doc-1", ChunkId: "c3", Filename: "sam.pdf", Snippet: "SAM extra CT", Score: 0.85},
		{DocumentId: "doc-2", ChunkId: "c4", Filename: "inference.pdf", Snippet: "economic inference CT", Score: 0.80},
	}

	got := selectEvidence(sources, item)
	if len(got) != 3 {
		t.Fatalf("selectEvidence() len = %d, want 3 (2 chunks from doc-1 + 1 from doc-2)", len(got))
	}
	perDoc := map[string]int{}
	for _, ev := range got {
		perDoc[ev.GetDocumentId()]++
	}
	if perDoc["doc-1"] != genEvidenceChunks || perDoc["doc-2"] != 1 {
		t.Fatalf("unexpected grouping: %+v", perDoc)
	}
}
