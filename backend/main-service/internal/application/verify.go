// Hypothesis verification (confirm / refute): retrieve corpus evidence for the
// hypothesis statement and have the LLM judge whether the evidence supports,
// contradicts or is insufficient for it — the falsification half of the factory.
// The verdict is stored in assessment.check, confidence_score is derived from
// it, and a "verified" revision is appended.

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

const verifyEvidenceTop = 10

const (
	verdictSupported    = "supported"
	verdictRefuted      = "refuted"
	verdictMixed        = "mixed"
	verdictInsufficient = "insufficient"
)

type verifyResult struct {
	Verdict       string   `json:"verdict"`
	Confidence    *float64 `json:"confidence"`
	Rationale     string   `json:"rationale"`
	Supporting    []string `json:"supporting"`
	Contradicting []string `json:"contradicting"`
}

// Verify checks a hypothesis the caller owns against the corpus and records the
// verdict. It runs two retrievals — one for the statement and one counter-query
// that hunts for disconfirming evidence — so the verdict is not confirmation-
// biased by searching with the hypothesis alone. Returns the updated hypothesis.
func (s *HypothesisService) Verify(ctx context.Context, ownerID, id string) (*commonv1.Hypothesis, error) {
	ctx, cancel := withDeadline(ctx, llmGenerateTimeout)
	defer cancel()
	h, err := s.GetHypothesis(ctx, ownerID, false, id)
	if err != nil {
		return nil, err
	}
	stmt := h.GetStatement()
	prompt := verifyPrompt(stmt)
	// Exclude documents that are themselves ready-made hypotheses ("leaked
	// answers") from verification retrieval, so they never become evidence.
	excludeDocIDs := s.answerDocIDs(ctx, ownerID)
	main, mainSrc, model, merr := s.judgeStatement(ctx, ownerID, stmt, prompt, excludeDocIDs)
	if merr != nil {
		return nil, merr
	}
	// Best-effort counter pass: a different query aimed at limitations and
	// contradictions. A failure here must not block the primary verdict.
	counter, counterSrc, _, _ := s.judgeStatement(ctx, ownerID, counterQuery(stmt), prompt, excludeDocIDs)

	// Persist supporting (primary) and contradicting (both passes) fragments as
	// typed evidence rows, each matched to a retrieved source.
	combined := verifyResult{
		Verdict:       main.Verdict,
		Confidence:    main.Confidence,
		Rationale:     main.Rationale,
		Supporting:    main.Supporting,
		Contradicting: append(append([]string{}, main.Contradicting...), counter.Contradicting...),
	}
	allSrc := append(append([]*commonv1.Source{}, mainSrc...), counterSrc...)
	h.Evidence = append(h.Evidence, verifierEvidence(h.GetEvidence(), allSrc, combined)...)

	// A grounded contradiction from the counter pass turns a clean "supported" or
	// "insufficient" into "mixed", then the confidence split derives belief /
	// verdict / evidence-quality from the reconciled verdict.
	combined.Verdict = reconcileVerdict(main.Verdict, h.GetEvidence())

	// The corpus could not decide — reach out to open literature (OpenAlex /
	// Crossref), re-judge with the found abstracts as extra context and keep the
	// publications as evidence; abstracts are also back-filled into the corpus.
	var pubWorks []ExternalWork
	if combined.Verdict == verdictInsufficient {
		pubWorks = s.reverifyWithPubs(ctx, ownerID, stmt, prompt, h, &combined, &allSrc, &model, excludeDocIDs)
	}

	scores := confidenceSplit(combined.Verdict, combined.Confidence, h.GetEvidence())
	h.Assessment = mergeScores(mergeCheck(h.GetAssessment(), combined, model, pubWorks), scores)
	h.ConfidenceScore = &scores.Belief
	// Confidence, evidence and the corpus verdict just changed — recompute the
	// transparent composite so the board re-ranks on real (not stale) features.
	s.applyRanking(ctx, h)
	summary := verifySummary(combined)
	if len(pubWorks) > 0 {
		summary += " (с учётом открытых публикаций)"
	}
	rev := &commonv1.HypothesisRevision{Action: "verified", EditorId: editorSystem, Summary: summary}
	if uerr := s.cat.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: h, Revision: rev}); uerr != nil {
		return nil, uerr
	}
	return s.GetHypothesis(ctx, ownerID, false, id)
}

