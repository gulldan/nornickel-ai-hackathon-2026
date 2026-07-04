package cache_test

// Unit tests for the Valkey-backed answer cache. An in-process miniredis stands
// in for Valkey so the JSON round-trip, the TTL and the epoch counter are
// exercised end-to-end without a real server.

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/infrastructure/cache"
	"github.com/example/llm-service/internal/platform/valkey"
)

// newCache starts an in-process Redis and returns an AnswerCache plus the raw
// miniredis handle (for direct key inspection).
func newCache(t *testing.T) (*cache.AnswerCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client, err := valkey.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() {
		if cerr := client.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return cache.NewAnswerCache(client, time.Minute), mr
}

// TestSetGetRoundTrip stores a result and reads it back intact.
func TestSetGetRoundTrip(t *testing.T) {
	c, _ := newCache(t)
	ctx := context.Background()
	want := domain.Result{
		Answer:  "16 bar",
		Sources: []domain.Source{{DocumentID: "d1", ChunkID: "c1", Snippet: "s", Score: 0.9}},
		Model:   "m",
	}
	if err := c.Set(ctx, "k", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, hit, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !hit {
		t.Fatal("expected a hit")
	}
	if got.Answer != want.Answer || got.Model != want.Model || len(got.Sources) != 1 {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestGetMiss returns a zero value and false for an absent key.
func TestGetMiss(t *testing.T) {
	c, _ := newCache(t)
	got, hit, err := c.Get(context.Background(), "absent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hit {
		t.Fatal("expected a miss for an absent key")
	}
	if got.Answer != "" {
		t.Fatalf("miss should yield a zero Result, got %+v", got)
	}
}

// TestSetAppliesTTL stores under the configured TTL.
func TestSetAppliesTTL(t *testing.T) {
	c, mr := newCache(t)
	if err := c.Set(context.Background(), "ttl-key", domain.Result{Answer: "a"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ttl := mr.TTL("ttl-key"); ttl != time.Minute {
		t.Fatalf("ttl = %v, want %v", ttl, time.Minute)
	}
}

// TestGetDecodeError surfaces an error when the stored bytes are not valid JSON.
func TestGetDecodeError(t *testing.T) {
	c, mr := newCache(t)
	if err := mr.Set("bad", "{not json"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := c.Get(context.Background(), "bad"); err == nil {
		t.Fatal("expected a decode error for malformed JSON")
	}
}

// TestEpochReadsCounter returns the corpus epoch for a scope.
func TestEpochReadsCounter(t *testing.T) {
	c, mr := newCache(t)
	if err := mr.Set("rag:corpus_epoch:shared", "42"); err != nil {
		t.Fatalf("seed epoch: %v", err)
	}
	if got := c.Epoch(context.Background(), "shared"); got != 42 {
		t.Fatalf("epoch = %d, want 42", got)
	}
}

// TestEpochMissingIsZero returns 0 when the scope has no counter yet.
func TestEpochMissingIsZero(t *testing.T) {
	c, _ := newCache(t)
	if got := c.Epoch(context.Background(), "fresh"); got != 0 {
		t.Fatalf("epoch = %d, want 0 for an unset scope", got)
	}
}

// TestEpochNonNumericIsZero swallows a parse error (best-effort) and returns 0.
func TestEpochNonNumericIsZero(t *testing.T) {
	c, mr := newCache(t)
	if err := mr.Set("rag:corpus_epoch:bad", "not-a-number"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := c.Epoch(context.Background(), "bad"); got != 0 {
		t.Fatalf("epoch = %d, want 0 on a non-numeric counter", got)
	}
}
