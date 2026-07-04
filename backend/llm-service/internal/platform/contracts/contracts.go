// Package contracts holds the cross-service constants that are not types: the
// AMQP exchange/queue/routing names and the document lifecycle status strings.
// The message and DTO *types* live in the generated protobuf packages
// (pkg/genproto); keeping the wiring constants here avoids duplicating magic
// strings across producers and consumers.
package contracts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// AMQP exchanges.
const (
	// ExchangeIngestion is a topic exchange: main-service publishes uploads with
	// a routing key selecting the parser; parsers publish to RouteChunk.
	ExchangeIngestion = "ingestion"
	// ExchangeEvents is a fanout exchange carrying ingestion progress events.
	ExchangeEvents = "events"
	// ExchangeDLX is the dead-letter exchange for rejected messages.
	ExchangeDLX = "ingestion.dlx"
	// QueueDead parks dead-lettered messages for inspection.
	QueueDead = "dead"
)

// Routing keys double as durable queue names bound to ExchangeIngestion.
const (
	RouteParseEmail  = "parse.email"   // .eml / .msg
	RouteParseOffice = "parse.office"  // .docx / .xlsx / .pptx
	RouteParsePDF    = "parse.pdf"     // text PDFs
	RouteParseOCR    = "parse.ocr"     // scanned PDFs and images
	RouteParseVLM    = "parse.vlm"     // image understanding
	RouteParseArch   = "parse.archive" // zip/rar/7z/tar -> archive-worker
	RouteParseDB     = "parse.db"      // SQLite databases -> db-parser
	RouteChunk       = "chunk"         // parsed text -> chunk-splitter
)

// Document lifecycle statuses (stored as plain strings in PostgreSQL and carried
// in the status fields of the protobuf messages).
const (
	StatusUploaded = "uploaded"
	StatusQueued   = "queued"
	StatusParsing  = "parsing"
	// StatusOCR marks a document rerouted to the OCR engine (scanned PDF or
	// image): still parsing, but the slow branch the UI shows as its own step.
	StatusOCR      = "ocr"
	StatusParsed   = "parsed"
	StatusChunking = "chunking"
	StatusIndexed  = "indexed"
	StatusFailed   = "failed"
)

// redeliveredKey marks a consumed message's context as a broker redelivery.
type redeliveredKey struct{}

// WithRedelivered stamps ctx with the delivery's redelivered flag. The
// messaging consumer sets it for every dispatched message.
func WithRedelivered(ctx context.Context, redelivered bool) context.Context {
	return context.WithValue(ctx, redeliveredKey{}, redelivered)
}

// Redelivered reports whether the current message is a broker redelivery —
// the final attempt after the automatic requeue-once. Handlers use it to defer
// surfacing "failed" until no retry is pending.
func Redelivered(ctx context.Context) bool {
	v, _ := ctx.Value(redeliveredKey{}).(bool)
	return v
}

// ParserQueues maps each queue bound to ExchangeIngestion to its binding key.
// messaging.DeclareTopology uses it so the wiring lives in one place.
func ParserQueues() map[string]string {
	return map[string]string{
		RouteParseEmail:  RouteParseEmail,
		RouteParseOffice: RouteParseOffice,
		RouteParsePDF:    RouteParsePDF,
		RouteParseOCR:    RouteParseOCR,
		RouteParseVLM:    RouteParseVLM,
		RouteParseArch:   RouteParseArch,
		RouteParseDB:     RouteParseDB,
		RouteChunk:       RouteChunk,
	}
}

// RouteForMIME maps a detected MIME type (and filename, as a tie-breaker for
// e-mail and archives) to the ingestion routing key. It is the single routing
// truth shared by main-service (uploads) and archive-worker (extracted files).
func RouteForMIME(mimeType, filename string) string {
	mt := strings.ToLower(mimeType)
	ext := strings.ToLower(filepath.Ext(filename))

	switch {
	case isArchiveMIME(mt, ext):
		return RouteParseArch
	case strings.Contains(mt, "pdf"):
		return RouteParsePDF
	case mt == "message/rfc822" || ext == ".eml" || ext == ".msg":
		return RouteParseEmail
	case strings.HasPrefix(mt, "image/"):
		// IMAGE_PARSE_ROUTE=vlm шлёт картинки в vlm-service (мультимодальное
		// описание схем/диаграмм с топологией) вместо OCR (только текст со
		// страницы). Дефолт — OCR: сканы страниц встречаются чаще схем.
		if strings.EqualFold(os.Getenv("IMAGE_PARSE_ROUTE"), "vlm") {
			return RouteParseVLM
		}
		return RouteParseOCR
	case isSQLiteMIME(mt, ext):
		return RouteParseDB
	default:
		// Office and unknown binary formats go to office-parser, which itself
		// can defer to Tika when configured.
		return RouteParseOffice
	}
}

// isSQLiteMIME reports SQLite database files for the db-parser. The sniffer
// yields application/vnd.sqlite3 (or x-sqlite3); the extension fallback covers
// undetected containers, mirroring isArchiveMIME.
func isSQLiteMIME(mt, ext string) bool {
	if strings.Contains(mt, "sqlite") {
		return true
	}
	switch ext {
	case ".db", ".sqlite", ".sqlite3":
		return mt == "application/octet-stream" || mt == ""
	default:
		return false
	}
}

// isArchiveMIME reports container formats that must be unpacked before
// ingestion. OOXML (docx/xlsx/pptx) is zip inside, but MIME detection reports
// it as its own specific types, so plain application/zip really is an archive.
func isArchiveMIME(mt, ext string) bool {
	switch {
	case mt == "application/zip",
		strings.Contains(mt, "x-rar"),
		strings.Contains(mt, "x-7z"),
		strings.Contains(mt, "x-tar"),
		mt == "application/gzip":
		return true
	}
	switch ext {
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".tgz":
		return mt == "application/octet-stream" || mt == ""
	default:
		return false
	}
}
