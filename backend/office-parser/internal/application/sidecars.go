package application

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/example/office-parser/internal/domain"

	commonv1 "github.com/example/office-parser/internal/platform/genproto/common/v1"
)

const defaultSidecarContentType = "text/plain; charset=utf-8"

func (p *Processor) storeSidecars(
	ctx context.Context,
	evt *commonv1.DocumentUploaded,
	parsed *commonv1.DocumentParsed,
	sidecars []domain.SidecarArtifact,
) error {
	for i, sidecar := range sidecars {
		if sidecar.Text == "" {
			continue
		}
		name := safeSidecarName(sidecar.Name, i)
		key := fmt.Sprintf("parsed/%s.%s", evt.GetDocumentId(), name)
		contentType := strings.TrimSpace(sidecar.ContentType)
		if contentType == "" {
			contentType = defaultSidecarContentType
		}
		if err := p.store.Put(ctx, key, strings.NewReader(sidecar.Text), int64(len(sidecar.Text)), contentType); err != nil {
			return fmt.Errorf("store parser sidecar %q: %w", name, err)
		}
		ensureMetadata(parsed)[sidecarMetadataKey(name, i, "object_key")] = key
		parsed.Metadata[sidecarMetadataKey(name, i, "content_type")] = contentType
	}
	return nil
}

func copyMetadata(metadata map[string]string) map[string]string {
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

func ensureMetadata(parsed *commonv1.DocumentParsed) map[string]string {
	if parsed.Metadata == nil {
		parsed.Metadata = make(map[string]string)
	}
	return parsed.Metadata
}

func safeSidecarName(name string, index int) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == "/" || base == "" {
		base = fmt.Sprintf("artifact-%d.txt", index+1)
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	cleaned := strings.Trim(b.String(), ".-_")
	if cleaned == "" {
		return fmt.Sprintf("artifact-%d.txt", index+1)
	}
	return cleaned
}

func sidecarMetadataKey(name string, index int, suffix string) string {
	token := sidecarToken(name)
	if token == "" {
		token = fmt.Sprintf("sidecar_%d", index+1)
	}
	return token + "_" + suffix
}

func sidecarToken(name string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{".json", ".txt", ".ndjson"} {
		base = strings.TrimSuffix(base, suffix)
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range base {
		isWord := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isWord {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
