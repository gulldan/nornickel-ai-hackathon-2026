package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/example/pdf-parser/internal/domain"
)

// ExtractedDocument and SectionOffset are re-exported from the domain package so
// callers within this package can use the short names while the contract lives
// with the ports. ParseTikaXHTML and the Tika extractor produce domain values.
type (
	// ExtractedDocument aliases domain.ExtractedDocument.
	ExtractedDocument = domain.ExtractedDocument
	// SectionOffset aliases domain.SectionOffset.
	SectionOffset = domain.SectionOffset
)

// TikaExtractor extracts a PDF's text layer via an Apache Tika server. Tika
// (PDFBox under the hood) reconstructs inter-word spacing from glyph positions,
// so LaTeX/pdfTeX PDFs (arXiv & co.) come out as real words instead of run-on
// text — unlike the pure-Go ledongthuc reader, whose GetPlainText concatenates
// glyphs without spaces. It is the primary extractor; ledongthuc stays as an
// offline fallback for when the Tika sidecar is unreachable (FallbackExtractor).
//
// For layout-aware extraction it requests Tika's XHTML output (Accept:
// text/html), where the PDF parser wraps every page in <div class="page">…</div>;
// concatenating those page texts with a single "\n" yields both the emitted text
// and the per-page rune offsets in one parse.
type TikaExtractor struct {
	endpoint string
	client   *http.Client
}

// NewTikaExtractor builds a TikaExtractor for the Tika base URL (e.g.
// http://tika:9998). The /tika endpoint returns the extracted body; we ask it
// for XHTML so page boundaries survive.
func NewTikaExtractor(baseURL string) *TikaExtractor {
	return &TikaExtractor{
		endpoint: strings.TrimRight(baseURL, "/") + "/tika",
		// Big scanned-but-tagged PDFs can take a while; keep a generous ceiling.
		client: &http.Client{Timeout: 180 * time.Second},
	}
}

// Extract implements domain.TextExtractor: it returns just the plain text. An
// empty result (no error) means no text layer — the caller routes those to OCR.
// It delegates to ExtractWithLayout so there is a single Tika round-trip and the
// text matches what the layout path computes offsets against.
func (t *TikaExtractor) Extract(ctx context.Context, data []byte) (string, error) {
	doc, err := t.ExtractWithLayout(ctx, data)
	if err != nil {
		return "", err
	}
	return doc.Text, nil
}

// ExtractWithLayout PUTs the PDF to Tika asking for XHTML, then parses the
// per-page <div class="page"> elements into text + page offsets. If the XHTML
// yields no page divs (an unusual document, or a non-PDF that Tika rendered
// differently) it falls back to Tika's plain-text endpoint and returns the text
// with PageOffsets == nil, so the page metadata is omitted but extraction still
// works. An empty Text with a nil error still means "no text layer" → OCR.
func (t *TikaExtractor) ExtractWithLayout(ctx context.Context, data []byte) (ExtractedDocument, error) {
	body, err := t.do(ctx, data, "text/html")
	if err != nil {
		return ExtractedDocument{}, err
	}

	doc, err := ParseTikaXHTML(bytes.NewReader(body))
	if err != nil {
		return ExtractedDocument{}, fmt.Errorf("parse tika xhtml: %w", err)
	}
	if len(doc.PageOffsets) > 0 {
		return doc, nil
	}

	// No page divs: keep current behaviour by reading Tika's plain text and
	// emitting it without page offsets.
	plain, err := t.do(ctx, data, "text/plain")
	if err != nil {
		return ExtractedDocument{}, err
	}
	return ExtractedDocument{Text: string(plain), Author: doc.Author, PublishedAt: doc.PublishedAt}, nil
}

