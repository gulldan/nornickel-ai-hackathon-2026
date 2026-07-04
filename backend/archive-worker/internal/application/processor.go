// Package application contains archive-worker's use case: consume a
// DocumentUploaded event for an archive, have the extraction backend unpack it
// straight into object storage, and register every extracted file as its own
// document — published back onto the ingestion exchange with a MIME-derived
// routing key, so each file walks the ordinary parse→chunk→index pipeline.
// Nested archives are re-queued to this same worker with an increased depth;
// depth is capped so archive bombs cannot recurse forever.
package application

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/example/archive-worker/internal/domain"
	"github.com/example/archive-worker/internal/platform/contracts"
	"github.com/example/archive-worker/internal/platform/logger"

	commonv1 "github.com/example/archive-worker/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/archive-worker/internal/platform/genproto/db/v1"
)

// maxDepth bounds nested-archive recursion: a file inside an archive inside an
// archive is the deepest the platform unpacks.
const maxDepth = 2

// registerConcurrency bounds the parallel per-entry registration fan-out
// (dedup lookup + CreateDocument + publish + status) against db-service and
// the broker.
const registerConcurrency = 8

// junkEntry reports archive noise that must not become documents.
func junkEntry(p string) bool {
	if strings.Contains(p, "__MACOSX/") {
		return true
	}
	base := path.Base(p)
	return base == "" || strings.HasPrefix(base, ".")
}

// DocumentCatalog registers extracted files and advances the archive's status
// (satisfied by platform/dbclient).
type DocumentCatalog interface {
	CreateDocument(ctx context.Context, req *dbv1.CreateDocumentRequest) (*commonv1.Document, error)
	FindDocumentByHash(ctx context.Context, ownerID, hash string) (*commonv1.Document, error)
	UpdateDocumentStatus(ctx context.Context, id, status, message string, chunkCount *int32) error
}

// Publisher emits downstream protobuf messages (satisfied by platform/messaging).
type Publisher interface {
	PublishProto(ctx context.Context, exchange, routingKey string, msg proto.Message) error
}

// Processor wires the use case dependencies.
type Processor struct {
	extractor  domain.Extractor
	docs       DocumentCatalog
	pub        Publisher
	maxEntries int // guard against pathological archives (ARCHIVE_MAX_ENTRIES)
}

// New constructs a Processor. maxEntries values < 1 fall back to 50000 — the
// spec's upper bound for one bulk drop.
func New(extractor domain.Extractor, docs DocumentCatalog, pub Publisher, maxEntries int) *Processor {
	if maxEntries < 1 {
		maxEntries = 50000
	}
	return &Processor{extractor: extractor, docs: docs, pub: pub, maxEntries: maxEntries}
}

// Process handles one archive end to end. A returned error dead-letters the
// message; failures also mark the archive document failed.
func (p *Processor) Process(ctx context.Context, evt *commonv1.DocumentUploaded) error {
	log := logger.From(ctx).With().Str("document_id", evt.GetDocumentId()).Str("source", "archive").Logger()

	if evt.GetArchiveDepth() >= maxDepth {
		msg := fmt.Sprintf("вложенные архивы глубже %d уровней не распаковываются", maxDepth)
		p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusFailed, msg, nil)
		p.emit(ctx, evt, contracts.StatusFailed, msg)
		log.Warn().Int32("depth", evt.GetArchiveDepth()).Msg("nested archive too deep")
		return nil // not poison: a retry won't help, so don't dead-letter
	}

	p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusParsing, "", nil)
	p.emit(ctx, evt, contracts.StatusParsing, "")

	// extractionID = document_id: a redelivery overwrites the same S3 keys
	// instead of spawning duplicate copies.
	res, err := p.extractor.Extract(ctx, evt.GetObjectKey(), evt.GetFilename(), evt.GetDocumentId())
	if err != nil {
		return p.fail(ctx, evt, fmt.Errorf("extract archive: %w", err))
	}
	if len(res.Entries) > p.maxEntries {
		return p.fail(ctx, evt,
			fmt.Errorf("в архиве %d файлов — больше предела %d", len(res.Entries), p.maxEntries))
	}

	// Per-entry counters. Key invariant: a SINGLE entry's failure must NOT bring
	// down the whole archive — otherwise one broken record would dead-letter the
	// parse.archive message, cancelling hundreds of files already sent into the
	// pipeline. Only a failure of the archive itself (download/unpack/archive-scan
	// response) dead-letters, and that is handled above via p.fail.
	tally := p.registerEntries(ctx, evt, res.Entries)
	// Fail only when the parent context is cancelled (shutdown or
	// HandleTimeout): registering further on a dead ctx is pointless, and the
	// message will be redelivered.
	if ctx.Err() != nil {
		return p.fail(ctx, evt, fmt.Errorf("archive processing interrupted: %w", ctx.Err()))
	}
	// Entries the archive-scan adapter could not parse (a broken stored-object
	// URI) are already dropped — count them among the service skips.
	skipped := res.Skipped + tally.skipped
	registered := int(tally.registered.Load())
	duplicates := int(tally.duplicates.Load())
	failed := int(tally.failed.Load())

	msg := fmt.Sprintf("архив распакован: %d файлов отправлено в обработку", registered)
	if failed > 0 {
		msg += fmt.Sprintf(", %d не удалось зарегистрировать", failed)
	}
	if duplicates > 0 {
		msg += fmt.Sprintf(", %d дубликатов пропущено", duplicates)
	}
	if skipped > 0 {
		msg += fmt.Sprintf(", %d служебных пропущено", skipped)
	}
	if res.SkippedOversize > 0 {
		msg += fmt.Sprintf(", %d превысили лимит размера", res.SkippedOversize)
	}
	// Status is indexed even on partial success (the service's convention for an
	// archive): there is no separate partial stage, and the archive as a message
	// is processed and not dead-lettered. The discrepancy is visible in msg and in
	// warn logs.
	count := int32(0)
	p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusIndexed, msg, &count)
	p.emit(ctx, evt, contracts.StatusIndexed, msg)
	log.Info().
		Int("registered", registered).
		Int("failed", failed).
		Int("duplicates", duplicates).
		Int("skipped", skipped).
		Int("skipped_oversize", res.SkippedOversize).
		Msg("archive unpacked")
	return nil
}

