package domain

import (
	"context"
	"time"
)

// AppSetting is one runtime override: Key mirrors an env var name.
type AppSetting struct {
	Key       string
	Value     string
	UpdatedAt time.Time
}

// AppSettingsRepository stores global runtime overrides.
type AppSettingsRepository interface {
	List(ctx context.Context) ([]*AppSetting, error)
	Upsert(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
}
