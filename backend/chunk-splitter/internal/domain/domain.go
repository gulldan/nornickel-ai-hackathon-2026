// Package domain defines chunk-splitter's core value objects and ports. The
// domain of a splitter is small: it turns a document's plain text into an
// ordered list of overlapping Chunks. Keeping the Splitter port here (with the
// concrete recursive implementation in infrastructure) lets the application
// orchestration be unit-tested with a fake splitter and keeps any future
// tokenizer/library dependency at the edge.
package domain

// Chunk is a contiguous slice of a document's text together with its position
// in the original sequence. Index is zero-based and monotonically increasing,
// so it can be persisted as chunk_index for stable ordering and citations.
// Section is the normalised heading of the section the chunk falls under
// (Abstract/Methods/... or their Russian equivalents), or "" when none was
// detected — best-effort provenance that helps retrieval prefer the right part
// of a paper.
type Chunk struct {
	Index   int    // position of this chunk within the document (0-based)
	Text    string // the chunk's plain UTF-8 text
	Section string // normalised enclosing section name, or "" when unknown
}

// Splitter breaks a document's text into overlapping chunks. Implementations
// decide the strategy (e.g. recursive character splitting on paragraph, then
// line, then word boundaries); the application layer only depends on this port.
type Splitter interface {
	// Split returns the ordered chunks for text. An empty or whitespace-only
	// input yields no chunks.
	Split(text string) []Chunk
}