// judgeStatement runs one retrieve+judge pass and returns the parsed verdict, the
// retrieved sources and the answering model. excludeDocIDs drops documents that
// are themselves ready-made hypotheses from the retrieved evidence.
func (s *HypothesisService) judgeStatement(
	ctx context.Context, ownerID, query, prompt string, excludeDocIDs []string,
) (verifyResult, []*commonv1.Source, string, error) {
	resp, err := s.answerer.Answer(opCtx(ctx, "verify"), &commonv1.RagRequest{
		OwnerId:            ownerID,
		Query:              query,
		Prompt:             prompt,
		TopK:               verifyEvidenceTop,
		ExcludeDocumentIds: excludeDocIDs,
	})
	if err != nil {
		return verifyResult{}, nil, "", err
	}
	res, perr := parseVerify(resp.GetAnswer())
	if perr != nil {
		return verifyResult{}, nil, "", perr
	}
	return res, resp.GetSources(), resp.GetModel(), nil
}

func verifyPrompt(statement string) string {
	var b strings.Builder
	b.WriteString("Проверяемая гипотеза: «")
	b.WriteString(statement)
	b.WriteString("».\n\nТы — научный рецензент. Опираясь ТОЛЬКО на приведённый выше контекст " +
		"(выдержки из документов), определи, подтверждается ли гипотеза, опровергается, частично " +
		"или данных недостаточно. Приведи дословные фрагменты «за» и «против».")
	b.WriteString(untrustedContextInstruction)
	b.WriteString(langInstruction)
	b.WriteString("\nВерни СТРОГО JSON без markdown:\n")
	b.WriteString(`{"verdict":"supported|refuted|mixed|insufficient","confidence":0..1,` +
		`"rationale":"кратко почему","supporting":["дословные фрагменты, подтверждающие"],` +
		`"contradicting":["дословные фрагменты, противоречащие"]}`)
	return b.String()
}

func parseVerify(answer string) (verifyResult, error) {
	start := strings.IndexByte(answer, '{')
	end := strings.LastIndexByte(answer, '}')
	if start < 0 || end <= start {
		return verifyResult{}, errors.New("no JSON object in verify output")
	}
	raw := answer[start : end+1]
	var r verifyResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		// Tolerate unescaped LaTeX backslashes the model may include (see
		// repairJSONBackslashes); retry once before giving up.
		if err2 := json.Unmarshal([]byte(repairJSONBackslashes(raw)), &r); err2 != nil {
			return verifyResult{}, fmt.Errorf("parse verify result: %w", err)
		}
	}
	return r, nil
}

// reverifyWithPubs runs the open-literature pass for an insufficient verdict:
// find publications for the statement, re-judge with their abstracts inlined
// and fold the new verdict, sources and evidence back into the verify state.
// Returns the works used (nil when the pass could not run or failed).
func (s *HypothesisService) reverifyWithPubs(
	ctx context.Context, ownerID, stmt, prompt string, h *commonv1.Hypothesis,
	combined *verifyResult, allSrc *[]*commonv1.Source, model *string, excludeDocIDs []string,
) []ExternalWork {
	works, pubSrc := s.externalVerifyWorks(ctx, ownerID, stmt)
	if len(pubSrc) == 0 {
		return nil
	}
	again, againSrc, pubModel, err := s.judgeStatement(ctx, ownerID, stmt, prompt+pubVerifyNote(works), excludeDocIDs)
	if err != nil {
		return nil
	}
	combined.Verdict = again.Verdict
	combined.Confidence = again.Confidence
	combined.Rationale = again.Rationale
	combined.Supporting = append(combined.Supporting, again.Supporting...)
	combined.Contradicting = append(combined.Contradicting, again.Contradicting...)
	*allSrc = append(append(*allSrc, againSrc...), pubSrc...)
	h.Evidence = append(h.Evidence, verifierEvidence(h.GetEvidence(), *allSrc, again)...)
	combined.Verdict = reconcileVerdict(again.Verdict, h.GetEvidence())
	if pubModel != "" {
		*model = pubModel
	}
	return works
}

