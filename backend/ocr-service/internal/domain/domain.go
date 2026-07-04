// Package domain defines ocr-service's ports. Like every parser, its domain is
// small: it turns raw bytes into text. The port carries the MIME type because an
// OCR engine needs to know whether it is decoding a scanned PDF or an image.
// Keeping the port here (and the concrete extractor in infrastructure) lets the
// application orchestration be unit-tested with a fake extractor and keeps the
// OCR client dependency at the edge.
package domain

import "context"

// Extraction is one OCR result. Text is the full recognized text; Pages holds
// the per-page texts in reading order when the backend reports page structure
// (empty otherwise). When Pages is present the processor derives the emitted
// text and the page offsets from it, so provenance always matches the text.
type Extraction struct {
	Text  string
	Pages []string
}

// TextExtractor turns raw document bytes into plain UTF-8 text. The mime hint is
// forwarded to the OCR backend so it can select the right decoder. Unlike the
// PDF parser an empty result is not special-cased: OCR is the terminal parser,
// so whatever text (or placeholder) comes back is forwarded to chunk-splitter.
type TextExtractor interface {
	Extract(ctx context.Context, data []byte, mime string) (Extraction, error)
}
