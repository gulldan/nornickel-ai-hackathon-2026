// Package scoringstore adapts a JSON key-value store (Valkey) to the
// application.ScoringWeightsStore port, so per-owner ranking weights survive
// restarts. It lives outside the application package to keep that package free
// of any concrete client dependency (clean architecture): it depends only on a
// structural JSONStore interface, satisfied by platform/valkey.Client.
package scoringstore

import (
	"context"
	"time"

	"github.com/example/main-service/internal/application"
)

// weightsKeyPrefix namespaces per-owner scoring overrides in Valkey.
const weightsKeyPrefix = "rag:scoring:weights:"

// weightsTTL keeps stored overrides effectively permanent; they are small and
// rewritten on every edit, so a long TTL only reaps abandoned owners.
const weightsTTL = 365 * 24 * time.Hour

// JSONStore is the minimal store surface the adapter needs; it is satisfied by
// platform/valkey.Client.
type JSONStore interface {
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
	GetJSON(ctx context.Context, key string, dest any) (bool, error)
}

// Store persists per-owner scoring weights as JSON.
type Store struct {
	kv JSONStore
}

// New builds a Store over the given JSON key-value backend.
func New(kv JSONStore) *Store {
	return &Store{kv: kv}
}

// Get returns the owner's stored weights. A miss yields the default profile so
// callers always get a usable, non-nil set of weights.
func (s *Store) Get(ctx context.Context, ownerID string) (*application.ScoringWeights, error) {
	w := application.DefaultWeights()
	if _, err := s.kv.GetJSON(ctx, weightsKeyPrefix+ownerID, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// Set persists the owner's weight override.
func (s *Store) Set(ctx context.Context, ownerID string, w application.ScoringWeights) error {
	return s.kv.SetJSON(ctx, weightsKeyPrefix+ownerID, w, weightsTTL)
}
