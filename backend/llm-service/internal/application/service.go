// Package application implements llm-service's single use case: answer a query
// with retrieval-augmented generation. It depends only on the domain ports, so
// the pipeline is independent of the concrete vector store, search engine, AI
// backends and cache wired in at the edges (infrastructure layer).
//
// The pipeline mirrors the architecture's hybrid-retrieval design:
//
//  1. cache check (Valkey) — return immediately with Cached=true on a hit;
//  2. embed the query (an embedder outage degrades to lexical-only retrieval);
//  3. retrieve candidates from Qdrant (vector) AND OpenSearch (lexical/BM25);
//  4. fuse the two ranked lists by Reciprocal Rank Fusion (RRF) on chunk id;
//  5. rerank the fused candidates with the cross-encoder and sort descending
//     (a reranker outage degrades to the RRF order);
//  6. keep the top-K (the request's TopK when set, else RAG_TOP_K);
//  7. build the labelled context string and the []domain.Source citations;
//     7a. abstain (no generation) when the top reranker score is below
//     RAG_SCORE_FLOOR or nothing was retrieved — the floor only with a real,
//     non-degraded reranker;
//  8. generate the grounded answer;
//  9. cache the result (degraded runs excluded) and return it.
//
// Every run carries a domain.Trace (stage timings, funnel counts, degradation
// and abstain verdicts, citation report) back to the caller and into the cache.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/logger"
)

// rrfK is the Reciprocal Rank Fusion constant. The conventional value of 60
// dampens the contribution of low-ranked items so the top of each list
// dominates the fused order without any single source overwhelming the other.
const rrfK = 60

// snippetLen bounds the citation snippet length (characters) so responses stay
// compact; the full chunk text still feeds the generation context.
const snippetLen = 200

// extractiveFallbackMaxSources bounds the source snippets included when
// generation is unavailable but retrieval succeeded. It keeps the fallback
// readable while returning the full citation list separately.
const extractiveFallbackMaxSources = 3

// extractiveFallbackModel marks a chat answer produced without the generator.
// It is deliberately not cached, so recovery of the external LLM immediately
// restores normal synthesized answers.
const extractiveFallbackModel = "extractive-fallback"

// nodeTypeRaptorSummary marks a Qdrant point as a RAPTOR summary node (vs a raw
// "chunk" leaf). A summary may win retrieval on a broad query (it spans many
// chunks), but it is not a real source — see expandSummaries.
const nodeTypeRaptorSummary = "raptor_summary"

// maxSummaryExpansion caps how many member leaf chunks one retrieved summary
// expands into, so a single high-altitude hit cannot flood the context/citation
// list with its whole subtree.
const maxSummaryExpansion = 6

// StageRecorder records per-stage RAG latency for the latency trace. Optional;
// a nil recorder disables recording. Satisfied by *observability.Metrics.
type StageRecorder interface {
	RecordStage(stage string, seconds float64)
}

// RAGService orchestrates the retrieval-augmented-generation pipeline over the
// domain ports.
type RAGService struct {
	embedder  domain.Embedder
	retrieval domain.Retriever // vector half (Qdrant)
	lexical   domain.Retriever // lexical half (OpenSearch)
	ranker    domain.Ranker
	answerer  domain.Answerer
	cache     domain.Cache
	chunks    domain.ChunkSource
	load      domain.LoadMarker // optional; nil disables load signalling
	metrics   StageRecorder     // optional; nil disables per-stage timing

	topK int // number of chunks kept after reranking (RAG_TOP_K)
	// sharedCorpus drops the per-owner retrieval filter: every signed-in user
	// searches the whole knowledge base (the "Справка" product mode), while
	// chats and uploads stay private. Off means strict tenant isolation.
	sharedCorpus bool

	// scoreFloor is the minimum top reranker score required to attempt
	// generation (RAG_SCORE_FLOOR). When the best retained source scores below
	// it — or nothing was retrieved — the pipeline abstains instead of letting
	// the model confabulate from weak/empty context (kills fail-open). Only
	// meaningful with a real cross-encoder; see rerankerActive.
	scoreFloor float64
	// rerankerActive reports whether a real reranker is wired (RERANKER_URL set).
	// The stub Jaccard reranker produces a different score scale (0..1), so the
	// floor is skipped when false to avoid spurious abstentions.
	rerankerActive bool

	// Retrieval tuning (see Tuning); set in New from config, defaults preserve
	// the baseline behaviour.
	maxPerDoc     int
	vecWeight     float64
	lexWeight     float64
	retrievalMult int
	retrievalMin  int

	// agentic is the optional Search-o1-style multi-hop controller (RAG_AGENTIC).
	// It is nil by default — Answer then runs the single-shot path below exactly
	// as before. It is set only via EnableAgentic when the flag is on, and even
	// then the controller gates on query complexity, so simple (and structured-
	// prompt) queries still take the unchanged single-shot path. See agentic.go.
	agentic *agenticController
}

