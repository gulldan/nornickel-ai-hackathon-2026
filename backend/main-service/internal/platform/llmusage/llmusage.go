// Package llmusage records and reads per-day LLM/OpenRouter usage in Valkey hot
// hashes. Writers (llm-service, chunk-splitter, the Python workers) increment a
// day-hash per (model, operation); main-service mirrors those into the durable
// Postgres ledger and serves them to the Metrics dashboard. The Valkey key/field
// contract here is shared byte-for-byte with the Python workers.
package llmusage

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	dayPrefix    = "rag:llm:usage:"  // + YYYYMMDD (UTC) → a hash
	reqMinPrefix = "rag:llm:reqmin:" // + unix-minute → a counter
	fieldSep     = "\x1f"            // unit separator: model \x1f operation \x1f metric
	// DayTTL keeps ~35 days hot so the 30-day dashboard window is always in Valkey.
	DayTTL    = 35 * 24 * time.Hour
	reqMinTTL = 120 * time.Second
)

// KV is the Valkey subset llmusage needs (satisfied by *valkey.Client).
type KV interface {
	HIncrBy(ctx context.Context, key, field string, n int64) error
	Expire(ctx context.Context, key string, ttl time.Duration) error
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	IncrTTL(ctx context.Context, key string, ttl time.Duration) (int64, error)
	GetInt(ctx context.Context, key string) (int64, error)
}

// DayKey is the hash key for a UTC day.
func DayKey(day time.Time) string { return dayPrefix + day.UTC().Format("20060102") }

func field(model, operation, metric string) string {
	return model + fieldSep + operation + fieldSep + metric
}

// Record accumulates one completion's usage into today's hot hash plus the
// per-minute request counter. Best-effort: callers should ignore the error — a
// metrics write must never fail or slow the LLM path.
func Record(ctx context.Context, kv KV, model, operation string, promptTokens, completionTokens int, costUSD float64) error {
	if kv == nil || model == "" || operation == "" {
		return nil
	}
	key := DayKey(time.Now())
	nano := int64(math.Round(costUSD * 1e9))
	if err := kv.HIncrBy(ctx, key, field(model, operation, "req"), 1); err != nil {
		return err
	}
	_ = kv.HIncrBy(ctx, key, field(model, operation, "pt"), int64(promptTokens))
	_ = kv.HIncrBy(ctx, key, field(model, operation, "ct"), int64(completionTokens))
	_ = kv.HIncrBy(ctx, key, field(model, operation, "cost"), nano)
	_ = kv.Expire(ctx, key, DayTTL)
	_, _ = kv.IncrTTL(ctx, reqMinPrefix+strconv.FormatInt(time.Now().Unix()/60, 10), reqMinTTL)
	return nil
}

// Cell is one (model, operation) aggregate for a day. CostNanoUSD is nano-USD.
type Cell struct {
	Model, Operation                                      string
	Requests, PromptTokens, CompletionTokens, CostNanoUSD int64
}

// ReadDay returns the per-(model,operation) cells recorded for a UTC day, parsed
// from the hot hash. main-service uses this to mirror the day into Postgres.
func ReadDay(ctx context.Context, kv KV, day time.Time) ([]Cell, error) {
	m, err := kv.HGetAll(ctx, DayKey(day))
	if err != nil {
		return nil, err
	}
	type acc struct{ req, pt, ct, cost int64 }
	byKey := map[[2]string]*acc{}
	for f, v := range m {
		parts := strings.SplitN(f, fieldSep, 3)
		if len(parts) != 3 {
			continue
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		k := [2]string{parts[0], parts[1]}
		a := byKey[k]
		if a == nil {
			a = &acc{}
			byKey[k] = a
		}
		switch parts[2] {
		case "req":
			a.req = n
		case "pt":
			a.pt = n
		case "ct":
			a.ct = n
		case "cost":
			a.cost = n
		}
	}
	cells := make([]Cell, 0, len(byKey))
	for k, a := range byKey {
		cells = append(cells, Cell{
			Model: k[0], Operation: k[1],
			Requests: a.req, PromptTokens: a.pt, CompletionTokens: a.ct, CostNanoUSD: a.cost,
		})
	}
	return cells, nil
}

// ReqPerMinute reads the request count in the current unix-minute window (for the
// free-tier rate gauge). Missing key → 0.
func ReqPerMinute(ctx context.Context, kv KV) int64 {
	n, err := kv.GetInt(ctx, reqMinPrefix+strconv.FormatInt(time.Now().Unix()/60, 10))
	if err != nil {
		return 0
	}
	return n
}
