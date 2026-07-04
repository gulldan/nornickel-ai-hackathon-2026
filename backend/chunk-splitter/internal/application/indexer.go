// Package application contains chunk-splitter's use case: consume a
// DocumentParsed event, split its text into overlapping chunks, embed them, and
// persist each chunk to both the vector store (Qdrant, for semantic search) and
// the lexical store (OpenSearch, for BM25). It depends only on small ports
// (status updater, embedder, vector store, search store, publisher) so it stays
// transport- and storage-agnostic and mirrors the pdf-parser processor shape.
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"github.com/example/chunk-splitter/internal/domain"
	"github.com/example/chunk-splitter/internal/infrastructure/delimited"
	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
	"github.com/example/chunk-splitter/internal/platform/contracts"
	"github.com/example/chunk-splitter/internal/platform/logger"

	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

// source labels the IngestionEvents this worker emits.
const source = "chunk-splitter"

// nodeTypeChunk tags every chunk point as a RAPTOR leaf (node_type in the Qdrant
// payload). Summary nodes raptor-worker writes use "raptor_summary"; retrieval
// reads this key to tell a leaf chunk from a summary.
const nodeTypeChunk = "chunk"

// chunkIDNamespace is a fixed UUID seeding the deterministic (UUIDv5/SHA-1)
// chunk point IDs. It must NEVER change: it is the only thing tying a chunk's ID
// to its (document_id, absolute chunk_index) so that re-processing the SAME
// DocumentParsed (broker restart, channel drop before Ack, AMQP redelivery)
// recomputes the SAME IDs and OVERWRITES the existing Qdrant points / OpenSearch
// docs instead of inserting duplicates. Kept as a const string (parsed per call)
// rather than a package-level uuid.UUID var, because gochecknoglobals forbids
// globals and parsing a constant literal cannot fail.
const chunkIDNamespace = "6f8a4d2e-1c3b-4f5a-9d7e-2b6c8a0f1e34"

// StatusUpdater advances the document's ingestion state (satisfied by
// platform/dbclient.Client).
type StatusUpdater interface {
	UpdateDocumentStatus(ctx context.Context, id, status, message string, chunkCount *int32) error
}

// Titler extracts a document's real article title and kind from the opening of
// its text (LLM), returning "" for a title none can be determined for (the UI
// then falls back to the filename) and "" for a regular document kind.
// Optional: a nil Titler disables the LLM pass (tests, or no generation
// backend configured); the heuristic kind classification still runs.
type Titler interface {
	Extract(ctx context.Context, text string) (title, kind string)
}

// TitleSetter persists the extracted article title (satisfied by
// platform/dbclient.Client).
type TitleSetter interface {
	SetDocumentTitle(ctx context.Context, id, title string) error
}

// KindSetter persists the document class determined during indexing (satisfied
// by platform/dbclient.Client). Reindexing overwrites the stored value, so the
// classification always reflects the latest run.
type KindSetter interface {
	SetDocumentKind(ctx context.Context, id, kind string) error
}

// MetaSetter persists parser-extracted document metadata (satisfied by
// platform/dbclient.Client).
type MetaSetter interface {
	SetDocumentMeta(ctx context.Context, id, author, publishedAt, sourceRef string) error
}

// Embedder turns chunk texts into dense vectors (satisfied by an adapter over
// platform/aiclients.Embedder).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Tokenizer reports the EXACT number of tokens a piece of text occupies under
// the embedding model's real tokenizer (F2LLM-v2), so chunks can be sized to a
// token budget rather than a rune budget. It is satisfied by the llama.cpp
// /tokenize adapter (internal/infrastructure/tokenizer). Optional: a nil
// Tokenizer on the Indexer keeps the rune-budgeted splitter, unchanged.
type Tokenizer interface {
	// CountTokens returns the token count of text. A returned error must leave
	// the caller free to fall back (the indexer falls back to a rune count for
	// that piece rather than failing ingestion).
	CountTokens(ctx context.Context, text string) (int, error)
}

// VectorPoint is one chunk's embedding plus the payload persisted alongside it.
// It is the application's transport type; the infrastructure adapter maps it to
// platform/vectorstore.Point.
//
// CharStart/CharEnd are the [start, end) RUNE offsets of this chunk's text
// within the document's FULL extracted text (absolute across split windows).
// They are correct-or-absent: [0, 0] means the span could not be located
// unambiguously, never a wrong offset (legal-grade provenance). PageStart/PageEnd
// are the 1-based page numbers the chunk spans (0 when the parser supplied no
// page structure); SectionHeading is the enclosing section title ("" when none).
type VectorPoint struct {
	ID             string
	Vector         []float32
	DocumentID     string
	OwnerID        string
	Filename       string
	Text           string
	ChunkIndex     int
	CharStart      int
	CharEnd        int
	PageStart      int
	PageEnd        int
	SectionHeading string
	Metadata       map[string]string
	// RAPTOR hierarchy tagging (additive, backward-compatible). Every chunk this
	// worker writes is a leaf: NodeType "chunk", NodeLevel 0, no MemberIDs. Summary
	// nodes at higher levels (node_type "raptor_summary", level >= 1, with
	// member_ids) are written by raptor-worker into the same collection; carrying
	// these keys on chunk points keeps the payload schema uniform so retrieval can
	// tell a leaf from a summary by reading node_type alone.
	NodeType  string
	NodeLevel int
	MemberIDs []string
}

// VectorStore upserts chunk embeddings (satisfied by an adapter over
// platform/vectorstore.Qdrant).
type VectorStore interface {
	Upsert(ctx context.Context, points []VectorPoint) error
}

// SearchDoc is one chunk's lexical document. The infrastructure adapter maps it
// to platform/searchstore.Doc. Section is the enclosing section name ("" when
// unknown), carried into the lexical payload so BM25 results expose it too.
type SearchDoc struct {
	ID         string
	Text       string
	DocumentID string
	OwnerID    string
	Filename   string
	Section    string
	Metadata   map[string]string
}

