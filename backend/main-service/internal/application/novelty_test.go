package application

import (
	"context"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// fakeAnswerer returns a fixed RagResponse, ignoring the request, so novelty
// retrieval can be exercised without llm-service.
type fakeAnswerer struct {
	sources []*commonv1.Source
	err     error
}

func (f *fakeAnswerer) Answer(_ context.Context, _ *commonv1.RagRequest) (*commonv1.RagResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &commonv1.RagResponse{Answer: "", Sources: f.sources, Model: "fake", Cached: false}, nil
}

// fakeMaterials is a MaterialsRef that reports a configured set of known formulas.
type fakeMaterials struct{ known map[string]bool }

func (f *fakeMaterials) Known(_ context.Context, formula string) (bool, error) {
	return f.known[formula], nil
}

func src(doc, snippet string) *commonv1.Source {
	return &commonv1.Source{DocumentId: doc, Snippet: snippet, Score: 0.5}
}

// materialFormula must pick out a formula-shaped token and ignore plain words and
// acronyms (method abbreviations like SEM/XRD).
func TestMaterialFormula_Extraction(t *testing.T) {
	cases := []struct {
		name string
		item genItem
		want string
	}{
		{"from material_system", genItem{MaterialSystem: "сплав Al2O3 покрытие"}, "Al2O3"},
		{"from composition_change", genItem{CompositionChange: "добавить TiAlN слой"}, "TiAlN"},
		{"plain words only", genItem{MaterialSystem: "коррозионная стойкость покрытия"}, ""},
		{"acronym not a formula", genItem{MaterialSystem: "метод SEM анализ"}, ""},
		{"prefers material_system", genItem{MaterialSystem: "Fe3O4", CompositionChange: "Al2O3"}, "Fe3O4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := materialFormula(c.item); got != c.want {
				t.Fatalf("materialFormula() = %q, want %q", got, c.want)
			}
		})
	}
}

// Dense corpus prior art ⇒ low novelty; sparse ⇒ high; no retrieval ⇒ neutral.
func TestComputeNovelty_PriorArtDensity(t *testing.T) {
	item := genItem{MaterialSystem: "композит керамика прочность трещиностойкость", TargetProperty: "трещиностойкость"}

	// Every retrieved source overlaps the hypothesis heavily ⇒ well-trodden ⇒ low.
	dense := make([]*commonv1.Source, 0, 4)
	for i, d := range []string{"a", "b", "c", "d"} {
		_ = i
		dense = append(dense, src(d, "композит керамика прочность трещиностойкость материал"))
	}
	svc := &HypothesisService{answerer: &fakeAnswerer{sources: dense, err: nil}}
	got := svc.computeNovelty(context.Background(), "owner", item)
	if got.Score > 0.3 {
		t.Fatalf("dense prior art ⇒ low novelty, got %.3f", got.Score)
	}
	if got.CloseMatches == 0 {
		t.Fatalf("expected close matches counted, got 0")
	}

	// Sources that share no ≥4-rune tokens ⇒ novel territory ⇒ high.
	sparse := []*commonv1.Source{src("x", "unrelated lorem ipsum dolor"), src("y", "another unrelated topic")}
	svc = &HypothesisService{answerer: &fakeAnswerer{sources: sparse, err: nil}}
	got = svc.computeNovelty(context.Background(), "owner", item)
	if got.Score < 0.7 {
		t.Fatalf("sparse prior art ⇒ high novelty, got %.3f", got.Score)
	}

	// No retrieval at all ⇒ neutral 0.5.
	svc = &HypothesisService{answerer: &fakeAnswerer{sources: nil, err: nil}}
	got = svc.computeNovelty(context.Background(), "owner", item)
	if got.Score != noveltyNeutral {
		t.Fatalf("no corpus ⇒ neutral %.1f, got %.3f", noveltyNeutral, got.Score)
	}
}

// A known Materials Project material nudges novelty DOWN; the rationale says so.
func TestComputeNovelty_MaterialsProjectNudge(t *testing.T) {
	item := genItem{MaterialSystem: "покрытие Al2O3", TargetProperty: "износостойкость"}
	sparse := []*commonv1.Source{src("x", "unrelated lorem ipsum dolor"), src("y", "another unrelated topic")}

	without := (&HypothesisService{answerer: &fakeAnswerer{sources: sparse, err: nil}, materials: nil}).
		computeNovelty(context.Background(), "owner", item)
	with := (&HypothesisService{
		answerer:  &fakeAnswerer{sources: sparse, err: nil},
		materials: &fakeMaterials{known: map[string]bool{"Al2O3": true}},
	}).computeNovelty(context.Background(), "owner", item)

	if !(with.Score < without.Score) {
		t.Fatalf("known MP material must lower novelty: with %.3f, without %.3f", with.Score, without.Score)
	}
	if !with.MaterialKnown {
		t.Fatal("expected MaterialKnown=true for a known formula")
	}
	if !strings.Contains(with.Rationale, "Materials Project") {
		t.Fatalf("rationale should mention Materials Project, got %q", with.Rationale)
	}
}

// A retrieval error degrades to the neutral score (novelty is best-effort).
func TestComputeNovelty_RetrievalErrorNeutral(t *testing.T) {
	svc := &HypothesisService{answerer: &fakeAnswerer{sources: nil, err: context.DeadlineExceeded}}
	got := svc.computeNovelty(context.Background(), "owner", genItem{Statement: "любая гипотеза о материале"})
	if got.Score != noveltyNeutral {
		t.Fatalf("retrieval error ⇒ neutral %.1f, got %.3f", noveltyNeutral, got.Score)
	}
}
