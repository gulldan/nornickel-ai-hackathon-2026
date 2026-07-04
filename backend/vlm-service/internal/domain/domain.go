// Package domain defines vlm-service's ports. Like every parser worker its
// domain is small: it turns raw image bytes into descriptive text. Keeping the
// port here (and the concrete VLM client in infrastructure) lets the
// application orchestration be unit-tested with a fake describer and keeps the
// AI client dependency at the edge.
package domain

import "context"

// ImageDescriber turns raw image bytes into a textual description suitable for
// document retrieval. The MIME type is passed through so the backend can pick
// the right decoder.
type ImageDescriber interface {
	Describe(ctx context.Context, data []byte, mime string) (string, error)
}
