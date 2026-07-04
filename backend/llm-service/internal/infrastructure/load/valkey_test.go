package load_test

// Unit tests for the Valkey-backed in-flight query marker. An in-process
// miniredis stands in for Valkey so the shared gauge and its TTL are exercised
// without a real server. The methods are best-effort, so the closed-client cases
// confirm a backend failure is swallowed rather than propagated.

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/example/llm-service/internal/infrastructure/load"
	"github.com/example/llm-service/internal/platform/valkey"
)

// newMarker starts an in-process Redis and returns a Marker plus the raw
// miniredis handle for gauge inspection.
func newMarker(t *testing.T) (*load.Marker, *miniredis.Miniredis) {
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
	return load.New(client), mr
}

// gauge reads the shared in-flight counter, failing the test on a read error.
func gauge(t *testing.T, mr *miniredis.Miniredis) string {
	t.Helper()
	v, err := mr.Get(load.Key)
	if err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return v
}

// TestStartedFinishedAdjustGauge increments on start and decrements on finish,
// leaving the shared key at zero.
func TestStartedFinishedAdjustGauge(t *testing.T) {
	m, mr := newMarker(t)
	ctx := context.Background()
	m.QueryStarted(ctx)
	m.QueryStarted(ctx)
	if got := gauge(t, mr); got != "2" {
		t.Fatalf("gauge after two starts = %q, want 2", got)
	}
	if mr.TTL(load.Key) <= 0 {
		t.Fatal("QueryStarted should refresh the gauge TTL")
	}
	m.QueryFinished(ctx)
	m.QueryFinished(ctx)
	if got := gauge(t, mr); got != "0" {
		t.Fatalf("gauge after matching finishes = %q, want 0", got)
	}
}

// TestMarkerBestEffortOnFailure swallows backend errors: neither method panics
// or surfaces an error when the client connection is closed.
func TestMarkerBestEffortOnFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	client, err := valkey.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	m := load.New(client)
	ctx := context.Background()
	// Both are best-effort; the assertion is simply that they return without
	// panicking even though every Valkey call now errors.
	m.QueryStarted(ctx)
	m.QueryFinished(ctx)
}
