package application

// Verification tests for the audit hypotheses P0.1 (TopK propagation),
// P0.2 (retrieval fail-open vs. abstain) and P0.3 (labelled context / citation
// flag). White-box (package application) so the fixed abstain reply and the
// pipeline internals are directly observable. No network: every port is a fake.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/example/llm-service/internal/domain"
)

// ---- fakes for the domain ports -------------------------------------------

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

// fakeRetriever returns a fixed candidate set, honouring the topN bound like a
// real backend. A non-nil err is swallowed by the application's retrieve()
// wrapper (the behaviour under test in the fail-open cases).
type fakeRetriever struct {
	chunks []domain.RetrievedChunk
	err    error
}

func (f fakeRetriever) Retrieve(
	_ context.Context, _ string, _ []float32, _ domain.RetrievalFilter, topN int,
) ([]domain.RetrievedChunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	if topN >= 0 && len(f.chunks) > topN {
		return f.chunks[:topN], nil
	}
	return f.chunks, nil
}

// fakeRanker assigns every candidate the same score, so the top reranker score
// (what the abstain floor checks) is exactly `score`.
type fakeRanker struct {
	score float64
	err   error
}

func (f fakeRanker) Rank(_ context.Context, _ string, chunks []domain.RetrievedChunk) ([]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]float64, len(chunks))
	for i := range out {
		out[i] = f.score
	}
	return out, nil
}

// fakeAnswerer records whether generation was invoked and with what context.
// reply overrides the returned answer text (default "GENERATED-ANSWER").
type fakeAnswerer struct {
	called     bool
	gotContext string
	gotCite    bool
	reply      string
	err        error
}

func (f *fakeAnswerer) Answer(_ context.Context, contextText, _ string, cite bool) (string, string, error) {
	f.called = true
	f.gotContext = contextText
	f.gotCite = cite
	if f.err != nil {
		return "", "", f.err
	}
	r := f.reply
	if r == "" {
		r = "GENERATED-ANSWER"
	}
	return r, "fake-model", nil
}

type fakeCache struct{}

func (fakeCache) Get(context.Context, string) (domain.Result, bool, error) {
	return domain.Result{}, false, nil // always a miss → pipeline runs every call
}
func (fakeCache) Set(context.Context, string, domain.Result) error { return nil }
func (fakeCache) Epoch(context.Context, string) int64              { return 0 }

// recordingCache counts Set calls so tests can assert what was (not) cached.
type recordingCache struct{ sets int }

func (c *recordingCache) Get(context.Context, string) (domain.Result, bool, error) {
	return domain.Result{}, false, nil
}
func (c *recordingCache) Set(context.Context, string, domain.Result) error {
	c.sets++
	return nil
}
func (*recordingCache) Epoch(context.Context, string) int64 { return 0 }

func makeChunks(n int) []domain.RetrievedChunk {
	const prefix = "v"
	out := make([]domain.RetrievedChunk, n)
	for i := range out {
		out[i] = domain.RetrievedChunk{
			ID:         fmt.Sprintf("%s-%d", prefix, i),
			Text:       fmt.Sprintf("passage %s %d about turbine blade efficiency", prefix, i),
			DocumentID: fmt.Sprintf("doc-%d", i),
			Filename:   "report.pdf",
			ChunkIndex: i,
		}
	}
	return out
}

// buildC wires a RAGService with an explicit cache (sharedCorpus off; chunks/
// load/metrics unused by Answer).
func buildC(
	cache domain.Cache, vec, lex domain.Retriever, ranker domain.Ranker, ans domain.Answerer,
	rerankerActive bool,
) *RAGService {
	return New(fakeEmbedder{}, vec, lex, ranker, ans, cache, nil, nil, nil, 5, false, -5, rerankerActive, Tuning{})
}

// build is buildC with a no-op cache.
func build(
	vec, lex domain.Retriever, ranker domain.Ranker, ans domain.Answerer,
	rerankerActive bool,
) *RAGService {
	return buildC(fakeCache{}, vec, lex, ranker, ans, rerankerActive)
}

// ---- P0.1: request TopK reaches the final source list ----------------------

