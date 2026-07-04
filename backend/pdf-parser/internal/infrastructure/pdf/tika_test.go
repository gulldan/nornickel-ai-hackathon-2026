package pdf

import (
	"strings"
	"testing"
)

// sampleTikaXHTML mirrors what Apache Tika (PDFBox) emits for a multi-page PDF:
// a <head> with metadata, then one <div class="page"> per page, each holding
// <p> paragraphs. The content is Cyrillic so the test exercises rune (not byte)
// offsets — a UTF-8 Cyrillic letter is two bytes but one rune.
const sampleTikaXHTML = `<html xmlns="http://www.w3.org/1999/xhtml">
<head>
<meta name="dc:title" content="Отчёт"/>
<meta name="pdf:PDFVersion" content="1.7"/>
<title>Отчёт</title>
</head>
<body><div class="page"><p>Первая страница отчёта.</p>
<p>Второй абзац первой страницы.</p>
</div>
<div class="page"><p>Вторая страница.</p>
</div>
<div class="page"><p>Третья и последняя страница документа.</p>
</div>
</body></html>`

func TestParseTikaXHTMLPages(t *testing.T) {
	doc, err := ParseTikaXHTML(strings.NewReader(sampleTikaXHTML))
	if err != nil {
		t.Fatalf("ParseTikaXHTML: %v", err)
	}

	// Three <div class="page"> -> three pages.
	if got := len(doc.PageOffsets); got != 3 {
		t.Fatalf("page count = %d, want 3 (offsets=%v)", got, doc.PageOffsets)
	}

	// Contract: element 0 is 0, strictly increasing.
	if doc.PageOffsets[0] != 0 {
		t.Errorf("PageOffsets[0] = %d, want 0", doc.PageOffsets[0])
	}
	for i := 1; i < len(doc.PageOffsets); i++ {
		if doc.PageOffsets[i] <= doc.PageOffsets[i-1] {
			t.Errorf("PageOffsets not strictly increasing at %d: %v", i, doc.PageOffsets)
		}
	}

	// Offsets are rune indices into doc.Text. Slice the text at each page's
	// [start,end) range and confirm the expected page text is there.
	runes := []rune(doc.Text)
	if last := doc.PageOffsets[len(doc.PageOffsets)-1]; last > len(runes) {
		t.Fatalf("last offset %d exceeds text length %d runes", last, len(runes))
	}

	wantContains := []string{
		"Первая страница отчёта.",                // page 1
		"Вторая страница.",                       // page 2
		"Третья и последняя страница документа.", // page 3
	}
	for i := range doc.PageOffsets {
		start := doc.PageOffsets[i]
		end := len(runes)
		if i+1 < len(doc.PageOffsets) {
			end = doc.PageOffsets[i+1]
		}
		pageText := string(runes[start:end])
		if !strings.Contains(pageText, wantContains[i]) {
			t.Errorf("page %d text %q does not contain %q", i+1, pageText, wantContains[i])
		}
		// Page i's marker must NOT bleed into the next/previous page slice.
		for j := range wantContains {
			if j != i && strings.Contains(pageText, wantContains[j]) {
				t.Errorf("page %d text %q unexpectedly contains page %d marker %q", i+1, pageText, j+1, wantContains[j])
			}
		}
	}

	// The first paragraph starts at the very beginning of the text (offset 0).
	if !strings.HasPrefix(doc.Text, "Первая страница отчёта.") {
		t.Errorf("text does not start at page 1 content: %q", firstRunes(doc.Text, 30))
	}

	// Whole-text reconstruction must contain every page, in order.
	idx := -1
	for _, marker := range wantContains {
		at := strings.Index(doc.Text, marker)
		if at <= idx {
			t.Errorf("marker %q out of order (at=%d, prev=%d)", marker, at, idx)
		}
		idx = at
	}

	// PDFs typically carry no <h1>..<h3>, so section offsets stay nil here.
	if doc.SectionOffsets != nil {
		t.Errorf("SectionOffsets = %v, want nil for headingless PDF XHTML", doc.SectionOffsets)
	}
}

