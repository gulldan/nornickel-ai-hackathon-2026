package application

// Tests for the token-budget chunking path wired into the REAL Indexer.Process
// loop (driven like process_integration_test.go, through fake stores). A fake
// Tokenizer returns WORD counts as a stand-in for F2LLM-v2 tokens — no live server.
// They assert three things end to end:
//   - when a tokenizer is configured, chunks respect the TOKEN (here word) budget;
//   - char-offset and page provenance still hold (token sizing did not break the
//     text-matching provenance, which is independent of the size measure);
//   - a tokenizer that ERRORS makes the per-piece measure fall back to rune length
//     rather than failing ingestion (the document still indexes).
//
// It reuses noopStatus/dummyEmbedder/capturingVectors/noopSearch/noopPub and the
// normWS/sliceRunesAbs helpers from the sibling test files.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

// wordTokenizer counts whitespace-separated fields, standing in for the F2LLM-v2
// tokenizer. calls counts how often it ran so a test can prove memoization
// avoids re-tokenizing identical pieces.
type wordTokenizer struct{ calls int64 }

func (w *wordTokenizer) CountTokens(_ context.Context, text string) (int, error) {
	atomic.AddInt64(&w.calls, 1)
	return len(strings.Fields(text)), nil
}

// failingTokenizer always errors, forcing the indexer's measure closure onto its
// rune-length fallback. calls proves it was actually consulted.
type failingTokenizer struct{ calls int64 }

func (f *failingTokenizer) CountTokens(_ context.Context, _ string) (int, error) {
	atomic.AddInt64(&f.calls, 1)
	return 0, errors.New("tokenizer unavailable")
}

func TestProcess_TokenBudget_RespectedAndProvenanceHolds(t *testing.T) {
	page1 := "Первая страница про буровые растворы и плотность жидкости, длинная фраза для нескольких чанков подряд."
	page2 := "Вторая страница описывает коррозию насосно-компрессорных труб и защиту от сероводорода в скважине надёжно."
	page3 := "Третья страница посвящена композитным материалам и температурной стойкости при высоких механических нагрузках."
	full := page1 + "\n" + page2 + "\n" + page3

	r2 := utf8.RuneCountInString(page1 + "\n")
	r3 := utf8.RuneCountInString(page1 + "\n" + page2 + "\n")
	raw := mustJSON(t, []int{0, r2, r3})

	const tokenBudget = 6 // "tokens" (words) per chunk

	tok := &wordTokenizer{}
	vecs := &capturingVectors{}
	ix := New(
		// This rune splitter must be IGNORED because a tokenizer is configured;
		// give it a wildly different size to prove the token path is the one used.
		splitter.NewRecursive(10000, 0),
		noopStatus{}, dummyEmbedder{}, vecs, noopSearch{}, noopPub{},
		nil, nil, // pacer, object store
		tok, tokenBudget, // tokenizer + token budget → token-budget chunking ON
		0, 8, // textMax (→ default), batchSize
		120, 4, // splitWindow (bytes), overlap (runes)
		true, // contextual headers on (default)
		nil,  // metrics
	)

	evt := &commonv1.DocumentParsed{
		DocumentId: "doc-tok",
		OwnerId:    "u1",
		Filename:   "report.pdf",
		Text:       full,
		Metadata:   map[string]string{"page_offsets": string(raw)},
	}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(vecs.points) == 0 {
		t.Fatal("no vector points captured — token-budget Process produced nothing")
	}
	if tok.calls == 0 {
		t.Fatal("tokenizer was never called — token-budget path not taken")
	}

	// Budget: the rune splitter (size 10000) would have emitted ONE chunk for this
	// short text; the token budget of 6 words must instead produce several, each
	// bounded. We assert the merge body honours the budget by stripping the
	// rune-overlap prefix is awkward across pages, so instead assert the strong
	// structural fact: more than one chunk, and the FIRST chunk of the document
	// (no carried overlap) is within budget.
	if len(vecs.points) < 3 {
		t.Fatalf("token budget %d should split this text into several chunks, got %d", tokenBudget, len(vecs.points))
	}
	if got := len(strings.Fields(vecs.points[0].Text)); got > tokenBudget {
		t.Fatalf("first chunk has %d word-tokens > budget %d: %q", got, tokenBudget, vecs.points[0].Text)
	}

	// Provenance: every located chunk's offsets reproduce its text, and page-aware
	// windowing keeps each chunk on a single page — identical guarantees to the
	// rune path, proving the size-measure swap left provenance untouched.
	spans, pages := 0, 0
	for _, p := range vecs.points {
		if p.CharStart == 0 && p.CharEnd == 0 {
			if p.PageStart != 0 || p.PageEnd != 0 {
				t.Fatalf("unlocated chunk carries page %d-%d: %q", p.PageStart, p.PageEnd, p.Text)
			}
			continue
		}
		if got, want := normWS(sliceRunesAbs(full, p.CharStart, p.CharEnd)), normWS(p.Text); got != want {
			t.Fatalf("token-path offsets wrong: span %q != chunk %q", got, want)
		}
		spans++
		if p.PageStart != p.PageEnd {
			t.Fatalf("chunk spans pages %d..%d: %q", p.PageStart, p.PageEnd, p.Text)
		}
		if p.PageStart < 1 || p.PageStart > 3 {
			t.Fatalf("page %d out of range: %q", p.PageStart, p.Text)
		}
		pages++
	}
	if spans == 0 || pages == 0 {
		t.Fatal("nothing verified — expected located, page-annotated chunks under token budget")
	}
	t.Logf("token-budget Process: %d chunks, %d offset spans reproduced, %d single-page chunks, %d tokenizer calls",
		len(vecs.points), spans, pages, tok.calls)
}

