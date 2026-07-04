package delimited

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errNotDelimited = errors.New("not a delimited table")

func readHeader(reader *csv.Reader) ([]string, int, error) {
	for {
		record, err := reader.Read()
		if err == io.EOF {
			return nil, 0, errNotDelimited
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read delimited header: %w", err)
		}
		if emptyRecord(record) {
			continue
		}
		headers := normalizeHeaders(record)
		if len(headers) < 2 {
			return nil, 0, errNotDelimited
		}
		return headers, 1, nil
	}
}

func cleanRecord(record []string) []string {
	out := make([]string, len(record))
	for i, cell := range record {
		out[i] = cleanCell(cell)
	}
	return out
}

func cleanCell(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	return strings.Join(strings.Fields(value), " ")
}

func dropFirstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[idx+1:]
	}
	return ""
}
