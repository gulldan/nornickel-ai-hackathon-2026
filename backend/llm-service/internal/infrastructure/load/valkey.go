// Package load implements domain.LoadMarker over Valkey: a shared in-flight
// interactive-query counter under a short TTL. chunk-splitter's pacer reads the
// same key to yield ingestion capacity while users are asking questions. The
// TTL self-heals the gauge if a service dies between Started and Finished, and
// every operation is best-effort — load signalling must never fail a query.
package load

import (
	"context"
	"time"

	"github.com/example/llm-service/internal/platform/logger"
	"github.com/example/llm-service/internal/platform/valkey"
)

// Key is the Valkey key holding the number of in-flight interactive queries.
// It is shared wiring between llm-service (writer) and chunk-splitter (reader).
const Key = "rag:active_queries"

// counterTTL bounds how long a crashed holder can keep the gauge inflated.
const counterTTL = 30 * time.Second

// Marker writes the in-flight query gauge to Valkey.
type Marker struct {
	vk *valkey.Client
}

// New wraps a Valkey client.
func New(vk *valkey.Client) *Marker { return &Marker{vk: vk} }

// QueryStarted increments the gauge and refreshes its TTL.
func (m *Marker) QueryStarted(ctx context.Context) {
	if _, err := m.vk.IncrTTL(ctx, Key, counterTTL); err != nil {
		logger.From(ctx).Warn().Err(err).Msg("load marker incr failed")
	}
}

// QueryFinished decrements the gauge. A negative value (possible when the TTL
// expired mid-flight) is left as-is; readers clamp at zero and the TTL clears
// the key shortly after.
func (m *Marker) QueryFinished(ctx context.Context) {
	if _, err := m.vk.Decr(ctx, Key); err != nil {
		logger.From(ctx).Warn().Err(err).Msg("load marker decr failed")
	}
}
