// Agentic multi-hop retrieve-reason controller (Search-o1 style) — the optional
// final layer of the RAG trajectory. It is additive and flag-gated: it runs only
// when RAG_AGENTIC is on AND a cheap complexity gate fires; otherwise Answer
// falls through to the unchanged single-shot pipeline in service.go.
//
// The loop, per hop, (a) retrieves for a sub-query via the EXISTING
// retrieveAndRank, (b) condenses the hits to the query-relevant facts with an
// LLM "reason-in-documents" step (noise filter), (c) lets a reasoner LLM decide
// the next focused search or stop. It then generates one grounded answer over
// the accumulated evidence, reusing buildContext/buildSources/citation
// sanitisation so the inline [Sn] labels stay 1:1 with the returned sources —
// exactly like the single-shot path. An optional graph-compute tool
// (RAG_AGENTIC_GRAPH) folds Personalized-PageRank neighbours into the candidates.

package application

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/rs/zerolog"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/logger"
)

// Gate heuristic knobs. The gate is a pure function of the query text (no LLM,
// no retrieval) so escalation is essentially free; it fires only on multi-hop
// structure. Escalation is opt-in (RAG_AGENTIC) and the loop still grounds and
// cites, so over-firing only costs latency while under-firing is safe (falls
// back to single-shot) — hence a deliberately conservative threshold.
const (
	gateThreshold         = 2  // total signal weight required to escalate
	gateComparativeWeight = 2  // a comparative/relational phrasing alone escalates
	gateMinQuestions      = 2  // distinct '?' marks → several sub-questions
	gateMinClauses        = 2  // clause-joining markers → enumerated/chained ask
	gateMinEntities       = 2  // distinct salient entities → likely cross-document join
	gateLongQueryTokens   = 18 // a long question is weakly complex
)

// Loop bounds. condenseChunkClip caps each passage handed to the condenser (kept
// small so the reasoning loop stays cheap); agenticMaxEvidence bounds the
// accumulated citation pool so multi-hop answers cannot explode the source list;
// graphChunksPerDoc caps how many chunks a single graph-expanded document
// contributes to the candidate set.
const (
	condenseChunkClip  = 600
	agenticMaxEvidence = 12
	graphChunksPerDoc  = 3
	condenseNone       = "NONE"
)

// reasonSystemPrompt steers the reasoner to act as a controller — it decides the
// next search or stops, but never answers itself (the grounded generator owns the
// final, cited answer).
const reasonSystemPrompt = "You are a meticulous research controller working a question step by step. " +
	"You are given the question and the facts gathered so far from a document corpus. " +
	"Decide whether those facts already answer the question. " +
	"Reply with EXACTLY ONE of: the token [answer] on its own line if the facts suffice, " +
	"or a single line '[search] <query>' requesting ONE focused follow-up search for a specific missing fact. " +
	"Do not answer the question yourself and do not add commentary."

// condenseSystemPrompt drives the reason-in-documents step: distill retrieved
// passages to the question-relevant facts and drop the rest.
const condenseSystemPrompt = "You distill retrieved passages into only the facts relevant to a question " +
	"(a reason-in-documents step). Read the passages and extract ONLY facts that help answer the question, " +
	"as a few short sentences. Omit anything irrelevant. If no passage is relevant, reply with exactly NONE."

// searchActionRe captures the query on a '[search] …' action line emitted by the
// reasoner (case-insensitive, multi-line). The absence of such a line is treated
// as "stop", which bounds the loop even on a malformed reply.
var searchActionRe = regexp.MustCompile(`(?im)^\s*\[search]\s*(.+?)\s*$`)

// Entity/clause detectors for the gate (package-level compiled regexps, the same
// pattern as service.go's citation matchers). Cyrillic ranges keep the heuristic
// working for the platform's Russian-and-English audience.
var (
	comparativeRe = regexp.MustCompile(`(?i)(compare|comparison|compared to|versus| vs\.?\b|` +
		`difference|differ|trade-?off|pros and cons|relationship between|` +
		`сравн|различ|разниц|отлич|взаимосвяз|соотнош|по сравнению)`)
	clauseRe = regexp.MustCompile(`(?i)(\band\b|\band then\b|\bas well as\b|\bor\b` +
		`| и | или | а также | затем | потом )`)
	quotedRe   = regexp.MustCompile(`"[^"]{1,80}"|«[^»]{1,80}»`)
	capMultiRe = regexp.MustCompile(`[A-ZА-ЯЁ][\p{L}]+(?:[ \-][A-ZА-ЯЁ][\p{L}]+)+`)
	acronymRe  = regexp.MustCompile(`\b[A-ZА-ЯЁ]{2,6}\b`)
	capWordRe  = regexp.MustCompile(`^[A-ZА-ЯЁ][\p{Ll}]+$`)
)