// abstainAnswer is the fixed reply returned when the retrieved context is too
// weak (or empty) to ground an honest answer. Returned verbatim, without
// invoking the generator, to every caller.
const abstainAnswer = "Не удалось найти достаточно релевантный контекст в базе знаний, " +
	"чтобы дать обоснованный ответ."

// Tuning carries the optional retrieval knobs. Zero values fall back to defaults
// that preserve the baseline behaviour, so callers may pass a zero Tuning.
type Tuning struct {
	MaxPerDoc     int     // cap on chunks per document in the final top-K; 0 = unlimited
	VecWeight     float64 // weighted-RRF weight for vector hits; <=0 → 1
	LexWeight     float64 // weighted-RRF weight for lexical hits; <=0 → 1
	RetrievalMult int     // candidate over-fetch multiplier; <=0 → 4
	RetrievalMin  int     // candidate floor; <=0 → 20
}

// New wires the dependencies into a RAGService. topK must be >= 1. load may be
// nil when no interactive-priority signalling is configured. scoreFloor is the
// abstain threshold on the top reranker score; it is enforced only when
// rerankerActive is true (a real cross-encoder is wired). tuning carries the
// optional retrieval knobs (a zero Tuning keeps the baseline behaviour).
func New(
	embedder domain.Embedder,
	retrieval domain.Retriever,
	lexical domain.Retriever,
	ranker domain.Ranker,
	answerer domain.Answerer,
	cache domain.Cache,
	chunks domain.ChunkSource,
	load domain.LoadMarker,
	metrics StageRecorder,
	topK int,
	sharedCorpus bool,
	scoreFloor float64,
	rerankerActive bool,
	tuning Tuning,
) *RAGService {
	if topK < 1 {
		topK = 1
	}
	if tuning.VecWeight <= 0 {
		tuning.VecWeight = 1
	}
	if tuning.LexWeight <= 0 {
		tuning.LexWeight = 1
	}
	if tuning.RetrievalMult <= 0 {
		tuning.RetrievalMult = 4
	}
	if tuning.RetrievalMin <= 0 {
		tuning.RetrievalMin = 20
	}
	return &RAGService{
		embedder:  embedder,
		retrieval: retrieval,
		lexical:   lexical,
		ranker:    ranker,
		answerer:  answerer,
		cache:     cache,
		chunks:    chunks,
		load:      load,
		metrics:   metrics,
		topK:      topK,

		sharedCorpus:   sharedCorpus,
		scoreFloor:     scoreFloor,
		rerankerActive: rerankerActive,

		maxPerDoc:     tuning.MaxPerDoc,
		vecWeight:     tuning.VecWeight,
		lexWeight:     tuning.LexWeight,
		retrievalMult: tuning.RetrievalMult,
		retrievalMin:  tuning.RetrievalMin,
	}
}

