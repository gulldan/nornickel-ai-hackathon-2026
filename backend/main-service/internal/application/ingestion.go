// Package application implements main-service's use cases: ingesting uploaded
// documents and answering chat messages via the RAG pipeline. Each service
// depends only on the domain ports, so the orchestration logic stays free of
// HTTP, S3, RabbitMQ and gRPC-client concerns (those live in infrastructure
// and the platform library).
package application

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"lukechampine.com/blake3"

	"github.com/example/main-service/internal/domain"
	"github.com/example/main-service/internal/platform/contracts"
	"github.com/example/main-service/internal/platform/dbclient"
	"github.com/example/main-service/internal/platform/httpx"
	"github.com/example/main-service/internal/platform/logger"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	llmv1 "github.com/example/main-service/internal/platform/genproto/llm/v1"
)

// IngestionService accepts uploads, stores them, records them in db-service and
// publishes the right DocumentUploaded event to start the parsing pipeline.
type IngestionService struct {
	store  domain.ObjectStore
	docs   domain.DocumentCatalog
	pub    domain.EventPublisher
	chunks domain.ChunkReader
}

// NewIngestionService wires the ingestion dependencies.
func NewIngestionService(
	store domain.ObjectStore,
	docs domain.DocumentCatalog,
	pub domain.EventPublisher,
	chunks domain.ChunkReader,
) *IngestionService {
	return &IngestionService{store: store, docs: docs, pub: pub, chunks: chunks}
}

// Upload runs the full upload use case: stream the content into the object
// store under "<ownerID>/<docID>/<filename>", create the document row, publish
// the upload event onto the ingestion exchange with a MIME-derived routing key,
// and mark the document queued. It returns the created document.
func (s *IngestionService) Upload(
	ctx context.Context, cmd domain.UploadCommand, mimeType string,
) (*commonv1.Document, bool, error) {
	docID := uuid.NewString()
	key := fmt.Sprintf("%s/%s/%s", cmd.OwnerID, docID, cmd.Filename)
	size := cmd.Size

	if err := s.store.Put(ctx, key, cmd.Body, size, mimeType); err != nil {
		return nil, false, fmt.Errorf("store object: %w", err)
	}

	return s.FinalizeObject(ctx, FinalizeRequest{
		OwnerID:       cmd.OwnerID,
		Filename:      cmd.Filename,
		MIMEType:      mimeType,
		ObjectKey:     key,
		Size:          size,
		ContentHash:   cmd.Hash,
		InlineText:    cmd.Text,
		ReuseExisting: cmd.Reuse,
	})
}

// FinalizeRequest describes a stored object ready to enter the pipeline,
// whether the bytes came from a plain upload or a multipart upload to S3.
type FinalizeRequest struct {
	OwnerID     string
	Filename    string
	MIMEType    string
	ObjectKey   string
	Size        int64
	ContentHash string
	// InlineText, when set, sends text/plain straight to the chunk-splitter,
	// bypassing the parsers (small text files are read at intake).
	InlineText string
	// ReuseExisting makes a hash-hit return the already indexed document
	// instead of registering a duplicate row (goal uploads attach the original).
	ReuseExisting bool
}

// FinalizeObject runs the post-storage half of ingestion: dedup, the document
// row and the pipeline dispatch. The bool reports a hash-hit on already
// indexed content.
func (s *IngestionService) FinalizeObject(
	ctx context.Context, req FinalizeRequest,
) (*commonv1.Document, bool, error) {
	log := logger.From(ctx)

	// A byte-for-byte identical document is already in the corpus: register a
	// row for the owner (so the upload isn't "lost") but don't enqueue it —
	// re-indexing would produce duplicates in results.
	if req.ContentHash != "" {
		if dup, derr := s.docs.FindDocumentByHash(ctx, req.OwnerID, req.ContentHash); derr == nil {
			if req.ReuseExisting {
				log.Info().
					Str("document_id", dup.GetId()).
					Msg("duplicate upload reuses existing document")
				return dup, true, nil
			}
			d, rerr := s.registerDuplicate(ctx, req, dup)
			return d, true, rerr
		} else if !errors.Is(derr, dbclient.ErrNotFound) {
			log.Warn().Err(derr).Msg("dedup lookup failed; indexing anyway")
		}
	}

	doc, err := s.docs.CreateDocument(ctx, &dbv1.CreateDocumentRequest{
		OwnerId:     req.OwnerID,
		Filename:    req.Filename,
		MimeType:    req.MIMEType,
		Size:        req.Size,
		ObjectKey:   req.ObjectKey,
		ContentHash: req.ContentHash,
	})
	if err != nil {
		return nil, false, fmt.Errorf("create document: %w", err)
	}

	// Route the job to the correct parser based on MIME (with filename as a
	// tie-breaker for email). text/plain with inline text skips straight to
	// the chunk-splitter.
	if derr := s.dispatch(ctx, doc, req.InlineText); derr != nil {
		// Roll the document forward to "failed" so the caller/UI sees why; the
		// row already exists, so surfacing the error is the most useful thing.
		s.setStatus(ctx, doc.GetId(), contracts.StatusFailed, derr.Error())
		return nil, false, derr
	}

	s.setStatus(ctx, doc.GetId(), contracts.StatusQueued, "")
	doc.Status = contracts.StatusQueued
	log.Info().
		Str("document_id", doc.GetId()).
		Str("mime_type", req.MIMEType).
		Int64("size", req.Size).
		Msg("document queued")
	return doc, false, nil
}

