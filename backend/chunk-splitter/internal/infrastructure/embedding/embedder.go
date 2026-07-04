// Package embedding adapts platform/aiclients.Embedder to the application's
// Embedder port. It is a thin pass-through; keeping the adapter here lets the
// application layer stay decoupled from the platform client type.
package embedding

import (
	"context"

	"github.com/example/chunk-splitter/internal/platform/aiclients"
)

// Adapter wraps a platform embedder.
type Adapter struct {
	client aiclients.Embedder
}

// New builds an Adapter over the given platform embedder.
func New(client aiclients.Embedder) *Adapter {
	return &Adapter{client: client}
}

// Embed returns one vector per input text.
func (a *Adapter) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return a.client.Embed(ctx, texts)
}
