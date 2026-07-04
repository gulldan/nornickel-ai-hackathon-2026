// Active-learning priority (P2.2): an information-gain signal that ranks which
// hypothesis is worth testing NEXT. A test resolves the most when the hypothesis
// is valuable, currently UNCERTAIN, and under-evidenced — so resolving it both
// matters and is unresolved. This is an ADDITIONAL signal stored alongside the
// ranking; it does not change the 6-factor weighted composite.

package application

import (
	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	"github.com/example/main-service/internal/platform/jsonx"
)

// learningPriority is the persisted shape under assessment.learning_priority: the
// headline score plus its three transparent inputs, so the board can both sort
// by it and explain it.
type learningPriority struct {
	Score           float64 `json:"score"`
	Value           float64 `json:"value"`
	Uncertainty     float64 `json:"uncertainty"`
	EvidenceQuality float64 `json:"evidence_quality"`
}

// computeLearningPriority derives the learning-priority signal from a hypothesis.
// value comes from value_score; belief/evidence_quality come from the verifier's
// assessment.scores (defaults: belief 0.5 ⇒ maximal uncertainty, evidence 0 ⇒
// un-evidenced). uncertainty peaks at belief 0.5 and falls to 0 at a confident
// 0/1 belief; under-evidenced hypotheses (low evidence_quality) score higher.
func computeLearningPriority(h *commonv1.Hypothesis) learningPriority {
	value := valOr(h.ValueScore)
	belief, evidenceQuality := beliefAndEvidence(h.GetAssessment())
	uncertainty := clamp01(1 - absFloat(belief-0.5)*2)
	score := value * uncertainty * (1 - evidenceQuality)
	return learningPriority{
		Score:           round3(clamp01(score)),
		Value:           round3(value),
		Uncertainty:     round3(uncertainty),
		EvidenceQuality: round3(evidenceQuality),
	}
}

// beliefAndEvidence reads belief_score and evidence_quality from
// assessment.scores. Absent scores default to belief 0.5 and evidence 0, so an
// unverified hypothesis reads as maximally uncertain and un-evidenced.
func beliefAndEvidence(assessment string) (belief, evidenceQuality float64) {
	belief, evidenceQuality = 0.5, 0
	if assessment == "" {
		return belief, evidenceQuality
	}
	var m struct {
		Scores struct {
			Belief          *float64 `json:"belief_score"`
			EvidenceQuality *float64 `json:"evidence_quality"`
		} `json:"scores"`
	}
	if err := jsonx.Unmarshal([]byte(assessment), &m); err != nil {
		return belief, evidenceQuality
	}
	if m.Scores.Belief != nil {
		belief = clamp01(*m.Scores.Belief)
	}
	if m.Scores.EvidenceQuality != nil {
		evidenceQuality = clamp01(*m.Scores.EvidenceQuality)
	}
	return belief, evidenceQuality
}

// mergeLearningPriority stores the learning-priority object under
// assessment.learning_priority, preserving the other assessment fields.
func mergeLearningPriority(assessment string, lp learningPriority) string {
	m := map[string]any{}
	if assessment != "" {
		_ = jsonx.Unmarshal([]byte(assessment), &m)
	}
	m["learning_priority"] = lp
	out, err := jsonx.Marshal(m)
	if err != nil {
		return assessment
	}
	return string(out)
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