// SearchStore indexes chunk documents for BM25 retrieval in one batch per
// document (satisfied by an adapter over platform/searchstore.OpenSearch).
type SearchStore interface {
	Index(ctx context.Context, docs []SearchDoc) error
}

// Publisher emits downstream protobuf messages (satisfied by
// platform/messaging.Publisher).
type Publisher interface {
	PublishProto(ctx context.Context, exchange, routingKey string, msg proto.Message) error
}

// Pacer yields ingestion capacity to interactive traffic: Wait blocks briefly
// while user queries are in flight and returns immediately when the system is
// idle. Implementations must trickle rather than starve (see the valkey-backed
// pacing adapter), so ingestion always makes progress.
type Pacer interface {
	Wait(ctx context.Context)
}

// ObjectStore resolves claim-check text bodies (text_object_key) that were too
// large for the AMQP message (satisfied by platform/storage).
type ObjectStore interface {
	GetBytes(ctx context.Context, key string) ([]byte, error)
}

// StageRecorder records per-stage processing latency so the time-trace can
// attribute time within this service to chunking, embedding and indexing
// separately (satisfied by platform/observability.Metrics). Optional; nil
// disables per-stage timing.
type StageRecorder interface {
	RecordStage(stage string, seconds float64)
}

// stageTimes accumulates, per document, the time spent in each internal stage
// so they can be reported separately rather than as one opaque "chunk" total.
type stageTimes struct {
	split time.Duration // recursive text splitting (CPU-bound)
	embed time.Duration // F2LLM-v2 embedding round-trips
	index time.Duration // Qdrant upsert + OpenSearch bulk index
}

// Indexer wires the use case dependencies.
type Indexer struct {
	splitter    domain.Splitter
	status      StatusUpdater
	embedder    Embedder
	vectors     VectorStore
	search      SearchStore
	pub         Publisher
	pacer       Pacer         // optional; nil disables interactive-priority pacing
	store       ObjectStore   // optional; nil disables claim-check text resolution
	tokenizer   Tokenizer     // optional; nil → rune-budgeted ix.splitter (default path)
	maxTokens   int           // chunk token budget when tokenizer != nil (CHUNK_MAX_TOKENS)
	textMax     int           // hard cap on claim-check text bytes (TEXT_MAX_MB)
	batchSize   int           // chunks per embed→upsert→index round-trip (EMBED_BATCH)
	splitWindow int           // bytes of text split at once (SPLIT_WINDOW_MB)
	overlap     int           // runes carried between windows (= splitter overlap)
	headers     bool          // prepend a title/section breadcrumb to embed+BM25 text (CONTEXTUAL_HEADERS)
	metrics     StageRecorder // optional; nil disables per-stage timing
	titler      Titler        // optional; nil disables article-title extraction
	titles      TitleSetter   // optional; persists the extracted title
	kinds       KindSetter    // optional; persists the document class
	meta        MetaSetter    // optional; persists parser-extracted metadata
}

// EnableTitles turns on LLM article-title extraction: each indexed document's
// real title is extracted from the opening of its text and persisted via setter,
// so the UI shows the article title instead of the uploaded filename. Wired from
// main only when a generation backend is configured; left off in tests and when
// VLLM_URL is unset (the UI then falls back to the filename).
func (ix *Indexer) EnableTitles(t Titler, setter TitleSetter) {
	ix.titler, ix.titles = t, setter
}

// EnableKinds turns on persisting the document class (heuristic + optional LLM
// signal from the Titler): documents that are themselves a list of ready-made
// hypotheses (brainstorm notes) get kind "hypotheses" and are excluded from
// hypothesis generation/verification retrieval. Wired from main; nil (tests)
// leaves the class unpersisted.
func (ix *Indexer) EnableKinds(setter KindSetter) {
	ix.kinds = setter
}

// EnableMeta turns on persisting parser-extracted metadata (author,
// published_at, source_ref) carried in DocumentParsed.metadata. Wired from
// main; nil (tests) leaves the metadata unpersisted.
func (ix *Indexer) EnableMeta(setter MetaSetter) {
	ix.meta = setter
}

// New constructs an Indexer. pacer/store may be nil when no interactive-priority
// signal or object store is configured; tokenizer may be nil to keep the
// rune-budgeted splitter (the default, unchanged path) — when it is non-nil,
// chunks are sized to maxTokens EXACT F2LLM-v2 tokens instead. batchSize values
// < 1 fall back to 64, textMax values < 1 to 512MiB, splitWindow values < 1 to
// 8MiB, overlap values < 0 to 0, maxTokens values < 1 to 512. headers prepends a
// "{title} — {section}" breadcrumb to the EMBEDDED and BM25 text (never to the
// Qdrant payload text, which stays raw for display/citation).
func New(
	splitter domain.Splitter,
	status StatusUpdater,
	embedder Embedder,
	vectors VectorStore,
	search SearchStore,
	pub Publisher,
	pacer Pacer,
	store ObjectStore,
	tokenizer Tokenizer,
	maxTokens int,
	textMax int,
	batchSize int,
	splitWindow int,
	overlap int,
	headers bool,
	metrics StageRecorder,
) *Indexer {
	if batchSize < 1 {
		batchSize = 64
	}
	if textMax < 1 {
		textMax = 512 << 20
	}
	if splitWindow < 1 {
		splitWindow = 8 << 20
	}
	if overlap < 0 {
		overlap = 0
	}
	if maxTokens < 1 {
		maxTokens = 512
	}
	return &Indexer{
		splitter:    splitter,
		status:      status,
		embedder:    embedder,
		vectors:     vectors,
		search:      search,
		pub:         pub,
		pacer:       pacer,
		store:       store,
		tokenizer:   tokenizer,
		maxTokens:   maxTokens,
		textMax:     textMax,
		batchSize:   batchSize,
		splitWindow: splitWindow,
		overlap:     overlap,
		headers:     headers,
		metrics:     metrics,
	}
}

