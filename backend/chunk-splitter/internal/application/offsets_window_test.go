package application

// Regression guard for the indexer's multi-window provenance-offset path. The
// splitter package's offsets_test.go proves LocateOffsets in isolation and the
// base-add arithmetic for a single window at offset 0; this test exercises the
// part that lives in the indexer — windowEnd + tailRunes carry + windowBaseRune —
// across SEVERAL windows, asserting every absolute span still reproduces its
// chunk against the FULL document (the start>0 + non-empty-carry case).

import (
	"strings"
	"testing"
	"unicode"

	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
)

// normWS collapses every run of Unicode whitespace to one ASCII space and trims,
// matching the splitter/LocateOffsets normalisation.
func normWS(s string) string {
	out := make([]rune, 0, len(s))
	pending := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			pending = true
			continue
		}
		if pending {
			if len(out) > 0 {
				out = append(out, ' ')
			}
			pending = false
		}
		out = append(out, r)
	}
	return string(out)
}

func sliceRunesAbs(source string, start, end int) string {
	r := []rune(source)
	return string(r[start:end])
}

func TestWindowedOffsets_MultiWindowCarry(t *testing.T) {
	// A long, multi-paragraph Cyrillic document (2 bytes/rune) so a modest byte
	// window splits it into several windows, each carrying an overlap tail.
	full := strings.Join([]string{
		"Первый абзац о буровых растворах содержит двойные  пробелы и тире — вот так.",
		"Вторая строка того же абзаца продолжает мысль и тянется ещё дальше по тексту.",
		"",
		"Второй абзац начинается здесь и описывает коррозию насосно-компрессорных труб,",
		"чтобы заведомо пересечь границу окна и заставить overlap-хвост повториться.",
		"",
		"Третий абзац рассказывает про композитные материалы и температурную стойкость,",
		"добавляя ещё немного длины, чтобы окон точно стало несколько подряд.",
		"",
		"Четвёртый абзац завершает документ короткой, но осмысленной фразой о результатах.",
	}, "\n")

	const (
		window  = 140 // bytes per split window — small enough to force several
		overlap = 12  // rune overlap carried across windows
	)
	sp := splitter.NewRecursive(40, overlap)

	var (
		carry   string
		windows int
		checked int
	)
	for start := 0; start < len(full); {
		end := windowEnd(full, start, window)
		seg := carry + full[start:end]
		chunks := sp.Split(seg)
		offsets := splitter.LocateOffsets(seg, chunkTexts(chunks))
		baseRune := windowBaseRune(full, start, carry)

		for _, c := range chunks {
			cs, ce := absOffset(offsets, c.Index, baseRune)
			if cs == 0 && ce == 0 {
				continue // unlocated — never shifted, nothing to verify
			}
			if cs < 0 || ce > len([]rune(full)) || cs >= ce {
				t.Fatalf("window@%d chunk %d: absolute span [%d,%d) out of bounds", start, c.Index, cs, ce)
			}
			if got, want := normWS(sliceRunesAbs(full, cs, ce)), normWS(c.Text); got != want {
				t.Fatalf("window@%d chunk %d: absolute span does not reproduce chunk\n  span  = %q\n  chunk = %q", start, c.Index, got, want)
			}
			checked++
		}
		carry = tailRunes(seg, overlap)
		start = end
		windows++
	}

	if windows < 2 {
		t.Fatalf("test must span >=2 windows to exercise the carry path, got %d", windows)
	}
	if checked == 0 {
		t.Fatal("no located spans were verified; coverage is vacuous")
	}
	t.Logf("verified %d absolute spans across %d windows", checked, windows)
}