// Answer runs the full RAG pipeline for one query and returns the grounded
// result. Errors from the cache, the embedder, the retrievers and the reranker
// are tolerated (logged, recorded on the trace, pipeline continues degraded) so
// a broken dependency never fails the whole request; only generation is a hard
// requirement, and even it falls back to an extractive answer on the chat path.
func (s *RAGService) Answer(ctx context.Context, q domain.Query) (domain.Result, error) {
	// Agentic multi-hop controller (RAG_AGENTIC, default off). s.agentic is nil
	// unless the flag is on, so this branch is skipped entirely and the single-
	// shot pipeline below runs byte-for-byte as before. When enabled, the
	// controller gates on query complexity and returns handled=false for simple
	// (or structured-prompt) queries, falling through to the unchanged path.
	if s.agentic != nil {
		if res, handled, err := s.agentic.answer(ctx, s, q); err != nil || handled {
			return res, err
		}
	}

	log := logger.From(ctx).With().Str("owner_id", q.OwnerID).Logger()

	// Mark the interactive query in flight so batch ingestion yields its
	// capacity for the duration (see domain.LoadMarker).
	if s.load != nil {
		s.load.QueryStarted(ctx)
		defer s.load.QueryFinished(ctx)
	}

	// The retrieval scope: per-owner in isolation mode, corpus-wide in shared
	// mode. Cache keys follow the scope so shared answers are shared.
	scope := q.OwnerID
	if s.sharedCorpus {
		scope = ""
	}

	// Per-stage latency trace: Prometheus (when wired) plus the response trace.
	tr := &domain.Trace{ScoreFloor: s.scoreFloor}
	overall := time.Now()
	stage := s.stageFn(tr)

	// (1) Cache check. The key carries the corpus epoch, so indexing new
	// documents invalidates stale answers without scanning keys. A hit returns
	// the trace of the run that produced the cached answer.
	scopeKey := scope
	if scopeKey == "" {
		scopeKey = "shared"
	}
	key := cacheKey(scopeKey, s.cache.Epoch(ctx, scopeKey), q.Text, q.Prompt, docScopeKey(q))
	if res, hit := s.cachedAnswer(ctx, log, key); hit {
		return res, nil
	}

	// (2) Embed the query for vector search. An embedder outage no longer fails
	// the request: the dense leg is skipped and retrieval continues lexical-only.
	tEmbed := time.Now()
	embedding, err := s.embedder.Embed(ctx, q.Text)
	if err != nil {
		log.Warn().Err(err).Msg("embed failed; continuing lexical-only")
		embedding = nil
		tr.MarkDegraded(domain.DegradedEmbedUnavailable)
	}
	stage("embed", tEmbed)

	// (3)–(6) Hybrid retrieval, fusion, reranking and top-K truncation.
	ranked, rerankOK := s.retrieveAndRank(ctx, log, q, embedding, scope, stage, tr)

	// (7) Assemble the generation context and the citation list from the same
	// retained chunks. RAPTOR summary hits are first expanded to their real leaf
	// chunks (small-to-big), so the model grounds on — and the UI cites — actual
	// passages rather than a synthetic summary. buildContext and buildSources run
	// over the SAME expanded list, keeping the inline [Sn] labels 1:1 with sources.
	expanded, raptorExpanded := expandSummaries(ranked)
	tr.RaptorExpanded = raptorExpanded
	contextText := buildContext(expanded)
	sources := buildSources(expanded)

	// (7a) Abstain instead of fail-open. We never generate from an empty context
	// (no retrieved sources) — regardless of which reranker is wired — and, with a
	// real cross-encoder, we also abstain when the best source scores below the
	// floor. This kills the confident-but-source-less answer a dead/empty
	// retrieval used to produce.
	if res, abstained := s.maybeAbstain(log, ranked, sources, rerankOK, tr); abstained {
		stage("total", overall)
		s.cacheResult(ctx, log, key, res, tr.Degraded)
		return res, nil
	}

	// (8) Generate the grounded answer. The citation instruction is added only on
	// the plain chat-Q&A path (no custom Prompt); structured-prompt callers
	// (generation/verification) must get their JSON unperturbed.
	tGen := time.Now()
	genQuestion := q.Text
	cite := true
	if q.Prompt != "" {
		genQuestion = q.Prompt
		cite = false
	}
	answer, model, err := s.answerer.Answer(ctx, contextText, genQuestion, cite)
	if err != nil {
		if cite && len(sources) > 0 {
			stage("generate", tGen)
			stage("total", overall)
			log.Warn().
				Err(err).
				Int("sources", len(sources)).
				Msg("generation failed; returning extractive fallback")
			return domain.Result{
				Answer:  extractiveFallbackAnswer(sources),
				Sources: sources,
				Model:   extractiveFallbackModel,
				Cached:  false,
				Trace:   tr,
			}, nil
		}
		return domain.Result{}, fmt.Errorf("generate answer: %w", err)
	}
	stage("generate", tGen)

	// (8a) Validate the answer's inline [Sn] citations against the sources that
	// were actually retrieved (chat path only — structured-prompt output is left
	// untouched). Citations pointing past the source list are hallucinated and are
	// stripped; an answer that cites nothing despite having sources is flagged.
	if cite {
		answer = s.sanitizeAnswerCitations(log, answer, sources, tr)
	}

	res := domain.Result{
		Answer:  answer,
		Sources: sources,
		Model:   model,
		Cached:  false,
		Trace:   tr,
	}

	// (9) Cache the result (best-effort) and return it — but never memoize an
	// answer computed under a degraded pipeline, which would outlive the outage.
	stage("total", overall)
	s.cacheResult(ctx, log, key, res, tr.Degraded)
	log.Info().Int("sources", len(sources)).Str("model", model).Msg("answered")
	return res, nil
}

// stageFn returns the per-stage latency recorder feeding both Prometheus (when
// wired) and the response trace.
func (s *RAGService) stageFn(tr *domain.Trace) func(string, time.Time) {
	return func(name string, start time.Time) {
		d := time.Since(start)
		if s.metrics != nil {
			s.metrics.RecordStage(name, d.Seconds())
		}
		tr.AddStage(traceStage(name), d)
	}
}

// traceStage maps the fine-grained Prometheus stage labels onto the trace's
// canonical names ("embed", "retrieve", "rerank", "generate", "total"): the
// two sequential retrieval legs fold into one "retrieve" entry.
func traceStage(name string) string {
	if name == "retrieve_vector" || name == "retrieve_lexical" {
		return "retrieve"
	}
	return name
}

// cacheResult stores a finished result best-effort — unless the pipeline ran
// degraded: a degraded outcome is potentially transient and must not be
// memoized past the outage.
func (s *RAGService) cacheResult(ctx context.Context, log zerolog.Logger, key string, res domain.Result, degraded bool) {
	if degraded {
		return
	}
	if err := s.cache.Set(ctx, key, res); err != nil {
		log.Warn().Err(err).Msg("cache set failed")
	}
}

