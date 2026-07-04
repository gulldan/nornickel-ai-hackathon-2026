package postgres

import (
	"context"
	"fmt"

	"github.com/example/db-service/internal/domain"
	"github.com/example/db-service/internal/infrastructure/postgres/sqlcgen"
)

// AppSettingsRepo persists global runtime setting overrides.
type AppSettingsRepo struct{ q *sqlcgen.Queries }

// NewAppSettingsRepo builds the repo over the shared pool.
func NewAppSettingsRepo(db *DB) *AppSettingsRepo { return &AppSettingsRepo{q: sqlcgen.New(db.Pool)} }

// List returns every override ordered by key.
func (r *AppSettingsRepo) List(ctx context.Context) ([]*domain.AppSetting, error) {
	rows, err := r.q.ListAppSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list app settings: %w", err)
	}
	out := make([]*domain.AppSetting, 0, len(rows))
	for i := range rows {
		out = append(out, &domain.AppSetting{Key: rows[i].Key, Value: rows[i].Value, UpdatedAt: rows[i].UpdatedAt})
	}
	return out, nil
}

// Upsert sets an override value for a key.
func (r *AppSettingsRepo) Upsert(ctx context.Context, key, value string) error {
	if err := r.q.UpsertAppSetting(ctx, sqlcgen.UpsertAppSettingParams{Key: key, Value: value}); err != nil {
		return fmt.Errorf("upsert app setting: %w", err)
	}
	return nil
}

// Delete removes an override, returning the key to env/default behaviour.
func (r *AppSettingsRepo) Delete(ctx context.Context, key string) error {
	if err := r.q.DeleteAppSetting(ctx, key); err != nil {
		return fmt.Errorf("delete app setting: %w", err)
	}
	return nil
}
