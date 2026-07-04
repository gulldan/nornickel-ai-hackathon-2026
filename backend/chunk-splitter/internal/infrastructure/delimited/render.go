package delimited

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxRenderedCellRunes = 256
	maxRowLineRunes      = 900
)

type renderedField struct {
	index int
	name  string
	text  string
}

func rowLines(filename string, headers []string, row DataRow) []string {
	fields := nonEmptyFields(headers, row.Cells)
	if len(fields) == 0 {
		return nil
	}
	key := fields[0]

	var lines []string
	var segment []renderedField
	flush := func() {
		if len(segment) == 0 {
			return
		}
		lines = append(lines, rowLine(filename, row.Number, key, segment))
		segment = nil
	}

	for _, field := range fields {
		candidate := append(append([]renderedField(nil), segment...), field)
		if len(segment) > 0 && utf8.RuneCountInString(rowLine(filename, row.Number, key, candidate)) > maxRowLineRunes {
			flush()
			segment = append(segment, field)
			continue
		}
		segment = candidate
	}
	flush()
	return lines
}

func rowLine(filename string, rowNumber int, key renderedField, fields []renderedField) string {
	fields = withKeyField(key, fields)
	parts := make([]string, 0, 2+len(fields))
	parts = append(parts,
		"source_uri="+sourceURI(filename, fieldNames(fields), rowNumber),
		fmt.Sprintf("row=%d", rowNumber),
	)
	for _, field := range fields {
		parts = append(parts, field.text)
	}
	return strings.Join(parts, " | ")
}

func nonEmptyFields(headers []string, cells []string) []renderedField {
	fields := make([]renderedField, 0, len(cells))
	for i, value := range cells {
		if value == "" {
			continue
		}
		header := headerAt(headers, i)
		fields = append(fields, renderedField{
			index: i,
			name:  safeField(header),
			text:  safeField(header) + "=" + safeField(truncateCell(value)),
		})
	}
	return fields
}

func withKeyField(key renderedField, fields []renderedField) []renderedField {
	for _, field := range fields {
		if field.index == key.index {
			return fields
		}
	}
	out := make([]renderedField, 0, len(fields)+1)
	out = append(out, key)
	out = append(out, fields...)
	return out
}

func fieldNames(fields []renderedField) []string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.name)
	}
	return names
}

func sourceURI(filename string, headers []string, rowNumber int) string {
	escapedName := url.PathEscape(filepath.Base(filename))
	params := make([]string, 0, len(headers)+1)
	params = append(params, fmt.Sprintf("rows=%d", rowNumber))
	for _, header := range headers {
		params = append(params, "columns="+url.QueryEscape(header))
	}
	return escapedName + "#" + strings.Join(params, "&")
}

func headerAt(headers []string, idx int) string {
	if idx >= 0 && idx < len(headers) {
		return headers[idx]
	}
	return fmt.Sprintf("extra_%d", idx+1)
}

func safeField(value string) string {
	value = strings.ReplaceAll(value, "|", "/")
	return strings.TrimSpace(value)
}

func truncateCell(value string) string {
	runes := []rune(value)
	if len(runes) <= maxRenderedCellRunes {
		return value
	}
	return string(runes[:maxRenderedCellRunes]) + "..."
}
