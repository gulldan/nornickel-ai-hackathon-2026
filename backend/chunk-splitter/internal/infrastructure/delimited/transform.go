package delimited

import (
	"encoding/csv"
	"io"
	"strings"
)

// Transform converts a delimited document into self-contained row evidence.
func Transform(filename, mimeType, text string) (TransformResult, bool) {
	return TransformWithLimit(filename, mimeType, text, 0)
}

// TransformWithLimit streams CSV/TSV-like text into row evidence, stopping at a
// row boundary before maxBytes is exceeded. maxBytes <= 0 means unlimited.
func TransformWithLimit(filename, mimeType, text string, maxBytes int) (TransformResult, bool) {
	reader, delimiter, err := newReader(filename, mimeType, text)
	if err != nil {
		return TransformResult{}, false
	}
	headers, rowNumber, err := readHeader(reader)
	if err != nil {
		return TransformResult{}, false
	}

	w := &rowWriter{maxBytes: maxBytes}
	if !w.line("Delimited table extracted for retrieval.") ||
		!w.line("Columns: "+strings.Join(headers, " | ")) ||
		!w.line("") {
		return TransformResult{}, false
	}

	rows, headers, ok := w.stream(reader, filename, headers, rowNumber)
	if !ok || rows == 0 || len(headers) < 2 {
		return TransformResult{}, false
	}
	return TransformResult{
		Text:      w.b.String(),
		Rows:      rows,
		Columns:   len(headers),
		Delimiter: delimiter,
		Truncated: w.truncated,
	}, true
}

type rowWriter struct {
	b         strings.Builder
	maxBytes  int
	truncated bool
}

func (w *rowWriter) line(text string) bool {
	if w.maxBytes > 0 && w.b.Len()+len(text)+1 > w.maxBytes {
		w.truncated = true
		return false
	}
	w.b.WriteString(text)
	w.b.WriteByte('\n')
	return true
}

func (w *rowWriter) stream(reader *csv.Reader, filename string, headers []string, rowNumber int) (int, []string, bool) {
	count := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			return count, headers, true
		}
		if err != nil {
			return count, headers, false
		}
		rowNumber++
		if emptyRecord(record) {
			continue
		}
		if len(record) > len(headers) {
			headers = extendHeaders(headers, len(record))
		}
		lines := rowLines(filename, headers, DataRow{Number: rowNumber, Cells: cleanRecord(record)})
		if len(lines) == 0 {
			continue
		}
		if w.writeRow(lines) {
			count++
		}
		if w.truncated {
			return count, headers, true
		}
	}
}

func (w *rowWriter) writeRow(lines []string) bool {
	wrote := false
	for _, line := range lines {
		if !w.line(line) {
			return wrote
		}
		wrote = true
	}
	return wrote
}

func newReader(filename, mimeType, text string) (*csv.Reader, rune, error) {
	if !IsCandidate(filename, mimeType) {
		return nil, 0, errNotDelimited
	}
	delimiter, ok := detectDelimiter(filename, mimeType, text)
	if !ok {
		return nil, 0, errNotDelimited
	}

	cleanText := strings.TrimPrefix(text, "\ufeff")
	if _, hasSep := explicitSeparator(cleanText); hasSep {
		cleanText = dropFirstLine(cleanText)
	}

	reader := csv.NewReader(strings.NewReader(cleanText))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	return reader, delimiter, nil
}
