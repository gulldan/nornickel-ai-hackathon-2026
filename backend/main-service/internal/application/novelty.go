// Computed novelty (P2.3): a deterministic novelty signal that replaces the
// LLM's self-rating. It combines two grounded sources of evidence — how much
// prior art in the corpus already covers the hypothesis (more coverage ⇒ less
// novel), and whether the proposed material is an established Materials Project
// entry (known/stable ⇒ less novel). The result is bounded to [0,1] with a
// short Russian rationale, and degrades to a neutral 0.5 when no signal exists.

package application

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// MaterialsRef checks a chemical formula against an external reference of known
// materials (the Materials Project). It is the application-layer port; the
// concrete adapter lives in internal/infrastructure/matproj. A nil ref or a
// keyless adapter degrades gracefully (every formula reads as "unknown").
type MaterialsRef interface {
	Known(ctx context.Context, formula string) (bool, error)
}

const (
	// noveltyNeutral is returned when no grounding signal is available.
	noveltyNeutral = 0.5
	// noveltyPriorArtTop is how many corpus passages are retrieved to gauge prior-art density.
	noveltyPriorArtTop = 12
	// noveltyCloseOverlap is the ≥4-rune token overlap above which a source counts as "close" prior art.
	noveltyCloseOverlap = 3
	// noveltyKnownPenalty is how far an established MP material nudges novelty down.
	noveltyKnownPenalty = 0.2
)

// noveltyResult is the computed novelty plus a human-readable basis.
type noveltyResult struct {
	Score     float64
	Rationale string
	// CloseMatches and Retrieved expose the prior-art tally for callers/tests.
	CloseMatches  int
	Retrieved     int
	MaterialKnown bool
}

// computeNovelty derives a grounded novelty score for one generated item. It
// retrieves prior art for the hypothesis statement and, when a formula is
// present, sanity-checks it against the materials reference. It is fully
// defensive: a retrieval error or an empty corpus yields the neutral 0.5, and a
// missing/keyless MaterialsRef simply skips the MP nudge.
func (s *HypothesisService) computeNovelty(
	ctx context.Context, ownerID string, item genItem,
) noveltyResult {
	density, retrieved, closeMatches := s.priorArtDensity(ctx, ownerID, item)

	// Map prior-art density (share of close matches in [0,1]) to novelty: dense
	// prior art ⇒ low novelty, sparse ⇒ high. No retrieval signal ⇒ neutral.
	score := noveltyNeutral
	if retrieved > 0 {
		score = clamp01(1 - density)
	}

	formula := materialFormula(item)
	known := false
	if formula != "" && s.materials != nil {
		if k, err := s.materials.Known(ctx, formula); err == nil && k {
			known = true
			score = clamp01(score - noveltyKnownPenalty)
		}
	}

	return noveltyResult{
		Score:         round3(score),
		Rationale:     noveltyRationale(retrieved, closeMatches, formula, known),
		CloseMatches:  closeMatches,
		Retrieved:     retrieved,
		MaterialKnown: known,
	}
}

// priorArtDensity retrieves corpus passages for the hypothesis and reports the
// share that closely overlaps it (a cheap proxy for prior-art coverage). It
// returns the density in [0,1], the number of distinct sources seen and the
// number that counted as close.
func (s *HypothesisService) priorArtDensity(
	ctx context.Context, ownerID string, item genItem,
) (density float64, retrieved, closeMatches int) {
	resp, err := s.answerer.Answer(ctx, &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   noveltyQuery(item),
		Prompt:  noveltyRetrievalPrompt,
		TopK:    noveltyPriorArtTop,
	})
	if err != nil {
		return 0, 0, 0
	}
	hyp := tokenSet(noveltyQuery(item))
	if len(hyp) == 0 {
		return 0, 0, 0
	}
	seen := make(map[string]struct{})
	for _, source := range resp.GetSources() {
		key := noveltySourceKey(source)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if overlap(hyp, tokenSet(source.GetSnippet())) >= noveltyCloseOverlap {
			closeMatches++
		}
	}
	retrieved = len(seen)
	if retrieved == 0 {
		return 0, 0, 0
	}
	return float64(closeMatches) / float64(retrieved), retrieved, closeMatches
}

