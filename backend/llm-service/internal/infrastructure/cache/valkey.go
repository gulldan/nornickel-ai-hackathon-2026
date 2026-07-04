// Package cache implements domain.Cache over Valkey (Redis protocol). It is the
// L2 answer cache from the architecture: a finished domain.Result is stored as
// JSON under a per-owner, per-query key with a configurable TTL, so repeated
// questions skip the retrieval/rerank/generate pipeline entirely.
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/valkey"
)

// AnswerCache stores RAG answers in Valkey with a fixed TTL.
type AnswerCache struct {
	client *valkey.Client
	ttl    time.Duration
}

// NewAnswerCache wraps a valkey.Client. ttl is the answer expiry (ANSWER_CACHE_TTL).
func NewAnswerCache(client *valkey.Client, ttl time.Duration) *AnswerCache {
	return &AnswerCache{client: client, ttl: ttl}
}

// Get loads a cached result, returning a zero value and false on a miss.
func (c *AnswerCache) Get(ctx context.Context, key string) (domain.Result, bool, error) {
	var res domain.Result
	hit, err := c.client.GetJSON(ctx, key, &res)
	if err != nil {
		return domain.Result{}, false, fmt.Errorf("cache get: %w", err)
	}
	return res, hit, nil
}

// Set stores res under key with the configured TTL.
func (c *AnswerCache) Set(ctx context.Context, key string, res domain.Result) error {
	if err := c.client.SetJSON(ctx, key, res, c.ttl); err != nil {
		return fmt.Errorf("cache set: %w", err)
	}
	return nil
}

// Epoch returns the corpus version counter for scope, or 0 when unset or
// unavailable (best-effort: a Valkey hiccup forgoes invalidation, never fails
// the query).
func (c *AnswerCache) Epoch(ctx context.Context, scope string) int64 {
	n, err := c.client.GetInt(ctx, "rag:corpus_epoch:"+scope)
	if err != nil {
		return 0
	}
	return n
}
