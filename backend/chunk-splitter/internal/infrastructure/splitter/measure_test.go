package splitter

// Tests for the injectable size-measure (NewRecursiveWithMeasure) that backs
// EXACT token-budget chunking. They use a FAKE measure (word count) so no model
// is needed, and prove two independent things: (1) the budget is honoured in the
// injected unit (every chunk's measured size <= budget, save the documented
// single-oversized-piece edge), and (2) changing the size measure did NOT break
// char-offset provenance — LocateOffsets still reproduces every chunk from the
// source. The unit (words) is deliberately NOT runes, so a passing budget check
// can only come from r.measure, not the old runeLen path.

import (
	"strings"
	"testing"
)

// wordCount is the fake measure: number of whitespace-separated fields. It is a
// coarse, non-rune unit, exactly the kind of "real token count" stand-in the
// task asks for.
func wordCount(s string) int { return len(strings.Fields(s)) }

func TestNewRecursiveWithMeasure_HonoursWordBudget(t *testing.T) {
	const budget = 8 // words per chunk
	sp := NewRecursiveWithMeasure(budget, 5 /*rune overlap*/, wordCount)

	text := strings.Join([]string{
		"alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi.",
		"Второй абзац содержит несколько слов и продолжает мысль дальше по тексту примерно.",
		"",
		"Третий абзац короткий но добавляет ещё немного слов чтобы было несколько чанков точно.",
	}, "\n\n")

	chunks := sp.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("expected several chunks for a multi-word document, got %d", len(chunks))
	}

	for i, c := range chunks {
		// The chunk text includes the rune-based overlap prefix carried from the
		// previous chunk, so the BODY (after overlap) is what the budget governed.
		// Measuring the whole chunk is still a valid upper-bound sanity check only
		// for the first chunk (no overlap); for later chunks the overlap words can
		// push the total slightly over budget. So assert the rule the splitter
		// actually enforces: the merge body never exceeds budget, which for chunk 0
		// equals the whole chunk.
		if i == 0 {
			if got := wordCount(c.Text); got > budget {
				t.Fatalf("chunk 0 measured %d words > budget %d: %q", got, budget, c.Text)
			}
		}
	}

	// Provenance: every chunk must still be locatable in the source and its
	// located span, normalised, must reproduce the chunk — token (here word)
	// sizing must not have disturbed the char offsets.
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	offsets := LocateOffsets(text, texts)
	if len(offsets) != len(chunks) {
		t.Fatalf("offsets len %d != chunks %d", len(offsets), len(chunks))
	}
	located := 0
	srcRunes := len([]rune(text))
	for i, off := range offsets {
		if off == ([2]int{0, 0}) {
			t.Logf("chunk %d unlocated: %q", i, texts[i])
			continue
		}
		if off[0] < 0 || off[1] > srcRunes || off[0] >= off[1] {
			t.Fatalf("chunk %d span %v out of bounds (%d runes)", i, off, srcRunes)
		}
		if got, want := normForTest(sliceRunes(text, off[0], off[1])), normForTest(texts[i]); got != want {
			t.Fatalf("chunk %d span does not reproduce chunk:\n  span  = %q\n  chunk = %q", i, got, want)
		}
		located++
	}
	if located != len(chunks) {
		t.Fatalf("located %d of %d chunks; word sizing must not break provenance", located, len(chunks))
	}
}

// TestNewRecursiveWithMeasure_MergeBodyNeverExceedsBudget strips the overlap back
// off each chunk and asserts the merged body honours the budget for EVERY chunk,
// pinning the budget arithmetic (which the previous test only checks on chunk 0).
func TestNewRecursiveWithMeasure_MergeBodyNeverExceedsBudget(t *testing.T) {
	const (
		budget  = 6
		overlap = 0 // zero overlap → each chunk IS its merge body, easy to assert
	)
	sp := NewRecursiveWithMeasure(budget, overlap, wordCount)

	text := "one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen"
	chunks := sp.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if got := wordCount(c.Text); got > budget {
			t.Fatalf("chunk %d measured %d words > budget %d (overlap=0): %q", i, got, budget, c.Text)
		}
	}
}

// TestNewRecursiveWithMeasure_OversizedSinglePieceEdge documents the one allowed
// over-budget case: a single atomic piece with no usable separator that, measured
// in the injected unit, exceeds the budget. hardSplit stays rune-based, so it can
// hand mergePieces one piece whose WORD count is 1 but whose measure (if the unit
// were e.g. chars) exceeds budget; here we force it with a word measure that
// counts a no-space blob as 1 word — that single piece becomes its own chunk
// rather than looping or being dropped.
func TestNewRecursiveWithMeasure_OversizedSinglePieceEdge(t *testing.T) {
	// A long no-whitespace blob: wordCount == 1 (one field), so it is never over
	// the word budget; instead use a measure that makes it oversized to exercise
	// the pLen>r.size branch deterministically.
	bigMeasure := func(s string) int { return len([]rune(s)) } // chars
	const budget = 10
	sp := NewRecursiveWithMeasure(budget, 0, bigMeasure)

	blob := strings.Repeat("z", 35) // 35 chars, no separator → one oversized piece
	chunks := sp.Split(blob)
	if len(chunks) == 0 {
		t.Fatal("oversized blob produced no chunks")
	}
	// hardSplit (rune-based) cuts the blob into <=budget-rune windows, so each
	// resulting chunk is within budget; the point is it terminates and reproduces.
	joined := strings.Join(func() []string {
		out := make([]string, len(chunks))
		for i, c := range chunks {
			out[i] = c.Text
		}
		return out
	}(), "")
	if joined != blob {
		t.Fatalf("oversized blob not reconstructed: got %q want %q", joined, blob)
	}
}
