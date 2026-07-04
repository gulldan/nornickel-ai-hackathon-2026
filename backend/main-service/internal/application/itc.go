// ITC (Индекс технологичности) — a deterministic, corpus-derived technology index
// that replaces the LLM-guessed novelty/confidence as the primary score. The four
// components (scientific momentum / novelty / impact / hype maturity), the 1–10 band
// and TechScore are computed OUTSIDE the request path by the deterministic engine
// (itc-worker/itc.py) from bibliometrics + embeddings + the evidence graph; this
// file only stores the result safely and serves the methodology rubric.

package application

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

//go:embed itc_rubric.json
var itcRubricRaw []byte

// ITCRubric exposes the raw ITC methodology rubric (components, bands, TechScore
// formula) so the UI can explain how the score is computed.
func (s *HypothesisService) ITCRubric() []byte { return itcRubricRaw }

// ITCInput is the computed ITC payload from the deterministic engine: the
// resolved theme link (primary_cluster_id) and the ITC object to store under
// assessment.itc. Composite is accepted for wire compatibility but ignored.
type ITCInput struct {
	ClusterID string          `json:"cluster_id"`
	ITC       json.RawMessage `json:"itc"`
	Composite *float64        `json:"composite_score"`
}

// StoreITC records a deterministically-computed ITC on a hypothesis the caller
// owns: it links the hypothesis to its theme (primary_cluster_id) and stores
// the ITC breakdown under assessment.itc. It re-reads the row first (keeping
// evidence with their ids) so the update never duplicates evidence — same
// discipline as AssessTRL.
func (s *HypothesisService) StoreITC(ctx context.Context, ownerID, id string, in ITCInput) (*commonv1.Hypothesis, error) {
	h, err := s.GetHypothesis(ctx, ownerID, false, id)
	if err != nil {
		return nil, err
	}
	if in.ClusterID != "" {
		cid := in.ClusterID
		h.PrimaryClusterId = &cid
	}
	// in.Composite is intentionally NOT applied: composite_score is always
	// computed by the rank-v1 scorer, never taken from the ITC payload.
	h.Assessment = mergeITC(h.GetAssessment(), in.ITC)
	rev := &commonv1.HypothesisRevision{
		Action: actionScoreOverride, EditorId: editorSystem,
		Summary: fmt.Sprintf("Индекс технологичности: %d/10", itcBand(in.ITC)),
	}
	if uerr := s.cat.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: h, Revision: rev}); uerr != nil {
		return nil, uerr
	}
	return s.GetHypothesis(ctx, ownerID, false, id)
}

// mergeITC stores the ITC object under assessment.itc, preserving other fields.
func mergeITC(assessment string, itc json.RawMessage) string {
	m := map[string]any{}
	if assessment != "" {
		_ = json.Unmarshal([]byte(assessment), &m)
	}
	var v any
	if len(itc) > 0 && json.Valid(itc) {
		_ = json.Unmarshal(itc, &v)
	}
	m["itc"] = v
	b, err := json.Marshal(m)
	if err != nil {
		return assessment
	}
	return string(b)
}

// itcBand extracts the 1..10 band from an ITC payload for the revision summary.
func itcBand(itc json.RawMessage) int {
	var v struct {
		Score int `json:"score"`
	}
	_ = json.Unmarshal(itc, &v)
	return v.Score
}