// noveltyQuery builds the prior-art probe from the hypothesis's material +
// intervention + target, falling back to the statement when the materials
// passport is empty.
func noveltyQuery(item genItem) string {
	parts := make([]string, 0, 5)
	for _, p := range []string{
		item.MaterialSystem, item.CompositionChange, item.ProcessChange, item.TargetProperty,
	} {
		if v := compactText(p); v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return compactText(item.Statement)
	}
	return strings.Join(parts, " ")
}

// noveltyRetrievalPrompt is a throwaway instruction: only the retrieved sources
// are used (to count prior art), never the model's generated text.
const noveltyRetrievalPrompt = "Перечисли кратко уже известные из источников работы по этой теме."

func noveltySourceKey(src *commonv1.Source) string {
	if src.GetDocumentId() != "" {
		return "doc:" + src.GetDocumentId()
	}
	if src.GetFilename() != "" {
		return "file:" + strings.ToLower(strings.TrimSpace(src.GetFilename()))
	}
	if src.GetChunkId() != "" {
		return "chunk:" + src.GetChunkId()
	}
	return ""
}

// formulaPattern matches a compact chemical-formula-like token: element symbols
// (an upper-case letter then optional lower-case) with optional counts, e.g.
// "Al2O3", "TiAlN", "Fe3O4". It is a heuristic, not a parser.
var formulaPattern = regexp.MustCompile(`^(?:[A-Z][a-z]?\d*){2,}$`)

// materialFormula extracts a best-effort chemical formula from the item's
// materials passport (material_system first, then composition_change). Returns
// "" when no formula-shaped token is found, so the MP lookup is simply skipped.
func materialFormula(item genItem) string {
	for _, field := range []string{item.MaterialSystem, item.CompositionChange} {
		if f := firstFormula(field); f != "" {
			return f
		}
	}
	return ""
}

// firstFormula returns the first formula-shaped token in s, or "".
func firstFormula(s string) string {
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:()[]{}«»\"'", r)
	}) {
		tok = strings.TrimSpace(tok)
		if looksLikeFormula(tok) {
			return tok
		}
	}
	return ""
}

// looksLikeFormula keeps the heuristic conservative: the token must contain at
// least two element-like groups, mix upper/lower or digits, and not be a plain
// word (all-letter all-lower) or an acronym without composition meaning.
func looksLikeFormula(tok string) bool {
	if len(tok) < 2 || len(tok) > 24 {
		return false
	}
	if !formulaPattern.MatchString(tok) {
		return false
	}
	// Reject all-upper acronyms with no digits and a single repeated style (e.g.
	// "SEM", "XRD") that the element pattern would otherwise accept.
	hasLowerOrDigit := false
	for _, r := range tok {
		if unicode.IsLower(r) || unicode.IsDigit(r) {
			hasLowerOrDigit = true
			break
		}
	}
	return hasLowerOrDigit
}

// noveltyRationale renders a short Russian explanation of the novelty basis.
func noveltyRationale(retrieved, closeMatches int, formula string, known bool) string {
	var b strings.Builder
	switch {
	case retrieved == 0:
		b.WriteString("Близких работ в корпусе не найдено")
	case closeMatches == 0:
		fmt.Fprintf(&b, "В корпусе %d источников по теме, близких совпадений нет", retrieved)
	default:
		fmt.Fprintf(&b, "%d близких работ в корпусе из %d по теме", closeMatches, retrieved)
	}
	switch {
	case formula == "":
	case known:
		fmt.Fprintf(&b, "; материал %s известен в Materials Project", formula)
	default:
		fmt.Fprintf(&b, "; материал %s не найден в Materials Project", formula)
	}
	b.WriteByte('.')
	return b.String()
}
