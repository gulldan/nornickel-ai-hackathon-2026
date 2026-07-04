package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/example/auth/internal/domain"
	"github.com/example/auth/internal/infrastructure/sessions"
	"github.com/example/auth/internal/platform/valkey"
)

// newStore builds a Store backed by an in-process miniredis and returns it
// alongside the server so tests can manipulate or stop the backend.
func newStore(t *testing.T, ttl time.Duration) (*sessions.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	vk, err := valkey.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("valkey.New: %v", err)
	}
	t.Cleanup(func() { _ = vk.Close() })
	return sessions.New(vk, ttl), mr
}

// TestSaveLoadRoundTrip stores an identity and reads it back intact.
func TestSaveLoadRoundTrip(t *testing.T) {
	store, mr := newStore(t, time.Hour)
	ctx := context.Background()
	want := &domain.Identity{UserID: "u1", Username: "bob", Roles: []string{"user", "editor"}}

	if err := store.Save(ctx, "tok-1", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The key is namespaced with the refresh: prefix.
	if !mr.Exists("refresh:tok-1") {
		t.Fatalf("expected key refresh:tok-1 to exist")
	}
	got, err := store.Load(ctx, "tok-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.UserID != want.UserID || got.Username != want.Username || len(got.Roles) != 2 {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

// TestSaveSetsTTL checks the configured TTL is applied to the stored key.
func TestSaveSetsTTL(t *testing.T) {
	store, mr := newStore(t, 30*time.Minute)
	if err := store.Save(context.Background(), "tok-ttl", &domain.Identity{UserID: "u1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if ttl := mr.TTL("refresh:tok-ttl"); ttl != 30*time.Minute {
		t.Fatalf("TTL = %s, want 30m", ttl)
	}
	// After fast-forwarding past the TTL the session is gone.
	mr.FastForward(31 * time.Minute)
	if _, err := store.Load(context.Background(), "tok-ttl"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Load after expiry = %v, want ErrSessionNotFound", err)
	}
}

// TestLoadMiss returns ErrSessionNotFound for an unknown token.
func TestLoadMiss(t *testing.T) {
	store, _ := newStore(t, time.Hour)
	if _, err := store.Load(context.Background(), "absent"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Load miss = %v, want ErrSessionNotFound", err)
	}
}

// TestDelete revokes a token; a subsequent load misses and deleting an absent
// token is a no-op.
func TestDelete(t *testing.T) {
	store, _ := newStore(t, time.Hour)
	ctx := context.Background()
	if err := store.Save(ctx, "tok-del", &domain.Identity{UserID: "u1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(ctx, "tok-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Load(ctx, "tok-del"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Load after delete = %v, want ErrSessionNotFound", err)
	}
	// Deleting an already-absent key must not error.
	if err := store.Delete(ctx, "tok-del"); err != nil {
		t.Fatalf("Delete absent = %v, want nil", err)
	}
}

// TestStoreErrorsWhenBackendDown verifies every method wraps and surfaces a
// backend failure once miniredis is closed.
func TestStoreErrorsWhenBackendDown(t *testing.T) {
	store, mr := newStore(t, time.Hour)
	ctx := context.Background()
	mr.Close() // drop the backend so every command fails

	if err := store.Save(ctx, "tok", &domain.Identity{UserID: "u1"}); err == nil {
		t.Fatal("Save: expected error when backend is down")
	}
	if _, err := store.Load(ctx, "tok"); err == nil || errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Load: expected a backend error, got %v", err)
	}
	if err := store.Delete(ctx, "tok"); err == nil {
		t.Fatal("Delete: expected error when backend is down")
	}
}
