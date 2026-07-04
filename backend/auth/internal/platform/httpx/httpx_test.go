package httpx_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/auth/internal/platform/httpx"
	"github.com/example/auth/internal/platform/logger"
)

// flushRecorder is an http.ResponseWriter that records whether Flush ran.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() { f.flushed = true }

// newReq builds a context-bearing request for handler-level tests.
func newReq(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), method, target, body)
}

// serve runs h against a fresh recorder and the given request, returning the recorder.
func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestNewServerRunGracefulShutdown serves a request then drains on ctx cancel.
func TestNewServerRunGracefulShutdown(t *testing.T) {
	addr := freeAddr(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"pong": "ok"})
	})
	srv := httpx.NewServer(addr, mux, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	waitForServer(t, "http://"+addr+"/ping")

	resp, err := httpGet(t, "http://"+addr+"/ping")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"pong"`) {
		t.Fatalf("body = %q, want it to contain \"pong\"", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// TestRunListenError reports an error when the address cannot be bound.
func TestRunListenError(t *testing.T) {
	srv := httpx.NewServer("127.0.0.1:1", http.NewServeMux(), zerolog.Nop())
	if err := srv.Run(context.Background()); err == nil {
		t.Fatal("Run on a privileged port should return an error")
	}
}

// TestChainOrder applies middleware so the first wrapper is the outermost.
func TestChainOrder(t *testing.T) {
	var order []string
	mw := func(tag string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, tag)
				next.ServeHTTP(w, r)
			})
		}
	}
	final := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
	})
	serve(httpx.Chain(final, mw("a"), mw("b")), newReq(http.MethodGet, "/", nil))

	want := []string{"a", "b", "handler"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q", i, order[i], want[i])
		}
	}
}

// TestChainEmpty returns the handler unchanged when no middleware is given.
func TestChainEmpty(t *testing.T) {
	hit := false
	final := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hit = true })
	serve(httpx.Chain(final), newReq(http.MethodGet, "/", nil))
	if !hit {
		t.Fatal("chained handler with no middleware was not invoked")
	}
}

// TestRequestIDGenerated injects a new id and echoes it back on the response.
func TestRequestIDGenerated(t *testing.T) {
	var fromCtx string
	final := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		fromCtx = httpx.RequestIDFromContext(r.Context())
	})
	rec := serve(httpx.RequestID(final), newReq(http.MethodGet, "/", nil))

	echoed := rec.Header().Get("X-Request-ID")
	if echoed == "" {
		t.Fatal("X-Request-ID header was not set")
	}
	if fromCtx != echoed {
		t.Fatalf("RequestIDFromContext = %q, want %q (the echoed header)", fromCtx, echoed)
	}
}

// TestRequestIDPreserved keeps an inbound id instead of minting a new one.
func TestRequestIDPreserved(t *testing.T) {
	const want = "client-supplied-id"
	var fromCtx string
	final := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		fromCtx = httpx.RequestIDFromContext(r.Context())
	})
	req := newReq(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", want)
	rec := serve(httpx.RequestID(final), req)

	if got := rec.Header().Get("X-Request-ID"); got != want {
		t.Fatalf("echoed id = %q, want %q", got, want)
	}
	if fromCtx != want {
		t.Fatalf("RequestIDFromContext = %q, want %q", fromCtx, want)
	}
}

// TestRequestIDFromContextMissing returns an empty string when no id is set.
func TestRequestIDFromContextMissing(t *testing.T) {
	if got := httpx.RequestIDFromContext(context.Background()); got != "" {
		t.Fatalf("RequestIDFromContext on bare context = %q, want empty", got)
	}
}

// TestRecover turns a panicking handler into a 500 instead of crashing.
func TestRecover(t *testing.T) {
	final := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	rec := serve(httpx.Recover(zerolog.Nop())(final), newReq(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Fatalf("body = %q, want it to contain \"internal error\"", rec.Body.String())
	}
}

// TestRecoverNoPanic passes the response through untouched when nothing panics.
func TestRecoverNoPanic(t *testing.T) {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	rec := serve(httpx.Recover(zerolog.Nop())(final), newReq(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
}

// TestLogRequests records the handler status and binds a logger onto context.
func TestLogRequests(t *testing.T) {
	var buf strings.Builder
	log := zerolog.New(&buf)
	var ctxLogger *zerolog.Logger
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxLogger = logger.From(r.Context())
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("body"))
	})
	rec := serve(httpx.LogRequests(log)(final), newReq(http.MethodGet, "/log", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, `"status":202`) {
		t.Fatalf("log line = %q, want it to record status 202", out)
	}
	if !strings.Contains(out, `"path":"/log"`) {
		t.Fatalf("log line = %q, want it to record the path", out)
	}
	if ctxLogger == nil {
		t.Fatal("handler did not receive a context-bound logger")
	}
}

// TestStatusWriterFlush forwards Flush to the underlying flusher.
func TestStatusWriterFlush(t *testing.T) {
	fr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("wrapped writer does not implement http.Flusher")
			return
		}
		f.Flush()
	})
	httpx.LogRequests(zerolog.Nop())(final).ServeHTTP(fr, newReq(http.MethodGet, "/", nil))
	if !fr.flushed {
		t.Fatal("Flush was not forwarded to the underlying writer")
	}
}

// TestStatusWriterFlushUnsupported is a no-op when the writer is not a flusher.
func TestStatusWriterFlushUnsupported(t *testing.T) {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("expected the middleware wrapper to expose Flush")
			return
		}
		f.Flush() // the underlying writer is not an http.Flusher; this must not panic.
	})
	w := &plainWriter{header: http.Header{}}
	httpx.LogRequests(zerolog.Nop())(final).ServeHTTP(w, newReq(http.MethodGet, "/", nil))
}

// plainWriter is a minimal ResponseWriter implementing neither Flusher nor Hijacker.
type plainWriter struct {
	header http.Header
}

func (n *plainWriter) Header() http.Header         { return n.header }
func (n *plainWriter) Write(b []byte) (int, error) { return len(b), nil }
func (n *plainWriter) WriteHeader(_ int)           {}

// TestStatusWriterWriteDefaultsToOK records 200 when the handler only writes.
func TestStatusWriterWriteDefaultsToOK(t *testing.T) {
	var buf strings.Builder
	log := zerolog.New(&buf)
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	rec := serve(httpx.LogRequests(log)(final), newReq(http.MethodGet, "/", nil))
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q, want \"hello\"", rec.Body.String())
	}
	if !strings.Contains(buf.String(), `"status":200`) {
		t.Fatalf("log line = %q, want default status 200", buf.String())
	}
}

// TestStatusWriterWriteHeaderOnce keeps the first status and ignores later ones.
func TestStatusWriterWriteHeaderOnce(t *testing.T) {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.WriteHeader(http.StatusBadGateway)
	})
	rec := serve(httpx.LogRequests(zerolog.Nop())(final), newReq(http.MethodGet, "/", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (first WriteHeader wins)", rec.Code)
	}
}

// TestStatusWriterWriteError propagates a write failure from the wrapped writer.
func TestStatusWriterWriteError(t *testing.T) {
	var writeErr error
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, writeErr = w.Write([]byte("data"))
	})
	fw := &failingWriter{header: http.Header{}}
	httpx.LogRequests(zerolog.Nop())(final).ServeHTTP(fw, newReq(http.MethodGet, "/", nil))
	if writeErr == nil {
		t.Fatal("expected the wrapper to surface the underlying write error")
	}
	if !strings.Contains(writeErr.Error(), "write response") {
		t.Fatalf("write error = %v, want it wrapped with \"write response\"", writeErr)
	}
}

// failingWriter is a ResponseWriter whose Write always fails.
type failingWriter struct {
	header http.Header
}

func (f *failingWriter) Header() http.Header         { return f.header }
func (f *failingWriter) Write(_ []byte) (int, error) { return 0, errors.New("disk full") }
func (f *failingWriter) WriteHeader(_ int)           {}

// TestStatusWriterHijack exposes the underlying connection for upgrades.
func TestStatusWriterHijack(t *testing.T) {
	hijacked := make(chan error, 1)
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			hijacked <- errors.New("wrapped writer is not an http.Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			hijacked <- err
			return
		}
		_ = conn.Close()
		hijacked <- nil
	})
	ts := httptest.NewServer(httpx.LogRequests(zerolog.Nop())(final))
	defer ts.Close()

	var d net.Dialer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case err := <-hijacked:
		if err != nil {
			t.Fatalf("Hijack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not hijack the connection")
	}
}

// TestStatusWriterHijackUnsupported returns ErrNotSupported for a plain writer.
func TestStatusWriterHijackUnsupported(t *testing.T) {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("expected the middleware wrapper to expose Hijack")
			return
		}
		_, _, err := hj.Hijack()
		if !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("Hijack error = %v, want http.ErrNotSupported", err)
		}
	})
	// httptest.ResponseRecorder is not an http.Hijacker.
	httpx.LogRequests(zerolog.Nop())(final).ServeHTTP(httptest.NewRecorder(), newReq(http.MethodGet, "/", nil))
}

// TestJSON writes the status code, content type and an encoded body.
func TestJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.JSON(rec, http.StatusCreated, map[string]int{"n": 5})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"n":5`) {
		t.Fatalf("body = %q, want it to contain \"n\":5", rec.Body.String())
	}
}

