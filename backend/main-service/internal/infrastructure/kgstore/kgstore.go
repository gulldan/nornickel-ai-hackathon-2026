// Package kgstore adapts a JSON key-value store (Valkey) to the
// application.GraphStore port, so the owner-scoped knowledge graph (typed triples
// with provenance) survives restarts. Like scoringstore it lives outside the
// application package to keep that package free of any concrete client
// dependency: it depends only on a structural JSONStore interface, satisfied by
// platform/valkey.Client.
//
// This backing is pragmatic — the whole owner graph is one JSON value, appended
// and de-duplicated in process, capped so a runaway owner cannot grow it without
// bound. A Postgres / graph-DB backing (indexed edges, partial reads, traversal
// in the store) is the production follow-up.
package kgstore

import (
	"context"
	"time"

	"github.com/example/main-service/internal/application"
)

// graphKeyPrefix namespaces per-owner knowledge graphs in Valkey.
const graphKeyPrefix = "rag:kg:"

// graphTTL keeps a stored graph effectively permanent; it is rewritten on every
// generation, so a long TTL only reaps abandoned owners.
const graphTTL = 365 * 24 * time.Hour

// maxEdges caps the stored edge count per owner so the single JSON value stays a
// sane size (a board-sized portfolio is far below this). Oldest edges are dropped
// first when the cap is exceeded.
const maxEdges = 5000

// JSONStore is the minimal store surface the adapter needs; it is satisfied by
// platform/valkey.Client.
type JSONStore interface {
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
	GetJSON(ctx context.Context, key string, dest any) (bool, error)
}

// Store persists per-owner knowledge-graph edges as a JSON edge list.
type Store struct {
	kv JSONStore
}

// New builds a Store over the given JSON key-value backend.
func New(kv JSONStore) *Store {
	return &Store{kv: kv}
}

// Edges returns the owner's stored edges (empty, never nil, on a miss).
func (s *Store) Edges(ctx context.Context, ownerID string) ([]application.KGEdge, error) {
	var edges []application.KGEdge
	if _, err := s.kv.GetJSON(ctx, graphKeyPrefix+ownerID, &edges); err != nil {
		return nil, err
	}
	if edges == nil {
		edges = []application.KGEdge{}
	}
	return edges, nil
}

// AddEdges appends new edges to the owner's graph, de-duplicating against what is
// already stored and capping the total. A no-op when there is nothing to add.
func (s *Store) AddEdges(ctx context.Context, ownerID string, edges []application.KGEdge) error {
	if len(edges) == 0 {
		return nil
	}
	existing, err := s.Edges(ctx, ownerID)
	if err != nil {
		return err
	}
	seen := make(map[application.KGEdge]struct{}, len(existing)+len(edges))
	merged := make([]application.KGEdge, 0, len(existing)+len(edges))
	for _, e := range existing {
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		merged = append(merged, e)
	}
	for _, e := range edges {
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		merged = append(merged, e)
	}
	if len(merged) > maxEdges {
		merged = merged[len(merged)-maxEdges:] // keep the most recent edges
	}
	return s.kv.SetJSON(ctx, graphKeyPrefix+ownerID, merged, graphTTL)
}