// do PUTs the bytes to Tika with the requested Accept type and returns the body.
func (t *TikaExtractor) do(ctx context.Context, data []byte, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, t.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build tika request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Content-Type", "application/pdf")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call tika: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read tika response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := body
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("tika status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return body, nil
}

// ParseTikaXHTML parses Tika's XHTML output and reconstructs the document text
// from its per-page <div class="page"> elements, recording the running RUNE
// offset at which each page's text begins. Pages are joined with a single "\n".
//
// The returned PageOffsets satisfy the chunk-splitter contract: element 0 is 0,
// the slice is strictly increasing, len is the page count, and page N (1-based)
// covers runes [PageOffsets[N-1], PageOffsets[N]) with the last page running to
// end of text. When no page divs are present PageOffsets is nil (so callers omit
// the key); Text is still returned best-effort from the whole body.
//
// Section offsets are populated from semantic <h1>..<h3> headings when Tika
// emits them, and — because PDFBox almost never does — ALSO from a heuristic
// applied to each <p>'s text (numbered/keyword-prefixed/short-standalone, see
// detectHeading). Each detected heading records the rune offset at which that
// paragraph's text begins in the emitted text. When nothing is detected,
// SectionOffsets stays nil and the section_offsets metadata key is omitted.
func ParseTikaXHTML(r io.Reader) (ExtractedDocument, error) {
	root, err := html.Parse(r)
	if err != nil {
		return ExtractedDocument{}, fmt.Errorf("parse tika xhtml: %w", err)
	}

	var p xhtmlParser
	p.findPages(root)
	author, created := headDocinfo(root)

	if len(p.offsets) == 0 {
		// No page structure: return the body text so the plain-text contract is
		// preserved, but leave PageOffsets nil so the caller omits the key.
		return ExtractedDocument{Text: collapseBody(root), Author: author, PublishedAt: created}, nil
	}

	doc := ExtractedDocument{
		Text: p.buf.String(), PageOffsets: p.offsets, Author: author, PublishedAt: created,
	}
	if len(p.sections) > 0 {
		doc.SectionOffsets = p.sections
	}
	return doc, nil
}

// headDocinfo pulls the author and creation date out of the <meta> tags Tika
// emits in the XHTML <head>: pdf:docinfo:creator / dc:creator for the author,
// dcterms:created / pdf:docinfo:created for the date (values kept verbatim).
func headDocinfo(root *html.Node) (author, created string) {
	metas := map[string]string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Meta {
			var name, content string
			for _, a := range n.Attr {
				switch a.Key {
				case "name":
					name = a.Val
				case "content":
					content = a.Val
				}
			}
			if name != "" && content != "" {
				metas[name] = content
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	author = firstOf(metas, "pdf:docinfo:creator", "dc:creator")
	created = firstOf(metas, "dcterms:created", "pdf:docinfo:created")
	return author, created
}

// firstOf returns the first non-empty value of the given keys.
func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// xhtmlParser accumulates the reconstructed text, the per-page rune offsets and
// the detected section offsets while walking Tika's XHTML tree. Splitting the
// walk into methods keeps ParseTikaXHTML's cognitive complexity low without
// changing behaviour.
type xhtmlParser struct {
	buf      strings.Builder
	offsets  []int
	sections []SectionOffset
	runeLen  int // running rune length of buf
}

// startPage appends the page separator for every page after the first and
// records the start offset of the page that follows.
func (p *xhtmlParser) startPage() {
	if len(p.offsets) > 0 {
		p.buf.WriteByte('\n')
		p.runeLen++ // '\n' is one rune
	}
	p.offsets = append(p.offsets, p.runeLen)
}

// findPages descends to each <div class="page"> and walks its content. Pages do
// not nest, so a matched div's subtree is walked but not re-scanned for pages.
func (p *xhtmlParser) findPages(n *html.Node) {
	if n.Type == html.ElementNode && n.DataAtom == atom.Div && hasClass(n, "page") {
		p.startPage()
		p.walkChildren(n, p.walkPage)
		return
	}
	p.walkChildren(n, p.findPages)
}

// walkPage emits text nodes and records headings, then recurses into children.
func (p *xhtmlParser) walkPage(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		p.buf.WriteString(n.Data)
		p.runeLen += utf8.RuneCountInString(n.Data)
	case html.ElementNode:
		if p.recordHeading(n) {
			return // a <p> heading walks its own children
		}
	default:
		// Comments, doctype, document nodes: nothing to emit, fall through.
	}
	p.walkChildren(n, p.walkPage)
}

// recordHeading records a section offset for n when it is a heading. For a
// semantic <h1>..<h3> it trusts Tika verbatim. For a <p> it walks the paragraph
// first (advancing the running offset) and then applies the heuristic; it
// reports true in that case so the caller skips re-walking the children.
func (p *xhtmlParser) recordHeading(n *html.Node) bool {
	switch {
	case isHeadingNode(n):
		// Tika emitted a semantic heading: trust it verbatim.
		p.sections = append(p.sections, SectionOffset{Rune: p.runeLen, Heading: collapse(textOf(n))})
		return false
	case n.DataAtom == atom.P:
		// PDFBox emits no <h1> for ordinary PDFs, so a heading hides in a <p>.
		// Snapshot the rune offset where this paragraph begins, walk it (which
		// appends the text + advances runeLen), then test the collapsed text
		// heuristically. The recorded offset skips any leading whitespace so
		// runes[offset:] starts at the heading.
		pStart := p.runeLen + leadingSpaceRunes(textOf(n))
		p.walkChildren(n, p.walkPage)
		if label := collapse(textOf(n)); detectHeading(label) {
			p.sections = append(p.sections, SectionOffset{Rune: pStart, Heading: label})
		}
		return true
	default:
		return false
	}
}

// walkChildren applies fn to each child of n in order.
func (p *xhtmlParser) walkChildren(n *html.Node, fn func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		fn(c)
	}
}

// isHeadingNode reports whether n is an <h1>, <h2> or <h3> element.
func isHeadingNode(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3:
		return true
	default:
		return false
	}
}