// externalVerifyWorks searches open literature for the hypothesis statement and
// returns the found works plus synthetic sources built from their abstracts, so
// verdict fragments citing a publication keep provenance. Found abstracts are
// back-filled into the corpus in the background.
func (s *HypothesisService) externalVerifyWorks(
	ctx context.Context, ownerID, statement string,
) ([]ExternalWork, []*commonv1.Source) {
	if s.pubs == nil {
		return nil, nil
	}
	query := s.pubSearchQuery(ctx, ownerID, statement, "")
	works, err := s.pubs.SearchWorks(ctx, query, statement, "", pubWorksLimit)
	if err != nil || len(works) == 0 {
		return nil, nil
	}
	if s.pubIngest != nil && ownerID != "" {
		go s.ingestWorks(context.WithoutCancel(ctx), ownerID, works)
	}
	return works, pubVerifySources(works)
}

func pubVerifySources(works []ExternalWork) []*commonv1.Source {
	out := make([]*commonv1.Source, 0, len(works))
	for _, w := range works {
		if strings.TrimSpace(w.Abstract) == "" {
			continue
		}
		out = append(out, &commonv1.Source{
			Filename: workFilename(w),
			Snippet:  strings.TrimSpace(w.Title + ". " + compactText(w.Abstract)),
		})
	}
	return out
}

