package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

type fakePubs struct{ works []ExternalWork }

func (f *fakePubs) SearchWorks(context.Context, string, string, string, int) ([]ExternalWork, error) {
	return f.works, nil
}

func TestSanitizePubQuery(t *testing.T) {
	cases := map[string]string{
		`{"query": "speaker verification"}`:                          "speaker verification",
		"```json\n{\"query\": \"encrypted traffic classification\"}": "encrypted traffic classification",
		`Вот запрос: {"query": "graph fraud detection"}`:             "graph fraud detection",
		`{"query": ""}`:                    "",
		"biochar saline soils":             "biochar saline soils",
		`{"query": "a b c d e f g h i j"}`: "a b c d e f g h",
	}
	for in, want := range cases {
		if got := sanitizePubQuery(in); got != want {
			t.Fatalf("sanitizePubQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopicFreshness(t *testing.T) {
	if _, _, ok := topicFreshness(nil, 2026); ok {
		t.Fatal("no works must yield no signal")
	}
	if _, _, ok := topicFreshness([]ExternalWork{{Year: 0}}, 2026); ok {
		t.Fatal("yearless works must yield no signal")
	}
	fresh, _, ok := topicFreshness([]ExternalWork{{Year: 2026}, {Year: 2025}}, 2026)
	if !ok || fresh < 0.9 {
		t.Fatalf("recent burst = %.2f ok=%v, want high", fresh, ok)
	}
	stale, _, ok := topicFreshness([]ExternalWork{{Year: 2021}, {Year: 2021}}, 2026)
	if !ok || stale > 0.1 {
		t.Fatalf("stale = %.2f ok=%v, want low", stale, ok)
	}
	if stale >= fresh {
		t.Fatalf("stale %.2f must sit below fresh %.2f", stale, fresh)
	}
}

func TestWorldPracticeNote(t *testing.T) {
	if worldPracticeNote(nil) != "" {
		t.Fatal("empty works must add nothing to the prompt")
	}
	works := []ExternalWork{{
		Title: "Regrinding of sulfide tailings", Year: 2021, DOI: "10.1/x",
		Venue: "Miner. Eng.", Abstract: strings.Repeat("а", 500),
	}}
	note := worldPracticeNote(works)
	for _, want := range []string{"[P1]", "Regrinding", "(2021)", "doi:10.1/x", "…"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note lost %q: %s", want, note)
		}
	}

	s := &HypothesisService{}
	if s.externalWorks(context.Background(), &commonv1.KPI{Title: "т"}, "") != nil {
		t.Fatal("nil searcher must be a no-op")
	}
	s.pubs = &fakePubs{works: works}
	if got := s.externalWorks(context.Background(), &commonv1.KPI{Title: "т"}, ""); len(got) != 1 {
		t.Fatalf("works = %v", got)
	}
}

type fakeIngestor struct{ files []string }

func (f *fakeIngestor) IngestTextIfNew(_ context.Context, _, filename, content string) error {
	if content == "" {
		return errors.New("empty content")
	}
	f.files = append(f.files, filename)
	return nil
}

func TestIngestWorksSkipsAbstractless(t *testing.T) {
	f := &fakeIngestor{}
	s := &HypothesisService{pubIngest: f}
	s.ingestWorks(context.Background(), "u1", []ExternalWork{
		{Title: "No abstract", DOI: "10.1/no"},
		{Title: "Flotation study", DOI: "10.1016/j.mineng.2021.107x", Abstract: "Copper recovery improved."},
	})
	if len(f.files) != 1 || f.files[0] != "pub_10-1016-j-mineng-2021-107x.txt" {
		t.Fatalf("unexpected ingested files: %v", f.files)
	}
}
