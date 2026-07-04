// Command chunk-splitter is a stateless AMQP worker. It consumes the "chunk"
// queue as a competing consumer (running N replicas linearly increases
// throughput toward the 50k-files/day target with no code changes), splits each
// parsed document into overlapping chunks, embeds them, and writes them to both
// Qdrant (vectors) and OpenSearch (BM25) — the two halves of the platform's
// hybrid retrieval. It follows the pdf-parser worker shape.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/chunk-splitter/internal/application"
	"github.com/example/chunk-splitter/internal/infrastructure/embedding"
	"github.com/example/chunk-splitter/internal/infrastructure/lexicalindex"
	"github.com/example/chunk-splitter/internal/infrastructure/pacing"
	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
	"github.com/example/chunk-splitter/internal/infrastructure/titler"
	"github.com/example/chunk-splitter/internal/infrastructure/tokenizer"
	"github.com/example/chunk-splitter/internal/infrastructure/vectorindex"
	amqpiface "github.com/example/chunk-splitter/internal/interfaces/amqp"
	"github.com/example/chunk-splitter/internal/platform/aiclients"
	"github.com/example/chunk-splitter/internal/platform/config"
	"github.com/example/chunk-splitter/internal/platform/contracts"
	"github.com/example/chunk-splitter/internal/platform/dbclient"
	"github.com/example/chunk-splitter/internal/platform/llmusage"
	"github.com/example/chunk-splitter/internal/platform/logger"
	"github.com/example/chunk-splitter/internal/platform/messaging"
	"github.com/example/chunk-splitter/internal/platform/observability"
	"github.com/example/chunk-splitter/internal/platform/searchstore"
	"github.com/example/chunk-splitter/internal/platform/storage"
	"github.com/example/chunk-splitter/internal/platform/valkey"
	"github.com/example/chunk-splitter/internal/platform/vectorstore"
)

func main() {
	log := logger.New("chunk-splitter", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("chunk-splitter stopped with error")
		os.Exit(1)
	}
}

