// Package application contains pdf-parser's use case: consume a DocumentUploaded
// event, extract text, and either forward it to chunk-splitter or — for scanned
// PDFs with no text layer — re-route the job to OCR. It depends only on small
// ports (object store, status updater, publisher), so it stays storage- and
// transport-agnostic. Every parser worker follows this same shape.
package application

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/example/pdf-parser/internal/domain"
	"github.com/example/pdf-parser/internal/platform/contracts"
	"github.com/example/pdf-parser/internal/platform/logger"

	commonv1 "github.com/example/pdf-parser/internal/platform/genproto/common/v1"
)

// source labels the DocumentParsed events this worker emits.
const source = "pdf"

// inlineTextMax is the largest extracted text sent inline in the AMQP body.
// Anything bigger goes to S3 as a claim check (text_object_key) — RabbitMQ
// caps message size at 128MB and huge bodies hurt the broker well before that.
const inlineTextMax = 4 << 20

// ObjectStore fetches the original file bytes and stores oversized extracted
// text for the claim-check handoff (satisfied by platform/storage).
type ObjectStore interface {
	GetBytes(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
}

// StatusUpdater advances the document's ingestion state (satisfied by platform/dbclient).
type StatusUpdater interface {
	UpdateDocumentStatus(ctx context.Context, id, status, message string, chunkCount *int32) error
}

// Publisher emits downstream protobuf messages (satisfied by platform/messaging).
type Publisher interface {
	PublishProto(ctx context.Context, exchange, routingKey string, msg proto.Message) error
}

// Processor wires the use case dependencies.
type Processor struct {
	extractor domain.TextExtractor
	store     ObjectStore
	status    StatusUpdater
	pub       Publisher
}

// New constructs a Processor.
func New(extractor domain.TextExtractor, store ObjectStore, status StatusUpdater, pub Publisher) *Processor {
	return &Processor{extractor: extractor, store: store, status: status, pub: pub}
}

// Process handles one upload event end to end. A returned error requeues the
// message once and then dead-letters it; only the final attempt marks the
// document failed.
func (p *Processor) Process(ctx context.Context, evt *commonv1.DocumentUploaded) error {
	log := logger.From(ctx).With().Str("document_id", evt.GetDocumentId()).Str("source", source).Logger()
	p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusParsing, "")
	p.emit(ctx, evt, contracts.StatusParsing, "")

	data, err := p.store.GetBytes(ctx, evt.GetObjectKey())
	if err != nil {
		return p.fail(ctx, evt, fmt.Errorf("download %s: %w", evt.GetObjectKey(), err))
	}
	doc, err := p.extract(ctx, data)
	if err != nil {
		return p.fail(ctx, evt, fmt.Errorf("extract text: %w", err))
	}
	text := doc.Text

	// A PDF with no text layer is a scan: hand it to OCR rather than indexing
	// nothing, and surface the OCR stage instead of a dangling "parsing".
	if strings.TrimSpace(text) == "" {
		log.Info().Msg("no text layer, routing to OCR")
		if perr := p.pub.PublishProto(ctx, contracts.ExchangeIngestion, contracts.RouteParseOCR, evt); perr != nil {
			return p.fail(ctx, evt, fmt.Errorf("route to ocr: %w", perr))
		}
		p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusOCR, "no text layer; routed to OCR")
		p.emit(ctx, evt, contracts.StatusOCR, "no text layer; routed to OCR")
		return nil
	}

	parsed := &commonv1.DocumentParsed{
		DocumentId: evt.GetDocumentId(), OwnerId: evt.GetOwnerId(), ObjectKey: evt.GetObjectKey(),
		Filename: evt.GetFilename(), MimeType: evt.GetMimeType(), Source: source, Text: text,
		Metadata: documentMetadata(doc), TraceId: evt.GetTraceId(),
	}
	if len(text) > inlineTextMax {
		key := "parsed/" + evt.GetDocumentId() + ".txt"
		if perr := p.store.Put(ctx, key, strings.NewReader(text), int64(len(text)), "text/plain; charset=utf-8"); perr != nil {
			return p.fail(ctx, evt, fmt.Errorf("store parsed text: %w", perr))
		}
		parsed.Text, parsed.TextObjectKey = "", key
		log.Info().Int("chars", len(text)).Str("text_object_key", key).Msg("text offloaded to object store")
	}
	if perr := p.pub.PublishProto(ctx, contracts.ExchangeIngestion, contracts.RouteChunk, parsed); perr != nil {
		return p.fail(ctx, evt, fmt.Errorf("publish parsed: %w", perr))
	}
	p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusParsed, "")
	p.emit(ctx, evt, contracts.StatusParsed, "")
	log.Info().Int("chars", len(text)).Int("pages", len(doc.PageOffsets)).Msg("parsed")
	return nil
}

