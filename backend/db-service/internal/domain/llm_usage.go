package domain

import (
	"context"
	"time"
)

// LLMUsageDaily is one (day, model, operation) row of the usage ledger.
// CostNanoUSD is nano-USD (exact integer; 0 for :free models).
type LLMUsageDaily struct {
	Day              time.Time
	Model            string
	Operation        string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	CostNanoUSD      int64
}

// LLMUsageRepository is the durable per-day LLM/OpenRouter usage ledger. SetDaily
// mirrors a running daily total (upsert with SET semantics); ListDaily returns an
// inclusive [from, to] date range for the Metrics dashboard.
type LLMUsageRepository interface {
	SetDaily(ctx context.Context, row *LLMUsageDaily) error
	ListDaily(ctx context.Context, from, to time.Time) ([]*LLMUsageDaily, error)
}