func pubVerifyNote(works []ExternalWork) string {
	if len(works) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nДополнительный контекст — открытые публикации (мировая практика), найденные " +
		"по теме гипотезы. Используй их наравне с документами выше, дословные фрагменты «за» и " +
		"«против» бери и из них; не выдумывай источники сверх списка:\n")
	for i, w := range works {
		fmt.Fprintf(&b, "[P%d] %s", i+1, w.Title)
		if w.Year > 0 {
			fmt.Fprintf(&b, " (%d)", w.Year)
		}
		if w.Venue != "" {
			fmt.Fprintf(&b, ", %s", w.Venue)
		}
		if w.DOI != "" {
			fmt.Fprintf(&b, ", doi:%s", w.DOI)
		}
		if a := compactText(w.Abstract); a != "" {
			r := []rune(a)
			if len(r) > pubAbstractMaxRunes {
				a = string(r[:pubAbstractMaxRunes]) + "…"
			}
			b.WriteString(" — ")
			b.WriteString(a)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// mergeCheck stores the verdict under assessment.check, preserving the other
// assessment fields (novelty/risk/value/...).
func mergeCheck(assessment string, res verifyResult, model string, works []ExternalWork) string {
	m := map[string]any{}
	if assessment != "" {
		_ = json.Unmarshal([]byte(assessment), &m)
	}
	check := map[string]any{
		"verdict": res.Verdict, "confidence": res.Confidence, keyRationale: res.Rationale,
		"supporting": res.Supporting, "contradicting": res.Contradicting,
		keyModel: model, "checked_at": time.Now().UTC().Format(time.RFC3339),
	}
	if len(works) > 0 {
		check["external_works"] = works
	}
	m["check"] = check
	b, err := json.Marshal(m)
	if err != nil {
		return assessment
	}
	return string(b)
}

// confidenceScores splits the single "confidence" number into the distinct ideas
// it used to conflate, all persisted under assessment.scores.
type confidenceScores struct {
	Belief          float64 `json:"belief_score"`       // P(hypothesis holds) given the corpus
	VerdictConf     float64 `json:"verdict_confidence"` // confidence in the verdict itself
	EvidenceQuality float64 `json:"evidence_quality"`   // how much independent evidence exists
	Unverified      bool    `json:"unverified"`         // true ⇒ the corpus could not decide
}

// reconcileVerdict downgrades a clean verdict when there is a grounded
// contradiction, so "supported"/"insufficient" become "mixed".
func reconcileVerdict(main string, evidence []*commonv1.HypothesisEvidence) string {
	contra := false
	for _, e := range evidence {
		if e.GetStance() == stanceContradicts {
			contra = true
			break
		}
	}
	if !contra {
		return main
	}
	switch main {
	case verdictSupported, verdictInsufficient:
		return verdictMixed
	default:
		return main
	}
}

// confidenceSplit derives the separate belief / verdict / evidence scores. A
// refuted hypothesis keeps a HIGH verdict confidence but a LOW belief; an
// insufficient verdict no longer leaves a stale high confidence in place — it
// drops belief and flags the card unverified.
func confidenceSplit(verdict string, verdictConf *float64, evidence []*commonv1.HypothesisEvidence) confidenceScores {
	vc := 0.5
	if verdictConf != nil {
		vc = clamp01(*verdictConf)
	}
	sup, con := evidenceCounts(evidence)
	eq := evidenceQuality(sup, con)
	var belief float64
	unverified := false
	switch verdict {
	case verdictSupported:
		belief = vc
	case verdictRefuted:
		belief = 1 - vc
	case verdictMixed:
		belief = 0.4
	default: // insufficient
		belief = 0.3 * eq
		unverified = true
	}
	return confidenceScores{
		Belief: round3(clamp01(belief)), VerdictConf: round3(vc),
		EvidenceQuality: round3(eq), Unverified: unverified,
	}
}

// evidenceQuality rates the evidence base: more independent sources and the
// presence of BOTH stances (the question was actually examined) score higher.
func evidenceQuality(sup, con int) float64 {
	total := sup + con
	if total == 0 {
		return 0
	}
	volume := clamp01(float64(total) / 5.0)
	balance := 0.6
	if sup > 0 && con > 0 {
		balance = 1.0
	}
	return clamp01(0.7*volume + 0.3*balance)
}

// mergeScores stores the confidence split under assessment.scores, preserving the
// other assessment fields.
func mergeScores(assessment string, sc confidenceScores) string {
	m := map[string]any{}
	if assessment != "" {
		_ = json.Unmarshal([]byte(assessment), &m)
	}
	m["scores"] = sc
	b, err := json.Marshal(m)
	if err != nil {
		return assessment
	}
	return string(b)
}

const verifierMaxEvidence = 6

// verifierEvidence turns the verdict's supporting/contradicting fragments into
// typed evidence rows (empty IDs ⇒ inserted on Update), each anchored to the
// best-matching retrieved source. It dedups against existing evidence by
// (chunk, stance) so re-verification never piles up duplicates, and skips
// fragments that match no source (so provenance is never fabricated).
func verifierEvidence(
	existing []*commonv1.HypothesisEvidence, sources []*commonv1.Source, res verifyResult,
) []*commonv1.HypothesisEvidence {
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		seen[e.GetChunkId()+"|"+e.GetFilename()+"|"+e.GetStance()] = true
	}
	out := make([]*commonv1.HypothesisEvidence, 0, verifierMaxEvidence)
	add := func(fragments []string, stance string) {
		for _, frag := range fragments {
			if len(out) >= verifierMaxEvidence {
				return
			}
			src := bestSource(sources, frag)
			if src == nil {
				continue
			}
			key := src.GetChunkId() + "|" + src.GetFilename() + "|" + stance
			if seen[key] {
				continue
			}
			seen[key] = true
			score := src.GetScore()
			ev := &commonv1.HypothesisEvidence{
				ChunkId: src.GetChunkId(), Filename: src.GetFilename(), Snippet: frag,
				Stance: stance, Score: &score, Ord: int32(100 + len(out)),
				PageStart: src.GetPageStart(), PageEnd: src.GetPageEnd(), SectionHeading: src.GetSectionHeading(),
			}
			if docID := src.GetDocumentId(); docID != "" {
				ev.DocumentId = &docID
			}
			out = append(out, ev)
		}
	}
	add(res.Supporting, stanceSupports)
	add(res.Contradicting, stanceContradicts)
	return out
}

// bestSource returns the retrieved source whose snippet overlaps the fragment
// most (by ≥4-rune token overlap), or nil if nothing meaningfully matches.
func bestSource(sources []*commonv1.Source, fragment string) *commonv1.Source {
	frag := tokenSet(fragment)
	var best *commonv1.Source
	bestScore := 0
	for _, s := range sources {
		if o := overlap(frag, tokenSet(s.GetSnippet())); o > bestScore {
			bestScore, best = o, s
		}
	}
	if bestScore == 0 {
		return nil
	}
	return best
}

func verifySummary(res verifyResult) string {
	labels := map[string]string{
		verdictSupported: "подтверждается", verdictRefuted: "опровергается",
		verdictMixed: "частично подтверждается", verdictInsufficient: "недостаточно данных",
	}
	label := labels[res.Verdict]
	if label == "" {
		label = res.Verdict
	}
	return "Проверка по корпусу: " + label
}