func TestP0_1_RequestTopKReachesSources(t *testing.T) {
	ctx := context.Background()
	// 30 vector candidates, no lexical; constant rerank score; stub reranker so
	// the abstain floor never interferes with the count.
	mk := func() *fakeAnswerer {
		return &fakeAnswerer{}
	}

	t.Run("default TopK falls back to RAG_TOP_K=5", func(t *testing.T) {
		svc := build(fakeRetriever{chunks: makeChunks(30)}, fakeRetriever{}, fakeRanker{score: 1}, mk(), false)
		res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "turbine efficiency"})
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
		if len(res.Sources) != 5 {
			t.Fatalf("default: want 5 sources, got %d", len(res.Sources))
		}
	})

	t.Run("request TopK=12 caps the final source list at 12", func(t *testing.T) {
		svc := build(fakeRetriever{chunks: makeChunks(30)}, fakeRetriever{}, fakeRanker{score: 1}, mk(), false)
		res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "turbine efficiency", TopK: 12})
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
		if len(res.Sources) != 12 {
			t.Fatalf("P0.1 would be CONFIRMED (bug) — want 12 sources, got %d", len(res.Sources))
		}
		t.Logf("P0.1 REFUTED: request TopK=12 propagated to %d sources", len(res.Sources))
	})
}

// ---- P0.2: retrieval fail-open is gated on rerankerActive ------------------

// THE FIX. Both backends error out (Qdrant + OpenSearch unavailable). Even with
// the stub reranker (RERANKER_URL unset — the compose default), the pipeline now
// ABSTAINS instead of generating a confident, source-less answer. This is the
// P0.2 fail-open fix: empty context never reaches the generator.
func TestP0_2_AbstainOnEmptyEvenWithStubReranker(t *testing.T) {
	ctx := context.Background()
	ans := &fakeAnswerer{}
	svc := build(
		fakeRetriever{err: errors.New("qdrant unavailable")},
		fakeRetriever{err: errors.New("opensearch unavailable")},
		fakeRanker{score: 1}, ans, false, // rerankerActive=false (stub)
	)
	res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if ans.called {
		t.Fatal("P0.2 fail-open NOT fixed: generator was called on empty context")
	}
	if res.Answer != abstainAnswer {
		t.Fatalf("expected abstain reply on empty context, got %q", res.Answer)
	}
	t.Logf("P0.2 FIXED: empty/dead retrieval abstains even with the stub reranker")
}

// A degraded retrieval (a backend errored) is potentially transient, so its
// abstain must NOT be cached — otherwise the outage outlives itself. A healthy
// retrieval that simply found nothing IS cached (stable verdict).
func TestP0_2_DegradedAbstainNotCached(t *testing.T) {
	ctx := context.Background()

	degraded := &recordingCache{}
	svc := buildC(degraded,
		fakeRetriever{err: errors.New("down")}, fakeRetriever{err: errors.New("down")},
		fakeRanker{score: 1}, &fakeAnswerer{}, true)
	if _, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if degraded.sets != 0 {
		t.Fatalf("degraded abstain must not be cached, got %d Set calls", degraded.sets)
	}

	healthy := &recordingCache{}
	svc2 := buildC(healthy,
		fakeRetriever{}, fakeRetriever{}, // empty but no error
		fakeRanker{score: 1}, &fakeAnswerer{}, true)
	if _, err := svc2.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if healthy.sets != 1 {
		t.Fatalf("healthy-empty abstain should be cached once, got %d", healthy.sets)
	}
}

// With a real reranker wired (RERANKER_URL set), the same dead-retrieval case
// abstains instead of generating — the fail-open is closed. This shows the
// mechanism EXISTS but is conditional on rerankerActive.
func TestP0_2_AbstainWithRealRerankerOnEmpty(t *testing.T) {
	ctx := context.Background()
	ans := &fakeAnswerer{}
	svc := build(
		fakeRetriever{err: errors.New("qdrant unavailable")},
		fakeRetriever{err: errors.New("opensearch unavailable")},
		fakeRanker{score: 1}, ans, true, // rerankerActive=true
	)
	res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if ans.called {
		t.Fatal("expected NO generation (abstain), but generator was called")
	}
	if res.Answer != abstainAnswer {
		t.Fatalf("expected abstain reply, got %q", res.Answer)
	}
	t.Logf("P0.2 closed only here: real reranker + empty retrieval → abstain, no generation")
}

// With a real reranker but the top score below RAG_SCORE_FLOOR, the pipeline
// abstains rather than answering from weak context.
func TestP0_2_AbstainOnLowScore(t *testing.T) {
	ctx := context.Background()
	ans := &fakeAnswerer{}
	svc := build(
		fakeRetriever{chunks: makeChunks(10)}, fakeRetriever{},
		fakeRanker{score: -7}, ans, true, // top score -7 < floor -5
	)
	res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if ans.called {
		t.Fatal("expected abstain on low score, but generator was called")
	}
	if res.Answer != abstainAnswer {
		t.Fatalf("expected abstain reply, got %q", res.Answer)
	}
}

