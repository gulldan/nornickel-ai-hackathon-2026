// Command main-service is the platform's edge orchestrator (BFF). It is the only
// service browsers talk to: it accepts document uploads, drives the ingestion
// pipeline over RabbitMQ, proxies chat turns to the RAG llm-service over gRPC,
// and pushes live ingestion progress to browsers over WebSocket. It validates
// JWTs locally (no hop to auth on the hot path) and is stateless behind nginx.
package main

import (
	"context"
	"errors"
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

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/infrastructure/kgstore"
	"github.com/example/main-service/internal/infrastructure/matproj"
	"github.com/example/main-service/internal/infrastructure/pubsearch"
	"github.com/example/main-service/internal/infrastructure/scoringstore"
	"github.com/example/main-service/internal/infrastructure/settingsstore"
	wshub "github.com/example/main-service/internal/infrastructure/ws"
	"github.com/example/main-service/internal/interfaces/httpapi"
	wsapi "github.com/example/main-service/internal/interfaces/ws"
	"github.com/example/main-service/internal/platform/config"
	"github.com/example/main-service/internal/platform/dbclient"
	"github.com/example/main-service/internal/platform/httpx"
	"github.com/example/main-service/internal/platform/jwt"
	"github.com/example/main-service/internal/platform/logger"
	"github.com/example/main-service/internal/platform/messaging"
	"github.com/example/main-service/internal/platform/observability"
	"github.com/example/main-service/internal/platform/ragclient"
	"github.com/example/main-service/internal/platform/runtimecfg"
	platformstorage "github.com/example/main-service/internal/platform/storage"
	"github.com/example/main-service/internal/platform/valkey"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(config.Get("METRICS_ADDR", ":9090")); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	log := logger.New("main-service", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("main-service stopped with error")
		os.Exit(1)
	}
}

