package splitter

import (
	"testing"
	"unicode"
)

// normForTest applies the SAME whitespace normalisation the splitter and
// LocateOffsets use: collapse every run of Unicode whitespace to one ASCII space
// and trim the ends. The test uses it to check that the located original span,
// once normalised, reproduces the chunk text — proving the offsets point at the
// real source span rather than coincidentally lining up.
func normForTest(s string) string {
	out := make([]rune, 0, len(s))
	pendingSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			pendingSpace = true
			continue
		}
		if pendingSpace {
			if len(out) > 0 {
				out = append(out, ' ')
			}
			pendingSpace = false
		}
		out = append(out, r)
	}
	return string(out)
}

// sliceRunes returns source[start:end] in RUNE coordinates (LocateOffsets emits
// rune offsets, so the test must slice on runes too, not bytes).
func sliceRunes(source string, start, end int) string {
	r := []rune(source)
	return string(r[start:end])
}

// multiParaSource has paragraph ("\n\n") and line ("\n") separators plus double
// spaces, so a chunk's whitespace-collapsed text is never a literal substring of
// the source — exactly the case LocateOffsets must handle. Unicode (Cyrillic +
// an em dash) keeps the rune-vs-byte distinction honest.
const multiParaSource = "Первый  абзац содержит  двойные пробелы и тире — здесь.\n" +
	"Вторая строка того же абзаца продолжает мысль дальше.\n\n" +
	"Второй абзац начинается тут и тянется достаточно долго,\n" +
	"чтобы пересечь границу чанка и заставить overlap повториться.\n\n" +
	"Третий абзац короткий, но осмысленный."

func TestLocateOffsets_RealSpans_WithOverlap(t *testing.T) {
	// Small size + nonzero overlap forces multiple chunks whose heads repeat the
	// previous chunk's tail (the tricky monotonic-cursor case).
	sp := NewRecursive(40, 12)
	chunks := sp.Split(multiParaSource)
	if len(chunks) < 3 {
		t.Fatalf("expected the source to split into several chunks, got %d", len(chunks))
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	offsets := LocateOffsets(multiParaSource, texts)
	if len(offsets) != len(chunks) {
		t.Fatalf("offsets len = %d, want %d", len(offsets), len(chunks))
	}

	srcRunes := len([]rune(multiParaSource))
	located := 0
	for i, off := range offsets {
		start, end := off[0], off[1]
		if start == 0 && end == 0 {
			// [0,0] is the explicit "unlocated" sentinel; allowed but every real
			// chunk of this source should locate, so flag it for visibility.
			t.Logf("chunk %d (%q) reported unlocated [0,0]", i, texts[i])
			continue
		}
		located++

		// Bounds + non-empty, end-exclusive span sanity.
		if start < 0 || end > srcRunes || start >= end {
			t.Fatalf("chunk %d: span [%d,%d) out of bounds (source has %d runes)", i, start, end, srcRunes)
		}

		// THE proof: the original runes at [start,end), normalised the same way,
		// must equal the normalised chunk text.
		gotSpan := normForTest(sliceRunes(multiParaSource, start, end))
		wantChunk := normForTest(texts[i])
		if gotSpan != wantChunk {
			t.Fatalf("chunk %d span mismatch:\n  span  = %q\n  chunk = %q", i, gotSpan, wantChunk)
		}
	}

	if located == 0 {
		t.Fatal("no chunks were located; expected all real chunks to resolve")
	}
	if located != len(chunks) {
		t.Errorf("located %d of %d chunks; all should resolve for this source", located, len(chunks))
	}
}

func TestLocateOffsets_NotPresent_IsZeroZero(t *testing.T) {
	source := "alpha beta gamma delta epsilon"
	texts := []string{
		"beta gamma",                   // present
		"this phrase is not in source", // absent → [0,0]
		"delta epsilon",                // present, AFTER the absent one
	}
	offsets := LocateOffsets(source, texts)

	if got := offsets[1]; got != [2]int{0, 0} {
		t.Fatalf("absent chunk should be [0,0], got %v", got)
	}
	// The absent chunk must NOT poison the cursor: the following present chunk
	// still resolves to its real span.
	for _, i := range []int{0, 2} {
		off := offsets[i]
		if off == ([2]int{0, 0}) {
			t.Fatalf("chunk %d (%q) should have located", i, texts[i])
		}
		if got := normForTest(sliceRunes(source, off[0], off[1])); got != normForTest(texts[i]) {
			t.Fatalf("chunk %d span %q != chunk %q", i, got, texts[i])
		}
	}
}

func TestLocateOffsets_Empty(t *testing.T) {
	if got := LocateOffsets("anything", nil); len(got) != 0 {
		t.Fatalf("nil chunks should yield empty offsets, got %v", got)
	}
	// A whitespace-only chunk normalises to "" → cannot be located → [0,0].
	got := LocateOffsets("some text here", []string{"   \n\t  "})
	if len(got) != 1 || got[0] != [2]int{0, 0} {
		t.Fatalf("whitespace-only chunk should be [0,0], got %v", got)
	}
}

// TestLocateOffsets_WindowBase mirrors the indexer's window-base addition: when
// the source given to LocateOffsets is a WINDOW (a slice) of a larger document,
// adding the window's base RUNE offset to every located span must land it on the
// matching span of the FULL document. [0,0] (unlocated) is never shifted.
func TestLocateOffsets_WindowBase(t *testing.T) {
	full := "PREFIX выкинутый текст. " + multiParaSource

	// The window is everything from multiParaSource onward; its base rune offset
	// within `full` is the rune length of the dropped prefix.
	prefix := "PREFIX выкинутый текст. "
	baseRune := len([]rune(prefix))
	window := multiParaSource

	sp := NewRecursive(40, 12)
	chunks := sp.Split(window)
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	localOffsets := LocateOffsets(window, texts)

	fullRunes := len([]rune(full))
	checked := 0
	for i, lo := range localOffsets {
		if lo == ([2]int{0, 0}) {
			continue // unlocated: stays [0,0], not shifted (matches absOffset)
		}
		// Apply the same lift the indexer's absOffset performs.
		absStart, absEnd := lo[0]+baseRune, lo[1]+baseRune
		if absStart < 0 || absEnd > fullRunes || absStart >= absEnd {
			t.Fatalf("chunk %d: absolute span [%d,%d) out of bounds (full has %d runes)", i, absStart, absEnd, fullRunes)
		}
		gotSpan := normForTest(sliceRunes(full, absStart, absEnd))
		wantChunk := normForTest(texts[i])
		if gotSpan != wantChunk {
			t.Fatalf("chunk %d absolute span mismatch:\n  span  = %q\n  chunk = %q", i, gotSpan, wantChunk)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no chunks checked against the full document; window-base coverage is vacuous")
	}
}
