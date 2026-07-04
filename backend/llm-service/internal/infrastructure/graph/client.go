// Package graph adapts the graph-compute gRPC service to llm-service's
// domain.GraphExpander port, the optional "graph tool" the agentic controller
// folds into its candidate set. It calls Rank (Personalized PageRank over the
// kNN document graph), seeding the walk with the current evidence documents so
// the result is a multi-hop expansion of related documents. It is strictly
// optional and wired only when RAG_AGENTIC_GRAPH is on; the controller degrades
// gracefully on any error, so an unreachable graph-compute never fails a query.
package graph

import (
	"context"
	"fmt"
	"time"

	graphv1 "github.com/example/llm-service/internal/platform/genproto/graph/v1"
)

// Client is a thin graph-compute Rank client implementing domain.GraphExpander.
type Client struct {
	client     graphv1.GraphComputeClient
	collection string
	timeout    time.Duration
}

// NewClient wraps a GraphComputeClient. collection is the Qdrant collection the
// document graph is built over (must match the corpus); timeout bounds each Rank
// call (non-positive disables the per-call deadline).
func NewClient(client graphv1.GraphComputeClient, collection string, timeout time.Duration) *Client {
	return &Client{client: client, collection: collection, timeout: timeout}
}

// Expand runs Personalized PageRank seeded by seedDocIDs and returns the top-N
// related document ids (the seeds themselves may appear; the caller filters them
// out). With no seeds it returns nothing rather than triggering a uniform,
// whole-corpus PageRank. ownerID scopes the corpus ("" = shared/global).
func (c *Client) Expand(ctx context.Context, ownerID string, seedDocIDs []string, topN int) ([]string, error) {
	if len(seedDocIDs) == 0 {
		return []string{}, nil
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp, err := c.client.Rank(ctx, &graphv1.RankRequest{
		OwnerId:    ownerID,
		Collection: c.collection,
		SeedIds:    seedDocIDs,
		TopN:       uint32(topN),
	})
	if err != nil {
		return nil, fmt.Errorf("graph rank: %w", err)
	}
	nodes := resp.GetNodes()
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if id := n.GetId(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
