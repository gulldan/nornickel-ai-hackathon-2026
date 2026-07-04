package application

import (
	"context"
	"errors"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// SuggestKPIs feeds cluster representatives to the LLM, parses the strict JSON
// array it returns and deduplicates near-identical candidates.
func TestSuggestKPIs_ParsesAndDedupes(t *testing.T) {
	cat := newCatalog()
	cat.clusters["c1"] = &commonv1.Cluster{
		Id: "c1", OwnerId: "alice", Label: "Жаропрочные сплавы",
		Keywords: []string{"никель", "предел текучести"}, DocumentCount: 5,
		Representatives: `[{"document_id":"d1","filename":"ni-alloy.pdf",` +
			`"snippet":"Предел текучести Ni-сплава при 800°C составил 420 МПа."}]`,
	}
	answer := `[
	  {"title":"Повысить жаропрочность Ni-сплава","metric":"предел текучести","unit":"МПа",
	   "direction":"increase","function_area":"Жаропрочные сплавы","rationale":"ni-alloy.pdf: базовое 420 МПа"},
	  {"title":"Повысить жаропрочность Ni-сплава","metric":"предел текучести","unit":"МПа",
	   "direction":"increase","function_area":"Жаропрочные сплавы","rationale":"дубликат"}
	]`
	svc := newHypService(cat, newAnswerer(reply(answer)))

	got, err := svc.SuggestKPIs(context.Background(), "alice")
	if err != nil {
		t.Fatalf("SuggestKPIs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 deduped suggestion, got %d: %+v", len(got), got)
	}
	if s := got[0]; s.Metric != "предел текучести" || s.Direction != "increase" || s.Unit != "МПа" {
		t.Fatalf("unexpected suggestion: %+v", s)
	}
}

// A direction the model omits or garbles is inferred from the goal wording.
func TestSuggestKPIs_InfersDirection(t *testing.T) {
	cat := newCatalog()
	answer := `[{"title":"Снизить пористость покрытия","metric":"пористость","unit":"%",` +
		`"direction":"","function_area":"Покрытия","rationale":"по работам о коррозии"}]`
	svc := newHypService(cat, newAnswerer(reply(answer)))

	got, err := svc.SuggestKPIs(context.Background(), "alice")
	if err != nil {
		t.Fatalf("SuggestKPIs: %v", err)
	}
	if len(got) != 1 || got[0].Direction != "decrease" {
		t.Fatalf("want inferred decrease, got %+v", got)
	}
}

// An unavailable LLM must yield an empty list, never an error (no 500 upstream).
func TestSuggestKPIs_LLMErrorYieldsEmpty(t *testing.T) {
	svc := newHypService(newCatalog(), newAnswerer(failReply(errors.New("llm down"))))

	got, err := svc.SuggestKPIs(context.Background(), "alice")
	if err != nil {
		t.Fatalf("SuggestKPIs must not error when LLM is down: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty on LLM error, got %d", len(got))
	}
}