// Heading heuristics, tuned for RU + EN + ZH scientific/technical/legal documents.
//
//   - maxHeadingRunes caps any heading; real section titles are short.
//   - numberedHeadingRe matches a section number like "1", "2.3.1" (up to five
//     dotted parts), an optional trailing dot, then whitespace and a letter —
//     so "1 Введение" and "2.3.1 Метод расчёта" qualify but "3.14 is pi" does
//     not start a numbered title that survives the length/sentence checks.
//   - keywordHeadingRe matches a leading structural keyword (RU + EN + ZH, plus
//     the section sign §) at a word boundary, e.g. "Глава 2", "Appendix A", "引言".
const (
	maxHeadingRunes      = 140
	maxStandaloneRunes   = 80
	maxStandaloneWords   = 12
	minUppercaseFraction = 0.6
)

var (
	numberedHeadingRe = regexp.MustCompile(`^\s*\d+(\.\d+){0,4}\.?\s+\p{L}`)
	// The keyword must end at a Unicode word boundary. Go's RE2 \b is ASCII-only,
	// so it never fires after a Cyrillic letter ("Глава "→no match); instead we
	// require the keyword be followed by a non-letter or end-of-string. § is a
	// symbol, so any following rune (incl. a space, as in "§ 12") is fine.
	// Chinese section names (摘要/引言/…) are matched the same way: CJK has no
	// case and headings carry no spaces; numbered forms like "1. 引言" are
	// already caught by the NUMBERED rule.
	keywordHeadingRe = regexp.MustCompile(`(?i)^\s*(` +
		`глава|раздел|часть|приложение|введение|заключение|аннотация|реферат|` +
		`список литературы|обозначения|chapter|section|part|appendix|` +
		`introduction|conclusion|abstract|references|` +
		`摘要|引言|绪论|实验方法|方法|实验|结果|讨论|结论|参考文献|致谢|§` +
		`)(?:[^\p{L}]|$)`)
	// numberPrefixRe captures just the leading section number (with its dots and
	// optional trailing dot) so the sentence-shape guard can ignore it when
	// hunting for an interior sentence period.
	numberPrefixRe = regexp.MustCompile(`^\s*\d+(\.\d+){0,4}\.?\s+`)
	// captionLikeRe vetoes figure/table/equation captions — a caption word, up to
	// three separators (". ", " — ", " №"), then a number. These are short and
	// often Title-Case, so they would otherwise pass rule 3 and pollute provenance
	// (the single most common short Title-Case line in sci/tech PDFs). RE2-safe:
	// uses [^\p{L}\d] (not ASCII \W/\b) so it works after Cyrillic.
	captionLikeRe = regexp.MustCompile(`(?i)^\s*(` +
		`рис|рисунок|рисунке|рисунка|табл|таблица|таблице|таблицы|фиг|фигура|` +
		`схема|формула|уравнение|диаграмма|fig|figure|table|eq|equation|` +
		`scheme|chart|diagram|plate` +
		`)[^\p{L}\d]{0,3}\d`)
	// yearRe matches a standalone calendar year (1900-2099): a short line carrying
	// one is a date / title-page / reference line, not a section heading.
	yearRe = regexp.MustCompile(`\b(19|20)\d\d\b`)
)

