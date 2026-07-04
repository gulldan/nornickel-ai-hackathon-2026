// Package splitter implements domain.Splitter with a recursive character
// splitter, the same strategy popularised by LangChain. It greedily packs text
// into windows of at most CHUNK_SIZE runes, preferring to break on the most
// semantic separator available ("\n\n" paragraph, then "\n" line, then " "
// word) so chunks stay coherent, and carries CHUNK_OVERLAP runes from the tail
// of one chunk into the head of the next so context spanning a boundary is not
// lost during retrieval.
package splitter

import (
	"strings"

	"github.com/example/chunk-splitter/internal/domain"
)

// separators returns the split candidates in order of decreasing semantic
// strength: paragraph, line, CJK sentence enders (китайский без пробелов),
// space, and "" as the guaranteed hard-split fallback.
func separators() []string { return []string{"\n\n", "\n", "。", "！", "？", "；", " ", ""} }

// Recursive is a recursive character splitter parameterised by a target chunk
// size and overlap. overlap is always measured in runes (it is context bleed
// across a boundary, where exactness is not required). The chunk-SIZE budget is
// measured by `measure`: by default runeLen (so multi-byte UTF-8 text is split
// on character boundaries), but it can be any cost function — e.g. a tokenizer's
// real token count, see NewRecursiveWithMeasure — letting chunks be sized to an
// EXACT token budget instead of a rune budget.
type Recursive struct {
	size    int              // target maximum chunk size, in units of `measure`
	overlap int              // runes shared between consecutive chunks (always runes)
	measure func(string) int // size of a piece in budget units (runeLen by default)
}

// NewRecursive builds a rune-budgeted splitter (size and overlap in runes).
// Defensive defaults keep it well-behaved if it is ever constructed with
// nonsensical configuration: size falls back to 1000, and overlap is clamped to
// [0, size-1] so progress is always made.
func NewRecursive(size, overlap int) *Recursive {
	return NewRecursiveWithMeasure(size, overlap, runeLen)
}

// NewRecursiveWithMeasure builds a splitter whose chunk-SIZE budget (maxSize) is
// counted by `measure` rather than runes — e.g. a F2LLM-v2 tokenizer's token count,
// for EXACT token-budget chunking. overlap stays rune-based regardless. measure
// nil falls back to runeLen. The same defensive size/overlap clamps as
// NewRecursive apply (size<=0 → 1000, overlap clamped to [0, size-1]); note the
// overlap clamp is rune-vs-size unit-mixed but only guards a pathological
// configuration, never the normal token-budget case.
func NewRecursiveWithMeasure(maxSize, overlap int, measure func(string) int) *Recursive {
	if maxSize <= 0 {
		maxSize = 1000
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= maxSize {
		overlap = maxSize - 1
	}
	if measure == nil {
		measure = runeLen
	}
	return &Recursive{size: maxSize, overlap: overlap, measure: measure}
}

// piece is a chunk-string together with the section heading in force where it
// originated, threaded through merging/overlap so the final chunk can be tagged.
type piece struct {
	text    string
	section string
}

// Split implements domain.Splitter. A structure-aware pre-pass groups the text
// into blocks (prose vs. table-like, with captions folded in and section
// headings tracked); each prose block is broken into atomic pieces no larger
// than the target size (recursing through ever-finer separators) and merged back
// into target-sized chunks, while each table block is kept WHOLE (bounded
// overage) so a "composition→temperature→property" row is never split. Overlap
// is applied across the assembled chunks, and each chunk is tagged with its
// enclosing section.
func (r *Recursive) Split(text string) []domain.Chunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	var assembled []piece
	for _, b := range segment(text) {
		assembled = append(assembled, r.chunkBlock(b)...)
	}
	assembled = r.applyOverlap(assembled)

	chunks := make([]domain.Chunk, 0, len(assembled))
	for _, p := range assembled {
		if strings.TrimSpace(p.text) == "" {
			continue // never emit a whitespace-only chunk
		}
		// Index is assigned post-filter so it stays contiguous (0..n-1) even
		// when a whitespace-only piece is skipped.
		chunks = append(chunks, domain.Chunk{Index: len(chunks), Text: p.text, Section: p.section})
	}
	return chunks
}

