package application

import (
	"context"
	"time"
)

// LLM-driven hypothesis operations get an explicit context deadline. Without one
// a hung llm-service (or its upstream model) keeps the gRPC call — and the
// admission slot it holds in llm-service — alive until the HTTP write timeout,
// which fires for the client but does not by itself cancel the in-flight RPC.
// An explicit deadline propagates through gRPC so the slot is released promptly.
// Budgets are sized to the number of sequential LLM passes each operation makes
// (measured p95 ≈ 27s per generation pass).
const (
	// llmSinglePassTimeout covers one Answer plus its surrounding db round-trips:
	// Verify, AssessTRL, TagHypothesis, AnalyzeCompetitors, chat Ask.
	llmSinglePassTimeout = 90 * time.Second
	// llmGenerateTimeout covers verification-style multi-pass operations.
	// Hypothesis Generate uses owner-editable runtime settings instead.
	llmGenerateTimeout = 150 * time.Second
	// llmRefineTimeout covers Refine (verify → revise → re-verify, up to ~4 passes).
	llmRefineTimeout = 200 * time.Second
)

// withDeadline wraps ctx with budget d, unless ctx already carries an earlier
// deadline (a caller-imposed shorter budget always wins). The caller must invoke
// the returned cancel. Nested calls (e.g. Refine → Verify) compose correctly:
// the inner pass gets its own tighter sub-budget while the outer cap still holds.
func withDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= d {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
