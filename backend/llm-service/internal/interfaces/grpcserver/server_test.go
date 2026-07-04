package grpcserver_test

// Unit tests for the gRPC delivery layer. The server is built over a real
// application.RAGService wired with fake domain ports, so the tests exercise the
// validation, the protobuf<->domain mapping and the bounded admission queue
// without any network backend.

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/llm-service/internal/application"
	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/interfaces/grpcserver"

	commonv1 "github.com/example/llm-service/internal/platform/genproto/common/v1"
	llmv1 "github.com/example/llm-service/internal/platform/genproto/llm/v1"
)

// ---- fake domain ports -----------------------------------------------------

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1}, nil
}

// fakeRetriever returns a fixed candidate set.
type fakeRetriever struct{ chunks []domain.RetrievedChunk }

func (f fakeRetriever) Retrieve(
	context.Context, string, []float32, domain.RetrievalFilter, int,
) ([]domain.RetrievedChunk, error) {
	return f.chunks, nil
}

// fakeRanker scores every chunk equally.
type fakeRanker struct{}

func (fakeRanker) Rank(_ context.Context, _ string, chunks []domain.RetrievedChunk) ([]float64, error) {
	out := make([]float64, len(chunks))
	for i := range out {
		out[i] = 1
	}
	return out, nil
}

// blockingAnswerer blocks until release is closed, so a test can hold an
// admission slot and observe the queue. entered (when non-nil) is signalled once
// generation begins, i.e. once the slot is actually held.
type blockingAnswerer struct {
	release chan struct{}
	entered chan struct{}
}

func (a *blockingAnswerer) Answer(context.Context, string, string, bool) (string, string, error) {
	if a.entered != nil {
		close(a.entered)
	}
	if a.release != nil {
		<-a.release
	}
	return "ANSWER", "model-x", nil
}

type stubCache struct{}

func (stubCache) Get(context.Context, string) (domain.Result, bool, error) {
	return domain.Result{}, false, nil
}
func (stubCache) Set(context.Context, string, domain.Result) error { return nil }
func (stubCache) Epoch(context.Context, string) int64              { return 0 }

// errChunks is a ChunkSource that always fails, to drive the Internal-error path
// of DocumentChunks.
type errChunks struct{}

func (errChunks) DocumentChunks(context.Context, string, string) ([]domain.StoredChunk, error) {
	return nil, errors.New("scroll failed")
}

// okChunks returns a fixed chunk list for the happy DocumentChunks path.
type okChunks struct{}

func (okChunks) DocumentChunks(context.Context, string, string) ([]domain.StoredChunk, error) {
	return []domain.StoredChunk{{ID: "c0", Index: 0, Text: "hello"}}, nil
}

// newService builds a RAGService with the given answerer and chunk source; the
// retriever returns one chunk so generation runs and a source is produced.
func newService(ans domain.Answerer, chunks domain.ChunkSource) *application.RAGService {
	src := fakeRetriever{chunks: []domain.RetrievedChunk{
		{ID: "c1", Text: "turbine blade efficiency", DocumentID: "d1", Filename: "r.pdf"},
	}}
	return application.New(
		fakeEmbedder{}, src, fakeRetriever{}, fakeRanker{}, ans,
		stubCache{}, chunks, nil, nil, 5, false, -5, false, application.Tuning{},
	)
}

// statusCode extracts the gRPC status code from an error, failing if it is not a
// status error.
func statusCode(t *testing.T, err error) codes.Code {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	return st.Code()
}

// TestAnswerSuccess maps a domain result (answer, model, sources, trace) into
// the protobuf response.
func TestAnswerSuccess(t *testing.T) {
	srv := grpcserver.New(newService(&blockingAnswerer{}, okChunks{}), 4, time.Second)
	resp, err := srv.Answer(context.Background(), &commonv1.RagRequest{OwnerId: "u1", Query: "q", TopK: 3})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if resp.GetAnswer() != "ANSWER" || resp.GetModel() != "model-x" {
		t.Fatalf("response = %+v, want answer/model mapped", resp)
	}
	if len(resp.GetSources()) != 1 || resp.GetSources()[0].GetDocumentId() != "d1" {
		t.Fatalf("sources not mapped: %+v", resp.GetSources())
	}
	if got := resp.GetSources()[0].GetOrigin(); got != domain.OriginDense {
		t.Fatalf("source origin = %q, want %q", got, domain.OriginDense)
	}
	tr := resp.GetTrace()
	if tr == nil || len(tr.GetStages()) == 0 || tr.GetCandidatesDense() != 1 || tr.GetCandidatesReturned() != 1 {
		t.Fatalf("trace not mapped: %+v", tr)
	}
}