// TestJSONNilBody writes the status and no body when v is nil.
func TestJSONNilBody(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.JSON(rec, http.StatusNoContent, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

// TestError writes the error envelope with the given status code.
func TestError(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Error(rec, http.StatusBadRequest, "nope")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"nope"`) {
		t.Fatalf("body = %q, want the error envelope", rec.Body.String())
	}
}

// TestDecodeValid decodes a well-formed JSON body into the target.
func TestDecodeValid(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := newReq(http.MethodPost, "/", strings.NewReader(`{"name":"owl"}`))
	var p payload
	if err := httpx.Decode(req, &p); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.Name != "owl" {
		t.Fatalf("decoded name = %q, want owl", p.Name)
	}
}

// TestDecodeInvalid rejects a malformed JSON body.
func TestDecodeInvalid(t *testing.T) {
	req := newReq(http.MethodPost, "/", strings.NewReader(`{not json`))
	var p map[string]any
	if err := httpx.Decode(req, &p); err == nil {
		t.Fatal("Decode of malformed JSON should error")
	}
}

// TestDecodeUnknownField rejects bodies with fields absent from the target.
func TestDecodeUnknownField(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := newReq(http.MethodPost, "/", strings.NewReader(`{"name":"owl","extra":1}`))
	var p payload
	if err := httpx.Decode(req, &p); err == nil {
		t.Fatal("Decode should reject unknown fields")
	}
}

// TestDecodeOversized rejects a body larger than the 1 MiB limit.
func TestDecodeOversized(t *testing.T) {
	big := `{"name":"` + strings.Repeat("a", (1<<20)+16) + `"}`
	req := newReq(http.MethodPost, "/", strings.NewReader(big))
	var p map[string]any
	if err := httpx.Decode(req, &p); err == nil {
		t.Fatal("Decode of an oversized body should error")
	}
}

// freeAddr reserves and releases an ephemeral loopback address for a server.
func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	if cerr := lis.Close(); cerr != nil {
		t.Fatalf("close probe listener: %v", cerr)
	}
	return addr
}

// httpGet issues a GET with a bounded context, used by the server-level test.
func httpGet(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	return resp, nil
}

// waitForServer polls url until it answers or the deadline elapses.
func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become ready in time")
}
