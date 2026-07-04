package splitter

import (
	"strings"
	"unicode"
)

// Structure-aware pre-pass for the recursive splitter. Materials-science papers
// encode their core data as tables and condition-lists ("composition →
// temperature → property" rows); the plain recursive separators ("\n\n", "\n")
// happily cut such a block mid-row, scattering one datum across two chunks. This
// file groups the text into BLOCKS before recursive splitting so that:
//   - a table-like block (a run of lines that look tabular) is kept WHOLE in one
//     chunk even if it slightly exceeds the size budget (bounded overage),
//   - a short caption line ("Рис.", "Таблица", "Figure", "Table"...) stays
//     attached to the block it annotates,
//   - the enclosing section heading (Abstract/Methods/... and the Russian
//     equivalents) is tracked and tagged onto every chunk under it.
//
// It is best-effort: prose with no tables collapses to a single flow block, so
// the recursive splitter sees exactly the original text and its output is
// unchanged. Detection prefers a miss (treat as flow / leave section "") over a
// wrong call.

// Block-shaping thresholds.
const (
	// minTableRows is the fewest consecutive tabular lines that constitute a
	// table block. Two lines are enough to be a row+row (or header+row) pattern
	// while still rejecting a lone wrapped sentence that happens to hold a gap.
	minTableRows = 2
	// maxCaptionRunes bounds how long a line may be and still count as a caption
	// (captions are short); a long paragraph beginning with "Рисунок" is prose.
	maxCaptionRunes = 200
	// maxHeadingRunes bounds a section-heading line: headings are short titles,
	// not sentences.
	maxHeadingRunes = 64
	// tableOverageFactor caps how far a kept-whole table block may exceed the
	// size budget before it is, as a last resort, split anyway: a pathological
	// "table" the size of the whole document must not defeat windowing. 3x keeps
	// real tables intact while bounding the worst case.
	tableOverageFactor = 3
)

// Normalised section names. Headings in either language fold to one of these,
// so retrieval can filter on a stable, language-independent section label.
const (
	secAbstract     = "Abstract"
	secIntroduction = "Introduction"
	secMethods      = "Methods"
	secResults      = "Results"
	secDiscussion   = "Discussion"
	secConclusion   = "Conclusion"
	secReferences   = "References"
	secAcknowledge  = "Acknowledgements"
)

// blockKind tags how a block must be chunked.
type blockKind int

const (
	kindFlow  blockKind = iota // ordinary prose: split with the recursive separators
	kindTable                  // tabular: keep whole (bounded overage)
)

// block is a contiguous span of the source text with its kind and the section
// heading in force at its start.
type block struct {
	text    string
	kind    blockKind
	section string
}

// segment splits text into ordered blocks: maximal runs of tabular lines become
// kindTable blocks, everything else is kindFlow, captions are folded into the
// adjacent block, and the current section heading is carried onto each block. A
// text with no tabular lines yields exactly one kindFlow block holding all of
// text (so the recursive splitter behaves as before).
func segment(text string) []block {
	lines := splitKeepNewlines(text)

	var (
		blocks  []block
		flow    strings.Builder // accumulates consecutive non-table lines
		section string
	)
	flushFlow := func() {
		if flow.Len() == 0 {
			return
		}
		blocks = append(blocks, block{text: flow.String(), kind: kindFlow, section: section})
		flow.Reset()
	}

	for i := 0; i < len(lines); {
		// A lone caption line directly above a table joins that table, so the two
		// stay in one chunk. captionThenTable reports the caption + table extent.
		if from, run := captionThenTable(lines, i); run > 0 {
			flushFlow()
			blocks = append(blocks, captionedTable(lines, from, i+run, section))
			i += run
			continue
		}
		if run := tableRun(lines, i); run > 0 {
			flushFlow()
			blocks = append(blocks, captionedTable(lines, i, i+run, section))
			i += run
			continue
		}
		if h := sectionHeading(lines[i].content); h != "" {
			// A heading both retags the following text and reads naturally as flow.
			section = h
			flow.WriteString(lines[i].raw)
			i++
			continue
		}
		flow.WriteString(lines[i].raw)
		i++
	}
	flushFlow()

	return attachCaptions(blocks)
}

