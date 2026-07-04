// Package domain defines llm-service's RAG vocabulary and the ports the
// application layer depends on. The domain of a retrieval-augmented-generation
// service is small: a query, a retrieved chunk (a candidate passage with its
// provenance and score), the grounded result with its citations, plus the four
// capabilities the pipeline composes — retrieval, ranking, answering and
// caching. Concrete adapters (Qdrant, OpenSearch, the aiclients
// reranker/generator, Valkey) live in the infrastructure layer and satisfy
// these interfaces, keeping the use case transport- and engine-agnostic.
package domain

import (
	"context"
	"time"
)

// Origin labels a Source/RetrievedChunk with the retrieval leg that produced
// it (mirrors proto Source.origin).
const (
	OriginDense = "dense"
	OriginBM25  = "bm25"
	OriginBoth  = "both"
)

// Degradation reasons recorded on Trace.DegradedReason: the pipeline leg that
// failed while the request continued (mirrors proto RagTrace.degraded_reason).
const (
	DegradedEmbedUnavailable    = "embed_unavailable"
	DegradedDenseUnavailable    = "dense_unavailable"
	DegradedLexicalUnavailable  = "lexical_unavailable"
	DegradedRerankerUnavailable = "reranker_unavailable"
)

// Abstain reasons recorded on Trace.AbstainReason (mirrors proto
// RagTrace.abstain_reason).
const (
	AbstainNoSources    = "no_sources"
	AbstainWeakEvidence = "weak_evidence"
)

// Query is one RAG question scoped to an owner. TopK optionally overrides the
// configured number of chunks kept after reranking (0 means "use the default").
type Query struct {
	OwnerID string
	Text    string
	TopK    int
	// Prompt, when non-empty, is what the LLM generates from; Text is then used
	// only for retrieval and reranking. Empty Prompt = classic Q&A (Text drives
	// both retrieval and generation).
	Prompt string
	// ScopeDocumentIDs restricts retrieval to these documents;
	// ExcludeDocumentIDs removes these documents from retrieval (a goal's own
	// input data must not double as its "world practice" evidence).
	ScopeDocumentIDs   []string
	ExcludeDocumentIDs []string
}

// Source is one citation backing an answer: the chunk that grounded it and the
// document it came from. The json tags matter — Result is serialised into the
// Valkey answer cache (the new span fields are additive, so old cached entries
// decode with zero values). CharStart/CharEnd are [start, end) rune offsets of
// the chunk within the document's extracted text; PageStart/PageEnd and
// SectionHeading are reserved for structured-parse output and stay 0/"" until a
// parser supplies document structure.
type Source struct {
	DocumentID     string  `json:"document_id"`
	Filename       string  `json:"filename"`
	ChunkID        string  `json:"chunk_id"`
	Snippet        string  `json:"snippet"`
	Score          float64 `json:"score"`
	CharStart      int     `json:"char_start,omitempty"`
	CharEnd        int     `json:"char_end,omitempty"`
	PageStart      int     `json:"page_start,omitempty"`
	PageEnd        int     `json:"page_end,omitempty"`
	SectionHeading string  `json:"section_heading,omitempty"`
	// Origin is the retrieval leg that surfaced the chunk (OriginDense/
	// OriginBM25/OriginBoth); RaptorSummaryID is set when the chunk was
	// materialized by expanding a retrieved RAPTOR summary node — its point id.
	Origin          string `json:"origin,omitempty"`
	RaptorSummaryID string `json:"raptor_summary_id,omitempty"`
}

// TraceStage is one timed step of the answer pipeline ("embed", "retrieve",
// "rerank", "generate", "total").
type TraceStage struct {
	Stage  string `json:"stage"`
	Millis int64  `json:"millis"`
}

