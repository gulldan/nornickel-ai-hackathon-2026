package splitter

// Focused tests for the structure-aware pre-pass: a table-like block must stay
// whole in one chunk (so a "composition→temperature→property" row is never
// split), a short caption must ride along with its block, and a section heading
// must tag the chunks beneath it. They use a small chunk size so the table
// exceeds CHUNK_SIZE yet stays within the bounded overage — proving the block is
// kept whole DESPITE being over budget, not merely because it fits.

import (
	"strings"
	"testing"
)

// chunkHolding returns the index of the single chunk whose text contains every
// marker, or -1 when no chunk holds them all (i.e. they were split apart).
func chunkHolding(chunks []chunkText, markers ...string) int {
	for i, c := range chunks {
		all := true
		for _, m := range markers {
			if !strings.Contains(c.text, m) {
				all = false
				break
			}
		}
		if all {
			return i
		}
	}
	return -1
}

// chunkText flattens domain.Chunk to the two fields these tests assert on.
type chunkText struct {
	text    string
	section string
}

// split runs the splitter and projects to chunkText for terse assertions.
func split(size, overlap int, doc string) []chunkText {
	src := NewRecursive(size, overlap).Split(doc)
	out := make([]chunkText, 0, len(src))
	for _, c := range src {
		out = append(out, chunkText{text: c.Text, section: c.Section})
	}
	return out
}

func TestSplit_TableBlockStaysIntact(t *testing.T) {
	// Each row is one datum line; the recursive separators would split this run on
	// its "\n" boundaries without the table guard, scattering the rows.
	pipeTable := "Состав | Температура | Свойство\n" +
		"LiFePO4 | 800 C | 120 mAh/g\n" +
		"TiO2 | 950 C | 85 mAh/g\n" +
		"Al-Mg-Si | 530 C | 310 MPa\n"
	spaceTable := "Состав      Температура      Свойство\n" +
		"LiFePO4      800 C      120 mAh/g\n" +
		"TiO2      950 C      85 mAh/g\n" +
		"Al-Mg-Si      530 C      310 MPa\n"

	tests := []struct {
		name  string
		table string
	}{
		{name: "pipe-separated", table: pipeTable},
		{name: "multi-space columns", table: spaceTable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := "Введение в исследование катодных материалов и их характеристик весьма подробно.\n\n" +
				tc.table +
				"\nПосле таблицы следует обсуждение результатов и краткие выводы по работе автора."
			// size 80 < the table's length, so a non-guarded splitter WOULD break it;
			// the table still fits under the 3x overage cap, so it is kept whole.
			chunks := split(80, 12, doc)
			if got := len([]rune(tc.table)); got <= 80 {
				t.Fatalf("test table too short (%d runes) to exceed size 80", got)
			}
			idx := chunkHolding(chunks, "LiFePO4", "TiO2", "Al-Mg-Si")
			if idx < 0 {
				for i, c := range chunks {
					t.Logf("chunk %d: %q", i, c.text)
				}
				t.Fatal("table rows were split across chunks; expected all rows in one chunk")
			}
		})
	}
}

func TestSplit_CaptionStaysWithBlock(t *testing.T) {
	table := "LiFePO4 | 800 C | 120\nTiO2 | 950 C | 85\nAl-Mg-Si | 530 C | 310\n"
	tests := []struct {
		name    string
		doc     string
		caption string
	}{
		{
			name: "caption below table",
			doc: "Преамбула раздела с описанием эксперимента и условий синтеза образцов подробно.\n\n" +
				table +
				"Таблица 1 — Свойства образцов.\n\n" +
				"Текст после таблицы и подписи продолжает изложение материала статьи дальше тут.",
			caption: "Таблица 1",
		},
		{
			name: "caption above table",
			doc: "Преамбула раздела с описанием эксперимента и условий синтеза образцов подробно.\n\n" +
				"Таблица 2 — Состав образцов.\n" +
				table +
				"\nТекст после таблицы продолжает изложение материала статьи дальше по разделу.",
			caption: "Таблица 2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks := split(80, 12, tc.doc)
			// The caption must live in the SAME chunk as the table it labels, not
			// orphaned into a chunk of its own.
			if chunkHolding(chunks, tc.caption, "LiFePO4") < 0 {
				for i, c := range chunks {
					t.Logf("chunk %d: %q", i, c.text)
				}
				t.Fatalf("caption %q was separated from its table", tc.caption)
			}
		})
	}
}

func TestSplit_SectionHeadingTagsChunks(t *testing.T) {
	tests := []struct {
		name    string
		heading string
		want    string
	}{
		{name: "english methods", heading: "Methods", want: "Methods"},
		{name: "russian results numbered", heading: "3. Результаты", want: "Results"},
		{name: "english conclusion colon", heading: "Conclusion:", want: "Conclusion"},
		{name: "not a heading", heading: "Краткое введение в проблему", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.heading + "\n\n" +
				"Тело раздела с достаточным количеством слов чтобы получить несколько чанков точно.\n\n" +
				"Второй абзац раздела добавляет ещё немного текста для второго чанка наверняка здесь."
			chunks := split(60, 10, doc)
			if len(chunks) < 2 {
				t.Fatalf("expected multiple chunks, got %d", len(chunks))
			}
			// Body chunks (after the heading) must carry the normalised section.
			tagged := 0
			for _, c := range chunks {
				if c.section == tc.want {
					tagged++
				}
			}
			if tc.want == "" {
				if tagged != len(chunks) {
					t.Fatalf("non-heading must leave section empty on all chunks, got %d/%d empty", tagged, len(chunks))
				}
				return
			}
			if tagged == 0 {
				t.Fatalf("no chunk tagged with section %q", tc.want)
			}
		})
	}
}