// TestProcess_TokenizerError_FallsBackToRunes proves a failing tokenizer does NOT
// fail ingestion: the per-piece measure falls back to rune length, the document
// still indexes, and provenance still holds.
func TestProcess_TokenizerError_FallsBackToRunes(t *testing.T) {
	full := strings.Join([]string{
		"Первый абзац о буровых растворах содержит двойные  пробелы и тире — вот так здесь.",
		"Второй абзац начинается здесь и описывает коррозию насосно-компрессорных труб подробно.",
	}, "\n\n")

	tok := &failingTokenizer{}
	vecs := &capturingVectors{}
	ix := New(
		splitter.NewRecursive(10000, 0), // ignored (tokenizer configured)
		noopStatus{}, dummyEmbedder{}, vecs, noopSearch{}, noopPub{},
		nil, nil,
		tok, 40, // tokenizer (errors) + token budget; fallback unit is runes
		0, 8,
		200, 8,
		true, // contextual headers on (default)
		nil,
	)

	evt := &commonv1.DocumentParsed{
		DocumentId: "doc-fallback",
		OwnerId:    "u1",
		Filename:   "f.txt",
		Text:       full,
	}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process must not fail when the tokenizer errors: %v", err)
	}
	if tok.calls == 0 {
		t.Fatal("failing tokenizer was never consulted — fallback path not exercised")
	}
	if len(vecs.points) == 0 {
		t.Fatal("tokenizer-error fallback produced no chunks; ingestion should still index")
	}
	// With the rune fallback and budget 40 runes, the text splits into several
	// chunks (it is well over 40 runes), and provenance still reproduces them.
	if len(vecs.points) < 2 {
		t.Fatalf("expected the rune-fallback budget to split the text, got %d chunks", len(vecs.points))
	}
	for _, p := range vecs.points {
		if p.CharStart == 0 && p.CharEnd == 0 {
			continue
		}
		if got, want := normWS(sliceRunesAbs(full, p.CharStart, p.CharEnd)), normWS(p.Text); got != want {
			t.Fatalf("fallback offsets wrong: span %q != chunk %q", got, want)
		}
	}
	t.Logf("tokenizer-error fallback: indexed %d chunks via rune length, %d tokenizer attempts", len(vecs.points), tok.calls)
}

// TestProcess_TokenizerMemoized proves the per-document memo avoids re-tokenizing
// identical pieces: the recursive splitter measures the same sub-strings more than
// once (and overlap repeats text), yet each DISTINCT piece is tokenized at most
// once, so tokenizer calls << total measure invocations.
func TestProcess_TokenizerMemoized(t *testing.T) {
	// Repeated identical sentences guarantee duplicate pieces across the merge.
	sentence := "буровой раствор плотность вязкость фильтрация скважина"
	full := strings.Join([]string{sentence, sentence, sentence, sentence, sentence}, "\n\n")

	tok := &wordTokenizer{}
	vecs := &capturingVectors{}
	ix := New(
		splitter.NewRecursive(10000, 0),
		noopStatus{}, dummyEmbedder{}, vecs, noopSearch{}, noopPub{},
		nil, nil,
		tok, 4, // small token budget → many measure calls on repeated pieces
		0, 8,
		400, 0, // single window, no overlap → duplicates come purely from repetition
		true, // contextual headers on (default)
		nil,
	)
	evt := &commonv1.DocumentParsed{DocumentId: "doc-memo", OwnerId: "u1", Filename: "f.txt", Text: full}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Distinct pieces here are few (the repeated sentence and its sub-words), so a
	// correct memo keeps calls small. A non-memoizing implementation would call
	// the tokenizer once per measure invocation across all five copies — many more.
	// We assert a generous upper bound that only a working memo can satisfy.
	if tok.calls == 0 {
		t.Fatal("tokenizer never called")
	}
	if tok.calls > 30 {
		t.Fatalf("memoization appears ineffective: %d tokenizer calls for a 5x-repeated sentence", tok.calls)
	}
	t.Logf("memoized token-budget: %d tokenizer calls, %d chunks", tok.calls, len(vecs.points))
}