// Trace explains how an answer was produced: stage timings, candidate counts
// at each funnel step, degradation/abstain verdicts and the citation report.
// It mirrors proto RagTrace and rides Result into the answer cache, so a
// cached answer replays the trace of the run that produced it.
type Trace struct {
	Stages             []TraceStage `json:"stages,omitempty"`
	CandidatesDense    int          `json:"candidates_dense,omitempty"`
	CandidatesLexical  int          `json:"candidates_lexical,omitempty"`
	CandidatesFused    int          `json:"candidates_fused,omitempty"`
	CandidatesReturned int          `json:"candidates_returned,omitempty"`
	Degraded           bool         `json:"degraded,omitempty"`
	DegradedReason     string       `json:"degraded_reason,omitempty"`
	AbstainReason      string       `json:"abstain_reason,omitempty"`
	TopScore           float64      `json:"top_score,omitempty"`
	ScoreFloor         float64      `json:"score_floor,omitempty"`
	Cited              []string     `json:"cited,omitempty"`
	UncitedRemoved     []string     `json:"uncited_removed,omitempty"`
	RaptorExpanded     int          `json:"raptor_expanded,omitempty"`
}

// AddStage appends a timed stage; a repeated stage name accumulates into one
// entry (the two sequential retrieval legs fold into a single "retrieve").
func (t *Trace) AddStage(stage string, d time.Duration) {
	ms := d.Milliseconds()
	for i := range t.Stages {
		if t.Stages[i].Stage == stage {
			t.Stages[i].Millis += ms
			return
		}
	}
	t.Stages = append(t.Stages, TraceStage{Stage: stage, Millis: ms})
}

// MarkDegraded flags the trace degraded, keeping the first (most upstream)
// reason when several legs fail.
func (t *Trace) MarkDegraded(reason string) {
	t.Degraded = true
	if t.DegradedReason == "" {
		t.DegradedReason = reason
	}
}

// Result is the grounded answer produced by the pipeline. Cached reports
// whether it was served from the answer cache. The json tags matter — Result
// is serialised into the Valkey answer cache.
type Result struct {
	Answer  string   `json:"answer"`
	Sources []Source `json:"sources"`
	Model   string   `json:"model"`
	Cached  bool     `json:"cached"`
	// Trace explains how the answer was produced; nil on legacy cache entries.
	Trace *Trace `json:"trace,omitempty"`
}

// RetrievedChunk is a candidate passage produced by a Retriever. ID is the chunk
// identifier shared across Qdrant and OpenSearch (the fusion key); Score is the
// adapter's own relevance score, later overwritten by ranking. The remaining
// fields carry enough provenance to build a Source citation.
type RetrievedChunk struct {
	ID         string  // chunk id (Qdrant point id == OpenSearch _id)
	Text       string  // chunk body, used to build the generation context
	DocumentID string  // owning document
	Filename   string  // owning document's filename
	ChunkIndex int     // position within the document (best-effort)
	Score      float64 // current relevance score (retrieval, then ranking)
	// Span provenance, read from the Qdrant payload (best-effort; 0/"" when the
	// chunk was indexed before chunk-splitter started emitting offsets, or came
	// only from the lexical half). CharStart/CharEnd are [start, end) rune
	// offsets within the document's extracted text.
	CharStart      int
	CharEnd        int
	PageStart      int
	PageEnd        int
	SectionHeading string
	// Origin is the retrieval leg that produced the chunk (OriginDense/
	// OriginBM25, upgraded to OriginBoth by fusion). RaptorSummaryID is set on
	// leaves emitted by expanding a retrieved RAPTOR summary — its point id.
	Origin          string
	RaptorSummaryID string
	// RAPTOR hierarchy (read from the Qdrant payload; empty/0 for plain chunk
	// leaves and for lexical-only hits). NodeType is "raptor_summary" for a summary
	// node, "chunk" (or "") for a leaf. NodeLevel is the tree height (0 = leaf).
	// Members are the real leaf chunks a summary stands for, denormalised by
	// raptor-worker so a retrieved summary can be expanded to its underlying chunks
	// for citation (small-to-big) without an extra fetch; MemberIDs lists every
	// member chunk id even when Members carries only a capped, citeable subset.
	NodeType  string
	NodeLevel int
	MemberIDs []string
	Members   []RetrievedChunk
}

