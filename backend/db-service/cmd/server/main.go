// Command db-service is the gRPC BFF over PostgreSQL: it owns users, documents,
// chats/messages and AI-model metadata, and is the only service that touches the
// database. It is stateless and horizontally scalable behind the pool.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/example/db-service/internal/application"
	"github.com/example/db-service/internal/domain"
	"github.com/example/db-service/internal/infrastructure/postgres"
	"github.com/example/db-service/internal/interfaces/grpcserver"
	"github.com/example/db-service/internal/platform/config"
	"github.com/example/db-service/internal/platform/grpcx"
	"github.com/example/db-service/internal/platform/logger"
	"github.com/example/db-service/internal/platform/observability"

	dbv1 "github.com/example/db-service/internal/platform/genproto/db/v1"
)

func main() {
	log := logger.New("db-service", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("db-service stopped with error")
		os.Exit(1)
	}
}

func run(log zerolog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Connect(ctx, config.MustGet("POSTGRES_DSN"), log)
	if err != nil {
		return err
	}
	defer db.Close()
	if merr := db.Migrate(ctx); merr != nil {
		return merr
	}

	svc := application.New(
		postgres.NewUserRepo(db),
		postgres.NewDocumentRepo(db),
		postgres.NewChatRepo(db),
		postgres.NewModelRepo(db),
		postgres.NewKPIRepo(db),
		postgres.NewClusterRepo(db),
		postgres.NewHypothesisRepo(db),
		postgres.NewLLMUsageRepo(db),
		postgres.NewAppSettingsRepo(db),
	)
	if serr := seed(ctx, svc); serr != nil {
		return serr
	}

	metrics := observability.NewMetrics()
	go func() {
		_ = observability.RunOps(ctx, config.Get("METRICS_ADDR", ":9090"), metrics, db.Ping, log)
	}()

	srv := grpcx.NewServer(log)
	dbv1.RegisterDbServiceServer(srv.Registrar(), grpcserver.New(svc))
	return srv.Run(ctx, config.Get("GRPC_ADDR", ":9091"))
}

// seed bootstraps the admin account and the AI-model catalogue.
func seed(ctx context.Context, svc *application.Service) error {
	adminPass := config.MustGet("ADMIN_PASSWORD")
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	models := []*domain.Model{
		{ID: "qwen3-35b-a3b", Name: "Qwen3-35B-A3B", Role: "generation", Backend: "vLLM"},
		{ID: "f2llm-v2-0.6b", Name: "F2LLM-v2-0.6B", Role: "embeddings", Backend: "llama.cpp"},
		{ID: "qwen3-reranker-0.6b", Name: "Qwen3-Reranker-0.6B", Role: "reranker", Backend: "llama.cpp"},
		{ID: "paddleocr-vl", Name: "PaddleOCR-VL", Role: "ocr", Backend: "paddleocr-vl-service"},
		{ID: "qwen3.5-9b", Name: "Qwen3.5-9B", Role: "vlm", Backend: "VLM Service"},
	}
	return svc.Seed(ctx, config.Get("ADMIN_USERNAME", "admin"), string(hash), models)
}
