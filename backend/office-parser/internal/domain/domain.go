// Package domain defines office-parser's ports. A parser's domain is small: it
// turns raw bytes into text. Keeping the port here (and the concrete extractor
// in infrastructure) lets the application orchestration be unit-tested with a
// fake extractor and keeps the office (zip/xml/excelize) and Tika dependencies
// at the edge.
package domain

import "context"

// ExtractionResult is the complete parser output handed to chunk-splitter.
// Text is the primary searchable representation. Metadata is copied to
// DocumentParsed.metadata, and sidecars are stored as auxiliary objects for
// parser/eval consumers that need cell-level fidelity.
type ExtractionResult struct {
	Text     string
	Metadata map[string]string
	Sidecars []SidecarArtifact
}

// SidecarArtifact is an auxiliary parser artifact, for example a workbook
// cell-coordinate JSON sidecar.
type SidecarArtifact struct {
	Name        string
	ContentType string
	Text        string
}

// TextExtractor turns a raw office document into plain UTF-8 text. Unlike the
// PDF extractor, an office document's format must be selected by its MIME type
// or filename extension, so both are passed alongside the bytes. An empty result
// with a nil error means no text could be extracted (e.g. an empty workbook).
//
// IsPlainText reports that the document already IS plain text (txt/md/csv/…):
// the stored object can be handed downstream as-is, so the caller must not
// download it at all — on hundreds of megabytes that is both an OOM and a
// pointless copy.
type TextExtractor interface {
	Extract(ctx context.Context, data []byte, filename, mimeType string) (ExtractionResult, error)
	IsPlainText(filename, mimeType string) bool
}