// AgenticConfig carries the tunables for the agentic controller. Zero values are
// normalised to safe defaults in EnableAgentic.
type AgenticConfig struct {
	MaxHops   int // RAG_AGENTIC_MAX_HOPS: max retrieve-reason iterations
	GraphTopN int // RAG_AGENTIC_GRAPH_TOPN: graph neighbours requested per hop
}

// agenticController holds the wiring for the multi-hop loop. reasoner drives the
// reasoning + condensation steps; graph is the optional graph-compute tool (nil
// disables it). The controller reuses the host RAGService's retriever, ranker,
// generator, cache and citation helpers, so behaviour stays consistent with the
// single-shot path.
type agenticController struct {
	cfg      AgenticConfig
	reasoner domain.Reasoner
	graph    domain.GraphExpander // optional; nil disables graph expansion
}

// agentAction is the parsed outcome of one reasoner step: either a follow-up
// search (with its query) or a stop signal.
type agentAction struct {
	search bool
	query  string
}

// EnableAgentic activates the optional Search-o1 controller (RAG_AGENTIC). It is
// called from main only when the flag is on; otherwise s.agentic stays nil and
// Answer runs the single-shot path unchanged. reasoner is the LLM used for the
// reasoning/condensation steps; graph is the optional graph-compute expander (nil
// disables the graph tool). It is intentionally separate from New so the existing
// constructor — and every caller/test of it — is untouched.
func (s *RAGService) EnableAgentic(cfg AgenticConfig, reasoner domain.Reasoner, graph domain.GraphExpander) {
	if cfg.MaxHops < 1 {
		cfg.MaxHops = 1
	}
	if cfg.GraphTopN < 1 {
		cfg.GraphTopN = 5
	}
	s.agentic = &agenticController{cfg: cfg, reasoner: reasoner, graph: graph}
}

// answer runs the gated agentic pipeline. It returns handled=false (with no
// error) when the query should NOT be escalated, so the caller falls through to
// the unchanged single-shot path; handled=true means the agentic path owns the
// result. It mirrors the single-shot path's cache, load-marker, abstain and
// caching semantics by reusing the host service's helpers.
func (a *agenticController) answer(ctx context.Context, s *RAGService, q domain.Query) (domain.Result, bool, error) {
	escalate, reasons := agenticGate(q)
	if !escalate {
		return domain.Result{}, false, nil
	}
	log := logger.From(ctx).With().Str("owner_id", q.OwnerID).Str("mode", "agentic").Logger()
	log.Info().Strs("gate", reasons).Int("max_hops", a.cfg.MaxHops).Msg("agentic escalation")

	if s.load != nil {
		s.load.QueryStarted(ctx)
		defer s.load.QueryFinished(ctx)
	}

	scope := q.OwnerID
	if s.sharedCorpus {
		scope = ""
	}
	scopeKey := scope
	if scopeKey == "" {
		scopeKey = "shared"
	}
	key := cacheKey(scopeKey, s.cache.Epoch(ctx, scopeKey), q.Text, q.Prompt, docScopeKey(q))
	if res, hit := s.cachedAnswer(ctx, log, key); hit {
		return res, true, nil
	}

	tr := &domain.Trace{ScoreFloor: s.scoreFloor}
	pool, rerankOK, err := a.gather(ctx, s, log, q, scope, tr)
	if err != nil {
		return domain.Result{}, true, err
	}
	res, err := a.finalize(ctx, s, log, q, pool, key, tr, rerankOK)
	return res, true, err
}

