package delimited

import (
	"encoding/csv"
	"io"
	"path/filepath"
	"strings"
)

const sampleRecords = 32

func detectDelimiter(filename, mimeType, text string) (rune, bool) {
	if sep, ok := explicitSeparator(text); ok {
		return sep, true
	}
	switch {
	case strings.EqualFold(filepath.Ext(filename), ".tsv"):
		return '\t', true
	case mimeBase(mimeType) == "text/tab-separated-values":
		return '\t', true
	}

	best, bestScore := rune(0), 0
	for _, candidate := range []rune{',', ';', '\t', '|'} {
		score := delimiterScore(text, candidate)
		if score > bestScore || (score == bestScore && preferDelimiter(text, candidate, best)) {
			best, bestScore = candidate, score
		}
	}
	return best, bestScore > 0
}

func explicitSeparator(text string) (rune, bool) {
	first := firstLine(strings.TrimPrefix(text, "\ufeff"))
	if len(first) != 5 || !strings.EqualFold(first[:4], "sep=") {
		return 0, false
	}
	switch r := []rune(first[4:])[0]; r {
	case ',', ';', '\t', '|':
		return r, true
	default:
		return 0, false
	}
}

func delimiterScore(text string, delimiter rune) int {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	rows := 0
	multiCell := 0
	lastWidth := 0
	consistent := 0
	for rows < sampleRecords {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0
		}
		if emptyRecord(record) {
			continue
		}
		rows++
		width := nonEmptyCells(record)
		if width > 1 {
			multiCell++
		}
		if lastWidth == width && width > 1 {
			consistent++
		}
		lastWidth = width
	}
	if rows < 2 || multiCell < 2 {
		return 0
	}
	return multiCell*10 + consistent
}

func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return strings.TrimRight(text[:idx], "\r")
	}
	return strings.TrimRight(text, "\r")
}

func preferDelimiter(text string, candidate, current rune) bool {
	if current == 0 {
		return true
	}
	line := firstNonEmptyLine(text)
	candidateAt := strings.IndexRune(line, candidate)
	currentAt := strings.IndexRune(line, current)
	switch {
	case candidateAt < 0:
		return false
	case currentAt < 0:
		return true
	default:
		return candidateAt < currentAt
	}
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

func emptyRecord(record []string) bool {
	return nonEmptyCells(record) == 0
}

func nonEmptyCells(record []string) int {
	n := 0
	for _, cell := range record {
		if strings.TrimSpace(cell) != "" {
			n++
		}
	}
	return n
}
