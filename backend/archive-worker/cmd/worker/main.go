// Command archive-worker is a stateless AMQP worker. It consumes the
// parse.archive queue as a competing consumer, hands each archive to the
// archive-scan service (Rust + libarchive, extracts straight into S3) and
// registers every extracted file as its own document in the ingestion
// pipeline. It follows the pdf-parser worker shape.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/archive-worker/internal/application"
	"github.com/example/archive-worker/internal/infrastructure/archivescan"
	amqpiface "github.com/example/archive-worker/internal/interfaces/amqp"
	"github.com/example/archive-worker/internal/platform/config"
	"github.com/example/archive-worker/internal/platform/contracts"
	"github.com/example/archive-worker/internal/platform/dbclient"
	"github.com/example/archive-worker/internal/platform/logger"
	"github.com/example/archive-worker/internal/platform/messaging"
	"github.com/example/archive-worker/internal/platform/observability"
	"github.com/example/archive-worker/internal/platform/storage"
)

// mib converts the megabyte-denominated archive size caps to bytes.
const mib int64 = 1 << 20

func main() {
	log := logger.New("archive-worker", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("archive-worker stopped with error")
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

	extractor := archivescan.New(
		config.MustGet("ARCHIVE_SCAN_URL"),
		store,
		config.GetDuration("ARCHIVE_HTTP_TIMEOUT", 2*time.Hour),
		archivescan.Limits{
			// Resource-exhaustion guards forwarded to archive-scan. Bytes are
			// configured in MB and the timeout in seconds; 0/absent disables a
			// guard.
			MaxFileBytes:   int64(config.GetInt("ARCHIVE_MAX_FILE_MB", 256)) * mib,
			MaxTotalBytes:  int64(config.GetInt("ARCHIVE_MAX_TOTAL_MB", 2048)) * mib,
			MaxRatio:       config.GetFloat("ARCHIVE_MAX_RATIO", 200),
			ExtractTimeout: time.Duration(config.GetInt("ARCHIVE_EXTRACT_TIMEOUT", 3600)) * time.Second,
		},
	)
	processor := application.New(extractor, db, pub, config.GetInt("ARCHIVE_MAX_ENTRIES", 50000))

	metrics := observability.NewMetrics()
	go func() {
		_ = observability.RunOps(ctx, config.Get("METRICS_ADDR", ":9090"), metrics, db.Ping, log)
	}()

	cfg := messaging.ConsumerConfig{
		Queue:    contracts.RouteParseArch,
		Workers:  config.GetInt("WORKER_CONCURRENCY", 4),
		Prefetch: 0,
		// Archives with thousands of files take a long time: this deadline must
		// be SAFELY larger than ARCHIVE_HTTP_TIMEOUT (archive-scan extraction,
		// 2h by default) plus the time to register every entry, or the dispatcher
		// would abort a legitimate long extraction and dead-letter the archive.
		HandleTimeout: config.GetDuration("ARCHIVE_HANDLE_TIMEOUT", 3*time.Hour),
		Recorder:      metrics,
	}
	return conn.Consume(ctx, cfg, amqpiface.Handler(processor))
}
