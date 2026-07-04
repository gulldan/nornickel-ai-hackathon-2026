// Package application contains vlm-service's use case: consume a
// DocumentUploaded event, describe the image with a vision-language model, and
// forward the resulting text to chunk-splitter. It depends only on small ports
// (object store, status updater, publisher) so it stays storage- and
// transport-agnostic. It follows the same shape as the pdf-parser exemplar.
package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/example/vlm-service/internal/domain"
	"github.com/example/vlm-service/internal/platform/contracts"
	"github.com/example/vlm-service/internal/platform/logger"

	commonv1 "github.com/example/vlm-service/internal/platform/genproto/common/v1"
)

// source labels the DocumentParsed events this worker emits.
const source = "vlm"

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
	describer domain.ImageDescriber
	store     ObjectStore
	status    StatusUpdater
	pub       Publisher
}

// New constructs a Processor.
func New(describer domain.ImageDescriber, store ObjectStore, status StatusUpdater, pub Publisher) *Processor {
	return &Processor{describer: describer, store: store, status: status, pub: pub}
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

	// Pass the MIME type through so the VLM backend can decode the image.
	text, err := p.describer.Describe(ctx, data, evt.GetMimeType())
	if err != nil {
		return p.fail(ctx, evt, fmt.Errorf("describe image: %w", err))
	}
	// Пустое описание — это провал, а не «документ с 0 чанков».
	if strings.TrimSpace(text) == "" {
		return p.fail(ctx, evt, errors.New("image description is empty"))
	}

	parsed := &commonv1.DocumentParsed{
		DocumentId: evt.GetDocumentId(), OwnerId: evt.GetOwnerId(), ObjectKey: evt.GetObjectKey(),
		Filename: evt.GetFilename(), MimeType: evt.GetMimeType(), Source: source, Text: text,
		Metadata: nil, TraceId: evt.GetTraceId(),
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
	log.Info().Int("chars", len(text)).Msg("described")
	return nil
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

// emit broadcasts a progress event for WebSocket subscribers. It is best-effort.
func (p *Processor) emit(ctx context.Context, evt *commonv1.DocumentUploaded, status, msg string) {
	ev := &commonv1.IngestionEvent{
		DocumentId: evt.GetDocumentId(), OwnerId: evt.GetOwnerId(),
		Status: status, Message: msg, Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := p.pub.PublishProto(ctx, contracts.ExchangeEvents, "", ev); err != nil {
		logger.From(ctx).Warn().Err(err).Msg("emit event failed")
	}
}