// cachedAnswer returns a cached result and true on a hit. A cache error is
// tolerated (logged, treated as a miss) so a degraded cache never fails the
// request.
func (s *RAGService) cachedAnswer(ctx context.Context, log zerolog.Logger, key string) (domain.Result, bool) {
	res, hit, err := s.cache.Get(ctx, key)
	if err != nil {
		log.Warn().Err(err).Msg("cache get failed")
		return domain.Result{}, false
	}
	if !hit {
		return domain.Result{}, false
	}
	log.Info().Msg("cache hit")
	res.Cached = true
	return res, true
}

// retrieveAndRank runs hybrid retrieval (vector + lexical, each over-fetched),
// fuses the lists by RRF, reranks them and keeps the top-K. Backend failures
// degrade instead of failing: a dead retriever leg contributes no candidates
// and a reranker outage keeps the RRF order — both recorded on tr, whose
// degraded outcome the caller must not cache. A nil embedding (embedder
// degraded upstream) skips the dense leg. rerankOK reports whether the
// cross-encoder actually rescored the candidates, i.e. whether the score floor
// is meaningful for them.
func (s *RAGService) retrieveAndRank(
	ctx context.Context,
	log zerolog.Logger,
	q domain.Query,
	embedding []float32,
	scope string,
	stage func(string, time.Time),
	tr *domain.Trace,
) (ranked []domain.RetrievedChunk, rerankOK bool) {
	// (3) Hybrid retrieval: vector and lexical, each over-fetched so fusion and
	// reranking have a healthy candidate pool. topN = max(20, TopK*4).
	topN := retrievalDepth(q.TopK, s.topK, s.retrievalMult, s.retrievalMin)
	filter := domain.RetrievalFilter{
		OwnerID:            scope,
		ScopeDocumentIDs:   q.ScopeDocumentIDs,
		ExcludeDocumentIDs: q.ExcludeDocumentIDs,
	}
	var vectorHits []domain.RetrievedChunk
	if embedding != nil {
		tVec := time.Now()
		hits, vecOK := s.retrieve(ctx, log, "qdrant", s.retrieval, q.Text, embedding, filter, topN)
		stage("retrieve_vector", tVec)
		if !vecOK {
			tr.MarkDegraded(domain.DegradedDenseUnavailable)
		}
		vectorHits = hits
	}
	tLex := time.Now()
	lexicalHits, lexOK := s.retrieve(ctx, log, "opensearch", s.lexical, q.Text, embedding, filter, topN)
	stage("retrieve_lexical", tLex)
	if !lexOK {
		tr.MarkDegraded(domain.DegradedLexicalUnavailable)
	}
	tagOrigin(vectorHits, domain.OriginDense)
	tagOrigin(lexicalHits, domain.OriginBM25)
	tr.CandidatesDense += len(vectorHits)
	tr.CandidatesLexical += len(lexicalHits)

	// (4) Weighted Reciprocal Rank Fusion on chunk id.
	fused := fuse([]weightedList{{s.vecWeight, vectorHits}, {s.lexWeight, lexicalHits}})
	tr.CandidatesFused += len(fused)

	// (5) Rerank the fused candidates. A reranker outage keeps the RRF order
	// instead of failing the query; the floor is then skipped downstream (it is
	// calibrated for rerank scores, not fusion scores).
	tRerank := time.Now()
	ranked, rerankOK = fused, true
	if scored, err := s.rerank(ctx, log, q.Text, fused); err != nil {
		log.Warn().Err(err).Msg("reranker failed; keeping fusion order")
		tr.MarkDegraded(domain.DegradedRerankerUnavailable)
		rerankOK = false
	} else {
		ranked = scored
	}
	stage("rerank", tRerank)
	// (6) Keep the top-K. Honour the request's TopK when set (callers such as
	// hypothesis generation ask for deeper evidence than the chat default), else
	// fall back to the configured RAG_TOP_K.
	eff := s.topK
	if q.TopK > 0 {
		eff = q.TopK
	}
	ranked = capPerDocument(ranked, eff, s.maxPerDoc)
	tr.CandidatesReturned += len(ranked)
	return ranked, rerankOK
}

// tagOrigin stamps the retrieval leg that produced each hit; fuse upgrades a
// chunk found by both legs to OriginBoth.
func tagOrigin(chunks []domain.RetrievedChunk, origin string) {
	for i := range chunks {
		chunks[i].Origin = origin
	}
}

