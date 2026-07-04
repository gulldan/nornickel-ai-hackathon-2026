// Package domain defines email-parser's ports. A parser's domain is small: it
// turns raw bytes into text. Keeping the port here (and the concrete extractor
// in infrastructure) lets the application orchestration be unit-tested with a
// fake extractor and keeps the email/Tika parsing details at the edge.
package domain

import "context"

// TextExtractor turns raw document bytes into plain UTF-8 text. For email this
// means decoding .eml messages (headers + text body + attachment names) or, for
// .msg files, delegating to Tika when configured.
type TextExtractor interface {
	Extract(ctx context.Context, data []byte) (string, error)
}

// ExtractedEmail is an extraction result with the message's metadata: Author is
// the From header, PublishedAt the Date header (RFC3339 when it parsed, raw
// otherwise). Empty strings mean the header was absent.
type ExtractedEmail struct {
	Text        string
	Author      string
	PublishedAt string
}

// MetaExtractor is the optional capability of yielding email metadata alongside
// the text. The application processor type-asserts an extractor against it;
// extractors that cannot provide metadata simply do not implement it.
type MetaExtractor interface {
	ExtractWithMeta(ctx context.Context, data []byte) (ExtractedEmail, error)
}