// IngestTextIfNew uploads a small text document unless byte-identical content
// is already in the owner's corpus; unlike Upload it skips silently instead of
// registering a duplicate row.
func (s *IngestionService) IngestTextIfNew(ctx context.Context, ownerID, filename, content string) error {
	sum := blake3.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	if _, err := s.docs.FindDocumentByHash(ctx, ownerID, hash); err == nil {
		return nil
	} else if !errors.Is(err, dbclient.ErrNotFound) {
		return err
	}
	_, _, err := s.Upload(ctx, domain.UploadCommand{
		OwnerID:  ownerID,
		Filename: filename,
		Size:     int64(len(content)),
		Body:     strings.NewReader(content),
		Text:     content,
		Hash:     hash,
	}, "text/plain; charset=utf-8")
	return err
}

// dispatch publishes the appropriate event to begin ingestion for doc.
func (s *IngestionService) dispatch(ctx context.Context, doc *commonv1.Document, inlineText string) error {
	// text/plain is already "parsed": forward the raw text directly to the
	// chunk-splitter instead of routing it through a parser worker. Large
	// text/plain (multipart path, text not read) takes the normal route.
	if isPlainText(doc.GetMimeType()) && inlineText != "" {
		parsed := &commonv1.DocumentParsed{
			DocumentId: doc.GetId(),
			OwnerId:    doc.GetOwnerId(),
			ObjectKey:  doc.GetObjectKey(),
			Filename:   doc.GetFilename(),
			MimeType:   doc.GetMimeType(),
			Source:     "text",
			Text:       inlineText,
			Metadata:   nil,
			TraceId:    traceID(ctx),
		}
		if err := s.pub.PublishProto(ctx, contracts.ExchangeIngestion, contracts.RouteChunk, parsed); err != nil {
			return fmt.Errorf("publish parsed text: %w", err)
		}
		return nil
	}

	evt := &commonv1.DocumentUploaded{
		DocumentId: doc.GetId(),
		OwnerId:    doc.GetOwnerId(),
		ObjectKey:  doc.GetObjectKey(),
		Filename:   doc.GetFilename(),
		MimeType:   doc.GetMimeType(),
		Size:       doc.GetSize(),
		TraceId:    traceID(ctx),
	}
	route := contracts.RouteForMIME(doc.GetMimeType(), doc.GetFilename())
	if err := s.pub.PublishProto(ctx, contracts.ExchangeIngestion, route, evt); err != nil {
		return fmt.Errorf("publish upload event to %s: %w", route, err)
	}
	return nil
}

// RequeueStuck re-dispatches documents stuck in the given status longer than
// olderThan (lost broker events), returning how many were re-published.
func (s *IngestionService) RequeueStuck(
	ctx context.Context, status string, olderThan time.Duration, limit int,
) (int, error) {
	docs, err := s.docs.ListDocuments(ctx, "")
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	n := 0
	for _, d := range docs {
		if d.GetStatus() != status {
			continue
		}
		if ts, terr := time.Parse(time.RFC3339Nano, d.GetUpdatedAt()); terr == nil && ts.After(cutoff) {
			continue
		}
		if derr := s.dispatch(ctx, d, ""); derr != nil {
			logger.From(ctx).Warn().Err(derr).Str("document_id", d.GetId()).Msg("requeue dispatch failed")
			continue
		}
		n++
		if limit > 0 && n >= limit {
			break
		}
	}
	return n, nil
}

func (s *IngestionService) setStatus(ctx context.Context, id, status, msg string) {
	if err := s.docs.UpdateDocumentStatus(ctx, id, status, msg, nil); err != nil {
		logger.From(ctx).Warn().Err(err).Str("document_id", id).Msg("update document status failed")
	}
}

