// Package grpcserver is llm-service's delivery layer: it implements the
// generated RagService gRPC server and translates between the protobuf DTOs
// and the domain query/result types. Missing arguments map to
// codes.InvalidArgument; pipeline failures to codes.Internal. It stays thin:
// validation and mapping only, with all business logic in the application
// layer.
package grpcserver

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/llm-service/internal/application"
	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/logger"

	commonv1 "github.com/example/llm-service/internal/platform/genproto/common/v1"
	llmv1 "github.com/example/llm-service/internal/platform/genproto/llm/v1"
)

// Server implements llmv1.RagServiceServer over the application service. A
// bounded admission queue (slots + maxWait) keeps tail latency predictable
// when a burst of questions lands: up to cap(slots) pipelines run concurrently,
// the next callers wait in line for maxWait, and everything beyond that is
// rejected fast with ResourceExhausted instead of melting the backends.
type Server struct {
	llmv1.UnimplementedRagServiceServer
	svc     *application.RAGService
	slots   chan struct{}
	maxWait time.Duration
}

// New builds the gRPC server. maxConcurrent bounds simultaneous RAG pipelines;
// queueWait bounds how long an admitted caller may wait for a slot.
func New(svc *application.RAGService, maxConcurrent int, queueWait time.Duration) *Server {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Server{
		UnimplementedRagServiceServer: llmv1.UnimplementedRagServiceServer{},
		svc:                           svc,
		slots:                         make(chan struct{}, maxConcurrent),
		maxWait:                       queueWait,
	}
}

// Answer runs the RAG pipeline for one query and returns the grounded answer
// with its citations.
func (s *Server) Answer(ctx context.Context, req *commonv1.RagRequest) (*commonv1.RagResponse, error) {
	owner := strings.TrimSpace(req.GetOwnerId())
	query := strings.TrimSpace(req.GetQuery())
	if owner == "" || query == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_id and query are required")
	}

	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	res, err := s.svc.Answer(ctx, domain.Query{
		OwnerID: owner, Text: query, TopK: int(req.GetTopK()), Prompt: req.GetPrompt(),
		ScopeDocumentIDs:   req.GetScopeDocumentIds(),
		ExcludeDocumentIDs: req.GetExcludeDocumentIds(),
	})
	if err != nil {
		logger.From(ctx).Error().Err(err).Msg("rag answer failed")
		return nil, status.Error(codes.Internal, "failed to answer query")
	}

	sources := make([]*commonv1.Source, 0, len(res.Sources))
	for _, src := range res.Sources {
		sources = append(sources, &commonv1.Source{
			DocumentId:      src.DocumentID,
			Filename:        src.Filename,
			ChunkId:         src.ChunkID,
			Snippet:         src.Snippet,
			Score:           src.Score,
			CharStart:       int32(src.CharStart),
			CharEnd:         int32(src.CharEnd),
			PageStart:       int32(src.PageStart),
			PageEnd:         int32(src.PageEnd),
			SectionHeading:  src.SectionHeading,
			Origin:          src.Origin,
			RaptorSummaryId: src.RaptorSummaryID,
		})
	}
	return &commonv1.RagResponse{
		Answer: res.Answer, Sources: sources, Model: res.Model, Cached: res.Cached,
		Trace: toProtoTrace(res.Trace),
	}, nil
}

// toProtoTrace maps the domain trace onto the protobuf DTO; a nil trace (e.g. a
// legacy cache entry) stays nil.
func toProtoTrace(t *domain.Trace) *commonv1.RagTrace {
	if t == nil {
		return nil
	}
	stages := make([]*commonv1.RagStage, 0, len(t.Stages))
	for _, st := range t.Stages {
		stages = append(stages, &commonv1.RagStage{Stage: st.Stage, Millis: st.Millis})
	}
	return &commonv1.RagTrace{
		Stages:             stages,
		CandidatesDense:    int32(t.CandidatesDense),
		CandidatesLexical:  int32(t.CandidatesLexical),
		CandidatesFused:    int32(t.CandidatesFused),
		CandidatesReturned: int32(t.CandidatesReturned),
		Degraded:           t.Degraded,
		DegradedReason:     t.DegradedReason,
		AbstainReason:      t.AbstainReason,
		TopScore:           t.TopScore,
		ScoreFloor:         t.ScoreFloor,
		Cited:              t.Cited,
		UncitedRemoved:     t.UncitedRemoved,
		RaptorExpanded:     int32(t.RaptorExpanded),
	}
}

// DocumentChunks lists one document's stored chunks for the preview pane.
// Chunk reads are cheap and skip the admission queue: they must stay snappy
// even while the answer pipeline is saturated.
func (s *Server) DocumentChunks(
	ctx context.Context, req *llmv1.DocumentChunksRequest,
) (*llmv1.DocumentChunksResponse, error) {
	owner := strings.TrimSpace(req.GetOwnerId())
	docID := strings.TrimSpace(req.GetDocumentId())
	if owner == "" || docID == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_id and document_id are required")
	}
	chunks, err := s.svc.Chunks(ctx, owner, docID)
	if err != nil {
		logger.From(ctx).Error().Err(err).Msg("document chunks failed")
		return nil, status.Error(codes.Internal, "failed to read document chunks")
	}
	out := make([]*llmv1.DocumentChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, &llmv1.DocumentChunk{Id: c.ID, Index: int32(c.Index), Text: c.Text})
	}
	return &llmv1.DocumentChunksResponse{Chunks: out}, nil
}

// acquire takes an admission slot, waiting up to maxWait. It returns the
// release function, or a ResourceExhausted/Canceled status when the caller
// cannot be admitted.
func (s *Server) acquire(ctx context.Context) (func(), error) {
	timer := time.NewTimer(s.maxWait)
	defer timer.Stop()
	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, nil
	case <-ctx.Done():
		return nil, status.Error(codes.Canceled, "request cancelled while queued")
	case <-timer.C:
		return nil, status.Error(codes.ResourceExhausted, "rag pipeline at capacity; retry shortly")
	}
}