// Retriever returns candidate chunks for a query scoped to a single owner. The
// vector adapter ignores the query string (it embeds upstream) and the lexical
// adapter ignores the embedding; each takes what it needs. Implementations that
// hit an unprepared backend (e.g. a missing collection) return no results rather
// than an error, so the pipeline degrades gracefully.
type Retriever interface {
	Retrieve(ctx context.Context, query string, embedding []float32, f RetrievalFilter, topN int) ([]RetrievedChunk, error)
}

// RetrievalFilter scopes one retrieval call: tenant plus optional per-document
// allow/deny lists.
type RetrievalFilter struct {
	OwnerID            string
	ScopeDocumentIDs   []string
	ExcludeDocumentIDs []string
}

// Ranker re-scores fused candidates against the query (the Qwen3-Reranker role).
// It returns one score per chunk, aligned with the input slice order.
type Ranker interface {
	Rank(ctx context.Context, query string, chunks []RetrievedChunk) ([]float64, error)
}

// Answerer turns the assembled context and the user's query into a grounded
// answer (the vLLM role). model identifies the backend that produced it. cite
// requests an inline-citation instruction ([S1], [S2] …) appended to the
// system prompt; it is set only for the plain chat-Q&A path, never when the
// caller supplies its own Prompt (generation/verification expect raw JSON and
// must not be perturbed).
type Answerer interface {
	Answer(ctx context.Context, contextText, query string, cite bool) (answer, model string, err error)
}

// Embedder turns the query into a dense vector for the vector half of retrieval.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Cache stores and retrieves finished RAG answers keyed by owner+query, so a
// repeated question is served from Valkey without re-running the pipeline.
type Cache interface {
	// Get returns the cached result and true on a hit, or false on a miss.
	Get(ctx context.Context, key string) (Result, bool, error)
	// Set stores res under key with the configured TTL.
	Set(ctx context.Context, key string, res Result) error
	// Epoch returns the corpus version for scope (0 if unknown). It is folded
	// into the cache key so newly indexed documents retire stale answers.
	Epoch(ctx context.Context, scope string) int64
}

// StoredChunk is one indexed chunk of a document, as persisted by
// chunk-splitter (the platform's canonical extracted content).
type StoredChunk struct {
	ID    string
	Index int
	Text  string
}

// ChunkSource lists the stored chunks of one document, scoped to its owner
// (satisfied by the Qdrant scroll adapter in infrastructure/retrieval).
type ChunkSource interface {
	DocumentChunks(ctx context.Context, ownerID, documentID string) ([]StoredChunk, error)
}

// LoadMarker publishes interactive-query activity to the rest of the platform
// so batch ingestion (chunk-splitter) can yield while users are asking
// questions and resume full speed the moment they stop. Both methods are
// best-effort: marker failures must never fail a query.
type LoadMarker interface {
	// QueryStarted records that one interactive query entered the pipeline.
	QueryStarted(ctx context.Context)
	// QueryFinished records that the query left the pipeline.
	QueryFinished(ctx context.Context)
}

// Reasoner is the chat LLM the optional agentic controller drives for its
// reasoning and document-condensation ("reason-in-documents") steps. Unlike
// Answerer — which frames a fixed grounded-Q&A prompt — it takes an explicit
// system+user prompt so the controller can steer the model to emit a search
// action or signal that it can answer. It is satisfied by an adapter over the
// same chat backend as the generator (see infrastructure/aiadapters), and is
// only wired when RAG_AGENTIC is on.
type Reasoner interface {
	Reason(ctx context.Context, system, user string) (string, error)
}

// GraphExpander expands a set of seed documents into related documents over the
// graph-compute kNN document graph (Personalized PageRank), so the agentic loop
// can fold a graph "tool" into its candidate set for multi-hop expansion. It is
// strictly optional: nil disables the graph tool, and implementations must
// degrade gracefully — returning no ids (not an error that fails the query) when
// graph-compute is unreachable. ownerID scopes the corpus ("" = shared/global).
type GraphExpander interface {
	Expand(ctx context.Context, ownerID string, seedDocIDs []string, topN int) ([]string, error)
}