// ---- Degradations: reranker/embedder outages must not fail the query --------

// errEmbedder fails, driving the embed-degradation (lexical-only) branch.
type errEmbedder struct{}

func (errEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embedder down")
}

// A reranker outage keeps the RRF fusion order: the answer is still generated,
// the trace reports reranker_unavailable, the score floor is NOT applied to
// fusion scores (floor 0.5 would otherwise abstain on RRF's ~1/60) and the
// degraded result is not cached.
func TestRerankerFailureDegradesToFusionOrder(t *testing.T) {
	cache := &recordingCache{}
	ans := &fakeAnswerer{}
	svc := New(fakeEmbedder{}, fakeRetriever{chunks: makeChunks(3)}, fakeRetriever{},
		fakeRanker{err: errors.New("reranker down")}, ans, cache, nil, nil, nil,
		5, false, 0.5, true, Tuning{})
	res, err := svc.Answer(context.Background(), domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer must degrade, not fail: %v", err)
	}
	if !ans.called || res.Answer != "GENERATED-ANSWER" {
		t.Fatalf("want generation over the fusion order, called=%v answer=%q", ans.called, res.Answer)
	}
	tr := res.Trace
	if tr == nil || !tr.Degraded || tr.DegradedReason != domain.DegradedRerankerUnavailable {
		t.Fatalf("trace = %+v, want degraded reason %q", tr, domain.DegradedRerankerUnavailable)
	}
	if tr.AbstainReason != "" {
		t.Fatalf("floor must not apply to fusion scores, got abstain %q", tr.AbstainReason)
	}
	if cache.sets != 0 {
		t.Fatalf("degraded answer must not be cached, got %d Set calls", cache.sets)
	}
}

// An embedder outage degrades to lexical-only retrieval (empty dense leg): the
// answer is generated from the BM25 hits, marked embed_unavailable, not cached.
func TestEmbedFailureContinuesLexicalOnly(t *testing.T) {
	cache := &recordingCache{}
	ans := &fakeAnswerer{}
	svc := New(errEmbedder{}, fakeRetriever{chunks: makeChunks(2)}, fakeRetriever{chunks: makeChunks(3)},
		fakeRanker{score: 1}, ans, cache, nil, nil, nil, 5, false, -5, false, Tuning{})
	res, err := svc.Answer(context.Background(), domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer must degrade, not fail: %v", err)
	}
	if !ans.called || len(res.Sources) != 3 {
		t.Fatalf("want lexical-only generation with 3 sources, called=%v sources=%d", ans.called, len(res.Sources))
	}
	for _, src := range res.Sources {
		if src.Origin != domain.OriginBM25 {
			t.Fatalf("lexical-only source origin = %q, want %q", src.Origin, domain.OriginBM25)
		}
	}
	tr := res.Trace
	if tr == nil || tr.DegradedReason != domain.DegradedEmbedUnavailable || tr.CandidatesDense != 0 {
		t.Fatalf("trace = %+v, want embed_unavailable with no dense candidates", tr)
	}
	if cache.sets != 0 {
		t.Fatalf("degraded answer must not be cached, got %d Set calls", cache.sets)
	}
}

