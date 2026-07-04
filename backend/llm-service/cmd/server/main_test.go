package main

// Tests for the composition root. run is driven only as far as its first hard
// dependency (the Valkey ping) so the wiring up to that point is exercised
// without standing up Qdrant/OpenSearch/Valkey; readiness is tested directly
// against httptest backends.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"

	"github.com/example/llm-service/internal/platform/searchstore"
	"github.com/example/llm-service/internal/platform/vectorstore"
)

// TestRunFailsWithoutValkey wires every adapter (AI stubs, retrieval clients)
// and then returns the Valkey connection error, since no Valkey is reachable at
// the configured address.
func TestRunFailsWithoutValkey(t *testing.T) {
	t.Setenv("VALKEY_ADDR", "127.0.0.1:1") // nothing listens here
	t.Setenv("GRPC_ADDR", "127.0.0.1:0")
	t.Setenv("METRICS_ADDR", "127.0.0.1:0")
	if err := run(zerolog.Nop()); err == nil {
		t.Fatal("expected run to fail when Valkey is unreachable")
	}
}

func TestReadyURL(t *testing.T) {
	cases := map[string]string{
		":9090":                 "http://127.0.0.1:9090/readyz",
		"0.0.0.0:9090":          "http://127.0.0.1:9090/readyz",
		"127.0.0.1:9090":        "http://127.0.0.1:9090/readyz",
		"http://localhost:9090": "http://localhost:9090/readyz",
	}
	for in, want := range cases {
		if got := readyURL(in); got != want {
			t.Fatalf("readyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/readyz" {
				t.Fatalf("path = %q, want /readyz", r.URL.Path)
			}
			_, _ = fmt.Fprint(w, "ready")
		}))
		t.Cleanup(ok.Close)
		if err := healthcheck(ok.URL); err != nil {
			t.Fatalf("healthcheck: %v", err)
		}
	})

	t.Run("not ready", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "qdrant down", http.StatusServiceUnavailable)
		}))
		t.Cleanup(bad.Close)
		if err := healthcheck(bad.URL); err == nil {
			t.Fatal("expected healthcheck to fail on non-2xx readyz")
		}
	})
}

// TestReadinessReady reports ready when both retrieval backends answer their
// health probes.
func TestReadinessReady(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ok.Close)
	ready := readiness(vectorstore.NewQdrant(ok.URL, "documents"), searchstore.NewOpenSearch(ok.URL, "chunks"))
	if err := ready(context.Background()); err != nil {
		t.Fatalf("readiness = %v, want nil when both backends are up", err)
	}
}

// TestReadinessQdrantDown fails when the vector backend is unreachable.
func TestReadinessQdrantDown(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ok.Close)
	ready := readiness(vectorstore.NewQdrant("http://127.0.0.1:1", "documents"), searchstore.NewOpenSearch(ok.URL, "chunks"))
	if err := ready(context.Background()); err == nil {
		t.Fatal("expected readiness to fail when Qdrant is down")
	}
}

// TestReadinessOpenSearchDown fails when the lexical backend is unreachable even
// though the vector backend is healthy.
func TestReadinessOpenSearchDown(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ok.Close)
	ready := readiness(vectorstore.NewQdrant(ok.URL, "documents"), searchstore.NewOpenSearch("http://127.0.0.1:1", "chunks"))
	if err := ready(context.Background()); err == nil {
		t.Fatal("expected readiness to fail when OpenSearch is down")
	}
}