// detectHeading reports whether a single paragraph reads like a section
// heading. It is deliberately conservative: a wrong section_heading pollutes a
// chunk's provenance, so when in doubt it returns false (miss a heading rather
// than invent one). A trimmed paragraph is a heading if ANY rule fires:
//
//  1. NUMBERED: "1 Введение", "2.3.1 Метод расчёта" — a section number then a
//     letter, length ≤ 140. A lone trailing dot after the number is fine; a
//     full sentence is rejected by the length cap and the sentence-shape guard.
//  2. KEYWORD-PREFIXED: starts (case-insensitively) with a structural keyword
//     (глава/раздел/…/chapter/section/…/§) at a word boundary, length ≤ 140.
//  3. SHORT STANDALONE: ≤ 80 runes and ≤ 12 words, does not end in sentence
//     punctuation, has no clause comma/semicolon, and is either mostly uppercase
//     (≥60% of letters — a single ALL-CAPS word qualifies) or Title Case with at
//     least two letter-words (so a lone capitalised word — an inflected form or a
//     proper noun like "Москва" — is not mistaken for a heading).
//
// Captions ("Рис. 3", "Таблица 2 — …", "Figure 1") and lines carrying a calendar
// year (dates, title pages) are vetoed up front: they are never section headings
// yet are short and Title-Case enough to slip past rule 3.
func detectHeading(paragraph string) bool {
	p := strings.TrimSpace(paragraph)
	if p == "" {
		return false
	}
	n := utf8.RuneCountInString(p)

	// Up-front vetoes: a figure/table caption or a year-bearing line is never a
	// heading regardless of which rule would otherwise fire.
	if captionLikeRe.MatchString(p) || yearRe.MatchString(p) {
		return false
	}

	// Rule 1: numbered. The title after the number must start with an uppercase
	// letter (section titles capitalise; the date "12 марта 2024" has a lowercase
	// month), and the line must not be sentence-shaped (an interior sentence
	// period followed by more words, or a clause comma).
	if n <= maxHeadingRunes && numberedHeadingRe.MatchString(p) &&
		startsUpperAfterNumber(p) && !looksLikeSentence(p) {
		return true
	}

	// Rule 2: structural keyword prefix, not ending like a sentence (so "Глава 2"
	// is a heading but "Введение описывает методику." is not).
	if n <= maxHeadingRunes && keywordHeadingRe.MatchString(p) && !endsWithSentencePunct(p) {
		return true
	}

	// Rule 3: short standalone line that is not a sentence and has no clause comma.
	if n <= maxStandaloneRunes && wordCount(p) <= maxStandaloneWords &&
		!endsWithSentencePunct(p) && !strings.ContainsAny(p, ",;") {
		if mostlyUppercase(p) {
			return true // ALL-CAPS, a single word is fine
		}
		if titleCase(p) && letterWords(p) >= 2 {
			return true // Title Case needs ≥2 letter-words
		}
	}

	return false
}

// startsUpperAfterNumber reports whether, after the leading section number, the
// title begins with an uppercase letter — true for "1 Введение"/"2.3 Метод",
// false for a date like "12 марта 2024" (lowercase month). Caseless scripts
// (CJK: "1. 引言") have no uppercase, so there any letter qualifies — hence
// "letter and not lowercase" rather than IsUpper.
func startsUpperAfterNumber(p string) bool {
	body := numberPrefixRe.ReplaceAllString(p, "")
	r, _ := utf8.DecodeRuneInString(body)
	return unicode.IsLetter(r) && !unicode.IsLower(r)
}