// With the embedder AND the lexical backend both down there is nothing left to
// retrieve: the pipeline abstains (no hard error) and does not cache.
func TestEmbedAndLexicalFailureAbstains(t *testing.T) {
	cache := &recordingCache{}
	ans := &fakeAnswerer{}
	svc := New(errEmbedder{}, fakeRetriever{chunks: makeChunks(2)}, fakeRetriever{err: errors.New("opensearch down")},
		fakeRanker{score: 1}, ans, cache, nil, nil, nil, 5, false, -5, false, Tuning{})
	res, err := svc.Answer(context.Background(), domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if ans.called || res.Answer != abstainAnswer {
		t.Fatalf("want abstain without generation, called=%v answer=%q", ans.called, res.Answer)
	}
	tr := res.Trace
	if tr == nil || tr.AbstainReason != domain.AbstainNoSources ||
		tr.DegradedReason != domain.DegradedEmbedUnavailable {
		t.Fatalf("trace = %+v, want no_sources abstain under embed_unavailable", tr)
	}
	if cache.sets != 0 {
		t.Fatalf("degraded abstain must not be cached, got %d Set calls", cache.sets)
	}
}

// ---- Response trace ----------------------------------------------------------

// TestAnswerTrace pins the happy-path trace: stage timings, funnel counts,
// origin fusion, the floor/top-score pair and the citation report.
func TestAnswerTrace(t *testing.T) {
	ans := &fakeAnswerer{reply: "Fact [S1]. Bogus [S9]."}
	// Vector returns v-0..v-2, lexical v-0..v-1 (shared ids) → 3 fused, with
	// v-0/v-1 surfaced by both legs and v-2 by dense only.
	svc := build(fakeRetriever{chunks: makeChunks(3)}, fakeRetriever{chunks: makeChunks(2)},
		fakeRanker{score: 2}, ans, true)
	res, err := svc.Answer(context.Background(), domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	tr := res.Trace
	if tr == nil {
		t.Fatal("want a trace on the response")
	}
	if tr.CandidatesDense != 3 || tr.CandidatesLexical != 2 || tr.CandidatesFused != 3 || tr.CandidatesReturned != 3 {
		t.Fatalf("funnel counts = %+v", tr)
	}
	seen := make(map[string]bool, len(tr.Stages))
	for _, st := range tr.Stages {
		seen[st.Stage] = true
	}
	for _, want := range []string{"embed", "retrieve", "rerank", "generate", "total"} {
		if !seen[want] {
			t.Fatalf("missing %q stage in %+v", want, tr.Stages)
		}
	}
	if tr.TopScore != 2 || tr.ScoreFloor != -5 {
		t.Fatalf("top_score/score_floor = %v/%v, want 2/-5", tr.TopScore, tr.ScoreFloor)
	}
	if len(tr.Cited) != 1 || tr.Cited[0] != "[S1]" ||
		len(tr.UncitedRemoved) != 1 || tr.UncitedRemoved[0] != "[S9]" {
		t.Fatalf("citation report = %v / %v, want [S1] / [S9]", tr.Cited, tr.UncitedRemoved)
	}
	if tr.Degraded || tr.AbstainReason != "" {
		t.Fatalf("healthy run must not be degraded/abstained: %+v", tr)
	}
	origins := make(map[string]string, len(res.Sources))
	for _, src := range res.Sources {
		origins[src.ChunkID] = src.Origin
	}
	if origins["v-0"] != domain.OriginBoth || origins["v-2"] != domain.OriginDense {
		t.Fatalf("origins = %+v, want v-0 both / v-2 dense", origins)
	}
}

// Control: real reranker, top score above the floor → normal generation.
func TestP0_2_GenerateOnGoodScore(t *testing.T) {
	ctx := context.Background()
	ans := &fakeAnswerer{}
	svc := build(
		fakeRetriever{chunks: makeChunks(10)}, fakeRetriever{},
		fakeRanker{score: 3}, ans, true,
	)
	res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !ans.called || res.Answer != "GENERATED-ANSWER" {
		t.Fatalf("expected generation, called=%v answer=%q", ans.called, res.Answer)
	}
	if len(res.Sources) != 5 {
		t.Fatalf("want 5 sources, got %d", len(res.Sources))
	}
}

// ---- P0.3: labelled context exists; citation flag is path-dependent --------

func TestP0_3_LabelledContextAndCiteFlag(t *testing.T) {
	ctx := context.Background()

	t.Run("plain Q&A: [Sn] labels present and cite instruction requested", func(t *testing.T) {
		ans := &fakeAnswerer{}
		svc := build(fakeRetriever{chunks: makeChunks(3)}, fakeRetriever{}, fakeRanker{score: 1}, ans, false)
		if _, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"}); err != nil {
			t.Fatalf("Answer: %v", err)
		}
		for _, label := range []string{"[S1] ", "[S2] ", "[S3] "} {
			if !strings.Contains(ans.gotContext, label) {
				t.Fatalf("P0.3 'no labelled context' would be CONFIRMED — missing %q in:\n%s", label, ans.gotContext)
			}
		}
		if !ans.gotCite {
			t.Fatal("expected cite=true on the plain Q&A path")
		}
		t.Logf("P0.3 PARTIALLY REFUTED: labelled context [S1..S3] built and cite=true requested on chat path")
	})

	t.Run("structured-prompt path: cite suppressed", func(t *testing.T) {
		ans := &fakeAnswerer{}
		svc := build(fakeRetriever{chunks: makeChunks(3)}, fakeRetriever{}, fakeRanker{score: 1}, ans, false)
		if _, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q", Prompt: "return JSON"}); err != nil {
			t.Fatalf("Answer: %v", err)
		}
		if ans.gotCite {
			t.Fatal("expected cite=false when a custom Prompt drives generation")
		}
	})
}

