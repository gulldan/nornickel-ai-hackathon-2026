// Package ocr implements domain.TextExtractor by delegating to the platform OCR
// client (the OCR engine role). When OCR_ENGINE_URL is empty the platform
// client is a deterministic stub that returns labelled placeholder text, so the
// ingestion pipeline still runs end to end without a neural backend.
package ocr

import (
	"context"
	"fmt"

	"github.com/example/ocr-service/internal/domain"
	"github.com/example/ocr-service/internal/platform/aiclients"
)

// Extractor adapts an aiclients.OCR backend to domain.TextExtractor.
type Extractor struct {
	client aiclients.OCR
}

// NewExtractor builds an Extractor over the given OCR client.
func NewExtractor(client aiclients.OCR) *Extractor {
	return &Extractor{client: client}
}

// Extract recognizes text in the document bytes, forwarding the MIME hint to the
// OCR backend so it can pick the right decoder. Per-page texts are passed
// through when the backend reports them.
func (e *Extractor) Extract(ctx context.Context, data []byte, mime string) (domain.Extraction, error) {
	res, err := e.client.Recognize(ctx, data, mime)
	if err != nil {
		return domain.Extraction{}, fmt.Errorf("ocr recognize: %w", err)
	}
	return domain.Extraction{Text: res.Text, Pages: res.Pages}, nil
}