// sanitizeAnswerCitations strips inline [Sn] citations pointing past the source
// list (hallucinated) and warns when an answer cites nothing despite having
// sources; the citation report lands on the trace. Chat path only;
// structured-prompt output is left untouched.
func (s *RAGService) sanitizeAnswerCitations(
	log zerolog.Logger, answer string, sources []domain.Source, tr *domain.Trace,
) string {
	answer, rep := sanitizeCitations(answer, len(sources))
	tr.Cited = citationMarkers(rep.Cited)
	tr.UncitedRemoved = citationMarkers(rep.OutOfRange)
	if len(rep.OutOfRange) > 0 {
		log.Warn().Ints("dropped", rep.OutOfRange).Int("sources", len(sources)).
			Msg("stripped citations to sources that were not retrieved")
	}
	if len(sources) > 0 && len(rep.Cited) == 0 {
		log.Warn().Msg("answer cited no retrieved source (ungrounded)")
	}
	return answer
}

// citationMarkers renders 1-based citation indices as their inline markers
// ("[S3]"), the form the trace exposes to the UI.
func citationMarkers(indices []int) []string {
	if len(indices) == 0 {
		return nil
	}
	out := make([]string, len(indices))
	for i, n := range indices {
		out[i] = "[S" + strconv.Itoa(n) + "]"
	}
	return out
}

// Chunks lists the stored chunks of one document for the preview pane. In
// shared-corpus mode the owner filter is dropped (any signed-in user may read
// any indexed document).
func (s *RAGService) Chunks(ctx context.Context, ownerID, documentID string) ([]domain.StoredChunk, error) {
	if s.sharedCorpus {
		ownerID = ""
	}
	return s.chunks.DocumentChunks(ctx, ownerID, documentID)
}

// retrieve calls one retriever and downgrades any error to an empty result. A
// missing collection/index or an unreachable backend must not fail the request;
// hybrid retrieval still works on whatever the other half returns. The bool
// reports whether the call succeeded: false means the backend errored, which the
// caller treats as a *degraded* (possibly transient) retrieval — distinct from a
// healthy backend that simply found nothing.
func (s *RAGService) retrieve(
	ctx context.Context,
	log zerolog.Logger,
	name string,
	r domain.Retriever,
	query string,
	embedding []float32,
	f domain.RetrievalFilter,
	topN int,
) ([]domain.RetrievedChunk, bool) {
	hits, err := r.Retrieve(ctx, query, embedding, f, topN)
	if err != nil {
		log.Warn().Str("retriever", name).Err(err).Msg("retriever failed; continuing with no results")
		return nil, false
	}
	return hits, true
}

// rerank scores the candidates with the Ranker and returns them sorted by score
// descending. With no candidates it returns an empty slice without calling the
// ranker. The reranker score replaces the fusion score on each chunk so it
// surfaces in the citation list.
func (s *RAGService) rerank(
	ctx context.Context,
	log zerolog.Logger,
	query string,
	chunks []domain.RetrievedChunk,
) ([]domain.RetrievedChunk, error) {
	if len(chunks) == 0 {
		return chunks, nil
	}
	scores, err := s.ranker.Rank(ctx, query, chunks)
	if err != nil {
		return nil, err
	}
	// Defensive: a misbehaving backend could return a mismatched length. Keep the
	// fusion order in that case rather than indexing out of range.
	if len(scores) != len(chunks) {
		log.Warn().Int("scores", len(scores)).Int("chunks", len(chunks)).
			Msg("reranker returned mismatched score count; keeping fusion order")
		return chunks, nil
	}
	for i := range chunks {
		chunks[i].Score = scores[i]
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		return chunks[i].Score > chunks[j].Score
	})
	return chunks, nil
}

// maybeAbstain decides whether to skip generation and return the fixed abstain
// reply (and true). It abstains when:
//
//   - there is no usable context (no retrieved sources) — ALWAYS, regardless of
//     the reranker, so a dead/empty retrieval can never fail open into a
//     confident, source-less answer; or
//   - a real cross-encoder is wired, it actually rescored this query (rerankOK)
//     and the top retained score is below RAG_SCORE_FLOOR (weak match). The
//     floor is skipped for the stub Jaccard reranker and for a degraded
//     reranker: both leave scores on a scale the floor was not calibrated for.
//
// It records the verdict on tr (abstain reason, top score before the floor
// check). The caller owns caching (see cacheResult) and the "total" stage, so
// both the abstain and answer paths are timed and never memoize a degraded run.
func (s *RAGService) maybeAbstain(
	log zerolog.Logger,
	ranked []domain.RetrievedChunk,
	sources []domain.Source,
	rerankOK bool,
	tr *domain.Trace,
) (domain.Result, bool) {
	empty := len(sources) == 0
	topScore, hasScore := topRerankScore(ranked)
	if rerankOK && hasScore {
		tr.TopScore = topScore
	}
	weak := false
	if !empty && s.rerankerActive && rerankOK {
		weak = !hasScore || topScore < s.scoreFloor
	}
	if !empty && !weak {
		return domain.Result{}, false
	}
	if empty {
		tr.AbstainReason = domain.AbstainNoSources
	} else {
		tr.AbstainReason = domain.AbstainWeakEvidence
	}
	log.Warn().
		Bool("empty", empty).
		Bool("weak", weak).
		Bool("degraded", tr.Degraded).
		Float64("top_score", topScore).
		Float64("score_floor", s.scoreFloor).
		Msg("answer abstained: insufficient context")
	return domain.Result{Answer: abstainAnswer, Sources: sources, Trace: tr}, true
}