func (ix *Indexer) normalizeDelimited(
	log zerolog.Logger, evt *commonv1.DocumentParsed, text string, trunc truncation,
) (string, truncation) {
	transformed, ok := delimited.TransformWithLimit(evt.GetFilename(), evt.GetMimeType(), text, ix.textMax)
	if !ok {
		return text, trunc
	}
	if transformed.Truncated {
		if !trunc.Truncated {
			trunc.OriginalBytes = len(text)
		}
		trunc.Truncated = true
		trunc.IndexedBytes = len(transformed.Text)
	}
	log.Info().
		Int("rows", transformed.Rows).
		Int("columns", transformed.Columns).
		Str("delimiter", string(transformed.Delimiter)).
		Bool("truncated", transformed.Truncated).
		Msg("delimited text normalized for indexing")
	return transformed.Text, trunc
}

// Process handles one parsed-document event end to end. A returned error causes
// the message to be dead-lettered; failures also mark the document failed and
// emit a failure event for live progress.
func (ix *Indexer) Process(ctx context.Context, evt *commonv1.DocumentParsed) error {
	log := logger.From(ctx).With().Str("document_id", evt.GetDocumentId()).Str("source", source).Logger()

	// Batch ingestion is the background citizen: yield to in-flight interactive
	// queries before touching the stores they read from.
	if ix.pacer != nil {
		ix.pacer.Wait(ctx)
	}

	// Enter the chunking state so the UI reflects work in progress.
	ix.setStatus(ctx, evt.GetDocumentId(), contracts.StatusChunking, "", nil)
	ix.emit(ctx, evt, contracts.StatusChunking, "")

	text, trunc, err := ix.resolveText(ctx, evt)
	if err != nil {
		return ix.fail(ctx, evt, err)
	}
	text, trunc = capTextToMax(text, ix.textMax, trunc)
	if trunc.Truncated {
		// Structured, machine-readable signal so downstream can downweight a
		// document whose tail was dropped (the status_msg also carries it).
		log.Warn().
			Bool("truncated", true).
			Int("original_bytes", trunc.OriginalBytes).
			Int("indexed_bytes", trunc.IndexedBytes).
			Msg("text truncated to TEXT_MAX for indexing")
	}
	text, trunc = ix.normalizeDelimited(log, evt, text, trunc)

	// Collapse letter-spacing (tracking): some PDFs extract tracked typography
	// as single letters separated by a single space/newline ("М е т о д..."),
	// which would tokenise into useless single-character chunks. This only removes
	// single whitespace inside long letter runs — paragraph/word boundaries (gaps
	// of length >= 2) are preserved — so it is safe to run on the full text before
	// splitting and before page/section offsets are derived from it.
	text = splitter.CollapseTracking(text)

	// Extract and persist the real article title from the opening of the text so
	// the documents page shows it instead of the uploaded filename, and reuse it
	// as the contextual chunk-header prefix. Best-effort: never blocks or fails
	// ingestion (the UI falls back to the filename; headers then use the section
	// alone, or the raw chunk).
	metaDone := make(chan struct{})
	go func() {
		defer close(metaDone)
		ix.maybeSetMeta(ctx, evt)
	}()
	title := ix.maybeSetTitleAndKind(ctx, evt, text)
	<-metaDone

	// Optional page/section structure from the parser (pdf-parser populates
	// metadata["page_offsets"]). Absent or invalid → no annotation and plain
	// byte-windowing; present → page-aware windowing + page/section provenance.
	ds := parseDocStructure(evt.GetMetadata(), text)
	workbookAnchors := parseWorkbookAnchorIndex(text)

	// Choose the splitter for THIS document. When a tokenizer is configured,
	// chunks are sized to an EXACT F2LLM-v2 token budget (per-document splitter with
	// a memoizing, error-tolerant token measure); otherwise the injected
	// rune-budgeted ix.splitter is used exactly as before. The size measure is the
	// ONLY thing that changes — char-offset provenance is computed by text-matching
	// the splitter's output (LocateOffsets), independent of how size was measured.
	sp := ix.documentSplitter(ctx)

	// Windowing: split and index the text in chunks rather than all at once.
	// Materialising every chunk at once is a second full copy of the text
	// (mergePieces copies via strings.Join) and OOMs on hundreds of MB; a window
	// keeps the peak at O(window).
	total := 0   // chunks produced by the splitter
	written := 0 // chunks confirmed written to BOTH stores (for reconciliation)
	carry := ""  // previous window's overlap tail — context across the boundary
	var st stageTimes
	for start := 0; start < len(text); {
		end := windowEnd(text, start, ix.splitWindow)
		// Page-aware: a window never crosses a page boundary, so no chunk spans
		// two pages and overlap context is not carried across a page break. Huge
		// single pages still get byte-sub-windowed (no clamp when the next page
		// boundary is beyond this window).
		atPageBreak := false
		if pb := ds.nextPageByteAfter(start, len(text)); pb > start && pb <= end {
			end = pb
			atPageBreak = pb < len(text)
		}
		seg := carry + text[start:end]
		tsplit := time.Now()
		chunks := sp.Split(seg)
		st.split += time.Since(tsplit)
		// Provenance offsets are absolute within the FULL document. seg begins at
		// rune index (rune count of text[:start]) − (rune count of carry), since
		// carry is the previous window's overlap tail, i.e. the runes of text
		// ending exactly at byte `start`. LocateOffsets resolves each chunk's span
		// within seg; baseRune lifts those local offsets into document space.
		offsets := splitter.LocateOffsets(seg, chunkTexts(chunks))
		baseRune := windowBaseRune(text, start, carry)
		w, werr := ix.indexChunks(ctx, evt, title, chunks, total, offsets, baseRune, ds, workbookAnchors, &st)
		if werr != nil {
			// A mid-batch store failure routes here: the document is marked failed
			// (never a clean "indexed"), so the partial dual-write stays observable.
			return ix.fail(ctx, evt, werr)
		}
		written += w
		total += len(chunks)
		if atPageBreak {
			carry = "" // a new page starts fresh: no cross-page overlap context
		} else {
			carry = tailRunes(seg, ix.overlap)
		}
		start = end
	}
	ix.recordStages(st)

	if total == 0 {
		// Nothing to index (empty or whitespace-only document); record it as
		// indexed with zero chunks so the lifecycle still completes cleanly.
		log.Info().Msg("no chunks produced; marking indexed with zero chunks")
		count := int32(0)
		ix.setStatus(ctx, evt.GetDocumentId(), contracts.StatusIndexed, "no text content", &count)
		ix.emit(ctx, evt, contracts.StatusIndexed, "no text content")
		return nil
	}

	// Reconciliation: every produced chunk must have been written to BOTH stores.
	// indexChunks only counts a batch after vector AND lexical writes succeed, so
	// written < total can only mean a partial dual-write that nonetheless returned
	// no error — surface it loudly rather than reporting a clean "indexed".
	if written != total {
		log.Error().
			Int("expected_chunks", total).
			Int("written_chunks", written).
			Msg("dual-write reconciliation mismatch")
	}

	msg := indexedStatusMsg(total, written, trunc)
	count := int32(written)
	ix.setStatus(ctx, evt.GetDocumentId(), contracts.StatusIndexed, msg, &count)
	ix.emit(ctx, evt, contracts.StatusIndexed, msg)
	log.Info().Int("chunks", total).Int("written", written).Msg("indexed")
	return nil
}

