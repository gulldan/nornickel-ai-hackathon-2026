// Command llm-service is the RAG gRPC service: it answers questions grounded in
// the owner's indexed corpus via hybrid retrieval (Qdrant + OpenSearch),
// cross-encoder reranking and LLM generation, fronted by a Valkey answer cache.
// It is stateless and horizontally scalable — all shared state lives in Qdrant,
// OpenSearch and Valkey. chunk-splitter (the writer) owns provisioning of the
// vector collection and search index; this service only reads them.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/llm-service/internal/application"
	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/infrastructure/aiadapters"
	"github.com/example/llm-service/internal/infrastructure/cache"
	"github.com/example/llm-service/internal/infrastructure/graph"
	"github.com/example/llm-service/internal/infrastructure/load"
	"github.com/example/llm-service/internal/infrastructure/retrieval"
	"github.com/example/llm-service/internal/interfaces/grpcserver"
	"github.com/example/llm-service/internal/platform/aiclients"
	"github.com/example/llm-service/internal/platform/config"
	"github.com/example/llm-service/internal/platform/grpcx"
	"github.com/example/llm-service/internal/platform/llmusage"
	"github.com/example/llm-service/internal/platform/logger"
	"github.com/example/llm-service/internal/platform/observability"
	"github.com/example/llm-service/internal/platform/runtimecfg"
	"github.com/example/llm-service/internal/platform/searchstore"
	"github.com/example/llm-service/internal/platform/valkey"
	"github.com/example/llm-service/internal/platform/vectorstore"

	graphv1 "github.com/example/llm-service/internal/platform/genproto/graph/v1"
	llmv1 "github.com/example/llm-service/internal/platform/genproto/llm/v1"
)

// usageRecorder adapts the Valkey client to aiadapters.UsageRecorder so every
// generation call feeds the shared LLM usage day-hashes (best-effort).
type usageRecorder struct{ kv *valkey.Client }

func (u usageRecorder) Record(ctx context.Context, model, operation string, promptTokens, completionTokens int, costUSD float64) {
	_ = llmusage.Record(ctx, u.kv, model, operation, promptTokens, completionTokens, costUSD)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(config.Get("METRICS_ADDR", ":9090")); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	log := logger.New("llm-service", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("llm-service stopped with error")
		os.Exit(1)
	}
}

