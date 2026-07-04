// Package ragclient is the typed gRPC client for llm-service's RagService, used
// by main-service to obtain grounded answers.
package ragclient

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"github.com/example/main-service/internal/platform/grpcx"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	llmv1 "github.com/example/main-service/internal/platform/genproto/llm/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Client calls llm-service over gRPC.
type Client struct {
	conn *grpc.ClientConn
	api  llmv1.RagServiceClient
}

// New dials llm-service at addr (host:port). The connection is lazy.
func New(addr string) (*Client, error) {
	conn, err := grpcx.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("dial llm-service: %w", err)
	}
	return &Client{conn: conn, api: llmv1.NewRagServiceClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close llm connection: %w", err)
	}
	return nil
}

// Answer requests a grounded answer for the query.
func (c *Client) Answer(ctx context.Context, req *commonv1.RagRequest) (*commonv1.RagResponse, error) {
	resp, err := c.api.Answer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("rag answer: %w", err)
	}
	return resp, nil
}

// DocumentChunks returns the indexed chunks of one document, ordered by index.
func (c *Client) DocumentChunks(ctx context.Context, ownerID, documentID string) ([]*llmv1.DocumentChunk, error) {
	resp, err := c.api.DocumentChunks(ctx, &llmv1.DocumentChunksRequest{
		DocumentId: documentID, OwnerId: ownerID,
	})
	if err != nil {
		return nil, fmt.Errorf("rag document chunks: %w", err)
	}
	return resp.GetChunks(), nil
}

// Ping checks llm-service health for readiness probes.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := healthpb.NewHealthClient(c.conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("llm-service health: %w", err)
	}
	return nil
}
