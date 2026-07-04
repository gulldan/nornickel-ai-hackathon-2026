// Evidence honesty: per-source stance classification (supports / contradicts /
// context) and counter-evidence retrieval, shared by generation and verification
// so the board never labels every fragment "supports" by default.

package application

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// evidenceRelationMax bounds a stored relation comment so it stays a one-line
// caption rather than an essay when the model is verbose.
const evidenceRelationMax = 280

// relationText normalizes a model-supplied per-fragment reason into a compact
// relation comment — how the fragment relates to the hypothesis (conditions →
// effect, with numbers). Empty in, empty out.
func relationText(reason string) string {
	r := compactText(reason)
	if utf8.RuneCountInString(r) <= evidenceRelationMax {
		return r
	}
	return strings.TrimRight(string([]rune(r)[:evidenceRelationMax]), " ,;:") + "…"
}

// stanceSnippetMax bounds how many runes of each snippet the classifier sees, so
// a batch of fragments stays within the model's context budget.
const stanceSnippetMax = 600

// counterRetrievalPrompt is the throwaway instruction for a counter-evidence
// retrieval pass: only the retrieved sources are used, not the generated text.
const counterRetrievalPrompt = "Перечисли кратко, что в источниках ограничивает или опровергает гипотезу."

// Canonical evidence stances; also the values stored in HypothesisEvidence.stance.
const (
	stanceSupports    = "supports"
	stanceContradicts = "contradicts"
	stanceContext     = "context"
)

type stanceVerdict struct {
	Index  int    `json:"i"`
	Stance string `json:"stance"`
	Reason string `json:"reason"`
}

// normalizeStance maps a model-emitted label to the canonical evidence stance,
// or "" when it is unrecognised.
func normalizeStance(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "supports", "support", "поддерживает", "подтверждает":
		return stanceSupports
	case "contradicts", "contradict", "противоречит", "опровергает":
		return stanceContradicts
	case "context", "background", "контекст", "метод":
		return stanceContext
	default:
		return ""
	}
}

// counterQuery turns a statement into a retrieval query aimed at disconfirming
// evidence — the falsification half of honest verification.
func counterQuery(statement string) string {
	return statement + " ограничения противоречия отрицательный результат нестабильность деградация неудача"
}

// snippetForStance trims a snippet to the classifier budget.
func snippetForStance(snippet string) string {
	runes := []rune(strings.TrimSpace(snippet))
	if len(runes) <= stanceSnippetMax {
		return string(runes)
	}
	return string(runes[:stanceSnippetMax]) + "…"
}

// buildStancePrompt asks the model to label each numbered fragment's stance
// toward the hypothesis.
func buildStancePrompt(statement string, ev []*commonv1.HypothesisEvidence) string {
	var b strings.Builder
	b.WriteString("Гипотеза: «")
	b.WriteString(statement)
	b.WriteString("».\n\nНиже пронумерованные фрагменты из документов. Для КАЖДОГО определи отношение к " +
		"гипотезе: подтверждает (supports), противоречит (contradicts) или только даёт контекст/метод " +
		"без прямого вывода (context). В поле reason кратко поясни, КАК фрагмент относится к гипотезе: " +
		"при каких условиях какой эффект получен, с конкретными числами из фрагмента. " +
		"Опирайся ТОЛЬКО на текст фрагмента.\n\n")
	for i, e := range ev {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(") ")
		b.WriteString(snippetForStance(e.GetSnippet()))
		b.WriteByte('\n')
	}
	b.WriteString(untrustedContextInstruction)
	b.WriteString(langInstruction)
	b.WriteString("\nВерни СТРОГО JSON-массив без markdown: " +
		`[{"i":1,"stance":"supports|contradicts|context",` +
		`"reason":"при каких условиях какой эффект (с цифрами) и как относится к гипотезе"}]`)
	return b.String()
}

// parseStances extracts the JSON array of stance verdicts from a model answer.
func parseStances(answer string) []stanceVerdict {
	start := strings.IndexByte(answer, '[')
	end := strings.LastIndexByte(answer, ']')
	if start < 0 || end <= start {
		return nil
	}
	var out []stanceVerdict
	if err := json.Unmarshal([]byte(answer[start:end+1]), &out); err != nil {
		return nil
	}
	return out
}

// classifyEvidenceStances labels each evidence fragment in place. Evidence keeps
// its prior (neutral "context") stance when classification is unavailable, so a
// model outage never manufactures support. It is defensive against a noisy LLM:
// out-of-range indices are dropped and the FIRST verdict per fragment wins, so a
// duplicate or contradictory later index can't flip an already-labelled fragment.
// Future upgrade: replace the LLM classifier with a dedicated NLI / cross-encoder
// model (entailment vs. contradiction) once one is available in this stack.
func (s *HypothesisService) classifyEvidenceStances(
	ctx context.Context, ownerID, statement string, ev []*commonv1.HypothesisEvidence,
) {
	if len(ev) == 0 {
		return
	}
	resp, err := s.answerer.Answer(ctx, &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   statement,
		Prompt:  buildStancePrompt(statement, ev),
		TopK:    1,
	})
	if err != nil {
		return
	}
	labelled := make(map[int]struct{}, len(ev))
	for _, v := range parseStances(resp.GetAnswer()) {
		idx := v.Index - 1
		if idx < 0 || idx >= len(ev) {
			continue // ignore out-of-range / partial indices
		}
		if _, done := labelled[idx]; done {
			continue // first verdict per fragment wins; tolerate duplicates
		}
		if st := normalizeStance(v.Stance); st != "" {
			ev[idx].Stance = st
			if r := relationText(v.Reason); r != "" {
				ev[idx].Relation = r
			}
			labelled[idx] = struct{}{}
		}
	}
}

// counterEvidenceSources retrieves disconfirming sources for a statement via a
// dedicated counter-query, so generation and verification see opposing evidence
// instead of only what the hypothesis itself recalls.
func (s *HypothesisService) counterEvidenceSources(
	ctx context.Context, ownerID, statement string, topK int32, excludeDocIDs []string,
) []*commonv1.Source {
	resp, err := s.answerer.Answer(ctx, &commonv1.RagRequest{
		OwnerId:            ownerID,
		Query:              counterQuery(statement),
		Prompt:             counterRetrievalPrompt,
		TopK:               topK,
		ExcludeDocumentIds: excludeDocIDs,
	})
	if err != nil {
		return nil
	}
	return resp.GetSources()
}