func healthcheck(metricsAddr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL(metricsAddr), nil)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("readyz: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("readyz status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func readyURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/") + "/readyz"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	} else if host, port, err := net.SplitHostPort(addr); err == nil {
		switch host {
		case "", "0.0.0.0", "::", "[::]":
			addr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	return "http://" + addr + "/readyz"
}

func run(log zerolog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// AI backends. An empty URL selects a deterministic stub, so the whole RAG
	// pipeline runs end-to-end without any GPUs (see aiclients docs).
	embedder := aiclients.NewEmbedder(
		config.Get("EMBEDDINGS_URL", ""),
		config.Get("EMBEDDINGS_MODEL", ""),
		config.GetInt("EMBEDDING_DIM", 1024),
		nil,
	)
	rerankerURL := config.Get("RERANKER_URL", "")
	reranker := aiclients.NewReranker(
		rerankerURL,
		config.Get("RERANKER_MODEL", ""),
		nil,
	)

	// Hybrid retrieval stores. These must match chunk-splitter's collection,
	// embedding dimension and index. This service never provisions them.
	qdrant := vectorstore.NewQdrant(
		config.Get("QDRANT_URL", "http://qdrant:6333"),
		config.Get("QDRANT_COLLECTION", "documents"),
	)
	opensearch := searchstore.NewOpenSearch(
		config.Get("OPENSEARCH_URL", "http://opensearch:9200"),
		config.Get("OPENSEARCH_INDEX", "chunks"),
	)

	// Valkey answer cache.
	valkeyClient, err := valkey.New(ctx, config.Get("VALKEY_ADDR", "valkey:6379"), config.Get("VALKEY_PASSWORD", ""), 0)
	if err != nil {
		return err
	}
	defer func() { _ = valkeyClient.Close() }()

	answerCache := cache.NewAnswerCache(valkeyClient, config.GetDuration("ANSWER_CACHE_TTL", time.Hour))

	// Generation backend resolved per call (runtime overrides from the admin
	// settings panel land in Valkey): switching VLLM_URL/MODEL/API_KEY — e.g.
	// local llama.cpp <-> OpenRouter — applies without a restart.
	ovr := runtimecfg.New(valkeyClient)
	generator := aiclients.NewDynamicGenerator(func(ctx context.Context) aiclients.GenTarget {
		return aiclients.GenTarget{
			URL:    ovr.Get(ctx, "VLLM_URL", ""),
			Model:  ovr.Get(ctx, "VLLM_MODEL", ""),
			APIKey: ovr.Get(ctx, "VLLM_API_KEY", ""),
			RPM:    ovr.GetInt(ctx, "VLLM_RPM", 0),
		}
	}, nil)

	// Wire the DDD layers: infrastructure adapters -> application use case. The
	// load marker publishes in-flight query activity so chunk-splitter yields
	// ingestion capacity while users are asking questions.
	metrics := observability.NewMetrics()
	go func() {
		_ = observability.RunOps(ctx, config.Get("METRICS_ADDR", ":9090"), metrics, readiness(qdrant, opensearch), log)
	}()

	// RAG_SCORE_FLOOR: minimum top reranker score to attempt an answer; below
	// it (or with nothing retrieved) the pipeline abstains instead of letting
	// the model confabulate. The 0.5 default matches the compose setting for
	// Qwen3-Reranker's normalised 0..1 scores. Enforced only when a real
	// reranker is wired and healthy (skipped for the stub scorer and while the
	// reranker leg is degraded).
	scoreFloor := config.GetFloat("RAG_SCORE_FLOOR", 0.5)
	log.Info().Float64("score_floor", scoreFloor).Bool("reranker", rerankerURL != "").
		Msg("abstain score floor configured")

	svc := application.New(
		aiadapters.NewEmbedder(embedder),
		retrieval.NewQdrant(qdrant),
		retrieval.NewOpenSearch(opensearch),
		aiadapters.NewReranker(reranker),
		aiadapters.NewGeneratorWithTimeout(generator, config.GetDuration("RAG_GENERATE_TIMEOUT", 150*time.Second)).
			WithUsage(usageRecorder{kv: valkeyClient}),
		answerCache,
		retrieval.NewChunkReader(qdrant),
		load.New(valkeyClient),
		metrics,
		config.GetInt("RAG_TOP_K", 5),
		config.GetBool("RAG_SHARED_CORPUS", true),
		scoreFloor,
		rerankerURL != "",
		application.Tuning{
			// Cap chunks per document in the final top-K so evidence spans several
			// sources instead of near-duplicate passages from one. Backfilled, so
			// the count never drops; doc-level recall is unchanged. 0 disables.
			MaxPerDoc:     config.GetInt("RAG_MAX_PER_DOC", 4),
			VecWeight:     config.GetFloat("RAG_VEC_WEIGHT", 1),
			LexWeight:     config.GetFloat("RAG_LEX_WEIGHT", 1),
			RetrievalMult: config.GetInt("RAG_RETRIEVAL_MULT", 4),
			RetrievalMin:  config.GetInt("RAG_RETRIEVAL_MIN", 20),
		},
	)

	// Optional agentic multi-hop controller (RAG_AGENTIC, default off). When
	// disabled this is a no-op and svc.Answer keeps the single-shot path exactly.
	if cleanup := configureAgentic(log, svc, ovr); cleanup != nil {
		defer cleanup()
	}

	// Bounded admission keeps query tail latency predictable under bursts; the
	// overflow is rejected with ResourceExhausted, which the edge maps to 503.
	srv := grpcx.NewServer(log)
	llmv1.RegisterRagServiceServer(srv.Registrar(), grpcserver.New(
		svc,
		config.GetInt("RAG_MAX_CONCURRENT", 16),
		config.GetDuration("RAG_QUEUE_WAIT", 10*time.Second),
	))
	return srv.Run(ctx, config.Get("GRPC_ADDR", ":9091"))
}

// configureAgentic enables the optional Search-o1-style multi-hop controller when
// RAG_AGENTIC is on, and returns a cleanup func (closing the graph-compute
// connection) or nil. With the flag off it does nothing, so Answer keeps the
// single-shot path. The reasoner reuses the same chat backend as the generator
// but may run a different model (RAG_REASONER_MODEL, default VLLM_MODEL); the
// graph tool is wired only when RAG_AGENTIC_GRAPH is on and degrades gracefully
// when graph-compute is unreachable.
func configureAgentic(log zerolog.Logger, svc *application.RAGService, ovr *runtimecfg.Overrides) func() {
	if !config.GetBool("RAG_AGENTIC", false) {
		return nil
	}
	reasonerGen := aiclients.NewDynamicGenerator(func(ctx context.Context) aiclients.GenTarget {
		return aiclients.GenTarget{
			URL:    ovr.Get(ctx, "VLLM_URL", ""),
			Model:  ovr.Get(ctx, "RAG_REASONER_MODEL", ovr.Get(ctx, "VLLM_MODEL", "")),
			APIKey: ovr.Get(ctx, "VLLM_API_KEY", ""),
			RPM:    ovr.GetInt(ctx, "VLLM_RPM", 0),
		}
	}, nil)

	var graphExp domain.GraphExpander
	var cleanup func()
	if config.GetBool("RAG_AGENTIC_GRAPH", false) {
		addr := config.Get("GRAPH_COMPUTE_ADDR", "graph-compute:9093")
		conn, err := grpcx.Dial(addr)
		if err != nil {
			log.Warn().Err(err).Str("addr", addr).Msg("agentic graph dial failed; graph tool disabled")
		} else {
			cleanup = func() { _ = conn.Close() }
			graphExp = graph.NewClient(
				graphv1.NewGraphComputeClient(conn),
				config.Get("QDRANT_COLLECTION", "documents"),
				// Distinct from the Rust workers' numeric GRAPH_COMPUTE_TIMEOUT
				// (seconds): this Go client wants a duration string (e.g. "10s").
				config.GetDuration("RAG_GRAPH_COMPUTE_TIMEOUT", 10*time.Second),
			)
		}
	}

	maxHops := config.GetInt("RAG_AGENTIC_MAX_HOPS", 3)
	svc.EnableAgentic(
		application.AgenticConfig{MaxHops: maxHops, GraphTopN: config.GetInt("RAG_AGENTIC_GRAPH_TOPN", 5)},
		aiadapters.NewReasoner(reasonerGen, config.GetInt("RAG_REASONER_MAX_TOKENS", 1024)),
		graphExp,
	)
	log.Info().Bool("graph", graphExp != nil).Int("max_hops", maxHops).
		Str("reasoner_model", config.Get("RAG_REASONER_MODEL", config.Get("VLLM_MODEL", ""))).
		Msg("agentic controller enabled")
	return cleanup
}

// readiness reports the service ready only when both retrieval backends are
// reachable. The answer cache is best-effort (the pipeline tolerates its
// failure), so it is intentionally excluded from the readiness gate.
func readiness(qdrant *vectorstore.Qdrant, opensearch *searchstore.OpenSearch) observability.ReadyFunc {
	return func(ctx context.Context) error {
		if err := qdrant.Ping(ctx); err != nil {
			return err
		}
		return opensearch.Ping(ctx)
	}
}