// gather runs the retrieve-reason loop, returning the accumulated evidence pool
// and whether every hop's reranking actually applied (so the score floor stays
// meaningful); per-hop degradations accumulate on tr, whose degraded outcome
// must not be cached. A hard error comes only from embedding. Hop 1 always
// searches the original question so coverage is never worse than single-shot;
// subsequent hops follow the reasoner's focused sub-queries until it stops or
// hops run out.
func (a *agenticController) gather(
	ctx context.Context, s *RAGService, log zerolog.Logger, q domain.Query, scope string, tr *domain.Trace,
) ([]domain.RetrievedChunk, bool, error) {
	stage := s.stageFn(tr)
	pool := make([]domain.RetrievedChunk, 0, agenticMaxEvidence)
	findings := make([]string, 0, a.cfg.MaxHops)
	rerankOK := true
	subquery := strings.TrimSpace(q.Text)

	for hop := 1; hop <= a.cfg.MaxHops; hop++ {
		embedding, err := s.embedder.Embed(ctx, subquery)
		if err != nil {
			return nil, rerankOK, fmt.Errorf("agentic embed (hop %d): %w", hop, err)
		}
		subQ := domain.Query{
			OwnerID: q.OwnerID, Text: subquery, TopK: q.TopK, Prompt: "",
			ScopeDocumentIDs: q.ScopeDocumentIDs, ExcludeDocumentIDs: q.ExcludeDocumentIDs,
		}
		ranked, hopRerankOK := s.retrieveAndRank(ctx, log, subQ, embedding, scope, stage, tr)
		rerankOK = rerankOK && hopRerankOK
		ranked = a.expandWithGraph(ctx, s, log, q, subquery, scope, pool, ranked)
		pool = mergeEvidence(pool, ranked)
		log.Info().Int("hop", hop).Str("subquery", subquery).
			Int("hits", len(ranked)).Int("pool", len(pool)).Msg("agentic hop")

		if finding := a.condense(ctx, log, q.Text, subquery, ranked); finding != "" {
			findings = append(findings, finding)
		}
		if hop == a.cfg.MaxHops {
			break
		}
		action := a.nextAction(ctx, log, q.Text, findings)
		if !action.search {
			log.Info().Int("hop", hop).Msg("agentic reasoner signalled final answer")
			break
		}
		subquery = action.query
	}
	return pool, rerankOK, nil
}

// finalize turns the accumulated evidence into the grounded, cited answer. It
// reuses the single-shot machinery verbatim — expandSummaries → buildContext /
// buildSources, maybeAbstain on empty/weak evidence, and citation sanitisation —
// so [Sn] labels stay 1:1 with the returned sources and the abstain/caching
// behaviour matches the rest of the service.
func (a *agenticController) finalize(
	ctx context.Context, s *RAGService, log zerolog.Logger,
	q domain.Query, pool []domain.RetrievedChunk, key string, tr *domain.Trace, rerankOK bool,
) (domain.Result, error) {
	expanded, raptorExpanded := expandSummaries(pool)
	tr.RaptorExpanded = raptorExpanded
	contextText := buildContext(expanded)
	sources := buildSources(expanded)

	if res, abstained := s.maybeAbstain(log, pool, sources, rerankOK, tr); abstained {
		s.cacheResult(ctx, log, key, res, tr.Degraded)
		return res, nil
	}

	answer, model, err := s.answerer.Answer(ctx, contextText, q.Text, true)
	if err != nil {
		return domain.Result{}, fmt.Errorf("agentic generate: %w", err)
	}
	answer = s.sanitizeAnswerCitations(log, answer, sources, tr)

	res := domain.Result{Answer: answer, Sources: sources, Model: model, Cached: false, Trace: tr}
	s.cacheResult(ctx, log, key, res, tr.Degraded)
	log.Info().Int("sources", len(sources)).Str("model", model).Msg("agentic answered")
	return res, nil
}

