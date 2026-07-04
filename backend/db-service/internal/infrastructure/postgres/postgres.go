// Package postgres is the PostgreSQL adapter implementing db-service's domain
// repositories on top of sqlc-generated, type-checked queries (package sqlcgen).
// It connects with retry and applies the embedded goose migrations on startup.
package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"

	"github.com/example/db-service/internal/platform/config"
)

// defaultStatementTimeoutSec caps how long any single SQL statement may run on
// the server before PostgreSQL aborts it, so a stuck or pathological query can
// never pin a pooled connection forever. Overridable via DB_STATEMENT_TIMEOUT_SEC.
const defaultStatementTimeoutSec = 15

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB owns the connection pool and the DSN (the latter is reused to open a
// short-lived database/sql handle for goose, which needs the stdlib driver).
type DB struct {
	Pool *pgxpool.Pool
	dsn  string
}

// Connect dials PostgreSQL, retrying until it is reachable or ctx is cancelled.
func Connect(ctx context.Context, dsn string, log zerolog.Logger) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10

	// Set a server-side statement_timeout on every pooled connection: this is the
	// per-SQL ceiling enforced by PostgreSQL itself (in milliseconds), independent
	// of request context cancellation. A non-positive value disables the cap.
	if sec := config.GetInt("DB_STATEMENT_TIMEOUT_SEC", defaultStatementTimeoutSec); sec > 0 {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(sec * 1000)
	}

	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, perr := pgxpool.NewWithConfig(ctx, cfg)
		if perr == nil {
			if perr = pool.Ping(ctx); perr == nil {
				log.Info().Msg("connected to postgres")
				return &DB{Pool: pool, dsn: dsn}, nil
			}
			pool.Close()
		}
		lastErr = perr
		log.Warn().Int("attempt", attempt).Err(perr).Msg("postgres not ready, retrying")
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect postgres: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("connect postgres: %w", lastErr)
}

// Migrate applies the embedded goose migrations. goose needs a database/sql
// handle, so a short-lived one is opened over the pgx stdlib driver and closed
// immediately; the pgxpool remains the app's connection.
func (db *DB) Migrate(ctx context.Context) error {
	sqlDB, err := sql.Open("pgx", db.dsn)
	if err != nil {
		return fmt.Errorf("open migration db: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(migrationsFS)
	if derr := goose.SetDialect("postgres"); derr != nil {
		return fmt.Errorf("goose dialect: %w", derr)
	}
	if uerr := goose.UpContext(ctx, sqlDB, "migrations"); uerr != nil {
		return fmt.Errorf("apply migrations: %w", uerr)
	}
	return nil
}

// Close releases the pool.
func (db *DB) Close() { db.Pool.Close() }

// Ping verifies connectivity for readiness probes.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}
