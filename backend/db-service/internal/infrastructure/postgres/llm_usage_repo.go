package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/example/db-service/internal/domain"
	"github.com/example/db-service/internal/infrastructure/postgres/sqlcgen"
)

// LLMUsageRepo persists the per-day LLM/OpenRouter usage ledger.
type LLMUsageRepo struct{ q *sqlcgen.Queries }

// NewLLMUsageRepo builds the repo over the shared pool.
func NewLLMUsageRepo(db *DB) *LLMUsageRepo { return &LLMUsageRepo{q: sqlcgen.New(db.Pool)} }

// SetDaily upserts a running daily total (SET semantics on conflict).
func (r *LLMUsageRepo) SetDaily(ctx context.Context, row *domain.LLMUsageDaily) error {
	err := r.q.SetLLMUsageDaily(ctx, sqlcgen.SetLLMUsageDailyParams{
		Day:              row.Day,
		Model:            row.Model,
		Operation:        row.Operation,
		Requests:         row.Requests,
		PromptTokens:     row.PromptTokens,
		CompletionTokens: row.CompletionTokens,
		CostNanoUsd:      row.CostNanoUSD,
	})
	if err != nil {
		return fmt.Errorf("set llm usage daily: %w", err)
	}
	return nil
}

// ListDaily returns the ledger for an inclusive [from, to] date range.
func (r *LLMUsageRepo) ListDaily(ctx context.Context, from, to time.Time) ([]*domain.LLMUsageDaily, error) {
	rows, err := r.q.ListLLMUsageDaily(ctx, sqlcgen.ListLLMUsageDailyParams{FromDay: from, ToDay: to})
	if err != nil {
		return nil, fmt.Errorf("list llm usage daily: %w", err)
	}
	out := make([]*domain.LLMUsageDaily, 0, len(rows))
	for i := range rows {
		out = append(out, &domain.LLMUsageDaily{
			Day:              rows[i].Day,
			Model:            rows[i].Model,
			Operation:        rows[i].Operation,
			Requests:         rows[i].Requests,
			PromptTokens:     rows[i].PromptTokens,
			CompletionTokens: rows[i].CompletionTokens,
			CostNanoUSD:      rows[i].CostNanoUsd,
		})
	}
	return out, nil
}