// condense is the reason-in-documents step: it distills one hop's passages to the
// question-relevant facts (dropping noise) so the reasoner reasons over signal,
// not raw chunks. A reasoner/condenser error is tolerated (logged, treated as no
// findings) — the retrieved chunks still enter the citation pool regardless.
func (a *agenticController) condense(
	ctx context.Context, log zerolog.Logger, question, subquery string, chunks []domain.RetrievedChunk,
) string {
	if a.reasoner == nil || len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range chunks {
		if t := strings.TrimSpace(c.Text); t != "" {
			fmt.Fprintf(&b, "[D%d] %s\n", i+1, clip(t, condenseChunkClip))
		}
	}
	passages := b.String()
	if passages == "" {
		return ""
	}
	user := "Question: " + question + "\nSearch query: " + subquery + "\n\nPassages:\n" + passages
	out, err := a.reasoner.Reason(ctx, condenseSystemPrompt, user)
	if err != nil {
		log.Warn().Err(err).Msg("agentic condense failed; skipping hop findings")
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "" || strings.EqualFold(out, condenseNone) {
		return ""
	}
	return out
}

// nextAction asks the reasoner for the next move given the findings so far. A nil
// reasoner or any error stops the loop (returns a non-search action), so the
// controller always terminates and degrades to whatever evidence it already has.
func (a *agenticController) nextAction(
	ctx context.Context, log zerolog.Logger, question string, findings []string,
) agentAction {
	if a.reasoner == nil {
		return agentAction{search: false, query: ""}
	}
	var b strings.Builder
	for i, f := range findings {
		fmt.Fprintf(&b, "%d. %s\n", i+1, f)
	}
	user := "Question: " + question + "\n\nFindings so far:\n" + b.String()
	out, err := a.reasoner.Reason(ctx, reasonSystemPrompt, user)
	if err != nil {
		log.Warn().Err(err).Msg("agentic reasoner failed; finalising")
		return agentAction{search: false, query: ""}
	}
	action := parseAction(out)
	log.Info().Bool("search", action.search).Str("next", action.query).Msg("agentic action")
	return action
}

// expandWithGraph optionally folds graph-compute neighbours into a hop's
// candidate set: it seeds Personalized PageRank with the current evidence
// documents, fetches the top neighbours' chunks, reranks them against the
// sub-query with the SAME ranker (so scores are comparable) and merges them in.
// It is a no-op unless the graph tool is wired, and degrades to the original
// candidates on any error so an unreachable graph-compute never fails a query.
func (a *agenticController) expandWithGraph(
	ctx context.Context, s *RAGService, log zerolog.Logger,
	q domain.Query, subquery, scope string, pool, ranked []domain.RetrievedChunk,
) []domain.RetrievedChunk {
	if a.graph == nil || s.chunks == nil {
		return ranked
	}
	have := docIDSet(pool, ranked)
	seeds := make([]string, 0, len(have))
	for id := range have {
		seeds = append(seeds, id)
	}
	if len(seeds) == 0 {
		return ranked
	}
	sort.Strings(seeds) // deterministic request for a stable cache/trace
	docIDs, err := a.graph.Expand(ctx, scope, seeds, a.cfg.GraphTopN)
	if err != nil {
		log.Warn().Err(err).Msg("agentic graph expand failed; continuing without graph")
		return ranked
	}
	docIDs = filterDocIDs(docIDs, q)
	extra := a.fetchGraphChunks(ctx, s, log, scope, docIDs, have)
	if len(extra) == 0 {
		return ranked
	}
	reranked, err := s.rerank(ctx, log, subquery, extra)
	if err != nil {
		log.Warn().Err(err).Msg("agentic graph rerank failed; continuing without graph")
		return ranked
	}
	merged := make([]domain.RetrievedChunk, 0, len(ranked)+len(reranked))
	merged = append(merged, ranked...)
	merged = append(merged, reranked...)
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	log.Info().Int("graph_docs", len(docIDs)).Int("graph_chunks", len(extra)).Msg("agentic graph expansion")
	return merged
}

// fetchGraphChunks loads chunks for graph-expanded documents not already in the
// candidate set, capping each document's contribution so one neighbour cannot
// dominate the pool. A per-document read error is tolerated (logged, skipped).
func (a *agenticController) fetchGraphChunks(
	ctx context.Context, s *RAGService, log zerolog.Logger,
	scope string, docIDs []string, have map[string]bool,
) []domain.RetrievedChunk {
	extra := make([]domain.RetrievedChunk, 0, len(docIDs)*graphChunksPerDoc)
	for _, id := range docIDs {
		if id == "" || have[id] {
			continue
		}
		stored, err := s.chunks.DocumentChunks(ctx, scope, id)
		if err != nil {
			log.Warn().Str("document_id", id).Err(err).Msg("agentic graph chunk fetch failed; skipping")
			continue
		}
		for i, sc := range stored {
			if i >= graphChunksPerDoc {
				break
			}
			extra = append(extra, domain.RetrievedChunk{
				ID: sc.ID, Text: sc.Text, DocumentID: id, ChunkIndex: sc.Index,
			})
		}
	}
	return extra
}

// filterDocIDs drops graph-expanded documents the query's document filters
// forbid: excluded ids always, and ids outside a non-empty scope list.
func filterDocIDs(docIDs []string, q domain.Query) []string {
	if len(q.ScopeDocumentIDs) == 0 && len(q.ExcludeDocumentIDs) == 0 {
		return docIDs
	}
	scope := map[string]bool{}
	for _, id := range q.ScopeDocumentIDs {
		scope[id] = true
	}
	excl := map[string]bool{}
	for _, id := range q.ExcludeDocumentIDs {
		excl[id] = true
	}
	out := docIDs[:0]
	for _, id := range docIDs {
		if excl[id] || (len(scope) > 0 && !scope[id]) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// agenticGate is the cheap TARG/Adaptive-style escalation decision: a pure
// function of the query text (no LLM, no retrieval) that fires only for queries
// showing multi-hop structure, so simple questions stay on the single-shot path.
// Structured-prompt callers (hypothesis generation/verification) are never
// escalated — they drive their own JSON generation and must not be rerouted
// through the chat loop. Signals: comparative/relational phrasing (strong);
// several sub-questions; clause chaining; multiple salient entities; a long
// query. It returns the verdict and the matched reasons (for the trace log).
func agenticGate(q domain.Query) (bool, []string) {
	if strings.TrimSpace(q.Prompt) != "" {
		return false, nil
	}
	text := q.Text
	lower := strings.ToLower(text)
	score := 0
	reasons := make([]string, 0, 5)

	if comparativeRe.MatchString(lower) {
		score += gateComparativeWeight
		reasons = append(reasons, "comparative")
	}
	if strings.Count(text, "?") >= gateMinQuestions {
		score++
		reasons = append(reasons, "multi-question")
	}
	if len(clauseRe.FindAllString(lower, -1)) >= gateMinClauses {
		score++
		reasons = append(reasons, "multi-clause")
	}
	if entityCount(text) >= gateMinEntities {
		score++
		reasons = append(reasons, "multi-entity")
	}
	if len(strings.Fields(text)) >= gateLongQueryTokens {
		score++
		reasons = append(reasons, "long-query")
	}
	return score >= gateThreshold, reasons
}

// entityCount estimates the number of distinct salient entities in the query:
// quoted spans, capitalised multi-word names, acronyms and mid-sentence
// capitalised words (the first token is skipped — it is capitalised by position,
// not by being a proper noun). Matches are deduplicated case-insensitively. It is
// a heuristic proxy for "this question joins facts about several things".
func entityCount(text string) int {
	set := make(map[string]struct{})
	add := func(s string) {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			set[s] = struct{}{}
		}
	}
	for _, m := range quotedRe.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range capMultiRe.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range acronymRe.FindAllString(text, -1) {
		add(m)
	}
	for i, f := range strings.Fields(text) {
		if i > 0 && capWordRe.MatchString(f) {
			add(f)
		}
	}
	return len(set)
}

// parseAction extracts the reasoner's move: a '[search] …' line yields a search
// action with the (quote-stripped) query; anything else — including [answer] or
// a malformed reply — yields a stop action, which bounds the loop.
func parseAction(text string) agentAction {
	if m := searchActionRe.FindStringSubmatch(text); m != nil {
		query := strings.Trim(strings.TrimSpace(m[1]), "\"'`«»")
		if query != "" {
			return agentAction{search: true, query: query}
		}
	}
	return agentAction{search: false, query: ""}
}

// mergeEvidence appends new candidates to the accumulated pool, deduplicating by
// chunk id (first occurrence wins, preserving hop order so [Sn] numbering is
// stable) and capping the pool at agenticMaxEvidence to bound the citation list.
func mergeEvidence(pool, add []domain.RetrievedChunk) []domain.RetrievedChunk {
	seen := make(map[string]bool, len(pool))
	for _, c := range pool {
		if c.ID != "" {
			seen[c.ID] = true
		}
	}
	for _, c := range add {
		if len(pool) >= agenticMaxEvidence {
			break
		}
		if c.ID != "" {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
		}
		pool = append(pool, c)
	}
	return pool
}

// docIDSet collects the distinct, non-empty document ids present across the given
// chunk lists — the graph-expansion seed set and the "already have" guard.
func docIDSet(lists ...[]domain.RetrievedChunk) map[string]bool {
	set := make(map[string]bool)
	for _, list := range lists {
		for _, c := range list {
			if c.DocumentID != "" {
				set[c.DocumentID] = true
			}
		}
	}
	return set
}

// clip trims s to at most maxRunes runes (never splitting a multibyte
// character), appending an ellipsis when truncated.
func clip(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}
