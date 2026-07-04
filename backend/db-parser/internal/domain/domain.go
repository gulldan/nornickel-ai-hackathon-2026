// Package domain defines db-parser's port: turning a database file into
// readable text. The concrete SQLite reader lives in infrastructure so the
// application orchestration stays unit-testable with a fake.
package domain

import "context"

// TextExtractor renders a database file as plain UTF-8 text. An empty result
// with a nil error means the database holds no rows worth indexing.
type TextExtractor interface {
	Extract(ctx context.Context, data []byte, filename, mimeType string) (string, error)
}