// retrievalDepth computes how many candidates to over-fetch per retriever:
// max(minDepth, effectiveTopK*mult), where the effective top-K prefers the
// request's TopK when set and falls back to the configured default.
func retrievalDepth(requestTopK, defaultTopK, mult, minDepth int) int {
	k := requestTopK
	if k < 1 {
		k = defaultTopK
	}
	depth := k * mult
	if depth < minDepth {
		depth = minDepth
	}
	return depth
}

// capPerDocument keeps at most maxPerDoc chunks from any single document in the
// returned top-k, preferring score order. If the cap under-fills k (too few
// distinct documents) it backfills from the capped-out chunks, so the count
// never drops below a plain top-k. maxPerDoc <= 0 disables the cap. Doc-level
// recall is unaffected (the top-scoring document is always kept); only the
// within-document chunk mix changes, trading near-duplicate passages for breadth.
func capPerDocument(ranked []domain.RetrievedChunk, k, maxPerDoc int) []domain.RetrievedChunk {
	if k > len(ranked) {
		k = len(ranked)
	}
	if maxPerDoc <= 0 {
		return ranked[:k]
	}
	perDoc := make(map[string]int, k)
	chosen := make([]domain.RetrievedChunk, 0, k)
	deferred := make([]domain.RetrievedChunk, 0, len(ranked))
	for _, c := range ranked {
		if len(chosen) >= k {
			break
		}
		if c.DocumentID != "" && perDoc[c.DocumentID] >= maxPerDoc {
			deferred = append(deferred, c)
			continue
		}
		perDoc[c.DocumentID]++
		chosen = append(chosen, c)
	}
	for _, c := range deferred {
		if len(chosen) >= k {
			break
		}
		chosen = append(chosen, c)
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].Score > chosen[j].Score })
	return chosen
}

// weightedList pairs a ranked candidate list with its RRF weight.
type weightedList struct {
	weight float64
	chunks []domain.RetrievedChunk
}