// indexedStatusMsg assembles the persisted status_msg for a successfully indexed
// document from the structured signals (truncation, reconciliation). It carries
// the human-readable Russian note plus compact, machine-parseable markers so a
// downstream consumer can detect — and downweight — a truncated or
// under-reconciled document from the status alone. Empty when nothing notable.
func indexedStatusMsg(total, written int, trunc truncation) string {
	var parts []string
	if note := truncationNote(trunc); note != "" {
		parts = append(parts, note)
	}
	if written != total {
		parts = append(parts, fmt.Sprintf("[reconcile expected=%d written=%d]", total, written))
	}
	return strings.Join(parts, " ")
}

// truncationNote renders the user-facing Russian truncation remark together with
// the machine-parseable marker, or "" when the text was not truncated.
func truncationNote(trunc truncation) string {
	if !trunc.Truncated {
		return ""
	}
	return fmt.Sprintf("текст усечён до %d МиБ для индексации %s", trunc.IndexedBytes>>20, trunc.statusToken())
}

func capTextToMax(text string, textMax int, trunc truncation) (string, truncation) {
	if textMax < 1 || len(text) <= textMax {
		return text, trunc
	}
	cut := textMax
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	if cut < 0 {
		cut = 0
	}
	if !trunc.Truncated {
		trunc.OriginalBytes = len(text)
	}
	trunc.Truncated = true
	trunc.IndexedBytes = cut
	return text[:cut], trunc
}

// documentSplitter returns the splitter to use for one document. With no
// tokenizer configured it returns the injected rune-budgeted splitter unchanged
// (the default path, behaviourally identical to before). With a tokenizer it
// builds a fresh token-budgeted splitter whose size measure asks the tokenizer
// for EXACT token counts, MEMOIZING results in a per-document map so an identical
// piece (the recursive splitter re-measures the same sub-strings, and overlap
// repeats text across windows) is tokenized at most once; on a tokenizer ERROR
// the measure falls back to the rune length of that piece so a flaky/slow model
// degrades to rune sizing for that call instead of failing ingestion.
//
// The returned splitter is built per document (not shared) because its memo map
// is document-scoped and Process runs one document per goroutine, so the closure
// needs no synchronisation.
func (ix *Indexer) documentSplitter(ctx context.Context) domain.Splitter {
	if ix.tokenizer == nil {
		return ix.splitter
	}
	log := logger.From(ctx)
	memo := make(map[string]int)
	measure := func(s string) int {
		if n, ok := memo[s]; ok {
			return n
		}
		n, err := ix.tokenizer.CountTokens(ctx, s)
		if err != nil {
			// Fall back to runes for THIS piece (do not poison the memo with the
			// fallback, so a later success can still record the real token count).
			log.Warn().Err(err).Msg("tokenizer count failed; falling back to rune length for this piece")
			return runeLen(s)
		}
		memo[s] = n
		return n
	}
	return splitter.NewRecursiveWithMeasure(ix.maxTokens, ix.overlap, measure)
}

// runeLen returns the number of runes in s, the rune-budget fallback unit used
// when the tokenizer errs on a piece.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// recordStages publishes this document's per-stage latencies to the histogram
// the time-trace reads. The names are prefixed (chunk_*) so they don't collide
// with llm-service's query stages in the shared rag_stage_duration_seconds.
func (ix *Indexer) recordStages(st stageTimes) {
	if ix.metrics == nil {
		return
	}
	ix.metrics.RecordStage("chunk_split", st.split.Seconds())
	ix.metrics.RecordStage("chunk_embed", st.embed.Seconds())
	ix.metrics.RecordStage("chunk_index", st.index.Seconds())
}