// extract runs the extractor, preferring its layout-aware capability (which
// yields page offsets in one pass) and otherwise falling back to plain text with
// no page boundaries.
func (p *Processor) extract(ctx context.Context, data []byte) (domain.ExtractedDocument, error) {
	if le, ok := p.extractor.(domain.LayoutExtractor); ok {
		return le.ExtractWithLayout(ctx, data)
	}
	text, err := p.extractor.Extract(ctx, data)
	if err != nil {
		return domain.ExtractedDocument{}, err
	}
	return domain.ExtractedDocument{Text: text}, nil
}

// documentMetadata combines the page/section boundaries with the docinfo
// metadata (author/published_at, labelled source_ref="pdf"); nil when neither
// is known so the keys are omitted entirely.
func documentMetadata(doc domain.ExtractedDocument) map[string]string {
	md := pageMetadata(doc)
	if doc.Author == "" && doc.PublishedAt == "" {
		return md
	}
	if md == nil {
		md = map[string]string{}
	}
	md["author"], md["published_at"], md["source_ref"] = doc.Author, doc.PublishedAt, "pdf"
	return md
}

// pageMetadata renders a document's page (and, when present, section) boundaries
// into the DocumentParsed.metadata map per the chunk-splitter contract:
//
//	page_offsets    JSON array of rune offsets, [0]==0, strictly increasing
//	page_count      decimal string, == len(page_offsets)
//	section_offsets JSON array of {"rune":int,"heading":string} (only if any)
//
// When page boundaries are unknown it returns nil so the keys are omitted
// entirely (never an empty array).
func pageMetadata(doc domain.ExtractedDocument) map[string]string {
	if len(doc.PageOffsets) == 0 {
		return nil
	}
	offsets, err := json.Marshal(doc.PageOffsets)
	if err != nil {
		return nil // offsets are []int, so this cannot happen; stay defensive
	}
	md := map[string]string{
		"page_offsets": string(offsets),
		"page_count":   strconv.Itoa(len(doc.PageOffsets)),
	}
	if len(doc.SectionOffsets) > 0 {
		if sections, serr := json.Marshal(doc.SectionOffsets); serr == nil {
			md["section_offsets"] = string(sections)
		}
	}
	return md
}

// fail records a processing failure. On the first delivery the broker requeues
// the message for one retry, so the document must not surface as failed yet;
// only the final (redelivered) attempt writes the failed status and event.
func (p *Processor) fail(ctx context.Context, evt *commonv1.DocumentUploaded, err error) error {
	if !contracts.Redelivered(ctx) {
		logger.From(ctx).Warn().Err(err).Str("document_id", evt.GetDocumentId()).
			Msg("processing failed; retry pending")
		return err
	}
	logger.From(ctx).Error().Err(err).Str("document_id", evt.GetDocumentId()).Msg("processing failed")
	p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusFailed, err.Error())
	p.emit(ctx, evt, contracts.StatusFailed, err.Error())
	return err
}

func (p *Processor) setStatus(ctx context.Context, id, status, msg string) {
	if err := p.status.UpdateDocumentStatus(ctx, id, status, msg, nil); err != nil {
		logger.From(ctx).Warn().Err(err).Str("document_id", id).Msg("update status failed")
	}
}

func (p *Processor) emit(ctx context.Context, evt *commonv1.DocumentUploaded, status, msg string) {
	ev := &commonv1.IngestionEvent{
		DocumentId: evt.GetDocumentId(), OwnerId: evt.GetOwnerId(),
		Status: status, Message: msg, Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := p.pub.PublishProto(ctx, contracts.ExchangeEvents, "", ev); err != nil {
		logger.From(ctx).Warn().Err(err).Msg("emit event failed")
	}
}