// fuse merges ranked candidate lists by weighted Reciprocal Rank Fusion keyed by
// chunk id: each chunk accumulates weight/(rrfK + rank) from every list it
// appears in (rank is 0-based within that list). The result is sorted by fused
// score descending. The first occurrence of a chunk wins for its provenance
// fields (vector hits are listed first and carry payload metadata).
func fuse(lists []weightedList) []domain.RetrievedChunk {
	type acc struct {
		chunk domain.RetrievedChunk
		score float64
	}
	merged := make(map[string]*acc)
	order := make([]string, 0)

	for _, list := range lists {
		for rank, chunk := range list.chunks {
			contribution := list.weight / float64(rrfK+rank)
			if a, ok := merged[chunk.ID]; ok {
				a.score += contribution
				mergeProvenance(&a.chunk, chunk)
				continue
			}
			merged[chunk.ID] = &acc{chunk: chunk, score: contribution}
			order = append(order, chunk.ID)
		}
	}

	out := make([]domain.RetrievedChunk, 0, len(order))
	for _, id := range order {
		a := merged[id]
		a.chunk.Score = a.score
		out = append(out, a.chunk)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// mergeProvenance fills empty provenance fields on dst from src so a chunk that
// appears in both retrievers ends up with the richest metadata available. A
// chunk surfaced by both legs gets OriginBoth.
func mergeProvenance(dst *domain.RetrievedChunk, src domain.RetrievedChunk) {
	switch {
	case dst.Origin == "":
		dst.Origin = src.Origin
	case src.Origin != "" && src.Origin != dst.Origin:
		dst.Origin = domain.OriginBoth
	}
	if dst.Text == "" {
		dst.Text = src.Text
	}
	if dst.DocumentID == "" {
		dst.DocumentID = src.DocumentID
	}
	if dst.Filename == "" {
		dst.Filename = src.Filename
	}
	if dst.ChunkIndex == 0 {
		dst.ChunkIndex = src.ChunkIndex
	}
	// Span provenance lives only on the vector (Qdrant) payload; backfill it when
	// the winning copy lacks it (e.g. a lexical-first chunk). A zero end offset
	// means "unset".
	if dst.CharEnd == 0 && src.CharEnd != 0 {
		dst.CharStart, dst.CharEnd = src.CharStart, src.CharEnd
	}
	if dst.PageEnd == 0 && src.PageEnd != 0 {
		dst.PageStart, dst.PageEnd = src.PageStart, src.PageEnd
	}
	if dst.SectionHeading == "" {
		dst.SectionHeading = src.SectionHeading
	}
}

// topRerankScore returns the maximum reranker score among the retained chunks
// and whether any non-empty chunk was present. ok is false for an empty list
// (so the caller abstains). After rerank the slice is sorted descending, but we
// scan defensively rather than assume index 0.
func topRerankScore(chunks []domain.RetrievedChunk) (float64, bool) {
	top := 0.0
	ok := false
	for _, c := range chunks {
		if !ok || c.Score > top {
			top = c.Score
			ok = true
		}
	}
	return top, ok
}

// expandSummaries replaces each retrieved RAPTOR summary with its real leaf
// chunks (small-to-big), so the answer is grounded on — and cites — actual
// passages instead of a synthetic summary. A summary's denormalised Members carry
// full citation provenance, so no extra fetch is needed. Order is preserved and
// every chunk id is emitted at most once (a leaf that was also retrieved on its
// own, or shared between two summaries, is not duplicated). Each expanded leaf
// inherits its summary's rerank score and origin, and carries the summary's id
// (RaptorSummaryID) so the citation traces back to the node that surfaced it.
// Plain chunks pass through unchanged; a list with no summaries (the common
// case) is returned as-is. The int reports how many summaries were expanded.
func expandSummaries(ranked []domain.RetrievedChunk) ([]domain.RetrievedChunk, int) {
	hasSummary := false
	for i := range ranked {
		if ranked[i].NodeType == nodeTypeRaptorSummary && len(ranked[i].Members) > 0 {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		return ranked, 0
	}
	d := &chunkDedup{
		out:  make([]domain.RetrievedChunk, 0, len(ranked)),
		seen: make(map[string]bool, len(ranked)),
	}
	expanded := 0
	for _, c := range ranked {
		if c.NodeType == nodeTypeRaptorSummary && len(c.Members) > 0 {
			expanded++
			d.expandInto(c)
			continue
		}
		d.add(c)
	}
	return d.out, expanded
}

// chunkDedup accumulates expanded chunks, emitting each chunk id at most once.
type chunkDedup struct {
	out  []domain.RetrievedChunk
	seen map[string]bool
}

// add appends c unless its id was already emitted, reporting whether it did.
func (d *chunkDedup) add(c domain.RetrievedChunk) bool {
	if c.ID != "" {
		if d.seen[c.ID] {
			return false
		}
		d.seen[c.ID] = true
	}
	d.out = append(d.out, c)
	return true
}

// expandInto emits up to maxSummaryExpansion of the summary's member leaves,
// each stamped with the summary's rerank score, origin and point id.
func (d *chunkDedup) expandInto(sum domain.RetrievedChunk) {
	added := 0
	for _, m := range sum.Members {
		if added >= maxSummaryExpansion {
			return
		}
		if sum.ID != "" && m.ID == sum.ID {
			continue // defensive: never re-emit the summary as its own leaf
		}
		m.Score = sum.Score // the leaf inherits the summary's rerank relevance
		m.Origin = sum.Origin
		m.RaptorSummaryID = sum.ID
		if d.add(m) {
			added++
		}
	}
}

// buildContext joins the chunk texts into the single context string handed to
// the Answerer. Each passage is labelled [S1], [S2], … (1-based, matching the
// order of the Sources returned) so the model — and any downstream display —
// can attribute claims to a specific citation.
func buildContext(chunks []domain.RetrievedChunk) string {
	parts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if t := strings.TrimSpace(c.Text); t != "" {
			parts = append(parts, fmt.Sprintf("[S%d] %s", len(parts)+1, t))
		}
	}
	return strings.Join(parts, "\n\n")
}

// buildSources turns the kept chunks into the citation list returned to the
// caller, truncating each snippet to roughly snippetLen characters. Empty-text
// chunks are skipped so the citation list stays 1:1 with the labelled [Sn]
// passages buildContext emits (same chunks, same order).
func buildSources(chunks []domain.RetrievedChunk) []domain.Source {
	sources := make([]domain.Source, 0, len(chunks))
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		sources = append(sources, domain.Source{
			DocumentID:      c.DocumentID,
			Filename:        c.Filename,
			ChunkID:         c.ID,
			Snippet:         snippet(c.Text),
			Score:           c.Score,
			CharStart:       c.CharStart,
			CharEnd:         c.CharEnd,
			PageStart:       c.PageStart,
			PageEnd:         c.PageEnd,
			SectionHeading:  c.SectionHeading,
			Origin:          c.Origin,
			RaptorSummaryID: c.RaptorSummaryID,
		})
	}
	return sources
}

// snippet returns the first ~snippetLen characters of text, counting runes so a
// multibyte character is never split, with an ellipsis when truncated.
func snippet(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= snippetLen {
		return text
	}
	return strings.TrimSpace(string(runes[:snippetLen])) + "…"
}

// extractiveFallbackAnswer is the live-demo safety net for chat Q&A: retrieval
// and reranking already found grounded evidence, but the external generator is
// unavailable. Return a transparent, source-cited extract instead of a 500. The
// full source list is still returned in Result.Sources; the text shows only the
// top few snippets to keep the answer scannable.
func extractiveFallbackAnswer(sources []domain.Source) string {
	var b strings.Builder
	b.WriteString("Генерация ответа сейчас недоступна, поэтому показываю наиболее релевантные фрагменты из базы знаний.\n")
	limit := len(sources)
	if limit > extractiveFallbackMaxSources {
		limit = extractiveFallbackMaxSources
	}
	for i := range limit {
		src := sources[i]
		title := strings.TrimSpace(src.Filename)
		if title == "" {
			title = strings.TrimSpace(src.DocumentID)
		}
		if title == "" {
			title = "источник"
		}
		snip := strings.TrimSpace(src.Snippet)
		if snip == "" {
			continue
		}
		fmt.Fprintf(&b, "\n[S%d] %s: %s", i+1, title, snip)
	}
	return strings.TrimSpace(b.String())
}

// citationRef matches an inline source citation as emitted by the citation
// instruction and the labelled context, e.g. [S1], [S2].
var citationRef = regexp.MustCompile(`\[S(\d+)\]`)

// Cleanup patterns for stripped citations: collapse runs of spaces/tabs (newlines
// are preserved so paragraph breaks survive) and remove a space left in front of
// punctuation.
var (
	multiSpace       = regexp.MustCompile(`[ \t]{2,}`)
	spaceBeforePunct = regexp.MustCompile(`[ \t]+([.,;:!?…])`)
)

// citationReport summarises how a generated answer's inline [Sn] citations line
// up with the sources actually handed to the model. Cited holds the distinct
// in-range indices (1-based); OutOfRange holds cited indices with no matching
// source (hallucinated references).
type citationReport struct {
	Cited      []int
	OutOfRange []int
}

// validateCitations scans answer for [Sn] references and classifies each distinct
// index as in-range (1..nSources) or out-of-range. It is pure and order-stable.
func validateCitations(answer string, nSources int) citationReport {
	seen := make(map[int]bool)
	var rep citationReport
	for _, m := range citationRef.FindAllStringSubmatch(answer, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || seen[n] {
			continue
		}
		seen[n] = true
		if n > nSources {
			rep.OutOfRange = append(rep.OutOfRange, n)
		} else {
			rep.Cited = append(rep.Cited, n)
		}
	}
	return rep
}

// sanitizeCitations validates the answer's citations and removes any that point
// past the source list (always wrong, and confusing to the reader), collapsing
// the whitespace they leave behind. In-range citations are untouched. It returns
// the cleaned answer and the (pre-strip) citation report.
func sanitizeCitations(answer string, nSources int) (string, citationReport) {
	rep := validateCitations(answer, nSources)
	if len(rep.OutOfRange) == 0 {
		return answer, rep
	}
	cleaned := citationRef.ReplaceAllStringFunc(answer, func(tok string) string {
		m := citationRef.FindStringSubmatch(tok)
		if n, _ := strconv.Atoi(m[1]); n > nSources {
			return ""
		}
		return tok
	})
	// Tidy the gaps the removed tokens leave, keeping newlines intact.
	cleaned = multiSpace.ReplaceAllString(cleaned, " ")
	cleaned = spaceBeforePunct.ReplaceAllString(cleaned, "$1")
	return strings.TrimSpace(cleaned), rep
}

// cacheKey is the Valkey key for a finished answer:
// answer:<scope>:e<epoch>:<sha256(query\x00prompt)>. scope is already normalised
// ("shared" or owner id); epoch is the corpus version, so reindexing changes the
// key and retires stale answers.
func cacheKey(scope string, epoch int64, query, prompt, docScope string) string {
	sum := sha256.Sum256([]byte(query + "\x00" + prompt + "\x00" + docScope))
	return "answer:" + scope + ":e" + strconv.FormatInt(epoch, 10) + ":" + hex.EncodeToString(sum[:])
}

// docScopeKey folds a query's document allow/deny lists into a stable string
// so scoped and unscoped answers never share a cache entry.
func docScopeKey(q domain.Query) string {
	if len(q.ScopeDocumentIDs) == 0 && len(q.ExcludeDocumentIDs) == 0 {
		return ""
	}
	scope := append([]string(nil), q.ScopeDocumentIDs...)
	excl := append([]string(nil), q.ExcludeDocumentIDs...)
	sort.Strings(scope)
	sort.Strings(excl)
	return "s:" + strings.Join(scope, ",") + "|x:" + strings.Join(excl, ",")
}
