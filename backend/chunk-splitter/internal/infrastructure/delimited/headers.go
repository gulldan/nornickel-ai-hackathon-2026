package delimited

import (
	"fmt"
	"strings"
)

func normalizeHeaders(raw []string) []string {
	headers := make([]string, len(raw))
	names := newHeaderNames(nil)
	for i, value := range raw {
		header := cleanCell(value)
		if header == "" {
			header = fmt.Sprintf("unnamed_%d", i+1)
		}
		headers[i] = names.Unique(header)
	}
	return headers
}

func extendHeaders(headers []string, width int) []string {
	out := append([]string(nil), headers...)
	names := newHeaderNames(out)
	for len(out) < width {
		out = append(out, names.Unique(fmt.Sprintf("extra_%d", len(out)+1)))
	}
	return out
}

type headerNames struct {
	seen map[string]struct{}
}

func newHeaderNames(existing []string) *headerNames {
	names := &headerNames{seen: map[string]struct{}{}}
	for _, header := range existing {
		names.seen[strings.ToLower(header)] = struct{}{}
	}
	return names
}

func (n *headerNames) Unique(base string) string {
	header := base
	for suffix := 1; ; suffix++ {
		key := strings.ToLower(header)
		if _, exists := n.seen[key]; !exists {
			n.seen[key] = struct{}{}
			return header
		}
		header = fmt.Sprintf("%s_%d", base, suffix+1)
	}
}