// letterWords counts whitespace-separated words whose first rune is a letter
// (digit-/symbol-leading tokens like "2" or "—" do not count).
func letterWords(p string) int {
	n := 0
	for _, w := range strings.Fields(p) {
		if r, _ := utf8.DecodeRuneInString(w); unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// looksLikeSentence reports whether p reads like running prose rather than a
// terse title: it ends in sentence punctuation while being long or multi-clause,
// or it contains a sentence-ending period that is followed by further words.
// Used only to veto rule-1 (numbered) false positives.
func looksLikeSentence(p string) bool {
	// Paragraph-length numbered lines read as prose even without punctuation: a
	// real numbered title is short. Both long AND wordy ⇒ treat as a sentence,
	// so "2.3.1 Метод расчёта" (3 words) stays a heading while a 13-plus-word
	// numbered line does not.
	if utf8.RuneCountInString(p) > maxStandaloneRunes && wordCount(p) > maxStandaloneWords {
		return true
	}
	// A trailing sentence mark on a short numbered title ("1. Введение") is the
	// allowed case; a comma still signals a clause to reject.
	if endsWithSentencePunct(p) {
		// A comma anywhere signals a clause: "1. Метод, основанный на …".
		if strings.ContainsRune(p, ',') {
			return true
		}
	}
	// An interior period followed by more words ("…точкой. Ещё…") is prose. Strip
	// the leading section number first so a dotted number like "2.3.1" is not
	// mistaken for a sentence boundary.
	body := numberPrefixRe.ReplaceAllString(p, "")
	if i := strings.IndexByte(body, '.'); i >= 0 && i+1 < len(body) {
		rest := strings.TrimSpace(body[i+1:])
		if strings.ContainsRune(rest, ' ') && wordCount(rest) >= 3 {
			return true
		}
	}
	return false
}

// endsWithSentencePunct reports whether p's last rune is sentence punctuation
// (ASCII or its fullwidth CJK counterpart).
func endsWithSentencePunct(p string) bool {
	r, _ := utf8.DecodeLastRuneInString(p)
	switch r {
	case '.', '!', '?', ',', ';', ':', '。', '！', '？', '，', '；', '：':
		return true
	default:
		return false
	}
}

// wordCount counts whitespace-separated tokens in p.
func wordCount(p string) int { return len(strings.Fields(p)) }

// mostlyUppercase reports whether at least minUppercaseFraction of the letters
// in p are uppercase (and p has at least one letter). ALL-CAPS titles like
// "ВВЕДЕНИЕ" or "METHODS" pass; mixed prose does not.
func mostlyUppercase(p string) bool {
	var letters, upper int
	for _, r := range p {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	if letters == 0 {
		return false
	}
	return float64(upper)/float64(letters) >= minUppercaseFraction
}

// titleCase reports whether most alphabetic words in p start with an uppercase
// letter (Title Case), e.g. "Метод Расчёта" or "Materials And Methods". Words
// that begin with a non-letter (digits, "§") are ignored for the ratio.
func titleCase(p string) bool {
	var words, capped int
	for _, w := range strings.Fields(p) {
		r, _ := utf8.DecodeRuneInString(w)
		if !unicode.IsLetter(r) {
			continue
		}
		words++
		if unicode.IsUpper(r) {
			capped++
		}
	}
	if words == 0 {
		return false
	}
	// "most words start uppercase": strictly more than half.
	return capped*2 > words
}

// leadingSpaceRunes returns the count of leading Unicode-space runes in s, so a
// recorded heading offset can be advanced past a paragraph's leading whitespace
// to point exactly at the first visible character.
func leadingSpaceRunes(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			break
		}
		n++
	}
	return n
}

// hasClass reports whether n has the given class token in its class attribute.
func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, tok := range strings.Fields(a.Val) {
			if tok == want {
				return true
			}
		}
	}
	return false
}

// textOf returns the concatenated text content of n's subtree.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// collapse trims and collapses internal whitespace runs to single spaces — used
// for heading labels, which should be short and clean.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// collapseBody returns the <body> text (or whole tree) as a single string,
// used only on the no-page fallback path.
func collapseBody(root *html.Node) string {
	var body *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if body != nil {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Body {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(root)
	if body == nil {
		body = root
	}
	return strings.TrimSpace(textOf(body))
}

// FallbackExtractor tries primary first and, only when primary returns a
// transport/parse error, falls back to secondary. An empty (no-error) primary
// result is honoured as-is — for PDFBox that means "no text layer" (a scan),
// which must reach OCR rather than the secondary's degraded reading.
type FallbackExtractor struct {
	primary   domain.TextExtractor
	secondary domain.TextExtractor
	log       func(error)
}

// NewFallbackExtractor composes primary over secondary. onErr (optional) is
// called when primary fails and the fallback is used.
func NewFallbackExtractor(primary, secondary domain.TextExtractor, onErr func(error)) *FallbackExtractor {
	return &FallbackExtractor{primary: primary, secondary: secondary, log: onErr}
}

// Extract implements domain.TextExtractor.
func (f *FallbackExtractor) Extract(ctx context.Context, data []byte) (string, error) {
	text, err := f.primary.Extract(ctx, data)
	if err != nil {
		if f.log != nil {
			f.log(err)
		}
		return f.secondary.Extract(ctx, data)
	}
	return text, nil
}

// ExtractWithLayout implements LayoutExtractor by forwarding to the primary's
// layout capability when it has one. If the primary fails, it falls back to the
// secondary's plain text with no page offsets (PageOffsets stays nil, so the
// page metadata is omitted). If the primary cannot provide layout at all, it
// likewise returns just the primary's text.
func (f *FallbackExtractor) ExtractWithLayout(ctx context.Context, data []byte) (ExtractedDocument, error) {
	if lp, ok := f.primary.(domain.LayoutExtractor); ok {
		doc, err := lp.ExtractWithLayout(ctx, data)
		if err == nil {
			return doc, nil
		}
		if f.log != nil {
			f.log(err)
		}
		text, serr := f.secondary.Extract(ctx, data)
		if serr != nil {
			return ExtractedDocument{}, serr
		}
		return ExtractedDocument{Text: text}, nil
	}

	text, err := f.primary.Extract(ctx, data)
	if err != nil {
		if f.log != nil {
			f.log(err)
		}
		text, err = f.secondary.Extract(ctx, data)
		if err != nil {
			return ExtractedDocument{}, err
		}
	}
	return ExtractedDocument{Text: text}, nil
}
