// Package sessions adapts Valkey (via platform/valkey) to the domain.SessionStore
// port. Refresh tokens live under the key "refresh:<token>" holding the identity
// JSON, and expire after a configurable TTL so abandoned sessions clean
// themselves up. Keeping this state in Valkey (not process memory) is what lets
// auth-service scale horizontally.
package sessions

import (
	"context"
	"fmt"
	"time"

	"github.com/example/auth/internal/domain"
	"github.com/example/auth/internal/platform/valkey"
)

// keyPrefix namespaces refresh-token keys in the shared Valkey instance.
const keyPrefix = "refresh:"

// Store persists refresh-token sessions in Valkey with a fixed TTL.
type Store struct {
	vk  *valkey.Client
	ttl time.Duration
}

// New builds a Store. ttl bounds how long a refresh token stays valid.
func New(vk *valkey.Client, ttl time.Duration) *Store {
	return &Store{vk: vk, ttl: ttl}
}

// Save stores the identity JSON under the token's key with the configured TTL.
func (s *Store) Save(ctx context.Context, token string, identity *domain.Identity) error {
	if err := s.vk.SetJSON(ctx, key(token), identity, s.ttl); err != nil {
		return fmt.Errorf("store session: %w", err)
	}
	return nil
}

// Load returns the identity behind a refresh token, or domain.ErrSessionNotFound
// on a cache miss (expired or never issued).
func (s *Store) Load(ctx context.Context, token string) (*domain.Identity, error) {
	var identity domain.Identity
	found, err := s.vk.GetJSON(ctx, key(token), &identity)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if !found {
		return nil, domain.ErrSessionNotFound
	}
	return &identity, nil
}

// Delete revokes a refresh token. Deleting an absent key is a no-op.
func (s *Store) Delete(ctx context.Context, token string) error {
	if err := s.vk.Del(ctx, key(token)); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func key(token string) string { return keyPrefix + token }
