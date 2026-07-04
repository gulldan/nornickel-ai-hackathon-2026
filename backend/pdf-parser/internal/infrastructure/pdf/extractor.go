// Package pdf implements domain.TextExtractor for PDF files using the pure-Go
// ledongthuc/pdf library (no native deps, so the distroless image stays tiny).
// Extraction is guarded against panics because the library can choke on
// malformed PDFs in the wild.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"

	pdflib "github.com/ledongthuc/pdf"
)

// Extractor extracts the text layer from a PDF.
type Extractor struct{}

// NewExtractor builds an Extractor.
func NewExtractor() *Extractor { return &Extractor{} }

// Extract returns the concatenated plain text of every page. An empty string
// with a nil error means the PDF has no text layer (a scan) — the caller routes
// those to OCR.
func (e *Extractor) Extract(_ context.Context, data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf parsing panicked: %v", r)
		}
	}()

	reader, err := pdflib.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}
	tr, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("get plain text: %w", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, tr); err != nil {
		return "", fmt.Errorf("read pdf text: %w", err)
	}
	return buf.String(), nil
}
