// Package application contains ocr-service's use case: consume a DocumentUploaded
// event (a scanned PDF or image routed to parse.ocr), recognize its text via the
// OCR backend, and forward the result to chunk-splitter. It depends only on small
// ports (object store, status updater, publisher) so it stays storage- and
// transport-agnostic. It mirrors pdf-parser, minus the no-text-layer reroute:
// OCR is the terminal parser, so its output is always forwarded.
package application

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/example/ocr-service/internal/domain"
	"github.com/example/ocr-service/internal/platform/contracts"
	"github.com/example/ocr-service/internal/platform/logger"

	commonv1 "github.com/example/ocr-service/internal/platform/genproto/common/v1"
)

// source labels the DocumentParsed events this worker emits.
const source = "ocr"

// inlineTextMax is the largest extracted text sent inline in the AMQP body.
// Anything bigger goes to S3 as a claim check (text_object_key) — RabbitMQ
// caps message size at 128MB and huge bodies hurt the broker well before that.
const inlineTextMax = 4 << 20

// pageSeparator joins per-page texts. Two runes wide, so page offsets stay
// strictly increasing (the chunk-splitter contract) even across empty pages.
const pageSeparator = "\n\n"

// statusOCRMessage is the user-facing label for the OCR stage.
const statusOCRMessage = "Распознавание сканов"

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
	// OCR is the slow branch the UI shows as its own step.
	p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusOCR, statusOCRMessage)
	p.emit(ctx, evt, contracts.StatusOCR, statusOCRMessage, 0, 0)

	data, err := p.store.GetBytes(ctx, evt.GetObjectKey())
	if err != nil {
		return p.fail(ctx, evt, fmt.Errorf("download %s: %w", evt.GetObjectKey(), err))
	}

	// The OCR backend needs the MIME type to select a decoder; pass it through.
	res, err := p.extractor.Extract(ctx, data, evt.GetMimeType())
	if err != nil {
		return p.fail(ctx, evt, fmt.Errorf("recognize text: %w", err))
	}
	text, metadata := pageText(res)
	if pages := len(res.Pages); pages > 0 {
		// Recognition finished: report full stage progress (N of N pages).
		p.emit(ctx, evt, contracts.StatusOCR, statusOCRMessage, pages, pages)
	}

	parsed := &commonv1.DocumentParsed{
		DocumentId: evt.GetDocumentId(), OwnerId: evt.GetOwnerId(), ObjectKey: evt.GetObjectKey(),
		Filename: evt.GetFilename(), MimeType: evt.GetMimeType(), Source: source, Text: text,
		Metadata: metadata, TraceId: evt.GetTraceId(),
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
	p.emit(ctx, evt, contracts.StatusParsed, "", 0, 0)
	log.Info().Int("chars", len(text)).Int("pages", len(res.Pages)).Msg("parsed")
	return nil
}

// pageText renders an extraction into the emitted text and, when per-page
// texts are known, the page metadata per the chunk-splitter contract:
//
//	page_offsets  JSON array of rune offsets, [0]==0, strictly increasing
//	page_count    decimal string, == len(page_offsets)
//
// Pages (empty ones included, so numbering matches the scan) are joined with
// pageSeparator and the offsets are derived from the same pass, so page
// provenance always matches the text. Without pages the backend's flat text is
// returned with nil metadata, omitting the keys entirely.
func pageText(res domain.Extraction) (string, map[string]string) {
	if len(res.Pages) == 0 {
		return res.Text, nil
	}
	var b strings.Builder
	offsets := make([]int, 0, len(res.Pages))
	runeLen := 0
	for i, page := range res.Pages {
		if i > 0 {
			b.WriteString(pageSeparator)
			runeLen += utf8.RuneCountInString(pageSeparator)
		}
		offsets = append(offsets, runeLen)
		b.WriteString(page)
		runeLen += utf8.RuneCountInString(page)
	}
	raw, err := json.Marshal(offsets)
	if err != nil {
		return b.String(), nil // []int cannot fail to marshal; stay defensive
	}
	return b.String(), map[string]string{
		"page_offsets": string(raw),
		"page_count":   strconv.Itoa(len(offsets)),
	}
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
	p.emit(ctx, evt, contracts.StatusFailed, err.Error(), 0, 0)
	return err
}

func (p *Processor) setStatus(ctx context.Context, id, status, msg string) {
	if err := p.status.UpdateDocumentStatus(ctx, id, status, msg, nil); err != nil {
		logger.From(ctx).Warn().Err(err).Str("document_id", id).Msg("update status failed")
	}
}

// emit broadcasts a progress event for WebSocket subscribers. It is best-effort.
// current/total carry stage progress (OCR pages); both zero means "no progress".
func (p *Processor) emit(ctx context.Context, evt *commonv1.DocumentUploaded, status, msg string, current, total int) {
	ev := &commonv1.IngestionEvent{
		DocumentId: evt.GetDocumentId(), OwnerId: evt.GetOwnerId(),
		Status: status, Message: msg, Timestamp: time.Now().UTC().Format(time.RFC3339),
		StageCurrent: int32(current), StageTotal: int32(total),
	}
	if err := p.pub.PublishProto(ctx, contracts.ExchangeEvents, "", ev); err != nil {
		logger.From(ctx).Warn().Err(err).Msg("emit event failed")
	}
}