// captionThenTable handles the "caption line then table" layout: when lines[i]
// is a caption immediately followed by a table run, it returns from==i (the
// caption is part of the block) and run = caption + table length. Otherwise
// run==0. The caller has already excluded the line being a table start itself.
func captionThenTable(lines []line, i int) (from, run int) {
	if i+1 >= len(lines) || !isCaptionLine(lines[i].content) {
		return i, 0
	}
	if t := tableRun(lines, i+1); t > 0 {
		return i, 1 + t
	}
	return i, 0
}

// captionedTable builds a kindTable block spanning lines[from:to] and absorbs a
// single caption line immediately BELOW the table (the common "table then
// 'Таблица 1 — ...'" layout), so the caption is kept with the table it labels.
func captionedTable(lines []line, from, to int, section string) block {
	if to < len(lines) && isCaptionLine(lines[to].content) {
		to++
	}
	return block{text: joinRaw(lines[from:to]), kind: kindTable, section: section}
}

// line is one source line: content is the text without its trailing newline,
// raw keeps the newline so blocks reconstruct the source byte-for-byte.
type line struct {
	content string
	raw     string
}

// splitKeepNewlines breaks s into lines, each retaining its trailing "\n" (the
// last line has none unless s ends in "\n"), so concatenating raws restores s.
func splitKeepNewlines(s string) []line {
	var out []line
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, line{content: s[start:i], raw: s[start : i+1]})
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, line{content: s[start:], raw: s[start:]})
	}
	return out
}

// tableRun returns how many consecutive lines starting at i form a table block
// (>= minTableRows tabular lines), or 0 when lines[i] does not begin one. Blank
// lines break a run, so a table separated from the next one by an empty line
// stays two blocks.
func tableRun(lines []line, i int) int {
	n := 0
	for i+n < len(lines) && isTabularLine(lines[i+n].content) {
		n++
	}
	if n >= minTableRows {
		return n
	}
	return 0
}

// isTabularLine reports whether a line looks like a table row: it carries a
// column separator — a pipe "|", a tab, or a 2+-space gap — between two
// non-empty cells. A blank or single-cell line is not tabular.
func isTabularLine(content string) bool {
	s := strings.TrimRight(content, " \t")
	if strings.TrimSpace(s) == "" {
		return false
	}
	if strings.Contains(s, "\t") {
		return hasTwoCells(s, "\t")
	}
	if strings.Contains(s, "|") {
		return hasTwoCells(s, "|")
	}
	return hasMultiSpaceColumns(s)
}

// hasTwoCells reports whether splitting s on sep yields at least two non-empty
// cells (so a leading/trailing "|" or a lone separator is not mistaken for a
// column boundary).
func hasTwoCells(s, sep string) bool {
	nonEmpty := 0
	for _, c := range strings.Split(s, sep) {
		if strings.TrimSpace(c) != "" {
			nonEmpty++
		}
	}
	return nonEmpty >= 2
}

// hasMultiSpaceColumns reports whether s has at least one run of 2+ spaces with
// non-space content on both sides — the whitespace-aligned column pattern PDF
// table extraction emits. Leading indentation is ignored.
func hasMultiSpaceColumns(s string) bool {
	trimmed := strings.TrimLeft(s, " ")
	runStart := -1
	sawColumnBreak := false
	for idx, r := range trimmed {
		if r == ' ' {
			if runStart < 0 {
				runStart = idx
			}
			continue
		}
		if runStart >= 0 {
			if idx-runStart >= 2 {
				sawColumnBreak = true
				break
			}
			runStart = -1
		}
	}
	return sawColumnBreak
}

// attachCaptions folds a standalone caption block into the block it annotates: a
// caption immediately FOLLOWING a table joins that table (the common "table then
// 'Таблица 1 — ...'" layout); otherwise it attaches to the block after it. A
// caption already inside a larger flow block is left untouched.
func attachCaptions(blocks []block) []block {
	out := make([]block, 0, len(blocks))
	for i := range blocks {
		b := blocks[i]
		if !isCaptionBlock(b) {
			out = append(out, b)
			continue
		}
		if n := len(out); n > 0 && out[n-1].kind == kindTable {
			out[n-1].text += b.text
			continue
		}
		if i+1 < len(blocks) {
			blocks[i+1].text = b.text + blocks[i+1].text
			if blocks[i+1].section == "" {
				blocks[i+1].section = b.section
			}
			continue
		}
		out = append(out, b)
	}
	return out
}

