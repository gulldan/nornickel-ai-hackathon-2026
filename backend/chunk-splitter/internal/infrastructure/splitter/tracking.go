package splitter

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// minTrackedRun is the minimum number of consecutive single-letter tokens (each
// joined to the next by a short whitespace gap) that constitutes a "tracked" run
// worth collapsing. Below it we leave the text untouched, so incidental short
// spaced sequences ("a b c d", "И. И.") are never merged.
//
// maxConnectorGap is the largest whitespace gap (in runes) that still glues a
// tracked run together. PDF extractors emit tracking with 1 OR 2 whitespace
// chars between glyphs — e.g. "М\nе\n\nт\nо\n\nд" (mixed single "\n" and double
// "\n\n" from column wrapping) — so a strict "exactly one" rule would split the
// run into 2-letter pieces and collapse nothing. Two is safe because the run also
// requires every token to be a SINGLE LETTER: real word/paragraph boundaries sit
// between multi-character words, which never form such a run.
const (
	minTrackedRun   = 5
	maxConnectorGap = 2
)

// CollapseTracking removes letter-spacing (tracked typography) that
// some PDF extractors emit as individual letters separated by a single space or
// newline, e.g. "М е т о д о л о г и я" → "Методология". It is pure, deterministic
// and RE2-free: a manual rune scan, no regexp (Go's RE2 lacks the lookahead this
// would otherwise need).
//
// The algorithm tokenises s into (token, gap) pairs where token is a maximal run
// of non-whitespace runes and gap is the exact whitespace string that follows it.
// A tracked run is a maximal sequence of tokens that are EACH a single letter and
// are joined by short whitespace gaps (1..maxConnectorGap runes — PDF tracking
// uses single "\n"/" " and double "\n\n"). A run of >= minTrackedRun such letters
// is collapsed into one token (the letters concatenated, no whitespace); the gap
// BEFORE the run and the gap AFTER it are preserved unchanged. A longer gap
// (> maxConnectorGap) ends a run. Everything that is not part of a collapsed run —
// normal words, multi-char tokens, paragraph structure, every gap — is emitted
// byte-for-byte identically, so f is safe to run over a whole document before
// chunking (it only deletes single whitespace inside letter runs, never a
// paragraph break) and is idempotent.
func CollapseTracking(s string) string {
	if s == "" {
		return s
	}

	tokens, gaps := tokenize(s)
	if len(tokens) == 0 {
		// Whitespace-only input: tokenize keeps the leading gap in gaps[0] and the
		// reassembly below would reproduce it, but with no tokens we can short-circuit.
		return s
	}

	// Reassemble, collapsing qualifying runs. We never mutate gaps; a collapsed
	// run drops only the single-whitespace gaps strictly BETWEEN its letters.
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(gaps[0]) // leading gap (possibly empty) before the first token

	i := 0
	for i < len(tokens) {
		runLen := trackedRunLength(tokens, gaps, i)
		if runLen >= minTrackedRun {
			for j := i; j < i+runLen; j++ {
				b.WriteString(tokens[j]) // single letters, concatenated, no gaps
			}
			// Gap AFTER the run (the gap following its last token) is preserved.
			b.WriteString(gaps[i+runLen])
			i += runLen
			continue
		}
		b.WriteString(tokens[i])
		b.WriteString(gaps[i+1])
		i++
	}
	return b.String()
}

// tokenize splits s into tokens (maximal non-whitespace runs) and gaps (the
// whitespace between/around them). gaps[0] is the leading whitespace before the
// first token (often ""), gaps[i+1] is the whitespace immediately after tokens[i]
// (the last is the trailing whitespace, often ""). Thus len(gaps) == len(tokens)+1
// and tokens[0]+gaps[1]+tokens[1]+... interleaved with the bookend gaps
// reconstructs s exactly.
func tokenize(s string) (tokens []string, gaps []string) {
	gaps = append(gaps, "") // gaps[0]: leading gap, filled below if s starts with space
	tokStart := -1          // byte index where the current token began, or -1 if in a gap
	gapStart := 0           // byte index where the current gap began

	for i, r := range s {
		if unicode.IsSpace(r) {
			if tokStart >= 0 {
				// Token ended: emit it and open a gap starting here.
				tokens = append(tokens, s[tokStart:i])
				tokStart = -1
				gapStart = i
			}
			continue
		}
		if tokStart < 0 {
			// Gap ended (or this is the very first rune): record the gap text.
			if len(tokens) == 0 {
				gaps[0] = s[gapStart:i] // leading gap
			} else {
				gaps = append(gaps, s[gapStart:i])
			}
			tokStart = i
		}
	}

	if tokStart >= 0 {
		tokens = append(tokens, s[tokStart:])
		gaps = append(gaps, "") // no trailing gap after the final token
	} else {
		// s ended in whitespace (or is all whitespace): the open gap is trailing.
		if len(tokens) == 0 {
			gaps[0] = s[gapStart:] // all-whitespace input
		} else {
			gaps = append(gaps, s[gapStart:])
		}
	}
	return tokens, gaps
}

// trackedRunLength returns how many tokens starting at i form a tracked run:
// each token is a single letter and consecutive ones are joined by a short
// whitespace gap (1..maxConnectorGap runes). It returns 0 when tokens[i] is not
// itself a single letter, otherwise the run length (>= 1).
func trackedRunLength(tokens, gaps []string, i int) int {
	if !isSingleLetter(tokens[i]) {
		return 0
	}
	n := 1
	for i+n < len(tokens) {
		// gaps[i+n] is the gap BETWEEN tokens[i+n-1] and tokens[i+n].
		if !isConnectorGap(gaps[i+n]) || !isSingleLetter(tokens[i+n]) {
			break
		}
		n++
	}
	return n
}

// isSingleLetter reports whether tok is exactly one rune and that rune is a
// Unicode letter.
func isSingleLetter(tok string) bool {
	if utf8.RuneCountInString(tok) != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(tok)
	return unicode.IsLetter(r)
}

// isConnectorGap reports whether gap is short enough (1..maxConnectorGap
// whitespace runes) to glue a tracked run together. PDF tracking uses 1-2
// whitespace chars between glyphs; a longer gap ends the run. tokenize only ever
// puts whitespace in a gap, so the rune count is the whitespace count.
func isConnectorGap(gap string) bool {
	n := utf8.RuneCountInString(gap)
	return n >= 1 && n <= maxConnectorGap
}
