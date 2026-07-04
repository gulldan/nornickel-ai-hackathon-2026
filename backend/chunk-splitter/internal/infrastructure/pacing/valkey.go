// Package pacing implements the application's Pacer port over Valkey. It reads
// the in-flight interactive-query gauge that llm-service maintains (see its
// load marker) and briefly holds ingestion back while users are asking
// questions. The scheme is work-conserving and starvation-free:
//
//   - no queries in flight  -> Wait returns immediately (one cheap GET);
//   - queries in flight     -> Wait polls until the gauge clears, but at most
//     maxYield per document, so ingestion trickles instead of stalling;
//   - Valkey down / errors  -> fail open: ingestion must never depend on the
//     load-signalling channel to make progress.
//
// The net effect under a burst of uploads + questions: queries keep their
// latency, the durable RabbitMQ queue holds the backlog, and the moment the
// gauge clears chunk-splitter returns to full speed.
package pacing

import (
	"context"
	"time"

	"github.com/example/chunk-splitter/internal/platform/logger"
	"github.com/example/chunk-splitter/internal/platform/valkey"
)

// activeQueriesKey mirrors llm-service's load-marker key (shared wiring).
const activeQueriesKey = "rag:active_queries"

// pollEvery is how often the gauge is re-checked while yielding.
const pollEvery = 200 * time.Millisecond

// Pacer yields ingestion capacity while interactive queries are in flight.
type Pacer struct {
	vk       *valkey.Client
	maxYield time.Duration
}

// New builds a Pacer. maxYield bounds the pause per document (the trickle
// interval); values <= 0 fall back to 2s.
func New(vk *valkey.Client, maxYield time.Duration) *Pacer {
	if maxYield <= 0 {
		maxYield = 2 * time.Second
	}
	return &Pacer{vk: vk, maxYield: maxYield}
}

// Wait blocks while interactive queries are in flight, up to maxYield.
func (p *Pacer) Wait(ctx context.Context) {
	deadline := time.Now().Add(p.maxYield)
	for {
		n, err := p.vk.GetInt(ctx, activeQueriesKey)
		if err != nil {
			// Fail open: a broken signalling channel must not stop ingestion.
			logger.From(ctx).Warn().Err(err).Msg("pacer gauge read failed; proceeding")
			return
		}
		if n <= 0 {
			return
		}
		if time.Now().After(deadline) {
			// Trickle: proceed with this document even though queries are still
			// active, so a sustained query stream cannot starve ingestion.
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollEvery):
		}
	}
}