// indexChunks embeds, upserts and bulk-indexes chunks in bounded batches so
// store/GPU requests have a predictable size. base offsets chunk indices so
// they stay contiguous across windows. offsets carries each chunk's [start, end)
// RUNE span within the current window (indexed by the chunk's per-window Index);
// baseRune lifts a located (non-zero) span into absolute document coordinates.
//
// The text fed to the embedder (and, in buildBatch, to the BM25 index) is the
// chunk with a "{title} — {section}" breadcrumb prepended when contextual headers
// are enabled (see withHeader); the Qdrant payload keeps the RAW chunk, so only
// the vector and the lexical text gain the context.
func (ix *Indexer) indexChunks(
	ctx context.Context,
	evt *commonv1.DocumentParsed,
	title string,
	chunks []domain.Chunk,
	base int,
	offsets [][2]int,
	baseRune int,
	ds docStructure,
	workbookAnchors workbookAnchorIndex,
	st *stageTimes,
) (int, error) {
	// One store-write stays in flight while the NEXT batch embeds, so the GPU
	// never idles behind Qdrant/OpenSearch. settleWrite drains it: `written` is
	// counted only after BOTH stores accepted the batch, so it remains the number
	// of chunks durably present in vector AND lexical stores alike, and a store
	// failure (including a partial dual-write, where the vector upsert landed but
	// the lexical write failed) still surfaces as the returned error so Process
	// routes to fail and reconciliation sees the gap.
	written := 0
	var inflight chan batchWrite
	for start := 0; start < len(chunks); start += ix.batchSize {
		end := min(start+ix.batchSize, len(chunks))
		batch := chunks[start:end]

		vectors, err := ix.embedBatch(ctx, batch, title, offsets, baseRune, ds, base+start, base+end, st)
		if err != nil {
			if werr := ix.settleWrite(inflight, &written, st); werr != nil {
				return written, werr
			}
			return written, err
		}

		points, docs := buildBatch(evt, title, ix.headers, batch, base, offsets, baseRune, ds, workbookAnchors, vectors)

		if werr := ix.settleWrite(inflight, &written, st); werr != nil {
			return written, werr
		}
		inflight = ix.startBatchWrite(ctx, points, docs, base+start, base+end)

		// Between batches of a long document, give way again if queries arrived
		// mid-flight: large documents must not monopolise the shared stores.
		if end < len(chunks) && ix.pacer != nil {
			ix.pacer.Wait(ctx)
		}
	}
	if werr := ix.settleWrite(inflight, &written, st); werr != nil {
		return written, werr
	}
	return written, nil
}

// batchWrite is the outcome of one asynchronous dual-store batch write.
type batchWrite struct {
	n   int
	dur time.Duration
	err error
}

// settleWrite waits for the in-flight batch write (nil channel means none),
// folds its store latency into the stage times and its chunk count into
// `written`, and returns its error if the write failed.
func (ix *Indexer) settleWrite(inflight chan batchWrite, written *int, st *stageTimes) error {
	if inflight == nil {
		return nil
	}
	res := <-inflight
	st.index += res.dur
	if res.err != nil {
		return res.err
	}
	*written += res.n
	return nil
}

// embedBatch builds the header-prefixed texts for one batch and embeds them in
// a single request, accounting the latency to the embed stage.
func (ix *Indexer) embedBatch(
	ctx context.Context, batch []domain.Chunk, title string,
	offsets [][2]int, baseRune int, ds docStructure, lo, hi int, st *stageTimes,
) ([][]float32, error) {
	texts := make([]string, len(batch))
	for i, c := range batch {
		charStart, _ := absOffset(offsets, c.Index, baseRune)
		texts[i] = withHeader(ix.headers, title, sectionOf(ds, charStart, c.Section), c.Text)
	}
	tembed := time.Now()
	vectors, err := ix.embedder.Embed(ctx, texts)
	st.embed += time.Since(tembed)
	if err != nil {
		return nil, fmt.Errorf("embed chunks [%d:%d]: %w", lo, hi, err)
	}
	if len(vectors) != len(batch) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d chunks", len(vectors), len(batch))
	}
	return vectors, nil
}

// startBatchWrite launches the dual-store write for one embedded batch and
// returns the channel its outcome will arrive on.
func (ix *Indexer) startBatchWrite(
	ctx context.Context, points []VectorPoint, docs []SearchDoc, lo, hi int,
) chan batchWrite {
	ch := make(chan batchWrite, 1)
	go func() {
		tindex := time.Now()
		if uerr := ix.vectors.Upsert(ctx, points); uerr != nil {
			ch <- batchWrite{dur: time.Since(tindex), err: fmt.Errorf("upsert vectors [%d:%d]: %w", lo, hi, uerr)}
			return
		}
		if ierr := ix.search.Index(ctx, docs); ierr != nil {
			ch <- batchWrite{dur: time.Since(tindex), err: fmt.Errorf("index chunks [%d:%d]: %w", lo, hi, ierr)}
			return
		}
		ch <- batchWrite{n: hi - lo, dur: time.Since(tindex)}
	}()
	return ch
}

