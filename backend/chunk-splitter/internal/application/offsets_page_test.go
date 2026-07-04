package application

// Tests for the page/section provenance the indexer derives from a parser's
// DocumentParsed.metadata: the pure docStructure accessors, and the page-aware
// windowing invariant (no chunk ever spans two pages, offsets still correct).

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
)

func TestDocStructure_PageOfAndValidation(t *testing.T) {
	text := "стр1 текст\nстр2 текст\nстр3 текст"
	p2 := utf8.RuneCountInString("стр1 текст\n")
	p3 := utf8.RuneCountInString("стр1 текст\nстр2 текст\n")
	raw := mustJSON(t, []int{0, p2, p3})

	ds := parseDocStructure(map[string]string{"page_offsets": string(raw)}, text)
	if len(ds.pageRuneStarts) != 3 {
		t.Fatalf("expected 3 pages parsed, got %d", len(ds.pageRuneStarts))
	}
	for _, c := range []struct{ r, want int }{
		{0, 1}, {p2 - 1, 1}, {p2, 2}, {p3 - 1, 2}, {p3, 3}, {utf8.RuneCountInString(text) - 1, 3},
	} {
		if got := ds.pageOf(c.r); got != c.want {
			t.Fatalf("pageOf(%d) = %d, want %d", c.r, got, c.want)
		}
	}

	// Malformed page_offsets (must start at 0, strictly increase) → no structure.
	for _, bad := range []string{"[5,2,1]", "[1,2,3]", "[0,0,1]", "[0,5,3]", "not json"} {
		if ds := parseDocStructure(map[string]string{"page_offsets": bad}, text); len(ds.pageRuneStarts) != 0 || ds.pageOf(3) != 0 {
			t.Fatalf("invalid page_offsets %q should yield no structure", bad)
		}
	}
	if parseDocStructure(nil, text).pageOf(3) != 0 {
		t.Fatal("nil metadata → pageOf 0")
	}
}

func TestDocStructure_SectionAt(t *testing.T) {
	text := strings.Repeat("a", 100)
	raw := mustJSON(t, []map[string]any{
		{"rune": 0, "heading": "Введение"},
		{"rune": 40, "heading": "  Методы  "}, // trimmed on parse
		{"rune": 80, "heading": "Выводы"},
	})
	ds := parseDocStructure(map[string]string{"section_offsets": string(raw)}, text)
	for _, c := range []struct {
		r    int
		want string
	}{
		{0, "Введение"}, {39, "Введение"}, {40, "Методы"}, {79, "Методы"}, {80, "Выводы"}, {99, "Выводы"},
	} {
		if got := ds.sectionAt(c.r); got != c.want {
			t.Fatalf("sectionAt(%d) = %q, want %q", c.r, got, c.want)
		}
	}
	if parseDocStructure(nil, text).sectionAt(50) != "" {
		t.Fatal("no sections → sectionAt empty")
	}
}

// TestPageAwareWindowing_NoCrossPage replicates the indexer's page-aware window
// loop over a 3-page document and asserts (a) offsets still reproduce each chunk
// and (b) NO chunk spans two pages — the P2.2 page-aware-chunking guarantee.
func TestPageAwareWindowing_NoCrossPage(t *testing.T) {
	page1 := "Первая страница про буровые растворы и их плотность, достаточно длинная фраза для нескольких чанков на странице."
	page2 := "Вторая страница описывает коррозию насосно-компрессорных труб и методы защиты от сероводорода в скважинах."
	page3 := "Третья страница посвящена композитным материалам и температурной стойкости при высоких механических нагрузках."
	full := page1 + "\n" + page2 + "\n" + page3

	r2 := utf8.RuneCountInString(page1 + "\n")
	r3 := utf8.RuneCountInString(page1 + "\n" + page2 + "\n")
	raw := mustJSON(t, []int{0, r2, r3})
	ds := parseDocStructure(map[string]string{"page_offsets": string(raw)}, full)
	if len(ds.pageRuneStarts) != 3 {
		t.Fatalf("expected 3 pages, got %d", len(ds.pageRuneStarts))
	}

	const (
		window  = 120 // bytes — small, forces several windows inside each page
		overlap = 10
	)
	sp := splitter.NewRecursive(50, overlap)

	var carry string
	checked := 0
	for start := 0; start < len(full); {
		end := windowEnd(full, start, window)
		atPageBreak := false
		if pb := ds.nextPageByteAfter(start, len(full)); pb > start && pb <= end {
			end = pb
			atPageBreak = pb < len(full)
		}
		seg := carry + full[start:end]
		chunks := sp.Split(seg)
		offsets := splitter.LocateOffsets(seg, chunkTexts(chunks))
		baseRune := windowBaseRune(full, start, carry)

		for _, c := range chunks {
			cs, ce := absOffset(offsets, c.Index, baseRune)
			if cs == 0 && ce == 0 {
				continue
			}
			if got, want := normWS(sliceRunesAbs(full, cs, ce)), normWS(c.Text); got != want {
				t.Fatalf("page-aware windowing broke offsets: span %q != chunk %q", got, want)
			}
			if ps, pe := ds.pageOf(cs), ds.pageOf(ce-1); ps != pe {
				t.Fatalf("chunk spans pages %d..%d (char [%d,%d)): %q", ps, pe, cs, ce, c.Text)
			} else if ps < 1 || ps > 3 {
				t.Fatalf("page %d out of range for chunk %q", ps, c.Text)
			}
			checked++
		}
		if atPageBreak {
			carry = ""
		} else {
			carry = tailRunes(seg, overlap)
		}
		start = end
	}
	if checked == 0 {
		t.Fatal("no chunks verified")
	}
	t.Logf("verified %d chunks, every one within a single page", checked)
}

// mustJSON marshals v to JSON, failing the test on error. Shared by the
// application package tests that build metadata fixtures.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}
