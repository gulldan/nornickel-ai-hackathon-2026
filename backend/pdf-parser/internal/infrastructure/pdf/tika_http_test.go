package pdf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tikaServer starts an httptest server that asserts the request shape pdf-parser
// sends to Tika and replies with handler-chosen bodies keyed by the Accept type.
// It returns a base URL suitable for NewTikaExtractor; the server is torn down
// via t.Cleanup.
func tikaServer(t *testing.T, byAccept map[string]string, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/tika") {
			t.Errorf("path = %s, want it to end with /tika", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/pdf" {
			t.Errorf("Content-Type = %q, want application/pdf", ct)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("tika failed"))
			return
		}
		body, ok := byAccept[r.Header.Get("Accept")]
		if !ok {
			t.Errorf("unexpected Accept header %q", r.Header.Get("Accept"))
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// NewTikaExtractor normalises the base URL into the /tika endpoint, trimming a
// trailing slash so it is not doubled.
func TestNewTikaExtractorEndpoint(t *testing.T) {
	if got := NewTikaExtractor("http://tika:9998/").endpoint; got != "http://tika:9998/tika" {
		t.Errorf("endpoint = %q, want http://tika:9998/tika", got)
	}
}

// ExtractWithLayout parses Tika's XHTML page divs into text plus per-page rune
// offsets in a single round-trip.
func TestTikaExtractWithLayoutPages(t *testing.T) {
	xhtml := `<html><body><div class="page"><p>Page one.</p></div>` +
		`<div class="page"><p>Page two.</p></div></body></html>`
	base := tikaServer(t, map[string]string{"text/html": xhtml}, http.StatusOK)

	doc, err := NewTikaExtractor(base).ExtractWithLayout(context.Background(), []byte("%PDF"))
	if err != nil {
		t.Fatalf("ExtractWithLayout returned error, want nil: %v", err)
	}
	if len(doc.PageOffsets) != 2 {
		t.Fatalf("page count = %d, want 2 (%v)", len(doc.PageOffsets), doc.PageOffsets)
	}
	if !strings.Contains(doc.Text, "Page one.") || !strings.Contains(doc.Text, "Page two.") {
		t.Errorf("text = %q, want both page bodies", doc.Text)
	}
}

// Extract returns just the text layer, delegating to ExtractWithLayout so there
// is a single Tika call.
func TestTikaExtractPlainText(t *testing.T) {
	xhtml := `<html><body><div class="page"><p>Only page.</p></div></body></html>`
	base := tikaServer(t, map[string]string{"text/html": xhtml}, http.StatusOK)

	text, err := NewTikaExtractor(base).Extract(context.Background(), []byte("%PDF"))
	if err != nil {
		t.Fatalf("Extract returned error, want nil: %v", err)
	}
	if !strings.Contains(text, "Only page.") {
		t.Errorf("text = %q, want it to contain the page body", text)
	}
}

// Extract propagates the error when the underlying ExtractWithLayout call fails
// (here, a non-200 Tika response).
func TestTikaExtractError(t *testing.T) {
	base := tikaServer(t, nil, http.StatusServiceUnavailable)

	_, err := NewTikaExtractor(base).Extract(context.Background(), []byte("%PDF"))
	if err == nil {
		t.Fatal("Extract returned nil error for a failing Tika call, want an error")
	}
}

// When the XHTML has no page divs, ExtractWithLayout falls back to Tika's
// plain-text endpoint and returns the text with nil PageOffsets.
func TestTikaExtractWithLayoutNoPagesFallsBackToPlain(t *testing.T) {
	bodies := map[string]string{
		"text/html":  `<html><body><p>No page divs here.</p></body></html>`,
		"text/plain": "Plain text body from Tika.",
	}
	base := tikaServer(t, bodies, http.StatusOK)

	doc, err := NewTikaExtractor(base).ExtractWithLayout(context.Background(), []byte("%PDF"))
	if err != nil {
		t.Fatalf("ExtractWithLayout returned error, want nil: %v", err)
	}
	if doc.PageOffsets != nil {
		t.Errorf("PageOffsets = %v, want nil on the plain-text fallback", doc.PageOffsets)
	}
	if doc.Text != "Plain text body from Tika." {
		t.Errorf("text = %q, want the plain-text endpoint body", doc.Text)
	}
}

// A non-200 Tika response surfaces as an error carrying the status code.
func TestTikaExtractWithLayoutServerError(t *testing.T) {
	base := tikaServer(t, nil, http.StatusInternalServerError)

	_, err := NewTikaExtractor(base).ExtractWithLayout(context.Background(), []byte("%PDF"))
	if err == nil {
		t.Fatal("ExtractWithLayout returned nil error for a 500 response, want an error")
	}
	if !strings.Contains(err.Error(), "tika status 500") {
		t.Errorf("error = %v, want it to mention the 500 status", err)
	}
}

// A transport failure (the server is closed before the call) surfaces as a
// call error rather than a panic.
func TestTikaExtractWithLayoutTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // close immediately so the connection is refused.

	_, err := NewTikaExtractor(base).ExtractWithLayout(context.Background(), []byte("%PDF"))
	if err == nil {
		t.Fatal("ExtractWithLayout returned nil error for a dead server, want a transport error")
	}
	if !strings.Contains(err.Error(), "call tika") {
		t.Errorf("error = %v, want it to mention the failed Tika call", err)
	}
}

