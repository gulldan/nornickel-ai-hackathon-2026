package observability_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/ocr-service/internal/platform/observability"
)

// scrape returns the metrics exposition served by the Metrics handler.
func scrape(t *testing.T, m *observability.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics handler status = %d, want %d", rec.Code, http.StatusOK)
	}
	return rec.Body.String()
}

// listen binds an ephemeral loopback listener via a context-aware ListenConfig.
func listen(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// freeAddr reserves an ephemeral loopback address for the ops server to bind.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln := listen(t)
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("close reservation: %v", cerr)
	}
	return addr
}

// TestNewMetricsHandler serves a non-empty exposition including the Go collector.
func TestNewMetricsHandler(t *testing.T) {
	m := observability.NewMetrics()
	body := scrape(t, m)
	if !strings.Contains(body, "go_goroutines") {
		t.Fatalf("exposition missing the Go runtime collector; body=%q", body)
	}
}

// TestRecordMessage increments the processed counter and observes its latency.
func TestRecordMessage(t *testing.T) {
	m := observability.NewMetrics()
	m.RecordMessage("parse.pdf", "ok", 0.5)
	m.RecordMessage("parse.pdf", "ok", 1.5)
	m.RecordMessage("parse.pdf", "error", 0.1)

	body := scrape(t, m)
	if !strings.Contains(body, `rag_messages_processed_total{outcome="ok",queue="parse.pdf"} 2`) {
		t.Fatalf("ok counter not at 2; body=%q", body)
	}
	if !strings.Contains(body, `rag_messages_processed_total{outcome="error",queue="parse.pdf"} 1`) {
		t.Fatalf("error counter not at 1; body=%q", body)
	}
	if !strings.Contains(body, `rag_processing_duration_seconds_count{queue="parse.pdf"} 3`) {
		t.Fatalf("duration histogram count not at 3; body=%q", body)
	}
}

// TestRecordStage observes per-stage latency into the stage histogram.
func TestRecordStage(t *testing.T) {
	m := observability.NewMetrics()
	m.RecordStage("embed", 0.02)
	m.RecordStage("retrieve", 0.2)

	body := scrape(t, m)
	if !strings.Contains(body, `rag_stage_duration_seconds_count{stage="embed"} 1`) {
		t.Fatalf("embed stage count not at 1; body=%q", body)
	}
	if !strings.Contains(body, `rag_stage_duration_seconds_count{stage="retrieve"} 1`) {
		t.Fatalf("retrieve stage count not at 1; body=%q", body)
	}
}

// get issues a GET against the running ops server, retrying until it is up.
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	var lastErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, derr := http.DefaultClient.Do(req)
		if derr != nil {
			lastErr = derr
			time.Sleep(20 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatalf("close body: %v", cerr)
		}
		return resp.StatusCode, string(body)
	}
	t.Fatalf("server never became reachable at %s: %v", url, lastErr)
	return 0, ""
}

// TestRunOpsServesEndpoints brings up the ops server and probes its endpoints.
func TestRunOpsServesEndpoints(t *testing.T) {
	addr := freeAddr(t)
	m := observability.NewMetrics()
	m.RecordMessage("chunk", "ok", 0.1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- observability.RunOps(ctx, addr, m, func(context.Context) error { return nil }, zerolog.Nop())
	}()

	base := "http://" + addr
	if code, _ := get(t, base+"/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want %d", code, http.StatusOK)
	}
	if code, body := get(t, base+"/readyz"); code != http.StatusOK || !strings.Contains(body, "ready") {
		t.Fatalf("/readyz = %d %q, want %d ready", code, body, http.StatusOK)
	}
	if code, body := get(t, base+"/metrics"); code != http.StatusOK || !strings.Contains(body, "rag_messages_processed_total") {
		t.Fatalf("/metrics = %d, want %d with the message counter; body=%q", code, http.StatusOK, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOps returned %v on graceful shutdown", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("RunOps did not return after context cancellation")
	}
}

// TestRunOpsReadyFailure surfaces a failing readiness probe as 503.
func TestRunOpsReadyFailure(t *testing.T) {
	addr := freeAddr(t)
	m := observability.NewMetrics()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	notReady := errors.New("dependency down")
	go func() {
		done <- observability.RunOps(ctx, addr, m, func(context.Context) error { return notReady }, zerolog.Nop())
	}()

	code, body := get(t, "http://"+addr+"/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(body, "dependency down") {
		t.Fatalf("/readyz body = %q, want the dependency error", body)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("RunOps did not return after context cancellation")
	}
}

// TestRunOpsNilReady treats a nil readiness probe as always ready.
func TestRunOpsNilReady(t *testing.T) {
	addr := freeAddr(t)
	m := observability.NewMetrics()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- observability.RunOps(ctx, addr, m, nil, zerolog.Nop())
	}()

	if code, body := get(t, "http://"+addr+"/readyz"); code != http.StatusOK || !strings.Contains(body, "ready") {
		t.Fatalf("/readyz with nil probe = %d %q, want %d ready", code, body, http.StatusOK)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("RunOps did not return after context cancellation")
	}
}

// TestRunOpsListenError reports a bind failure on an already-used address.
func TestRunOpsListenError(t *testing.T) {
	// Hold a listener so RunOps cannot bind the same address.
	ln := listen(t)
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- observability.RunOps(ctx, ln.Addr().String(), observability.NewMetrics(), nil, zerolog.Nop())
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("RunOps returned nil for an unavailable address")
		}
		if !strings.Contains(err.Error(), "ops server") {
			t.Fatalf("error = %v, want it wrapped as an ops server failure", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("RunOps did not report the bind failure")
	}
}
