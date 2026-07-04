package main

import (
	"context"
	"os"
	"testing"

	"github.com/rs/zerolog"

	"github.com/example/db-service/internal/application"
	"github.com/example/db-service/internal/infrastructure/postgres"
)

// TestSeed runs the startup seeding against a live PostgreSQL: it hashes the
// admin password and upserts the model catalogue, and must be idempotent across
// repeated startups.
func TestSeed(t *testing.T) {
	dsn := os.Getenv("DB_TEST_DSN")
	if dsn == "" {
		t.Skip("set DB_TEST_DSN to run db-service seeding test")
	}
	t.Setenv("ADMIN_PASSWORD", "test-admin-password")
	ctx := context.Background()
	db, err := postgres.Connect(ctx, dsn, zerolog.Nop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	if merr := db.Migrate(ctx); merr != nil {
		t.Fatalf("migrate: %v", merr)
	}

	svc := application.New(
		postgres.NewUserRepo(db), postgres.NewDocumentRepo(db), postgres.NewChatRepo(db),
		postgres.NewModelRepo(db), postgres.NewKPIRepo(db), postgres.NewClusterRepo(db),
		postgres.NewHypothesisRepo(db),
		postgres.NewLLMUsageRepo(db), postgres.NewAppSettingsRepo(db),
	)

	if serr := seed(ctx, svc); serr != nil {
		t.Fatalf("seed: %v", serr)
	}
	// Seeding twice must not fail (model upserts are idempotent; the admin is
	// only created on an empty table).
	if serr := seed(ctx, svc); serr != nil {
		t.Fatalf("seed (idempotent): %v", serr)
	}

	models, err := svc.ListModels(ctx)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) < 5 {
		t.Fatalf("expected the seeded model catalogue, got %d models", len(models))
	}
}
