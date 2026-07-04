// Package domain defines archive-worker's ports. The worker's domain is small:
// it turns one stored archive object into the list of extracted entries that
// the extraction backend (archive-scan) has already written to object storage.
package domain

import "context"

// Entry is one extracted file: where it landed in object storage and what the
// backend learned about it.
type Entry struct {
	// Path is the sanitized path inside the archive (used for the filename).
	Path string
	// ObjectKey is the S3 key the backend stored the file under.
	ObjectKey string
	// MIMEType as detected by the extraction backend.
	MIMEType string
	Size     int64
	// NestedArchive marks entries that are themselves archives.
	NestedArchive bool
	// Hash is the BLAKE3-256 hex of the entry bytes (archive-scan full_hash).
	Hash string
}

// Extraction is the outcome for one archive.
type Extraction struct {
	Entries     []Entry
	TotalFiles  int64
	StoredBytes int64
	// Skipped counts entries that the backend reported but that the adapter
	// could not turn into a usable object key (e.g. a malformed stored-object
	// URI). They are dropped rather than failing the whole extraction.
	Skipped int
	// SkippedOversize counts entries the backend dropped because their
	// uncompressed size exceeded the per-file cap (ARCHIVE_MAX_FILE_MB). Like
	// Skipped, these do not fail the archive.
	SkippedOversize int
}

// Extractor unpacks a stored archive into object storage and reports the
// entries (satisfied by the archive-scan HTTP adapter).
type Extractor interface {
	// Extract reads the archive object and extracts it under an
	// extraction-scoped prefix. extractionID must be stable per document so
	// redeliveries are idempotent.
	Extract(ctx context.Context, objectKey, filename, extractionID string) (*Extraction, error)
}