// buildBatch builds the vector points and lexical docs for one embedded batch.
// Each chunk gets one shared, DETERMINISTIC id (a pure function of document_id
// and its ABSOLUTE index base+c.Index): the vector point and lexical document
// are correlated (fused by id during hybrid retrieval) AND a re-delivered
// DocumentParsed OVERWRITES the existing records in both stores rather than
// duplicating them. Page/section provenance is derived from the located span;
// the section prefers the parser's structural offsets and falls back to the
// splitter's best-effort detection (c.Section).
//
// The vector point's payload Text is the RAW chunk (what the UI shows and
// llm-service cites). The lexical doc's Text gets the SAME "{title} — {section}"
// breadcrumb as the embedded vector (withHeader, when headers is set) so BM25
// ranks on the context too; the bare section stays in SearchDoc.Section.
func buildBatch(
	evt *commonv1.DocumentParsed,
	title string,
	headers bool,
	batch []domain.Chunk,
	base int,
	offsets [][2]int,
	baseRune int,
	ds docStructure,
	workbookAnchors workbookAnchorIndex,
	vectors [][]float32,
) (points []VectorPoint, docs []SearchDoc) {
	points = make([]VectorPoint, len(batch))
	docs = make([]SearchDoc, len(batch))
	for i, c := range batch {
		chunkIndex := base + c.Index
		id := chunkPointID(evt.GetDocumentId(), chunkIndex)
		charStart, charEnd := absOffset(offsets, c.Index, baseRune)
		pageStart, pageEnd := 0, 0
		if charStart != 0 || charEnd != 0 {
			pageStart = ds.pageOf(charStart)
			pageEnd = ds.pageOf(charEnd - 1)
		}
		heading := sectionOf(ds, charStart, c.Section)
		metadata := workbookAnchors.metadataAt(charStart, charEnd)
		if len(metadata) == 0 {
			metadata = workbookAnchorMetadata(c.Text)
		}
		points[i] = VectorPoint{
			ID:             id,
			Vector:         vectors[i],
			DocumentID:     evt.GetDocumentId(),
			OwnerID:        evt.GetOwnerId(),
			Filename:       evt.GetFilename(),
			Text:           c.Text,
			ChunkIndex:     chunkIndex,
			CharStart:      charStart,
			CharEnd:        charEnd,
			PageStart:      pageStart,
			PageEnd:        pageEnd,
			SectionHeading: heading,
			Metadata:       metadata,
			// Leaf of the RAPTOR tree: a raw chunk at level 0 with no members.
			NodeType:  nodeTypeChunk,
			NodeLevel: 0,
		}
		docs[i] = SearchDoc{
			ID:         id,
			Text:       withHeader(headers, title, heading, c.Text),
			DocumentID: evt.GetDocumentId(),
			OwnerID:    evt.GetOwnerId(),
			Filename:   evt.GetFilename(),
			Section:    heading,
			Metadata:   metadata,
		}
	}
	return points, docs
}

// sectionOf resolves the enclosing section heading for a chunk whose start sits
// at rune offset charStart: the parser's structural section there, falling back
// to the splitter's best-effort detection (fallback), or "" when neither knows.
// Shared by indexChunks (the embed/BM25 header) and buildBatch (the section
// metadata) so both derive the SAME heading for one chunk.
func sectionOf(ds docStructure, charStart int, fallback string) string {
	if h := ds.sectionAt(charStart); h != "" {
		return h
	}
	return fallback
}

// withHeader prepends a "{title} — {section}" breadcrumb to a chunk so the
// EMBEDDED vector and the BM25 text carry the document/section context a bare
// chunk usually lacks — a cheap, high-ROI retrieval boost. Empty title/section
// parts are skipped; when the breadcrumb is empty, or enabled is false, the raw
// chunk is returned. This output feeds ONLY the embedder and the lexical index:
// the Qdrant payload keeps the raw chunk (display/citation), so the breadcrumb
// never leaks into what the UI shows or llm-service quotes.
func withHeader(enabled bool, title, section, chunk string) string {
	if !enabled {
		return chunk
	}
	crumb := breadcrumb(title, section)
	if crumb == "" {
		return chunk
	}
	return crumb + "\n\n" + chunk
}

// breadcrumb joins the non-empty, trimmed title and section with an em dash, or
// returns "" when both are empty.
func breadcrumb(title, section string) string {
	title, section = strings.TrimSpace(title), strings.TrimSpace(section)
	switch {
	case title != "" && section != "":
		return title + " — " + section
	case title != "":
		return title
	default:
		return section
	}
}

// windowEnd returns the end of the split window starting at start: no farther
// than limit bytes, on a rune boundary, and on a whitespace char when possible
// (scanning back <=4KiB) so a word is not cut at a window seam.
func windowEnd(text string, start, limit int) int {
	end := start + limit
	if end >= len(text) {
		return len(text)
	}
	for end > start && !utf8.RuneStart(text[end]) {
		end--
	}
	const lookback = 4096
	for i := end - 1; i > end-lookback && i > start; i-- {
		if text[i] == ' ' || text[i] == '\n' {
			return i + 1
		}
	}
	return end
}

// windowBaseRune returns the absolute rune index at which a window's seg begins
// in the full document. seg = carry + text[start:end], and carry is exactly the
// runes of text ending at byte `start` (the previous window's overlap tail), so
// seg's rune 0 sits at runeCount(text[:start]) − runeCount(carry). Lifting each
// chunk's window-local span by this base yields document-absolute offsets.
func windowBaseRune(text string, start int, carry string) int {
	return utf8.RuneCountInString(text[:start]) - utf8.RuneCountInString(carry)
}

// tailRunes returns the last n runes of s (the cross-window overlap carry).
func tailRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := len(s)
	for ; n > 0 && i > 0; n-- {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return s[i:]
}

