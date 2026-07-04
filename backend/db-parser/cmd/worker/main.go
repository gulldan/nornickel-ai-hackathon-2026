// Command db-parser is a stateless AMQP worker. It consumes the parse.db queue
// as a competing consumer and renders SQLite database files as readable text
// for chunk-splitter. It follows the office-parser reference shape; only the
// extractor differs.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/db-parser/internal/application"
	sqliteinfra "github.com/example/db-parser/internal/infrastructure/sqlitedb"
	amqpiface "github.com/example/db-parser/internal/interfaces/amqp"
	"github.com/example/db-parser/internal/platform/config"
	"github.com/example/db-parser/internal/platform/contracts"
	"github.com/example/db-parser/internal/platform/dbclient"
	"github.com/example/db-parser/internal/platform/logger"
	"github.com/example/db-parser/internal/platform/messaging"
	"github.com/example/db-parser/internal/platform/observability"
	"github.com/example/db-parser/internal/platform/storage"
)

func main() {
	log := logger.New("db-parser", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("db-parser stopped with error")
		os.Exit(1)
	}
}

func run(log zerolog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := storage.New(ctx, storage.ConfigFromEnv())
	if err != nil {
		return err
	}
	db, err := dbclient.New(config.MustGet("DB_SERVICE_ADDR"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

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

	// Лимит строк на таблицу защищает индекс от многогигабайтных дампов.
	extractor := sqliteinfra.NewExtractor(config.GetInt("DB_MAX_ROWS_PER_TABLE", 2000))
	processor := application.New(extractor, store, db, pub)

	metrics := observability.NewMetrics()
	go func() {
		_ = observability.RunOps(ctx, config.Get("METRICS_ADDR", ":9090"), metrics, db.Ping, log)
	}()

	cfg := messaging.ConsumerConfig{
		Queue:         contracts.RouteParseDB,
		Workers:       config.GetInt("WORKER_CONCURRENCY", 2),
		Prefetch:      0,
		HandleTimeout: config.GetDuration("DB_HANDLE_TIMEOUT", 10*time.Minute),
		Recorder:      metrics,
	}
	return conn.Consume(ctx, cfg, amqpiface.Handler(processor))
}
