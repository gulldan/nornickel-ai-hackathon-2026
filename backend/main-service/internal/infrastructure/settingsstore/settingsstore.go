// Package settingsstore adapts a JSON key-value store (Valkey) to the
// application.RuntimeSettingsStore port, so per-owner hypothesis factory
// settings survive restarts.
package settingsstore

import (
	"context"
	"time"

	"github.com/example/main-service/internal/application"
)

const settingsKeyPrefix = "rag:hypothesis:runtime_settings:"
const settingsTTL = 365 * 24 * time.Hour

// JSONStore is the minimal store surface the adapter needs; it is satisfied by
// platform/valkey.Client.
type JSONStore interface {
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
	GetJSON(ctx context.Context, key string, dest any) (bool, error)
}

// Store persists per-owner runtime settings as JSON.
type Store struct {
	kv JSONStore
}

// New builds a Store backed by the given JSON key-value store.
func New(kv JSONStore) *Store {
	return &Store{kv: kv}
}

// Get returns the owner's stored runtime settings. A miss yields the default
// profile so callers always get a usable, non-nil set of settings.
func (s *Store) Get(ctx context.Context, ownerID string) (*application.HypothesisRuntimeSettings, error) {
	settings := application.DefaultHypothesisRuntimeSettings()
	if _, err := s.kv.GetJSON(ctx, settingsKeyPrefix+ownerID, &settings); err != nil {
		return nil, err
	}
	settings = application.NormalizeHypothesisRuntimeSettings(settings)
	return &settings, nil
}

// Set stores the owner's runtime settings (normalized), overwriting any prior value.
func (s *Store) Set(ctx context.Context, ownerID string, settings application.HypothesisRuntimeSettings) error {
	return s.kv.SetJSON(ctx, settingsKeyPrefix+ownerID, application.NormalizeHypothesisRuntimeSettings(settings), settingsTTL)
}