// chunkTexts projects chunks to their texts in order, the input
// splitter.LocateOffsets expects (one entry per chunk, aligned to chunk.Index).
func chunkTexts(chunks []domain.Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

// chunkPointID derives the stable UUIDv5 used as both the Qdrant point ID and
// the OpenSearch _id for one chunk. It is a pure function of (documentID,
// absolute chunkIndex), so re-processing the same DocumentParsed recomputes the
// same ID and overwrites the existing records in both stores — the property that
// makes this worker idempotent under AMQP redelivery. The namespace is parsed
// from a fixed constant (cannot fail), so a parse error would be a programmer
// bug; MustParse panics loudly at first use rather than silently producing
// colliding IDs.
//
// TODO(platform): deterministic IDs make re-indexing the SAME content
// idempotent, but re-indexing a document into FEWER chunks (edits, better
// extraction) leaves the surplus higher-index points/docs orphaned. Purging them
// needs a delete-by-document_id on vectorstore/searchstore, which lives in the
// platform layer (out of this service's scope).
func chunkPointID(documentID string, chunkIndex int) string {
	ns := uuid.MustParse(chunkIDNamespace)
	return uuid.NewSHA1(ns, []byte(documentID+":"+strconv.Itoa(chunkIndex))).String()
}

// absOffset returns the absolute [char_start, char_end) RUNE span for the chunk
// at per-window index idx, lifted into document coordinates by baseRune. A
// missing entry or an unlocated [0, 0] span stays [0, 0] (NOT shifted) so an
// absent offset is never turned into a spurious one.
func absOffset(offsets [][2]int, idx, baseRune int) (start, end int) {
	if idx < 0 || idx >= len(offsets) {
		return 0, 0
	}
	o := offsets[idx]
	if o == [2]int{0, 0} {
		return 0, 0
	}
	return o[0] + baseRune, o[1] + baseRune
}

// docStructure holds the optional page (and section) boundaries a parser may
// supply in DocumentParsed.metadata, used to annotate chunks with page numbers
// and the enclosing section heading. The zero value carries no structure, so
// every accessor degrades to 0/"" and the indexer behaves exactly as before.
type docStructure struct {
	pageRuneStarts []int         // rune offset where each page begins ([0]==0, strictly increasing)
	pageByteStarts []int         // the same boundaries as byte offsets in text (page-aware windowing)
	sections       []sectionMark // section headings by rune offset, ascending
}

// sectionMark is one section heading anchored at a rune offset in the text.
type sectionMark struct {
	rune    int
	heading string
}

// parseDocStructure reads page_offsets / section_offsets from the parser
// metadata. Malformed or out-of-range data is ignored (no structure for that
// dimension) so a bad producer can never corrupt indexing.
func parseDocStructure(meta map[string]string, text string) docStructure {
	var ds docStructure
	if meta == nil {
		return ds
	}
	runeLen := utf8.RuneCountInString(text)
	if raw := meta["page_offsets"]; raw != "" {
		var offs []int
		if err := json.Unmarshal([]byte(raw), &offs); err == nil && validPageOffsets(offs, runeLen) {
			ds.pageRuneStarts = offs
			ds.pageByteStarts = byteStartsOf(text, offs)
		}
	}
	if raw := meta["section_offsets"]; raw != "" {
		var marks []struct {
			Rune    int    `json:"rune"`
			Heading string `json:"heading"`
		}
		if err := json.Unmarshal([]byte(raw), &marks); err == nil {
			for _, m := range marks {
				if h := strings.TrimSpace(m.Heading); m.Rune >= 0 && m.Rune <= runeLen && h != "" {
					ds.sections = append(ds.sections, sectionMark{rune: m.Rune, heading: h})
				}
			}
			sort.Slice(ds.sections, func(i, j int) bool { return ds.sections[i].rune < ds.sections[j].rune })
		}
	}
	return ds
}

// validPageOffsets requires a non-empty list that starts at 0, strictly
// increases, and stays within the text's rune length.
func validPageOffsets(offs []int, runeLen int) bool {
	if len(offs) == 0 || offs[0] != 0 {
		return false
	}
	for i := 1; i < len(offs); i++ {
		if offs[i] <= offs[i-1] || offs[i] > runeLen {
			return false
		}
	}
	return true
}

// byteStartsOf converts page rune-offsets to byte offsets within text (page-aware
// windowing operates on bytes): each entry is the byte index of the rune at the
// corresponding page-start rune offset.
func byteStartsOf(text string, runeStarts []int) []int {
	out := make([]int, 0, len(runeStarts))
	ri, next := 0, 0
	for bi := range text {
		for next < len(runeStarts) && runeStarts[next] == ri {
			out = append(out, bi)
			next++
		}
		ri++
	}
	for next < len(runeStarts) { // a boundary at end-of-text (defensive)
		out = append(out, len(text))
		next++
	}
	return out
}

// pageOf returns the 1-based page number containing rune offset r, or 0 when no
// page structure is known.
func (ds docStructure) pageOf(r int) int {
	if len(ds.pageRuneStarts) == 0 {
		return 0
	}
	if r < 0 {
		r = 0
	}
	// Index of the last page start <= r, made 1-based.
	i := sort.Search(len(ds.pageRuneStarts), func(i int) bool { return ds.pageRuneStarts[i] > r }) - 1
	if i < 0 {
		i = 0
	}
	return i + 1
}

// sectionAt returns the heading of the innermost section starting at or before
// rune offset r, or "" when there is no section structure / none precedes r.
func (ds docStructure) sectionAt(r int) string {
	h := ""
	for _, m := range ds.sections {
		if m.rune > r {
			break
		}
		h = m.heading
	}
	return h
}

// nextPageByteAfter returns the byte offset of the first page boundary strictly
// after start (or textLen when none lies beyond it), or -1 when there is no page
// structure (so the caller leaves windowing untouched).
func (ds docStructure) nextPageByteAfter(start, textLen int) int {
	if len(ds.pageByteStarts) == 0 {
		return -1
	}
	for _, b := range ds.pageByteStarts {
		if b > start {
			return b
		}
	}
	return textLen
}

// truncation is the structured record of a textMax cap: a downstream-visible
// flag (Truncated) plus the original and indexed byte sizes so consumers can
// downweight a document whose tail was dropped. The zero value means "not
// truncated" and serialises to nothing.
type truncation struct {
	Truncated     bool
	OriginalBytes int
	IndexedBytes  int
}

// statusToken renders a compact, machine-parseable marker appended to the
// document's status_msg (the only persisted document-level channel reachable
// here): "[truncated orig=<n> indexed=<n> bytes]". Empty when not truncated.
func (t truncation) statusToken() string {
	if !t.Truncated {
		return ""
	}
	return fmt.Sprintf("[truncated orig=%d indexed=%d bytes]", t.OriginalBytes, t.IndexedBytes)
}

// resolveText returns the document text, fetching the claim-check object when
// the parser offloaded it (text_object_key). The returned truncation records,
// in a structured form, whether the text was capped at textMax and by how much
// (the zero value when nothing was dropped).
func (ix *Indexer) resolveText(
	ctx context.Context, evt *commonv1.DocumentParsed,
) (text string, trunc truncation, err error) {
	key := evt.GetTextObjectKey()
	if key == "" {
		return evt.GetText(), truncation{Truncated: false, OriginalBytes: 0, IndexedBytes: 0}, nil
	}
	if ix.store == nil {
		return "", truncation{}, errors.New("text_object_key set but no object store configured")
	}
	data, gerr := ix.store.GetBytes(ctx, key)
	if gerr != nil {
		return "", truncation{}, fmt.Errorf("fetch parsed text %s: %w", key, gerr)
	}
	trunc = truncation{Truncated: false, OriginalBytes: len(data), IndexedBytes: len(data)}
	if len(data) > ix.textMax {
		// Hard cap against OOM: truncate on a rune boundary — search runs over the
		// first textMax bytes. The drop is recorded structurally (trunc) so the
		// final status carries both the Russian note and a parseable marker.
		cut := ix.textMax
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		data = data[:cut]
		trunc.Truncated = true
		trunc.IndexedBytes = len(data)
	}
	if !utf8.Valid(data) {
		// Text sources arrive by reference as-is — sanitise here. Only invalid
		// data is copied (the valid path avoids an extra 100+ MB copy).
		data = bytes.ToValidUTF8(data, []byte("�"))
	}
	return string(data), trunc, nil
}

func (ix *Indexer) fail(ctx context.Context, evt *commonv1.DocumentParsed, err error) error {
	logger.From(ctx).Error().Err(err).Str("document_id", evt.GetDocumentId()).Msg("indexing failed")
	ix.setStatus(ctx, evt.GetDocumentId(), contracts.StatusFailed, err.Error(), nil)
	ix.emit(ctx, evt, contracts.StatusFailed, err.Error())
	return err
}

// maybeSetTitleAndKind extracts the document's real article title and kind from
// the opening of its text (one LLM call) and persists both, returning the
// extracted title (or "") so the caller can reuse it as the contextual
// chunk-header prefix. The kind combines the LLM signal with a deterministic
// heuristic, so ready-made hypotheses lists are caught even with the LLM off,
// and is always written (reindexing clears a stale class). Best-effort: a
// disabled titler, empty results or persist errors are all silently tolerated.
func (ix *Indexer) maybeSetTitleAndKind(ctx context.Context, evt *commonv1.DocumentParsed, text string) string {
	var title, llmKind string
	if ix.titler != nil && ix.titles != nil {
		title, llmKind = ix.titler.Extract(ctx, text)
	}
	if title != "" {
		if err := ix.titles.SetDocumentTitle(ctx, evt.GetDocumentId(), title); err != nil {
			logger.From(ctx).Warn().Err(err).Str("document_id", evt.GetDocumentId()).Msg("set document title failed")
		}
	}
	if ix.kinds != nil {
		kind := docKind(text, llmKind)
		if err := ix.kinds.SetDocumentKind(ctx, evt.GetDocumentId(), kind); err != nil {
			logger.From(ctx).Warn().Err(err).Str("document_id", evt.GetDocumentId()).Msg("set document kind failed")
		}
	}
	return title
}

const docKindHypotheses = "hypotheses"

// docKind classifies the document: "hypotheses" when either the LLM or the
// opening-text heuristic recognises a ready-made hypotheses/brainstorm list,
// "" for a regular document.
func docKind(text, llmKind string) string {
	if llmKind == docKindHypotheses || heuristicDocKind(text) == docKindHypotheses {
		return docKindHypotheses
	}
	return ""
}

// heuristicDocKind flags openings like "Гипотезы по результатам мозгового
// штурма: 1. …" — a hypotheses heading followed by a numbered list, or an
// explicit brainstorm mention next to the word.
func heuristicDocKind(text string) string {
	head := strings.ToLower(strings.TrimSpace(text))
	if r := []rune(head); len(r) > 400 {
		head = string(r[:400])
	}
	if head == "" {
		return ""
	}
	firstLine := head
	if nl := strings.IndexAny(firstLine, "\r\n"); nl >= 0 {
		firstLine = strings.TrimSpace(firstLine[:nl])
	}
	numbered := strings.Contains(head, "1.") || strings.Contains(head, "1)")
	if strings.HasPrefix(firstLine, "гипотез") && numbered {
		return docKindHypotheses
	}
	if strings.Contains(head, "гипотез") && strings.Contains(head, "мозгово") {
		return docKindHypotheses
	}
	return ""
}

// maybeSetMeta persists the author/published_at/source_ref the parser supplied
// in DocumentParsed.metadata. Best-effort, like maybeSetTitle: absent keys or a
// persist error never block ingestion.
func (ix *Indexer) maybeSetMeta(ctx context.Context, evt *commonv1.DocumentParsed) {
	if ix.meta == nil {
		return
	}
	md := evt.GetMetadata()
	author, published, ref := md["author"], md["published_at"], md["source_ref"]
	if author == "" && published == "" && ref == "" {
		return
	}
	if err := ix.meta.SetDocumentMeta(ctx, evt.GetDocumentId(), author, published, ref); err != nil {
		logger.From(ctx).Warn().Err(err).Str("document_id", evt.GetDocumentId()).Msg("set document meta failed")
	}
}

// setStatus advances the document's persisted ingestion state. chunkCount is
// optional; pass nil when it does not apply.
func (ix *Indexer) setStatus(ctx context.Context, id, status, msg string, chunkCount *int32) {
	if err := ix.status.UpdateDocumentStatus(ctx, id, status, msg, chunkCount); err != nil {
		logger.From(ctx).Warn().Err(err).Str("document_id", id).Msg("update status failed")
	}
}

// emit broadcasts a progress event for WebSocket subscribers. It is best-effort.
func (ix *Indexer) emit(ctx context.Context, evt *commonv1.DocumentParsed, status, msg string) {
	ev := &commonv1.IngestionEvent{
		DocumentId: evt.GetDocumentId(),
		OwnerId:    evt.GetOwnerId(),
		Status:     status,
		Message:    msg,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := ix.pub.PublishProto(ctx, contracts.ExchangeEvents, "", ev); err != nil {
		logger.From(ctx).Warn().Err(err).Msg("emit event failed")
	}
}