func healthcheck(metricsAddr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL(metricsAddr), nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
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

// jwtManagerFromEnv builds the verifying JWT manager. main-service only
// verifies tokens (auth-service issues them), so the TTL is not used on this
// path; it is read for parity with the platform's env model.
func jwtManagerFromEnv() *jwt.Manager {
	// Fail closed on the shipped dev default: compose falls back to it when
	// JWT_SECRET is unset in .env, which would accept forged admin tokens.
	secret := config.MustGet("JWT_SECRET")
	if secret == "dev-secret-change-me" {
		_, _ = fmt.Fprintln(os.Stderr, "FATAL: JWT_SECRET is the insecure default 'dev-secret-change-me'; set a strong secret in infra/.env before starting")
		os.Exit(1)
	}
	return jwt.NewManager(
		secret,
		config.Get("JWT_ISSUER", "rag-platform"),
		config.GetDuration("JWT_ACCESS_TTL", 30*time.Minute),
	)
}

func run(log zerolog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---- Object storage (original uploaded files) ----
	store, err := platformstorage.New(ctx, platformstorage.ConfigFromEnv())
	if err != nil {
		return err
	}

	// ---- db-service client (documents, chats, models) ----
	db, err := dbclient.New(config.MustGet("DB_SERVICE_ADDR"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// ---- llm-service client (RAG answers) ----
	llm, err := ragclient.New(config.MustGet("LLM_SERVICE_ADDR"))
	if err != nil {
		return err
	}
	defer func() { _ = llm.Close() }()

	// ---- RabbitMQ connection, topology and publisher ----
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

	// ---- JWT verification ----
	jwtMgr := jwtManagerFromEnv()

	// ---- Shared state store (Valkey): upload sessions, jobs, scoring weights ----
	vk, err := valkey.New(ctx, config.Get("VALKEY_ADDR", "valkey:6379"), config.Get("VALKEY_PASSWORD", ""), 0)
	if err != nil {
		return err
	}
	defer func() { _ = vk.Close() }()
	ovr := runtimecfg.New(vk)

	// ---- Application services ----
	ingestion := application.NewIngestionService(store, db, pub, llm)
	chat := application.NewChatService(db, llm)
	admin := application.NewAdminService(db)
	hyp := buildHypothesisService(db, llm, vk, ingestion, ovr)

	uploads := application.NewUploadSessionService(
		store, store, vk, ingestion,
		int64(config.GetInt("UPLOAD_HASH_MAX_MB", 1024))<<20,
		int64(config.GetInt("UPLOAD_MAX_GB", 200))<<30,
	)

	// ---- WebSocket hub + live event fan-out ----
	// An AMQP drop is fatal to the whole process: the publisher lives on the same
	// connection, and a half-alive main-service would silently drop every upload
	// ("channel/connection is not open"). Exit → docker restart → reconnect.
	ctx, failFatal := context.WithCancelCause(ctx)
	defer failFatal(nil)
	hub := wshub.NewHub(log)
	hub.SetOnIndexed(corpusEpochBumper(vk))
	go func() {
		// ConsumeEvents binds an exclusive queue to the events fanout and pushes
		// each IngestionEvent to the owning user's sockets. It returns when ctx
		// is cancelled (graceful shutdown).
		if cerr := conn.ConsumeEvents(ctx, hub.HandleEvent); cerr != nil && ctx.Err() == nil {
			log.Error().Err(cerr).Msg("events consumer stopped; shutting down to reconnect")
			failFatal(cerr)
		}
	}()

	// ---- Ops server (metrics + health probes) ----
	metrics := observability.NewMetrics()
	go func() {
		_ = observability.RunOps(ctx, config.Get("METRICS_ADDR", ":9090"), metrics, readiness(db, store, llm), log)
	}()

	// ---- HTTP routing ----
	// Business endpoints live behind the JWT middleware; the /ws upgrade is
	// mounted on the root mux and authenticates itself from a header or ?token=
	// query parameter. Health endpoints live on the ops port (RunOps).
	hypJobs := application.NewHypothesisJobService(hyp, vk)
	hypJobs.StartWorker(ctx)
	usageSvc := application.NewLLMUsageService(vk, db)
	startUsageFlusher(ctx, usageSvc)
	api := httpapi.New(
		ingestion, chat, admin, uploads, hyp, hypJobs, vk, usageSvc, metrics,
		int64(config.GetInt("MAX_UPLOAD_MB", 50)),
		config.GetBool("RAG_SHARED_CORPUS", true),
	)
	setupRuntimeSettings(ctx, api, db, vk, ovr, log)
	protected := http.NewServeMux()
	api.Routes(protected)

	root := http.NewServeMux()
	root.Handle("GET /ws", wsapi.New(hub, jwtMgr))
	root.Handle("/", jwtMgr.Middleware(protected))

	handler := httpx.Chain(root,
		httpx.RequestID,
		httpx.Recover(log),
		httpx.LogRequests(log),
	)

	srv := httpx.NewServer(config.Get("HTTP_ADDR", ":8080"), handler, log)
	err = srv.Run(ctx)
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return fmt.Errorf("events consumer failed: %w", cause)
	}
	return err
}

// buildHypothesisService wires the hypothesis factory: the owner-scoped
// knowledge graph lives in Valkey (generation mines typed triples into it; the
// KPI bridge endpoint reads them back — a Postgres/graph-DB backing is the
// production follow-up), the trailing llm arg is the ChunkReader for Stage-2
// enrichment, and an empty MP_API_KEY yields a safe Materials Project stub
// (formulas read as "unknown", never errors), so it stays optional.
func buildHypothesisService(
	db *dbclient.Client, llm *ragclient.Client, vk *valkey.Client,
	ingestion *application.IngestionService, ovr *runtimecfg.Overrides,
) *application.HypothesisService {
	hyp := application.NewHypothesisService(
		db, llm, scoringstore.New(vk), settingsstore.New(vk),
		matproj.New(config.Get("MP_API_KEY", "")), kgstore.New(vk), llm,
	)
	hyp.SetPubSearcher(pubsearch.NewAdapter(ovr))
	hyp.SetPubIngestor(ingestion)
	hyp.SetRuntimeOverrides(ovr)
	return hyp
}

// setupRuntimeSettings wires the DB-backed runtime overrides: the shared reader
// (override → env → default) and the admin store on the API.
func setupRuntimeSettings(
	ctx context.Context, api *httpapi.API, db *dbclient.Client, vk *valkey.Client,
	ovr *runtimecfg.Overrides, log zerolog.Logger,
) {
	s := httpapi.NewAppSettingsStore(db, vk)
	api.SetAppSettingsStore(s)
	api.SetRuntimeOverrides(ovr)
	startSettingsPublisher(ctx, s, log)
}

// startSettingsPublisher mirrors the DB-backed runtime overrides into Valkey on
// startup and then every minute, so services see them even after a Valkey flush.
func startSettingsPublisher(ctx context.Context, s *httpapi.AppSettingsStore, log zerolog.Logger) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			if err := s.Republish(ctx); err != nil && ctx.Err() == nil {
				log.Warn().Err(err).Msg("app settings republish failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// startUsageFlusher periodically mirrors the Valkey LLM-usage day-hashes into the
// durable Postgres ledger, so usage history survives Valkey eviction.
func startUsageFlusher(ctx context.Context, usage *application.LLMUsageService) {
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				usage.Flush(ctx)
			}
		}
	}()
}

// corpusEpochBumper returns a hub OnIndexed callback that bumps the answer-cache
// corpus epoch (per scope) when a document is indexed, retiring stale answers.
func corpusEpochBumper(vk *valkey.Client) func(context.Context, string) {
	const epochTTL = 30 * 24 * time.Hour
	return func(ctx context.Context, ownerID string) {
		_, _ = vk.IncrTTL(ctx, "rag:corpus_epoch:shared", epochTTL)
		if ownerID != "" {
			_, _ = vk.IncrTTL(ctx, "rag:corpus_epoch:"+ownerID, epochTTL)
		}
	}
}

// readiness reports ready only when hard dependencies answer their health
// checks: db-service, object storage and the RAG backend.
func readiness(db *dbclient.Client, store *platformstorage.Client, llm *ragclient.Client) observability.ReadyFunc {
	return func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return err
		}
		if err := store.Ping(ctx); err != nil {
			return err
		}
		return llm.Ping(ctx)
	}
}