// ListDocuments returns the owner's documents.
func (s *IngestionService) ListDocuments(ctx context.Context, ownerID string) ([]*commonv1.Document, error) {
	return s.docs.ListDocuments(ctx, ownerID)
}

// GetDocument returns a single document owned by ownerID, or ErrForbidden if it
// belongs to someone else.
func (s *IngestionService) GetDocument(ctx context.Context, ownerID, id string) (*commonv1.Document, error) {
	doc, err := s.docs.GetDocument(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc.GetOwnerId() != ownerID {
		return nil, ErrForbidden
	}
	return doc, nil
}

// registerDuplicate records an upload whose bytes already exist in the corpus:
// the row is created (the owner sees their upload), immediately marked indexed
// with a pointer to the original, and never enters the parse pipeline.
func (s *IngestionService) registerDuplicate(
	ctx context.Context, req FinalizeRequest, original *commonv1.Document,
) (*commonv1.Document, error) {
	doc, err := s.docs.CreateDocument(ctx, &dbv1.CreateDocumentRequest{
		OwnerId:     req.OwnerID,
		Filename:    req.Filename,
		MimeType:    req.MIMEType,
		Size:        req.Size,
		ObjectKey:   req.ObjectKey,
		ContentHash: req.ContentHash,
	})
	if err != nil {
		return nil, fmt.Errorf("create duplicate document: %w", err)
	}
	msg := fmt.Sprintf("дубликат: содержимое уже проиндексировано как «%s»", original.GetFilename())
	s.setStatus(ctx, doc.GetId(), contracts.StatusIndexed, msg)
	doc.Status = contracts.StatusIndexed
	doc.StatusMsg = msg
	logger.From(ctx).Info().
		Str("document_id", doc.GetId()).
		Str("duplicate_of", original.GetId()).
		Msg("duplicate upload skipped")
	return doc, nil
}

// DocumentChunks returns the indexed chunks of a document for the preview
// pane. Privileged callers (operator/admin) may read any document; everyone
// else only their own. The chunks are fetched under the document owner's id,
// because that is how chunk-splitter scoped them in the stores.
func (s *IngestionService) DocumentChunks(
	ctx context.Context, requesterID string, privileged bool, documentID string,
) (*commonv1.Document, []*llmv1.DocumentChunk, error) {
	doc, err := s.docs.GetDocument(ctx, documentID)
	if err != nil {
		return nil, nil, err
	}
	if !privileged && doc.GetOwnerId() != requesterID {
		return nil, nil, ErrForbidden
	}
	chunks, err := s.chunks.DocumentChunks(ctx, doc.GetOwnerId(), documentID)
	if err != nil {
		return nil, nil, fmt.Errorf("document chunks: %w", err)
	}
	if len(chunks) == 0 && doc.GetContentHash() != "" {
		orig, derr := s.docs.FindDocumentByHash(ctx, doc.GetOwnerId(), doc.GetContentHash())
		if derr == nil && orig.GetId() != doc.GetId() {
			if oc, cerr := s.chunks.DocumentChunks(ctx, orig.GetOwnerId(), orig.GetId()); cerr == nil {
				return doc, oc, nil
			}
		}
	}
	return doc, chunks, nil
}

// OpenDocument streams the original stored file for a document the requester may
// see (its owner, or anyone when the corpus is shared/privileged). The caller
// must close the returned reader. It powers the in-app document preview.
func (s *IngestionService) OpenDocument(
	ctx context.Context, requesterID string, privileged bool, documentID string,
) (io.ReadCloser, *commonv1.Document, error) {
	doc, err := s.docs.GetDocument(ctx, documentID)
	if err != nil {
		return nil, nil, err
	}
	if !privileged && doc.GetOwnerId() != requesterID {
		return nil, nil, ErrForbidden
	}
	rc, err := s.store.Get(ctx, doc.GetObjectKey())
	if err != nil {
		return nil, nil, fmt.Errorf("open object: %w", err)
	}
	return rc, doc, nil
}

// ListAllDocuments returns every document in the corpus (operator/admin view).
func (s *IngestionService) ListAllDocuments(ctx context.Context) ([]*commonv1.Document, error) {
	return s.docs.ListDocuments(ctx, "")
}

// isPlainText reports whether the MIME type is text/plain (ignoring any
// "; charset=..." suffix mimetype may append).
func isPlainText(mt string) bool {
	return strings.HasPrefix(strings.ToLower(mt), "text/plain")
}

// traceID reuses the inbound request id (if any) as the pipeline trace id so a
// document can be followed across services; it falls back to a fresh uuid.
func traceID(ctx context.Context) string {
	if id := httpx.RequestIDFromContext(ctx); id != "" {
		return id
	}
	return uuid.NewString()
}