// TestParseTikaXHTMLNoPages asserts the fallback contract: XHTML without any
// page divs yields text but a nil PageOffsets (so the caller omits the key).
func TestParseTikaXHTMLNoPages(t *testing.T) {
	const noPages = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>x</title></head>` +
		`<body><p>Просто текст без разбиения на страницы.</p></body></html>`

	doc, err := ParseTikaXHTML(strings.NewReader(noPages))
	if err != nil {
		t.Fatalf("ParseTikaXHTML: %v", err)
	}
	if doc.PageOffsets != nil {
		t.Errorf("PageOffsets = %v, want nil when no page divs", doc.PageOffsets)
	}
	if !strings.Contains(doc.Text, "Просто текст без разбиения") {
		t.Errorf("fallback text missing body content: %q", doc.Text)
	}
}

// TestParseTikaXHTMLSections checks that when Tika DOES emit headings (some
// converters do), they surface as section offsets pointing at the heading's
// start rune within the reconstructed text.
func TestParseTikaXHTMLSections(t *testing.T) {
	const withHeads = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>x</title></head>` +
		`<body><div class="page"><h1>Введение</h1><p>Текст раздела.</p></div>` +
		`<div class="page"><h2>Методы</h2><p>Описание методов.</p></div></body></html>`

	doc, err := ParseTikaXHTML(strings.NewReader(withHeads))
	if err != nil {
		t.Fatalf("ParseTikaXHTML: %v", err)
	}
	if len(doc.SectionOffsets) != 2 {
		t.Fatalf("section count = %d, want 2 (%v)", len(doc.SectionOffsets), doc.SectionOffsets)
	}
	runes := []rune(doc.Text)
	for _, sec := range doc.SectionOffsets {
		if sec.Rune < 0 || sec.Rune > len(runes) {
			t.Fatalf("section rune %d out of range [0,%d]", sec.Rune, len(runes))
		}
		// The heading text appears starting at its recorded rune offset.
		if !strings.HasPrefix(string(runes[sec.Rune:]), sec.Heading) {
			t.Errorf("text at rune %d does not start with heading %q: %q",
				sec.Rune, sec.Heading, firstRunes(string(runes[sec.Rune:]), 20))
		}
	}
	if doc.SectionOffsets[0].Heading != "Введение" || doc.SectionOffsets[1].Heading != "Методы" {
		t.Errorf("unexpected headings: %v", doc.SectionOffsets)
	}
}