// A request that cannot be built (a malformed endpoint URL) is reported before
// any network call.
func TestTikaExtractRequestBuildError(t *testing.T) {
	// Control characters in the URL make http.NewRequestWithContext fail.
	bad := &TikaExtractor{endpoint: "http://\x7f/tika", client: http.DefaultClient}
	_, err := bad.ExtractWithLayout(context.Background(), []byte("%PDF"))
	if err == nil {
		t.Fatal("ExtractWithLayout returned nil error for an invalid endpoint, want a build error")
	}
	if !strings.Contains(err.Error(), "build tika request") {
		t.Errorf("error = %v, want it to mention building the request", err)
	}
}

// stubExtractor returns canned text or an error for the plain-text port.
type stubExtractor struct {
	text string
	err  error
}

func (s stubExtractor) Extract(_ context.Context, _ []byte) (string, error) {
	return s.text, s.err
}

// stubLayoutExtractor additionally provides layout output for the layout port.
type stubLayoutExtractor struct {
	doc ExtractedDocument
	err error
}

func (s stubLayoutExtractor) Extract(_ context.Context, _ []byte) (string, error) {
	return s.doc.Text, s.err
}

func (s stubLayoutExtractor) ExtractWithLayout(_ context.Context, _ []byte) (ExtractedDocument, error) {
	return s.doc, s.err
}

// FallbackExtractor.Extract returns the primary's text when the primary succeeds
// and never consults the secondary.
func TestFallbackExtractPrimaryWins(t *testing.T) {
	f := NewFallbackExtractor(stubExtractor{text: "primary"}, stubExtractor{text: "secondary"}, nil)
	got, err := f.Extract(context.Background(), []byte("x"))
	if err != nil {
		t.Fatalf("Extract returned error, want nil: %v", err)
	}
	if got != "primary" {
		t.Errorf("text = %q, want primary", got)
	}
}

// When the primary errors, Extract falls back to the secondary and invokes the
// onErr hook with the primary's error.
func TestFallbackExtractSecondaryUsedOnPrimaryError(t *testing.T) {
	var logged error
	f := NewFallbackExtractor(
		stubExtractor{err: errors.New("tika down")},
		stubExtractor{text: "secondary"},
		func(err error) { logged = err },
	)
	got, err := f.Extract(context.Background(), []byte("x"))
	if err != nil {
		t.Fatalf("Extract returned error, want nil: %v", err)
	}
	if got != "secondary" {
		t.Errorf("text = %q, want secondary", got)
	}
	if logged == nil {
		t.Error("onErr was not called with the primary error")
	}
}

// ExtractWithLayout forwards to a layout-capable primary and returns its doc when
// the primary succeeds.
func TestFallbackExtractWithLayoutPrimaryLayout(t *testing.T) {
	doc := ExtractedDocument{Text: "p", PageOffsets: []int{0}}
	f := NewFallbackExtractor(stubLayoutExtractor{doc: doc}, stubExtractor{text: "s"}, nil)
	got, err := f.ExtractWithLayout(context.Background(), []byte("x"))
	if err != nil {
		t.Fatalf("ExtractWithLayout returned error, want nil: %v", err)
	}
	if len(got.PageOffsets) != 1 || got.Text != "p" {
		t.Errorf("doc = %+v, want the primary's layout doc", got)
	}
}

// When a layout-capable primary fails, ExtractWithLayout falls back to the
// secondary's plain text with nil page offsets.
func TestFallbackExtractWithLayoutPrimaryFailsLayout(t *testing.T) {
	f := NewFallbackExtractor(
		stubLayoutExtractor{err: errors.New("tika down")},
		stubExtractor{text: "secondary text"},
		nil,
	)
	got, err := f.ExtractWithLayout(context.Background(), []byte("x"))
	if err != nil {
		t.Fatalf("ExtractWithLayout returned error, want nil: %v", err)
	}
	if got.PageOffsets != nil {
		t.Errorf("PageOffsets = %v, want nil from the secondary fallback", got.PageOffsets)
	}
	if got.Text != "secondary text" {
		t.Errorf("text = %q, want secondary text", got.Text)
	}
}

// When the layout primary fails AND the secondary also fails, the error is
// propagated.
func TestFallbackExtractWithLayoutBothFail(t *testing.T) {
	f := NewFallbackExtractor(
		stubLayoutExtractor{err: errors.New("tika down")},
		stubExtractor{err: errors.New("ledongthuc down")},
		nil,
	)
	if _, err := f.ExtractWithLayout(context.Background(), []byte("x")); err == nil {
		t.Fatal("ExtractWithLayout returned nil error when both extractors fail, want an error")
	}
}

// When the primary cannot provide layout at all, ExtractWithLayout returns the
// primary's plain text with nil page offsets.
func TestFallbackExtractWithLayoutPrimaryNoLayout(t *testing.T) {
	f := NewFallbackExtractor(stubExtractor{text: "plain primary"}, stubExtractor{text: "s"}, nil)
	got, err := f.ExtractWithLayout(context.Background(), []byte("x"))
	if err != nil {
		t.Fatalf("ExtractWithLayout returned error, want nil: %v", err)
	}
	if got.PageOffsets != nil {
		t.Errorf("PageOffsets = %v, want nil for a non-layout primary", got.PageOffsets)
	}
	if got.Text != "plain primary" {
		t.Errorf("text = %q, want plain primary", got.Text)
	}
}

// When a non-layout primary fails, ExtractWithLayout falls back to the secondary
// and, if the secondary also fails, propagates the error.
func TestFallbackExtractWithLayoutNoLayoutPrimaryFails(t *testing.T) {
	f := NewFallbackExtractor(
		stubExtractor{err: errors.New("primary down")},
		stubExtractor{err: errors.New("secondary down")},
		func(error) {},
	)
	if _, err := f.ExtractWithLayout(context.Background(), []byte("x")); err == nil {
		t.Fatal("ExtractWithLayout returned nil error when both non-layout extractors fail, want an error")
	}
}