func run(log zerolog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Embedding dimension is shared with the Qdrant collection; it must match
	// the configured embedder, so it is read once and reused.
	embeddingDim := config.GetInt("EMBEDDING_DIM", 1024)

	// db-service client for status transitions.
	db, err := dbclient.New(config.MustGet("DB_SERVICE_ADDR"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Qdrant: ensure the collection exists with the right vector dimension.
	// chunk-splitter owns this collection; llm-service only reads it.
	qdrant := vectorstore.NewQdrant(config.MustGet("QDRANT_URL"), config.Get("QDRANT_COLLECTION", "documents"))
	if cerr := qdrant.EnsureCollection(ctx, embeddingDim); cerr != nil {
		return fmt.Errorf("ensure qdrant collection: %w", cerr)
	}

	// OpenSearch: ensure the lexical index exists with the chunk mapping.
	opensearch := searchstore.NewOpenSearch(config.MustGet("OPENSEARCH_URL"), config.Get("OPENSEARCH_INDEX", "chunks"))
	if ierr := opensearch.EnsureIndex(ctx); ierr != nil {
		return fmt.Errorf("ensure opensearch index: %w", ierr)
	}

	// Embedder: real HTTP backend when EMBEDDINGS_URL is set, deterministic stub
	// otherwise (so the pipeline runs end-to-end with zero GPUs).
	embedder := aiclients.NewEmbedder(
		config.Get("EMBEDDINGS_URL", ""),
		config.Get("EMBEDDINGS_MODEL", ""),
		embeddingDim,
		nil,
	)

	// RabbitMQ connection, topology and publisher.
	conn, err := messaging.Dial(ctx, config.MustGet("RABBITMQ_URL"), log)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if terr := conn.DeclareTopology(); terr != nil {
		return terr
	}
	pub, err := conn.NewPublisher()
	if err != nil {
		return err
	}
	defer func() { _ = pub.Close() }()

	// Interactive-priority pacing: yield to in-flight user queries (gauge kept
	// by llm-service in Valkey). Fail open — ingestion runs unpaced when Valkey
	// is unavailable, because load signalling is an optimisation, not a
	// dependency.
	var pacer application.Pacer
	var usageKV llmusage.KV // shared LLM-usage ledger; nil when Valkey is down
	if vk, verr := valkey.New(ctx, config.Get("VALKEY_ADDR", "valkey:6379"), config.Get("VALKEY_PASSWORD", ""), 0); verr != nil {
		log.Warn().Err(verr).Msg("valkey unavailable; ingestion pacing disabled")
	} else {
		defer func() { _ = vk.Close() }()
		pacer = pacing.New(vk, config.GetDuration("INGEST_YIELD_MAX_WAIT", 2*time.Second))
		usageKV = vk
	}

	// Claim-check texts: a huge parsed text arrives as an S3 key, not in the AMQP
	// body, so reading it needs an object store.
	store, err := storage.New(ctx, storage.ConfigFromEnv())
	if err != nil {
		return err
	}

	// Assemble the use case from the recursive splitter and the platform-backed
	// adapters. The rune-budgeted splitter is always constructed: it is the
	// fallback used whenever no tokenizer is configured (token mode OFF).
	overlap := config.GetInt("CHUNK_OVERLAP", 150)
	chunker := splitter.NewRecursive(config.GetInt("CHUNK_SIZE", 1000), overlap)

	// EXACT token-budget chunking: when TOKENIZER_URL is set, size chunks by real
	// F2LLM-v2 tokens (CHUNK_MAX_TOKENS) via the llama.cpp /tokenize endpoint on the
	// same server the embedder uses. Empty TOKENIZER_URL → tok stays nil → the
	// rune splitter above is used unchanged. The interface variable is left nil
	// unless a concrete client exists, so the indexer's `tokenizer != nil` gate is
	// not tripped by a typed-nil pointer.
	var tok application.Tokenizer
	if lc := tokenizer.New(config.Get("TOKENIZER_URL", ""), nil); lc != nil {
		tok = lc
	}

	metrics := observability.NewMetrics()
	indexer := application.New(
		chunker,
		db,
		embedding.New(embedder),
		vectorindex.New(qdrant),
		lexicalindex.New(opensearch),
		pub,
		pacer,
		store,
		tok,
		config.GetInt("CHUNK_MAX_TOKENS", 512),
		config.GetInt("TEXT_MAX_MB", 512)<<20,
		config.GetInt("EMBED_BATCH", 64),
		config.GetInt("SPLIT_WINDOW_MB", 8)<<20,
		overlap,
		config.GetBool("CONTEXTUAL_HEADERS", true),
		metrics,
	)

	configureEnrichers(indexer, db, usageKV)

	ready := readiness(db, qdrant, opensearch)
	go func() {
		_ = observability.RunOps(ctx, config.Get("METRICS_ADDR", ":9090"), metrics, ready, log)
	}()

	cfg := messaging.ConsumerConfig{
		Queue:    contracts.RouteChunk,
		Workers:  config.GetInt("WORKER_CONCURRENCY", 4),
		Prefetch: 0,
		// Bound per-message processing so a hung embedder/store frees the worker
		// slot (the message dead-letters) instead of pinning it forever. The
		// default is deliberately generous — large documents take a long time to
		// embed and index — and is overridable via CHUNK_HANDLE_TIMEOUT.
		HandleTimeout: config.GetDuration("CHUNK_HANDLE_TIMEOUT", 30*time.Minute),
		Recorder:      metrics,
	}
	return conn.Consume(ctx, cfg, amqpiface.Handler(indexer))
}

// configureEnrichers wires the metadata enrichers that run alongside indexing:
// LLM title+kind extraction (when a generation backend is configured), document
// classification persistence, and parser-metadata persistence.
func configureEnrichers(ix *application.Indexer, db *dbclient.Client, usageKV llmusage.KV) {
	configureTitles(ix, db, usageKV)
	ix.EnableKinds(db)
	ix.EnableMeta(db)
}

// configureTitles enables LLM article-title extraction on ix when a generation
// backend is configured (VLLM_URL): each document's real title is extracted from
// its opening text and persisted via setter so the documents page shows the
// article title instead of the uploaded filename. No backend → no-op, and the UI
// falls back to the filename.
func configureTitles(ix *application.Indexer, setter application.TitleSetter, usageKV llmusage.KV) {
	genURL := config.Get("VLLM_URL", "")
	if genURL == "" {
		return
	}
	gen := aiclients.NewGenerator(
		genURL,
		config.Get("VLLM_MODEL", ""),
		config.Get("VLLM_API_KEY", ""),
		config.GetInt("VLLM_RPM", 0),
		nil,
	)
	ix.EnableTitles(titler.New(gen, usageKV), setter)
}

// readiness reports ready only when every hard dependency of the indexer —
// db-service (status updates), Qdrant (vectors) and OpenSearch (BM25) — is
// reachable, since losing any one of them stalls indexing.
func readiness(db *dbclient.Client, qdrant *vectorstore.Qdrant, opensearch *searchstore.OpenSearch) observability.ReadyFunc {
	return func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return err
		}
		if err := qdrant.Ping(ctx); err != nil {
			return err
		}
		return opensearch.Ping(ctx)
	}
}
