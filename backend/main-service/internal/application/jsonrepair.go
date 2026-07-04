package application

import "strings"

// repairJSONBackslashes doubles backslashes that are not part of a valid JSON
// escape sequence. Models asked to emit LaTeX (\ce{…}, \frac, \pu{…}) inside
// JSON string values often forget to escape the backslash, which makes the
// output invalid JSON. This repair lets such output parse. It is applied ONLY as
// a fallback after a strict json.Unmarshal fails, so well-formed JSON is never
// altered. Scanning is byte-wise: a backslash (0x5C) never occurs inside a
// UTF-8 multibyte sequence, so this is safe for Cyrillic text.
func repairJSONBackslashes(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 < len(s) && isJSONEscapeByte(s[i+1]) {
			// Valid escape (including \\): keep both bytes verbatim.
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		// Lone backslash (e.g. "\ce"): escape it so JSON stays valid.
		b.WriteString(`\\`)
	}
	return b.String()
}

func isJSONEscapeByte(c byte) bool {
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	default:
		return false
	}
}