// TestDetectHeading table-tests the heuristic on RU + EN cases: numbered,
// keyword-prefixed, ALL-CAPS short, and Title-Case short lines are headings;
// body sentences, long paragraphs, and short sentences ending in punctuation
// are not. Conservatism (prefer a miss over a false heading) is the priority.
func TestDetectHeading(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Rule 1 — numbered.
		{"numbered ru", "1 Введение", true},
		{"numbered dotted ru", "2.3.1 Метод расчёта", true},
		{"numbered trailing dot", "1. Введение", true},
		{"numbered en", "3 Results", true},
		{"numbered deep", "4.5.6.7 Граничные условия", true},
		{"numbered but sentence", "1. Это длинное предложение, которое заканчивается точкой.", false},
		{"numbered long no punct flows like prose",
			"5 В этой части мы подробно рассматриваем все возможные источники " +
				"систематической ошибки измерения", false},

		// Rule 2 — keyword prefix (case-insensitive).
		{"keyword glava", "Глава 2. Теоретические основы", true},
		{"keyword razdel", "Раздел 1", true},
		{"keyword prilozhenie", "Приложение А", true},
		{"keyword vvedenie", "Введение", true},
		{"keyword zaklyuchenie", "Заключение", true},
		{"keyword spisok", "Список литературы", true},
		{"keyword chapter", "Chapter 4", true},
		{"keyword appendix lower", "appendix b", true},
		{"keyword references", "References", true},
		{"keyword section sign", "§ 12 Ответственность сторон", true},
		{"keyword as substring not prefix", "В разделе ниже мы покажем, что метод сходится.", false},

		// Rule 3 — short standalone (ALL-CAPS or Title Case, no end punctuation).
		{"allcaps ru", "ВВЕДЕНИЕ", true},
		{"allcaps en", "MATERIALS AND METHODS", true},
		{"title case ru", "Метод Конечных Элементов", true},
		{"title case en", "Results And Discussion", true},
		{"single capitalized word", "Аннотация", true},

		// Negatives — body / prose / sentence-shaped.
		{"body sentence ru", "Первая страница отчёта.", false},
		{"body sentence en", "This is a normal sentence that ends with a period.", false},
		{"short sentence with period", "Это так.", false},
		{"lowercase short phrase", "далее по тексту", false},
		{"ends with comma", "во-первых,", false},
		{"ends with colon", "Таблица параметров:", false},
		{"long title-case paragraph over 80 runes",
			"Очень Длинный Заголовок Который На Самом Деле Является Абзацем И " +
				"Превышает Восемьдесят Рун Поэтому Не Считается", false},
		{"many words not heading", "Один Два Три Четыре Пять Шесть Семь Восемь Девять Десять Одиннадцать Двенадцать Тринадцать", false},
		{"sentence-case prose no punct", "это обычный текст без заглавных букв и без точки", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},

		// FP guards (adversarial probes): captions, dates, clause/comma lines and
		// lone capitalised words must NOT be flagged — a wrong section heading
		// pollutes provenance, so these are the priority cases.
		{"caption ris", "Рис. 3", false},
		{"caption risunok dash", "Рисунок 3 — Схема установки", false},
		{"caption tablica", "Таблица 2", false},
		{"caption tablica dash", "Таблица 2 — Результаты измерений", false},
		{"caption figure", "Figure 3", false},
		{"caption fig dot", "Fig. 3", false},
		{"caption table en", "Table 2", false},
		{"date ru lowercase month", "12 марта 2024 года", false},
		{"place and year", "Москва, 2024", false},
		{"place and country comma", "Москва, Россия", false},
		{"author list comma", "Иванов, Петров и Сидоров", false},
		{"clause comma title", "Во-первых, Метод", false},
		{"single inflected word vvedeniem", "Введением", false},
		{"single inflected word razdele", "Разделе", false},
		{"lone proper noun", "Москва", false},

		// Regression: real headings still detected after the guards above.
		{"numbered upper still heading", "2 Методология", true},
		{"allcaps single still heading", "РЕЗУЛЬТАТЫ", true},
		{"title case two words still heading", "Граничные Условия", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectHeading(c.in); got != c.want {
				t.Errorf("detectHeading(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestParseTikaXHTMLHeuristicSections feeds Tika-style XHTML (only <p> tags, no
// semantic <h1>) that mixes heading paragraphs with body paragraphs across two
// pages, and asserts the heuristic surfaces exactly the headings at the rune
// offset where each heading's text begins, with body paragraphs absent.
func TestParseTikaXHTMLHeuristicSections(t *testing.T) {
	const xhtml = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>x</title></head>` +
		`<body>` +
		`<div class="page">` +
		`<p>1 Введение</p>` +
		`<p>Это первый абзац основного текста, он заканчивается точкой.</p>` +
		`<p>ВВЕДЕНИЕ В ПРЕДМЕТ</p>` +
		`<p>Ещё один обычный абзац с нормальным предложением.</p>` +
		`</div>` +
		`<div class="page">` +
		`<p>2.1 Метод расчёта</p>` +
		`<p>Здесь описывается методика, применённая в работе.</p>` +
		`<p>Глава 3</p>` +
		`</div>` +
		`</body></html>`

	doc, err := ParseTikaXHTML(strings.NewReader(xhtml))
	if err != nil {
		t.Fatalf("ParseTikaXHTML: %v", err)
	}

	wantHeadings := []string{"1 Введение", "ВВЕДЕНИЕ В ПРЕДМЕТ", "2.1 Метод расчёта", "Глава 3"}
	if len(doc.SectionOffsets) != len(wantHeadings) {
		t.Fatalf("section count = %d, want %d (%v)", len(doc.SectionOffsets), len(wantHeadings), doc.SectionOffsets)
	}

	runes := []rune(doc.Text)
	var prev = -1
	for i, sec := range doc.SectionOffsets {
		// Heading text matches in document order.
		if sec.Heading != wantHeadings[i] {
			t.Errorf("section %d heading = %q, want %q", i, sec.Heading, wantHeadings[i])
		}
		// Offsets are ascending.
		if sec.Rune <= prev {
			t.Errorf("section %d rune %d not ascending after %d", i, sec.Rune, prev)
		}
		prev = sec.Rune
		// The recorded rune offset is exactly where the heading text begins.
		if sec.Rune < 0 || sec.Rune > len(runes) {
			t.Fatalf("section %d rune %d out of range [0,%d]", i, sec.Rune, len(runes))
		}
		if !strings.HasPrefix(string(runes[sec.Rune:]), sec.Heading) {
			t.Errorf("text at rune %d does not start with heading %q: %q",
				sec.Rune, sec.Heading, firstRunes(string(runes[sec.Rune:]), len([]rune(sec.Heading))+5))
		}
	}

	// Body paragraphs must not appear as headings.
	for _, sec := range doc.SectionOffsets {
		for _, body := range []string{
			"Это первый абзац основного текста",
			"Ещё один обычный абзац",
			"Здесь описывается методика",
		} {
			if strings.Contains(sec.Heading, body) {
				t.Errorf("body paragraph leaked into heading: %q", sec.Heading)
			}
		}
	}
}

// TestParseTikaXHTMLHeuristicNone confirms a page of pure body prose yields no
// section offsets (key omitted), preserving prior behaviour for plain documents.
func TestParseTikaXHTMLHeuristicNone(t *testing.T) {
	const xhtml = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>x</title></head>` +
		`<body><div class="page">` +
		`<p>Этот документ состоит только из обычных предложений.</p>` +
		`<p>Ни один абзац не похож на заголовок раздела.</p>` +
		`</div></body></html>`

	doc, err := ParseTikaXHTML(strings.NewReader(xhtml))
	if err != nil {
		t.Fatalf("ParseTikaXHTML: %v", err)
	}
	if doc.SectionOffsets != nil {
		t.Errorf("SectionOffsets = %v, want nil for body-only document", doc.SectionOffsets)
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
