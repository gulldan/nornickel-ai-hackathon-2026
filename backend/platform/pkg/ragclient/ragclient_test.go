package ragclient_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/status"

	"github.com/example/rag-mvp/pkg/ragclient"

	commonv1 "github.com/example/rag-mvp/pkg/genproto/common/v1"
	llmv1 "github.com/example/rag-mvp/pkg/genproto/llm/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// fakeRag implements RagServiceServer with canned responses and an optional
// per-RPC error so error translation can be exercised.
type fakeRag struct {
	llmv1.UnimplementedRagServiceServer
	answerErr error
	chunksErr error
}

// Answer echoes the query into a canned RagResponse or returns answerErr.
func (f *fakeRag) Answer(_ context.Context, req *commonv1.RagRequest) (*commonv1.RagResponse, error) {
	if f.answerErr != nil {
		return nil, f.answerErr
	}
	return &commonv1.RagResponse{
		Answer:  "answer for " + req.GetQuery(),
		Model:   "test-model",
		Cached:  true,
		Sources: []*commonv1.Source{{DocumentId: "doc-1"}},
	}, nil
}

// DocumentChunks echoes the document id into one canned chunk or returns chunksErr.
func (f *fakeRag) DocumentChunks(
	_ context.Context, req *llmv1.DocumentChunksRequest,
) (*llmv1.DocumentChunksResponse, error) {
	if f.chunksErr != nil {
		return nil, f.chunksErr
	}
	return &llmv1.DocumentChunksResponse{
		Chunks: []*llmv1.DocumentChunk{
			{Id: "c0", Index: 0, Text: "chunk of " + req.GetDocumentId()},
			{Id: "c1", Index: 1, Text: "owner " + req.GetOwnerId()},
		},
	}, nil
}

// newTestClient starts a real loopback gRPC server with fake and health
// services and returns a connected client.
func newTestClient(t *testing.T, fake *fakeRag) *ragclient.Client {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()
	llmv1.RegisterRagServiceServer(s, fake)
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	client, err := ragclient.New(lis.Addr().String())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestNewDialError checks that New surfaces a dial error for an invalid target.
func TestNewDialError(t *testing.T) {
	if _, err := ragclient.New("\x00invalid"); err == nil {
		t.Fatalf("expected dial error for invalid target")
	}
}

// TestAnswer verifies Answer maps the request and returns the response fields.
func TestAnswer(t *testing.T) {
	client := newTestClient(t, &fakeRag{})
	resp, err := client.Answer(context.Background(), &commonv1.RagRequest{Query: "q1", OwnerId: "u1"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if resp.GetAnswer() != "answer for q1" {
		t.Errorf("answer = %q, want %q", resp.GetAnswer(), "answer for q1")
	}
	if resp.GetModel() != "test-model" || !resp.GetCached() {
		t.Errorf("model/cached = %q/%v", resp.GetModel(), resp.GetCached())
	}
	if len(resp.GetSources()) != 1 || resp.GetSources()[0].GetDocumentId() != "doc-1" {
		t.Errorf("sources = %v", resp.GetSources())
	}
}

// TestAnswerError verifies Answer wraps a server error instead of dropping it.
func TestAnswerError(t *testing.T) {
	client := newTestClient(t, &fakeRag{answerErr: status.Error(codes.Internal, "boom")})
	if _, err := client.Answer(context.Background(), &commonv1.RagRequest{Query: "q"}); err == nil {
		t.Fatalf("expected error from Answer")
	}
}

// TestDocumentChunks verifies DocumentChunks maps both ids and returns chunks.
func TestDocumentChunks(t *testing.T) {
	client := newTestClient(t, &fakeRag{})
	chunks, err := client.DocumentChunks(context.Background(), "owner-9", "doc-7")
	if err != nil {
		t.Fatalf("DocumentChunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0].GetText() != "chunk of doc-7" {
		t.Errorf("chunk[0].text = %q", chunks[0].GetText())
	}
	if chunks[1].GetText() != "owner owner-9" || chunks[1].GetIndex() != 1 {
		t.Errorf("chunk[1] = %v", chunks[1])
	}
}

// TestDocumentChunksError verifies DocumentChunks wraps a server error.
func TestDocumentChunksError(t *testing.T) {
	client := newTestClient(t, &fakeRag{chunksErr: status.Error(codes.NotFound, "missing")})
	if _, err := client.DocumentChunks(context.Background(), "o", "d"); err == nil {
		t.Fatalf("expected error from DocumentChunks")
	}
}

// TestPing verifies Ping succeeds against a SERVING health server.
func TestPing(t *testing.T) {
	client := newTestClient(t, &fakeRag{})
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestPingAfterClose verifies Ping fails once the connection is closed.
func TestPingAfterClose(t *testing.T) {
	client := newTestClient(t, &fakeRag{})
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Ping(context.Background()); err == nil {
		t.Fatalf("expected Ping to fail after Close")
	}
}
