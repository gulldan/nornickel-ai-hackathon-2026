package splitter

import "unicode"

// LocateOffsets resolves, for each chunk text, the [start, end) RUNE offsets of
// that chunk's span within source. A chunk's text is a near-faithful slice of
// source (the splitter keeps the separators it splits on) but may differ in
// edge whitespace and carries an overlap of runes from the previous chunk, so it
// is not guaranteed to be an exact byte-for-byte substring. We therefore match
// on a whitespace-normalised projection of the source and translate the located
// normalised span back to original-rune coordinates.
//
// Provenance here is legal-grade: an offset is returned only when the chunk can
// be located unambiguously while scanning forward from a monotonic cursor.
// Anything else yields [0, 0] rather than a guess — correct-or-absent.
//
// The returned slice has one [start, end) pair per input chunk, in order.
// Offsets are RUNE indices into source (not bytes). A pair of [0, 0] means the
// chunk could not be located.
func LocateOffsets(source string, chunkTexts []string) [][2]int {
	out := make([][2]int, len(chunkTexts))
	if len(chunkTexts) == 0 {
		return out
	}

	// Build the normalised projection of the source (as runes, so the offset map
	// is rune-indexed and matching is rune-exact) together with a map from each
	// normalised-rune index back to the original-rune index it came from.
	norm, mapNormToOrig := normalizeWithMap(source)

	// cursor is the lowest normalised index the next chunk may start at. It is
	// monotonic so each chunk is found at or after the previous one — the chunks
	// are emitted in document order and (because of overlap) a later chunk's head
	// repeats the previous chunk's tail, so we must not rewind past it.
	cursor := 0
	for i, ct := range chunkTexts {
		needle := normalize(ct)
		if len(needle) == 0 {
			out[i] = [2]int{0, 0}
			continue
		}
		ns := indexFrom(norm, needle, cursor)
		if ns < 0 {
			// Not locatable from here: leave [0, 0] and DO NOT advance the cursor,
			// so a transient miss can't drag every following chunk off its span.
			out[i] = [2]int{0, 0}
			continue
		}
		ne := ns + len(needle)
		out[i] = [2]int{mapNormToOrig[ns], mapNormToOrig[ne]}
		// Advance to ns+1, NOT ne: the next chunk's overlap repeats this chunk's
		// tail, which lives inside [ns, ne); it must still be matchable going
		// forward. Stepping one past the start keeps the scan monotonic without
		// skipping over that repeated overlap.
		cursor = ns + 1
	}
	return out
}

// normalizeWithMap collapses every run of Unicode whitespace in s to a single
// ASCII space and trims leading/trailing whitespace, returning the normalised
// runes alongside mapNormToOrig: for each normalised-rune index it holds the
// original-rune index that produced it. For a collapsed whitespace run, the
// emitted space maps to the FIRST original rune of that run. A final sentinel is
// appended so mapNormToOrig[len(norm)] == len(origRunes), letting callers
// translate an end-exclusive normalised index.
func normalizeWithMap(s string) (norm []rune, mapNormToOrig []int) {
	orig := []rune(s)
	norm = make([]rune, 0, len(orig))
	mapNormToOrig = make([]int, 0, len(orig)+1)

	pendingSpace := false // a whitespace run seen but not yet emitted
	spaceOrig := 0        // original index of that run's first rune
	for i, r := range orig {
		if unicode.IsSpace(r) {
			if !pendingSpace {
				pendingSpace = true
				spaceOrig = i
			}
			continue
		}
		if pendingSpace {
			// Emit the collapsed run as one space only if it is interior (i.e.
			// some non-space rune already precedes it); a leading run is trimmed.
			if len(norm) > 0 {
				norm = append(norm, ' ')
				mapNormToOrig = append(mapNormToOrig, spaceOrig)
			}
			pendingSpace = false
		}
		norm = append(norm, r)
		mapNormToOrig = append(mapNormToOrig, i)
	}
	// A trailing whitespace run (pendingSpace still set) is dropped — trimmed.

	// Sentinel: end-exclusive normalised len(norm) maps to original len(orig).
	mapNormToOrig = append(mapNormToOrig, len(orig))
	return norm, mapNormToOrig
}

// normalize collapses Unicode whitespace runs to a single ASCII space and trims
// the ends — the same projection normalizeWithMap applies, used for the needle.
func normalize(s string) []rune {
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
	return out
}

// indexFrom finds the first occurrence of needle in haystack at or after the
// given RUNE offset, returning that rune offset (or -1). Both are rune slices, so
// the returned index is directly usable against the rune-indexed offset map,
// regardless of multi-byte UTF-8 content. Callers guarantee len(needle) > 0.
func indexFrom(haystack, needle []rune, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
