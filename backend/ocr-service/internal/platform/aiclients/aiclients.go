// Package aiclients holds the clients for every neural-network backend: embeddings
// (Triton e5), generation (vLLM Qwen), reranking (Qwen3-Reranker), OCR
// (the OCR engine role) and VLM (Qwen-VL).
//
// The models are NOT part of this MVP. Each constructor takes a backend URL from
// an environment variable; when that URL is empty the client returns a
// deterministic STUB, so the whole pipeline runs locally with zero GPUs. Set the
// env var to a real engine and the same code path issues real HTTP calls.
package aiclients

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/example/ocr-service/internal/platform/jsonx"
)

const defaultTimeout = 120 * time.Second

// modelField is the JSON key naming the model in every backend request body.
const modelField = "model"

func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: defaultTimeout}
}

// reqOption decorates an outgoing request before it is sent — e.g. to attach an
// Authorization header for a hosted OpenAI-compatible gateway.
type reqOption func(*http.Request)

// postJSON sends in as JSON to url and decodes the response into out. Optional
// reqOptions decorate the request (auth headers, etc.).
func postJSON(ctx context.Context, c *http.Client, url string, in, out any, opts ...reqOption) error {
	raw, err := jsonx.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, opt := range opts {
		opt(req)
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("post %s: status %d: %s", url, resp.StatusCode, string(body))
	}
	if out != nil {
		if derr := jsonx.NewDecoder(resp.Body).Decode(out); derr != nil {
			return fmt.Errorf("decode response: %w", derr)
		}
	}
	return nil
}