// entryTally accumulates per-entry outcomes across the concurrent fan-out.
type entryTally struct {
	registered, duplicates, failed atomic.Int64
	skipped                        int
}

// registerEntries fans the extracted entries out over a bounded worker pool:
// each entry is 4-5 independent network calls, so registering them serially
// would cost len(entries) round-trip chains. Intra-archive duplicates are
// claimed by hash BEFORE the fan-out so two identical files in one archive
// still dedup even without a serial DB-check ordering between them. Launching
// stops once the parent context dies; the caller decides what that means.
func (p *Processor) registerEntries(
	ctx context.Context, evt *commonv1.DocumentUploaded, entries []domain.Entry,
) *entryTally {
	tally := &entryTally{}
	seenHashes := make(map[string]struct{}, len(entries))
	sem := make(chan struct{}, registerConcurrency)
	var wg sync.WaitGroup
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		if junkEntry(entry.Path) {
			tally.skipped++
			continue
		}
		if entry.Hash != "" {
			if _, ok := seenHashes[entry.Hash]; ok {
				tally.duplicates.Add(1)
				continue
			}
			seenHashes[entry.Hash] = struct{}{}
		}
		wg.Add(1)
		go func(entry domain.Entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p.registerOne(ctx, evt, entry, tally)
		}(entry)
	}
	wg.Wait()
	return tally
}

// registerOne deduplicates and registers a single extracted entry, folding the
// outcome into the tally. One entry's failure (CreateDocument or a publish
// without a publisher-confirm) is a local failure: log it and move on.
func (p *Processor) registerOne(
	ctx context.Context, evt *commonv1.DocumentUploaded, entry domain.Entry, tally *entryTally,
) {
	// A byte-for-byte identical file is already indexed (BLAKE3 from
	// archive-scan) — re-indexing would yield duplicates in results.
	if p.isDuplicate(ctx, evt.GetOwnerId(), entry) {
		tally.duplicates.Add(1)
		return
	}
	if rerr := p.registerEntry(ctx, evt, entry); rerr != nil {
		tally.failed.Add(1)
		logger.From(ctx).Warn().Err(rerr).Str("entry", entry.Path).Msg("entry registration failed")
		return
	}
	tally.registered.Add(1)
}

// registerEntry creates the document row for one extracted file and publishes
// its DocumentUploaded so the ordinary pipeline picks it up.
func (p *Processor) registerEntry(
	ctx context.Context, evt *commonv1.DocumentUploaded, entry domain.Entry,
) error {
	doc, err := p.docs.CreateDocument(ctx, &dbv1.CreateDocumentRequest{
		OwnerId:     evt.GetOwnerId(),
		Filename:    path.Base(entry.Path),
		MimeType:    entry.MIMEType,
		Size:        entry.Size,
		ObjectKey:   entry.ObjectKey,
		ContentHash: entry.Hash,
	})
	if err != nil {
		return fmt.Errorf("create document for %s: %w", entry.Path, err)
	}

	child := &commonv1.DocumentUploaded{
		DocumentId:   doc.GetId(),
		OwnerId:      doc.GetOwnerId(),
		ObjectKey:    doc.GetObjectKey(),
		Filename:     doc.GetFilename(),
		MimeType:     doc.GetMimeType(),
		Size:         doc.GetSize(),
		TraceId:      evt.GetTraceId(),
		ArchiveDepth: evt.GetArchiveDepth() + 1,
	}
	route := contracts.RouteForMIME(doc.GetMimeType(), doc.GetFilename())
	if entry.NestedArchive {
		route = contracts.RouteParseArch
	}
	if perr := p.pub.PublishProto(ctx, contracts.ExchangeIngestion, route, child); perr != nil {
		return fmt.Errorf("publish %s to %s: %w", entry.Path, route, perr)
	}
	p.setStatus(ctx, doc.GetId(), contracts.StatusQueued, "", nil)
	p.emit(ctx, child, contracts.StatusQueued, "")
	return nil
}

// isDuplicate reports whether the entry's content hash already exists in the
// owner's corpus. Lookup errors fail open: dedup is an optimisation, not a
// dependency. Scoping by owner keeps one tenant's archive from deduplicating
// against another tenant's documents.
func (p *Processor) isDuplicate(ctx context.Context, ownerID string, entry domain.Entry) bool {
	if entry.Hash == "" {
		return false
	}
	if _, err := p.docs.FindDocumentByHash(ctx, ownerID, entry.Hash); err == nil {
		return true
	}
	return false
}

func (p *Processor) fail(ctx context.Context, evt *commonv1.DocumentUploaded, err error) error {
	logger.From(ctx).Error().Err(err).Str("document_id", evt.GetDocumentId()).Msg("archive processing failed")
	p.setStatus(ctx, evt.GetDocumentId(), contracts.StatusFailed, err.Error(), nil)
	p.emit(ctx, evt, contracts.StatusFailed, err.Error())
	return err
}

func (p *Processor) setStatus(ctx context.Context, id, status, msg string, chunkCount *int32) {
	if err := p.docs.UpdateDocumentStatus(ctx, id, status, msg, chunkCount); err != nil {
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
