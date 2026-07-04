package application

import (
	"context"
	"time"

	"google.golang.org/grpc/metadata"

	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	"github.com/example/main-service/internal/platform/llmusage"
)

// dayFmt is the wire/DB date format for the usage ledger.
const dayFmt = "2006-01-02"

// opCtx tags an outgoing llm-service call with an operation label, so llm-service
// attributes the completion's token/cost usage to that operation.
func opCtx(ctx context.Context, operation string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "operation", operation)
}

// LLMUsageDB is the db-service subset the usage ledger needs.
type LLMUsageDB interface {
	SetLLMUsageDaily(
		ctx context.Context, day, model, operation string,
		requests, promptTokens, completionTokens, costNanoUSD int64,
	) error
	ListLLMUsageDaily(ctx context.Context, fromDay, toDay string) ([]*dbv1.LLMUsageDailyRow, error)
}

// LLMUsageService mirrors the Valkey hot day-hashes (written by llm-service,
// chunk-splitter and the Python workers) into the durable Postgres ledger, and
// serves a date range plus the live quota counters for the Metrics dashboard.
type LLMUsageService struct {
	kv llmusage.KV
	db LLMUsageDB
}

// NewLLMUsageService wires the Valkey client and db client.
func NewLLMUsageService(kv llmusage.KV, db LLMUsageDB) *LLMUsageService {
	return &LLMUsageService{kv: kv, db: db}
}

// Flush mirrors today's and yesterday's Valkey day-hashes into Postgres (SET
// semantics, idempotent). Best-effort — errors are skipped, never surfaced.
func (s *LLMUsageService) Flush(ctx context.Context) {
	if s == nil || s.kv == nil || s.db == nil {
		return
	}
	now := time.Now().UTC()
	for _, day := range []time.Time{now, now.AddDate(0, 0, -1)} {
		cells, err := llmusage.ReadDay(ctx, s.kv, day)
		if err != nil {
			continue
		}
		ds := day.Format(dayFmt)
		for _, c := range cells {
			_ = s.db.SetLLMUsageDaily(ctx, ds, c.Model, c.Operation,
				c.Requests, c.PromptTokens, c.CompletionTokens, c.CostNanoUSD)
		}
	}
}

// List flushes the hot window, then returns the durable ledger for [from, to].
func (s *LLMUsageService) List(ctx context.Context, from, to time.Time) ([]*dbv1.LLMUsageDailyRow, error) {
	s.Flush(ctx)
	return s.db.ListLLMUsageDaily(ctx, from.Format(dayFmt), to.Format(dayFmt))
}

// TodayRequests sums today's request counts from the hot hash (for the quota
// gauge — more current than the flushed ledger).
func (s *LLMUsageService) TodayRequests(ctx context.Context) int64 {
	if s == nil || s.kv == nil {
		return 0
	}
	cells, err := llmusage.ReadDay(ctx, s.kv, time.Now().UTC())
	if err != nil {
		return 0
	}
	var n int64
	for _, c := range cells {
		n += c.Requests
	}
	return n
}

// ReqPerMinute returns the request count in the current minute (rate gauge).
func (s *LLMUsageService) ReqPerMinute(ctx context.Context) int64 {
	if s == nil || s.kv == nil {
		return 0
	}
	return llmusage.ReqPerMinute(ctx, s.kv)
}