// isCaptionBlock reports whether a flow block is a single short caption line, so
// attachCaptions can glue it to the neighbouring block rather than letting it
// become a tiny standalone chunk.
func isCaptionBlock(b block) bool {
	if b.kind != kindFlow {
		return false
	}
	t := strings.TrimSpace(b.text)
	return t != "" && !strings.ContainsRune(t, '\n') && isCaptionLine(t)
}

// isCaptionLine reports whether line starts with a figure/table caption prefix
// (Russian or English) and is short enough to be a caption rather than prose.
func isCaptionLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || runeLen(t) > maxCaptionRunes {
		return false
	}
	lower := strings.ToLower(t)
	for _, p := range captionPrefixes() {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// captionPrefixes lists the lower-cased figure/table caption openers, Russian
// and English. A function (not a global) to satisfy gochecknoglobals.
func captionPrefixes() []string {
	return []string{"рис.", "рисунок", "табл.", "таблица", "figure", "fig.", "table"}
}

// sectionHeading returns the NORMALISED section name when line is a recognised
// heading (Abstract/Introduction/.../References and their Russian or Chinese
// equivalents), or "" otherwise. A heading is a short line whose leading word —
// after any numbering like "2.", "II." or "一、" — matches a known section;
// trailing punctuation (a colon, a period, their CJK forms) is tolerated.
func sectionHeading(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || runeLen(t) > maxHeadingRunes {
		return ""
	}
	t = strings.TrimLeft(t, "0123456789.IVXivx )一二三四五六七八九十、")
	t = strings.TrimRight(t, " .:—-：。、")
	word := firstWord(t)
	if word == "" {
		return ""
	}
	return canonicalSection(strings.ToLower(word))
}

// firstWord returns the first whitespace-delimited word of s (the heading's
// keyword), or "" when s is empty.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if unicode.IsSpace(r) {
			return s[:i]
		}
	}
	return s
}

// canonicalSection maps a lower-cased heading keyword to its normalised English
// section name, or "" when the keyword names no known section.
func canonicalSection(word string) string {
	return sectionKeywords()[word]
}

// sectionKeywords maps every recognised heading keyword (English, Russian and
// Chinese) to its normalised section name. A function (not a global) to satisfy
// gochecknoglobals; the map literal is the single source of the section
// vocabulary. CJK has no case, so Chinese keys pass strings.ToLower unchanged.
func sectionKeywords() map[string]string {
	return map[string]string{
		"abstract": secAbstract, "аннотация": secAbstract, "реферат": secAbstract,
		"摘要":           secAbstract,
		"introduction": secIntroduction, "введение": secIntroduction,
		"引言": secIntroduction, "绪论": secIntroduction,
		"methods": secMethods, "methodology": secMethods,
		"методы": secMethods, "методика": secMethods, "методология": secMethods,
		"方法": secMethods, "实验方法": secMethods, "实验": secMethods,
		"results": secResults, "результаты": secResults, "结果": secResults,
		"discussion": secDiscussion, "обсуждение": secDiscussion, "讨论": secDiscussion,
		"conclusion": secConclusion, "conclusions": secConclusion,
		"выводы": secConclusion, "заключение": secConclusion, "结论": secConclusion,
		"references": secReferences, "bibliography": secReferences,
		"литература": secReferences, "библиография": secReferences,
		"参考文献":             secReferences,
		"acknowledgements": secAcknowledge, "acknowledgments": secAcknowledge,
		"благодарности": secAcknowledge, "致谢": secAcknowledge,
	}
}

// joinRaw concatenates the raw (newline-keeping) text of lines, reconstructing
// that span of the source exactly.
func joinRaw(lines []line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.raw)
	}
	return b.String()
}