// chunkBlock turns one structural block into chunk pieces (without overlap): a
// table block is emitted whole unless it blows past the bounded overage limit
// (then it falls back to ordinary recursive splitting so windowing is never
// defeated); a flow block goes through the unchanged recursive-split + merge
// pipeline. Every produced piece carries the block's section.
func (r *Recursive) chunkBlock(b block) []piece {
	if b.kind == kindTable && r.measure(b.text) <= r.size*tableOverageFactor {
		return []piece{{text: b.text, section: b.section}}
	}
	merged := r.mergePieces(r.splitRecursive(b.text, 0))
	out := make([]piece, len(merged))
	for i, c := range merged {
		out[i] = piece{text: c, section: b.section}
	}
	return out
}

// splitRecursive breaks text into pieces each at most r.size (in measure units)
// long, trying separators[sepIdx] first and recursing into finer separators for
// any piece that is still too large.
func (r *Recursive) splitRecursive(text string, sepIdx int) []string {
	if r.measure(text) <= r.size {
		return []string{text}
	}
	seps := separators()
	if sepIdx >= len(seps) {
		// No separators left: hard-split on the rune boundary at r.size.
		return r.hardSplit(text)
	}

	sep := seps[sepIdx]
	if sep == "" {
		// Empty separator means "split between every rune"; defer to hardSplit
		// which produces size-bounded pieces instead of one piece per rune.
		return r.hardSplit(text)
	}

	parts := strings.Split(text, sep)
	out := make([]string, 0, len(parts))
	for idx, p := range parts {
		// Keep the separator we split on (except after the final part) so merging
		// the pieces reconstructs the source, preserving line/paragraph structure
		// (tables, formulas, lists) instead of collapsing it to single spaces.
		piece := p
		if idx < len(parts)-1 {
			piece += sep
		}
		if piece == "" {
			continue
		}
		if r.measure(piece) <= r.size {
			out = append(out, piece)
			continue
		}
		// Still too big on this separator: recurse to the next finer one.
		out = append(out, r.splitRecursive(piece, sepIdx+1)...)
	}
	return out
}

// hardSplit cuts text into consecutive windows of r.size runes. It is the
// terminal strategy for text with no usable separator.
func (r *Recursive) hardSplit(text string) []string {
	runes := []rune(text)
	var out []string
	for i := 0; i < len(runes); i += r.size {
		end := i + r.size
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

// mergePieces greedily concatenates atomic pieces into chunks of up to r.size
// (in measure units), joining with "" so the separators splitRecursive kept
// reconstruct the source faithfully. Each piece's measure already includes its
// trailing separator, so there is no extra join cost. Overlap is applied later,
// across the assembled chunks of all blocks (see Split).
func (r *Recursive) mergePieces(pieces []string) []string {
	var (
		chunks  []string
		current []string
		curLen  int
	)

	flush := func() {
		if len(current) == 0 {
			return
		}
		chunks = append(chunks, strings.Join(current, ""))
		current = current[:0]
		curLen = 0
	}

	for _, p := range pieces {
		pLen := r.measure(p)
		if curLen+pLen > r.size && len(current) > 0 {
			flush()
		}
		// A single piece larger than the window (only from hardSplit edge cases,
		// which stay rune-based) becomes its own chunk.
		if pLen > r.size {
			flush()
			chunks = append(chunks, p)
			continue
		}
		current = append(current, p)
		curLen += pLen
	}
	flush()
	return chunks
}

// applyOverlap prepends the trailing r.overlap runes of each chunk's text onto
// the following chunk, so context that straddles a boundary appears in both. The
// section tag is left as the chunk's own (the prepended tail is borrowed
// context, not a change of section).
func (r *Recursive) applyOverlap(chunks []piece) []piece {
	if r.overlap == 0 || len(chunks) <= 1 {
		return chunks
	}
	out := make([]piece, len(chunks))
	out[0] = chunks[0]
	for i := 1; i < len(chunks); i++ {
		prev := []rune(chunks[i-1].text)
		tail := prev
		if len(prev) > r.overlap {
			tail = prev[len(prev)-r.overlap:]
		}
		out[i] = piece{text: string(tail) + chunks[i].text, section: chunks[i].section}
	}
	return out
}

// runeLen returns the number of runes (Unicode code points) in s.
func runeLen(s string) int { return len([]rune(s)) }
