package application

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const sourceURIKey = "source_uri"

type workbookAnchor struct {
	runeStart int
	metadata  map[string]string
}

type workbookAnchorIndex []workbookAnchor

func parseWorkbookAnchorIndex(text string) workbookAnchorIndex {
	var anchors workbookAnchorIndex
	runeOffset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if uri, ok := sourceURIFromLine(line); ok {
			meta := metadataFromSourceURI(uri)
			if len(meta) > 1 {
				anchors = append(anchors, workbookAnchor{runeStart: runeOffset, metadata: meta})
			}
		}
		runeOffset += utf8.RuneCountInString(line)
	}
	return anchors
}

func (idx workbookAnchorIndex) metadataAt(charStart, charEnd int) map[string]string {
	if len(idx) == 0 || (charStart == 0 && charEnd == 0) {
		return nil
	}
	if charEnd > charStart {
		inside := sort.Search(len(idx), func(i int) bool { return idx[i].runeStart >= charStart })
		if inside < len(idx) && idx[inside].runeStart < charEnd {
			return idx[inside].metadata
		}
	}
	n := sort.Search(len(idx), func(i int) bool { return idx[i].runeStart > charStart })
	if n > 0 {
		return idx[n-1].metadata
	}
	return nil
}

func workbookAnchorMetadata(text string) map[string]string {
	for _, line := range strings.Split(text, "\n") {
		if uri, ok := sourceURIFromLine(line); ok {
			meta := metadataFromSourceURI(uri)
			if len(meta) > 1 {
				return meta
			}
		}
	}
	return nil
}

func sourceURIFromLine(line string) (string, bool) {
	idx := strings.Index(strings.ToLower(line), sourceURIKey)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(line[idx+len(sourceURIKey):])
	if rest == "" {
		return "", false
	}
	delimiter := rest[0]
	if delimiter != '=' && delimiter != ':' && delimiter != '|' {
		return "", false
	}
	rest = strings.TrimSpace(rest[1:])
	if rest == "" {
		return "", false
	}
	if end := strings.IndexFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || r == '|' }); end >= 0 {
		rest = rest[:end]
	}
	if delimiter == '|' && !strings.Contains(rest, "#") {
		return "", false
	}
	return rest, rest != ""
}

func metadataFromSourceURI(raw string) map[string]string {
	uri := strings.Trim(strings.TrimSpace(raw), `"'`)
	if uri == "" {
		return nil
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return map[string]string{sourceURIKey: uri}
	}
	params := sourceURIParams(uri, parsed)
	meta := map[string]string{sourceURIKey: uri}
	copyFirstParam(meta, params, "sheet", "sheet", "worksheet", "sheet_name")
	copyFirstParam(meta, params, "row_start", "row_start")
	copyFirstParam(meta, params, "row_end", "row_end")
	copyFirstParam(meta, params, "block_id", "block_id", "table_id", "section_id")
	if columns := params["columns"]; len(columns) > 0 {
		meta["columns"] = strings.Join(columns, "|")
	}
	if rng := firstParam(params, "range"); rng != "" {
		meta["range"] = rng
		if meta["row_start"] == "" || meta["row_end"] == "" {
			if start, end, ok := rowBoundsFromA1Range(rng); ok {
				meta["row_start"] = strconv.Itoa(start)
				meta["row_end"] = strconv.Itoa(end)
			}
		}
		if meta["block_id"] == "" && meta["sheet"] != "" {
			meta["block_id"] = meta["sheet"] + "!" + rng
		}
	}
	return meta
}

func sourceURIParams(raw string, parsed *url.URL) url.Values {
	params := parsed.Query()
	if parsed.Fragment != "" {
		for key, values := range parseQueryish(parsed.Fragment) {
			params[key] = append(params[key], values...)
		}
	}
	if len(params) == 0 {
		fragment, ok := cutAfter(raw, "#")
		if !ok {
			return params
		}
		for key, values := range parseQueryish(fragment) {
			params[key] = append(params[key], values...)
		}
	}
	return params
}

func cutAfter(s, sep string) (string, bool) {
	idx := strings.Index(s, sep)
	if idx < 0 {
		return "", false
	}
	return s[idx+len(sep):], true
}

func parseQueryish(raw string) url.Values {
	if values, err := url.ParseQuery(raw); err == nil {
		return values
	}
	return url.Values{}
}

func copyFirstParam(meta map[string]string, params url.Values, target string, names ...string) {
	if value := firstParam(params, names...); value != "" {
		meta[target] = value
	}
}

func firstParam(params url.Values, names ...string) string {
	for _, name := range names {
		for _, value := range params[name] {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func rowBoundsFromA1Range(rng string) (int, int, bool) {
	parts := strings.Split(rng, ":")
	if len(parts) == 0 || len(parts) > 2 {
		return 0, 0, false
	}
	start, ok := rowNumberFromA1(parts[0])
	if !ok {
		return 0, 0, false
	}
	end := start
	if len(parts) == 2 {
		parsed, ok := rowNumberFromA1(parts[1])
		if !ok {
			return 0, 0, false
		}
		end = parsed
	}
	if start > end {
		start, end = end, start
	}
	return start, end, true
}

func rowNumberFromA1(cell string) (int, bool) {
	digits := strings.Builder{}
	for _, r := range cell {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	if digits.Len() == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(digits.String())
	return n, err == nil && n > 0
}
