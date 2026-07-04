// Package aiadapters bridges the platform aiclients (embeddings, reranker,
// generator) to llm-service's domain ports. Each adapter is a thin shape
// translation; the heavy lifting — real HTTP calls or deterministic stubs — is
// owned by the vendored aiclients package and selected by whether the backend
// URL is configured.
package aiadapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/aiclients"
)

// genMaxTokens is the completion budget for structured (non-cite) generation.
// RAG_GEN_MAX_TOKENS overrides; the default leaves room for hidden reasoning
// tokens of DeepSeek-style models on top of the JSON payload itself.
func genMaxTokens() int {
	if v, err := strconv.Atoi(os.Getenv("RAG_GEN_MAX_TOKENS")); err == nil && v > 0 {
		return v
	}
	return 12288
}

// Embedder adapts aiclients.Embedder to domain.Embedder, embedding a single
// query at a time.
type Embedder struct {
	client aiclients.Embedder
}

// NewEmbedder wraps an aiclients.Embedder.
func NewEmbedder(client aiclients.Embedder) *Embedder {
	return &Embedder{client: client}
}

// Embed returns the dense vector for one piece of text. The underlying client
// works in batches, so we send a one-element batch and unwrap the result.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.client.Embed(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(vecs) == 0 {
		return nil, errors.New("embed: no vector returned")
	}
	return vecs[0], nil
}

// Reranker adapts aiclients.Reranker to domain.Ranker.
type Reranker struct {
	client aiclients.Reranker
}

// NewReranker wraps an aiclients.Reranker.
func NewReranker(client aiclients.Reranker) *Reranker {
	return &Reranker{client: client}
}

// Rank scores each chunk's text against the query and returns the scores aligned
// with the input order. The application layer applies the ordering.
func (r *Reranker) Rank(ctx context.Context, query string, chunks []domain.RetrievedChunk) ([]float64, error) {
	docs := make([]string, len(chunks))
	for i, c := range chunks {
		docs[i] = c.Text
	}
	scores, err := r.client.Rerank(ctx, query, docs)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	out := make([]float64, len(scores))
	for i, s := range scores {
		out[i] = float64(s)
	}
	return out, nil
}

// Reasoner adapts aiclients.Generator to domain.Reasoner: a raw system+user chat
// call for the agentic controller's reasoning and condensation steps. It
// deliberately bypasses the grounded-Q&A framing (no citation instruction, no
// "Context:" wrapper) so the controller fully controls the prompt. A distinct
// underlying client lets the reasoner run a different model (RAG_REASONER_MODEL)
// from the answer generator.
type Reasoner struct {
	client    aiclients.Generator
	maxTokens int
}

// NewReasoner wraps an aiclients.Generator for the agentic reasoning steps.
// maxTokens bounds each reasoning/condensation reply; non-positive falls back to
// a sane default.
func NewReasoner(client aiclients.Generator, maxTokens int) *Reasoner {
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return &Reasoner{client: client, maxTokens: maxTokens}
}

// Reason issues one system+user chat completion. An empty context request makes
// the underlying client send the user prompt verbatim (no grounded-Q&A framing),
// so the caller's system prompt fully steers the model. A low temperature keeps
// the control decisions (search vs. stop) stable.
func (r *Reasoner) Reason(ctx context.Context, system, user string) (string, error) {
	resp, err := r.client.Generate(ctx, aiclients.GenRequest{
		System:      system,
		Context:     "",
		Query:       user,
		MaxTokens:   r.maxTokens,
		Temperature: 0.1,
		Cite:        false,
	})
	if err != nil {
		return "", fmt.Errorf("reason: %w", err)
	}
	return resp.Text, nil
}

// UsageRecorder records one completion's token/cost usage, tagged by operation.
// Optional; a nil recorder disables usage metrics. Best-effort — implementations
// must never block or fail the generation path.
type UsageRecorder interface {
	Record(ctx context.Context, model, operation string, promptTokens, completionTokens int, costUSD float64)
}

// Generator adapts aiclients.Generator to domain.Answerer.
type Generator struct {
	client  aiclients.Generator
	timeout time.Duration
	usage   UsageRecorder
}

// NewGenerator wraps an aiclients.Generator.
func NewGenerator(client aiclients.Generator) *Generator {
	return &Generator{client: client}
}

// WithUsage attaches a usage recorder (fluent; returns the same Generator).
func (g *Generator) WithUsage(rec UsageRecorder) *Generator {
	g.usage = rec
	return g
}

// operationFromContext reads the "operation" gRPC metadata the caller set
// (main-service tags each call: generate/verify/enrich/…). Defaults to "answer"
// for the plain chat path or untagged callers.
func operationFromContext(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("operation"); len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return "answer"
}

// NewGeneratorWithTimeout wraps an aiclients.Generator and bounds the whole
// generation call. This protects the chat path from hosted-provider tail latency:
// the application layer can then return its extractive fallback with sources.
func NewGeneratorWithTimeout(client aiclients.Generator, timeout time.Duration) *Generator {
	return &Generator{client: client, timeout: timeout}
}

// Answer issues a grounded-generation call. An empty contextText is passed
// through unchanged; the generator handles the no-context case itself. cite
// turns on the inline-citation instruction (chat-Q&A path only).
func (g *Generator) Answer(ctx context.Context, contextText, query string, cite bool) (string, string, error) {
	if g.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.timeout)
		defer cancel()
	}
	maxTokens := 1024
	temperature := float32(0.0)
	if !cite {
		// Reasoning-модели (DeepSeek на Yandex) сжигают тысячи скрытых токенов
		// рассуждения ДО контента в общий max_tokens бюджет: 4096 давал
		// finish=length с пустым content на структурной генерации гипотез.
		maxTokens = genMaxTokens()
		temperature = 0.2
	}
	resp, err := g.client.Generate(ctx, aiclients.GenRequest{
		Context:     contextText,
		Query:       query,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Cite:        cite,
	})
	if err != nil {
		return "", "", fmt.Errorf("generate: %w", err)
	}
	if g.usage != nil {
		g.usage.Record(ctx, resp.Model, operationFromContext(ctx),
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.CostUSD)
	}
	return resp.Text, resp.Model, nil
}