// ---- P0.3 (cont.): citation validation against the retrieved sources ---------

func TestP0_3_ValidateCitations(t *testing.T) {
	// 3 sources; answer cites S1, S2 (valid, deduped) and S9 (hallucinated).
	rep := validateCitations("Pressure is 16 bar [S1][S2]. See also [S9] and again [S1].", 3)
	if len(rep.Cited) != 2 {
		t.Fatalf("want 2 in-range citations, got %v", rep.Cited)
	}
	if len(rep.OutOfRange) != 1 || rep.OutOfRange[0] != 9 {
		t.Fatalf("want out-of-range [9], got %v", rep.OutOfRange)
	}
}

func TestP0_3_SanitizeStripsHallucinatedCitations(t *testing.T) {
	out, rep := sanitizeCitations("The pressure is 16 bar [S1]. Also relevant [S9].", 2)
	if strings.Contains(out, "[S9]") {
		t.Fatalf("hallucinated [S9] not stripped: %q", out)
	}
	if !strings.Contains(out, "[S1]") {
		t.Fatalf("valid [S1] was dropped: %q", out)
	}
	if len(rep.OutOfRange) != 1 {
		t.Fatalf("want 1 out-of-range, got %v", rep.OutOfRange)
	}
	// No dangling space before the period left by the removed token.
	if strings.Contains(out, " .") {
		t.Fatalf("leftover space before punctuation: %q", out)
	}
	t.Logf("P0.3 citation validation: sanitized %q", out)
}

// End-to-end: a generated answer that cites a non-existent source has that
// citation stripped before it reaches the caller (chat path only).
func TestP0_3_AnswerStripsOutOfRangeCitations(t *testing.T) {
	ctx := context.Background()
	ans := &fakeAnswerer{reply: "Fact A [S1]. Fact B [S2]. Hallucinated [S9]."}
	svc := build(fakeRetriever{chunks: makeChunks(3)}, fakeRetriever{}, fakeRanker{score: 1}, ans, false)
	res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Contains(res.Answer, "[S9]") {
		t.Fatalf("answer still contains hallucinated [S9]: %q", res.Answer)
	}
	if !strings.Contains(res.Answer, "[S1]") || !strings.Contains(res.Answer, "[S2]") {
		t.Fatalf("answer dropped valid citations: %q", res.Answer)
	}
}

// If retrieval succeeded but the external generator is temporarily unavailable,
// chat Q&A returns a transparent extractive answer with the same grounded
// sources instead of failing the request. The fallback is not cached, so a
// recovered generator immediately restores synthesized answers.
func TestGenerationFailureReturnsExtractiveFallbackForChat(t *testing.T) {
	ctx := context.Background()
	cache := &recordingCache{}
	ans := &fakeAnswerer{err: errors.New("upstream rate limited")}
	svc := buildC(cache, fakeRetriever{chunks: makeChunks(4)}, fakeRetriever{}, fakeRanker{score: 1}, ans, false)

	res, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q"})
	if err != nil {
		t.Fatalf("Answer should degrade to extractive fallback, got error: %v", err)
	}
	if res.Model != extractiveFallbackModel {
		t.Fatalf("model = %q, want %q", res.Model, extractiveFallbackModel)
	}
	if !strings.Contains(res.Answer, "Генерация ответа сейчас недоступна") ||
		!strings.Contains(res.Answer, "[S1]") ||
		!strings.Contains(res.Answer, "passage v 0") {
		t.Fatalf("fallback answer is not source-cited extract: %q", res.Answer)
	}
	if len(res.Sources) != 4 {
		t.Fatalf("fallback must return the retrieved sources, got %d", len(res.Sources))
	}
	if cache.sets != 0 {
		t.Fatalf("extractive fallback must not be cached, got %d Set calls", cache.sets)
	}
}

// Structured-prompt callers expect machine-readable JSON. They must see the
// generation error instead of receiving a prose fallback.
func TestGenerationFailurePropagatesForStructuredPrompt(t *testing.T) {
	ctx := context.Background()
	ans := &fakeAnswerer{err: errors.New("upstream rate limited")}
	svc := build(fakeRetriever{chunks: makeChunks(2)}, fakeRetriever{}, fakeRanker{score: 1}, ans, false)

	if _, err := svc.Answer(ctx, domain.Query{OwnerID: "u1", Text: "q", Prompt: "return JSON"}); err == nil {
		t.Fatal("structured prompt generation error must propagate")
	}
}