// TestAnswerValidation rejects missing owner or query with InvalidArgument.
func TestAnswerValidation(t *testing.T) {
	srv := grpcserver.New(newService(&blockingAnswerer{}, okChunks{}), 4, time.Second)
	cases := map[string]*commonv1.RagRequest{
		"missing owner": {OwnerId: " ", Query: "q"},
		"missing query": {OwnerId: "u1", Query: "  "},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := srv.Answer(context.Background(), req)
			if got := statusCode(t, err); got != codes.InvalidArgument {
				t.Fatalf("code = %v, want InvalidArgument", got)
			}
		})
	}
}

// TestAnswerPipelineError maps a pipeline failure to Internal. Generation is
// the only hard requirement left (embed/retrieval/rerank outages degrade), so
// a failing generator on the structured-prompt path — which has no extractive
// fallback — drives the error.
func TestAnswerPipelineError(t *testing.T) {
	srv := grpcserver.New(newService(failingAnswerer{}, okChunks{}), 4, time.Second)
	_, err := srv.Answer(context.Background(), &commonv1.RagRequest{OwnerId: "u1", Query: "q", Prompt: "return JSON"})
	if got := statusCode(t, err); got != codes.Internal {
		t.Fatalf("code = %v, want Internal", got)
	}
}

// failingAnswerer errors so the pipeline's hard-required generation step fails.
type failingAnswerer struct{}

func (failingAnswerer) Answer(context.Context, string, string, bool) (string, string, error) {
	return "", "", errors.New("generator down")
}

// TestDocumentChunksSuccess maps stored chunks into the protobuf response.
func TestDocumentChunksSuccess(t *testing.T) {
	srv := grpcserver.New(newService(&blockingAnswerer{}, okChunks{}), 4, time.Second)
	resp, err := srv.DocumentChunks(context.Background(), &llmv1.DocumentChunksRequest{OwnerId: "u1", DocumentId: "d1"})
	if err != nil {
		t.Fatalf("DocumentChunks: %v", err)
	}
	if len(resp.GetChunks()) != 1 || resp.GetChunks()[0].GetId() != "c0" || resp.GetChunks()[0].GetText() != "hello" {
		t.Fatalf("chunks not mapped: %+v", resp.GetChunks())
	}
}

// TestDocumentChunksValidation rejects missing owner or document id.
func TestDocumentChunksValidation(t *testing.T) {
	srv := grpcserver.New(newService(&blockingAnswerer{}, okChunks{}), 4, time.Second)
	cases := map[string]*llmv1.DocumentChunksRequest{
		"missing owner":    {OwnerId: "", DocumentId: "d1"},
		"missing document": {OwnerId: "u1", DocumentId: ""},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := srv.DocumentChunks(context.Background(), req)
			if got := statusCode(t, err); got != codes.InvalidArgument {
				t.Fatalf("code = %v, want InvalidArgument", got)
			}
		})
	}
}

// TestDocumentChunksError maps a read failure to Internal.
func TestDocumentChunksError(t *testing.T) {
	srv := grpcserver.New(newService(&blockingAnswerer{}, errChunks{}), 4, time.Second)
	_, err := srv.DocumentChunks(context.Background(), &llmv1.DocumentChunksRequest{OwnerId: "u1", DocumentId: "d1"})
	if got := statusCode(t, err); got != codes.Internal {
		t.Fatalf("code = %v, want Internal", got)
	}
}

// TestAnswerAtCapacity rejects an extra caller with ResourceExhausted once the
// single admission slot is held and the queue wait elapses.
func TestAnswerAtCapacity(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	srv := grpcserver.New(newService(&blockingAnswerer{release: release, entered: entered}, okChunks{}), 1, 20*time.Millisecond)

	go func() {
		// Holds the only slot until release fires.
		_, _ = srv.Answer(context.Background(), &commonv1.RagRequest{OwnerId: "u1", Query: "q"})
	}()
	<-entered // the slot is now held

	_, err := srv.Answer(context.Background(), &commonv1.RagRequest{OwnerId: "u2", Query: "q"})
	if got := statusCode(t, err); got != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted", got)
	}
	close(release)
}

// TestAnswerQueueCancelled returns Canceled when the caller's context is
// cancelled while waiting for a slot.
func TestAnswerQueueCancelled(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	srv := grpcserver.New(newService(&blockingAnswerer{release: release, entered: entered}, okChunks{}), 1, 5*time.Second)

	go func() {
		_, _ = srv.Answer(context.Background(), &commonv1.RagRequest{OwnerId: "u1", Query: "q"})
	}()
	<-entered // the slot is now held, so the next caller queues

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // unblock the waiter on ctx.Done rather than the timer
	_, err := srv.Answer(ctx, &commonv1.RagRequest{OwnerId: "u2", Query: "q"})
	if got := statusCode(t, err); got != codes.Canceled {
		t.Fatalf("code = %v, want Canceled", got)
	}
}
