// Package tokenizer adapts a llama.cpp server's /tokenize endpoint to the
// application's Tokenizer port, giving chunk-splitter EXACT token counts from the
// embedding model's real tokenizer (F2LLM-v2) so chunks can be sized to a token
// budget instead of a rune budget.
//
// It uses only the standard library (net/http + encoding/json) on purpose: it
// must not pull in or modify the vendored platform/aiclients HTTP plumbing, and a
// token count is a tiny, self-contained round-trip.
package tokenizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultTimeout bounds a single /tokenize call. Tokenizing one chunk-sized
// piece is cheap, so a short timeout keeps a stalled model from blocking the
// per-document split loop; on any failure the caller falls back to rune sizing.
const defaultTimeout = 30 * time.Second

// tokenizePath is the llama.cpp endpoint appended to a base URL.
const tokenizePath = "/tokenize"

// LlamaCPP counts tokens via llama.cpp's POST {base}/tokenize, which returns the
// piece's token ids under the model's real tokenizer. The token count is the
// length of that ids array.
//
// Wire contract (llama.cpp):
//
//	POST {base}/tokenize
//	Content-Type: application/json
//	{"content":"<text>"}                       → request
//	{"tokens":[128000, 9906, 1917, ...]}        → response (count = len(tokens))
//
// "add_special" is left unset (defaults to false on the server) so the count
// reflects the raw piece, not query/passage special tokens — chunk SIZE only.
type LlamaCPP struct {
	url   string // fully-resolved {base}/tokenize endpoint
	httpc *http.Client
}

// New builds a tokenizer client from a base URL (e.g.
// "http://host.docker.internal:8085") or an already-/tokenize URL; the /tokenize
// path is appended when absent so either form works. A nil http.Client gets a
// default with defaultTimeout. New returns nil when base is empty, so callers can
// treat "no TOKENIZER_URL configured" as "no tokenizer" (rune-budget path).
func New(base string, c *http.Client) *LlamaCPP {
	base = strings.TrimSpace(base)
	if base == "" {
		return nil
	}
	url := strings.TrimRight(base, "/")
	if !strings.HasSuffix(url, tokenizePath) {
		url += tokenizePath
	}
	if c == nil {
		c = &http.Client{Timeout: defaultTimeout}
	}
	return &LlamaCPP{url: url, httpc: c}
}

// tokenizeRequest is the llama.cpp /tokenize request body.
type tokenizeRequest struct {
	Content string `json:"content"`
}

// tokenizeResponse is the relevant part of the /tokenize response; the token
// count is len(Tokens).
type tokenizeResponse struct {
	Tokens []int `json:"tokens"`
}

// CountTokens returns the number of F2LLM-v2 tokens in text by asking the
// llama.cpp server to tokenize it and counting the returned ids. An empty string
// is zero tokens without a round-trip. Any transport, status or decode failure
// is returned as an error so the caller can fall back to rune sizing for that
// piece (chunk-splitter never fails ingestion on a tokenizer hiccup).
func (t *LlamaCPP) CountTokens(ctx context.Context, text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	raw, err := json.Marshal(tokenizeRequest{Content: text})
	if err != nil {
		return 0, fmt.Errorf("marshal tokenize request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(raw))
	if err != nil {
		return 0, fmt.Errorf("new tokenize request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("post %s: %w", t.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("post %s: status %d: %s", t.url, resp.StatusCode, string(body))
	}

	var out tokenizeResponse
	if derr := json.NewDecoder(resp.Body).Decode(&out); derr != nil {
		return 0, fmt.Errorf("decode tokenize response: %w", derr)
	}
	return len(out.Tokens), nil
}
