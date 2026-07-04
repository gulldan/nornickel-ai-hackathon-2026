// Package domain defines pdf-parser's ports. A parser's domain is small: it
// turns raw bytes into text. Keeping the port here (and the concrete extractor
// in infrastructure) lets the application orchestration be unit-tested with a
// fake extractor and keeps the PDF library dependency at the edge.
package domain

import "context"

// TextExtractor turns raw document bytes into plain UTF-8 text. An empty result
// (no error) means the document has no text layer and should be routed to OCR.
type TextExtractor interface {
	Extract(ctx context.Context, data []byte) (string, error)
}

// ExtractedDocument is a layout-aware extraction result: the plain text plus
// where each page (and, when available, each heading) begins within it. Offsets
// are RUNE indices into Text — the exact string the caller emits as the parsed
// text (or offloads to object storage). PageOffsets[0] is always 0, the slice is
// strictly increasing, and len(PageOffsets) is the page count; page N (1-based)
// covers runes [PageOffsets[N-1], PageOffsets[N]) with the last page running to
// end of text. PageOffsets is nil when page structure could not be determined,
// in which case the page_offsets metadata key must be omitted (never an empty
// array). SectionOffsets is nil unless headings were reliably present.
type ExtractedDocument struct {
	Text           string
	PageOffsets    []int
	SectionOffsets []SectionOffset
	// Docinfo metadata from Tika's XHTML head ("" = absent): Author from
	// pdf:docinfo:creator / dc:creator, PublishedAt from dcterms:created /
	// pdf:docinfo:created (verbatim).
	Author      string
	PublishedAt string
}

// SectionOffset marks where a heading begins, as a rune offset into the text.
type SectionOffset struct {
	Rune    int    `json:"rune"`
	Heading string `json:"heading"`
}

// LayoutExtractor is the optional capability of yielding page (and section)
// boundaries alongside the text. The application processor type-asserts an
// extractor against it; extractors that cannot provide layout simply do not
// implement it (or return ExtractedDocument.PageOffsets == nil), and the page
// metadata is then omitted.
type LayoutExtractor interface {
	ExtractWithLayout(ctx context.Context, data []byte) (ExtractedDocument, error)
}
