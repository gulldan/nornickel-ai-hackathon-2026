// Two-stage hypothesis generation, Stage 2 — enrichment + quality gate.
//
// Stage 1 (generate.go) produces many light "idea core" hypotheses fast and wide.
// Stage 2, here, deepens one hypothesis: it reads the FULL text of its source
// documents (not just the retrieved chunks) and, in a single LLM call, fills the
// rich passport fields Stage 1 deliberately skipped (causal chain, experiment
// plan, feasibility, mechanism, composition/process change, methods, failure
// modes). The merge never clobbers fields that generation or the experiment
// planner already wrote — same re-read-and-keep-evidence discipline as AssessTRL
// and StoreITC. Finally a quality gate archives junk (thin + ungrounded, or an
// exact duplicate) off the board.

package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	"github.com/example/main-service/internal/platform/jsonx"
	"github.com/example/main-service/internal/platform/logger"
	"github.com/example/main-service/internal/platform/runtimecfg"
)

const (
	// enrichEvidenceTop is a small supplementary retrieval depth: the full source
	// text supplied in the prompt is the primary context, so retrieval only adds a
	// little extra grounding.
	enrichEvidenceTop = 6
	// enrichMaxRunes caps the source text embedded in the prompt (~12k characters),
	// keeping the LLM context bounded regardless of document length.
	enrichMaxRunes = 12000
	// genQualityFloorDefault is the default schema-completeness floor for the gate.
	genQualityFloorDefault = 0.35
	// actionEnriched marks the revision appended by Stage-2 enrichment.
	actionEnriched = "enriched"
	// statusArchived is the board-hidden status the quality gate assigns to junk.
	statusArchived = "archived"
	// keyScore and keyMissing are assessment.schema keys (score + unmet checks).
	keyScore   = "score"
	keyMissing = "missing"
)

// genBreadthEnabled reports whether Stage-1 breadth generation is on
// (RAG_GEN_BREADTH, default true): the light schema for any count so more,
// weakly-grounded ideas are surfaced, with the rich fields deferred to Stage 2.
func genBreadthEnabled(ctx context.Context, ovr *runtimecfg.Overrides) bool {
	return ovr.GetBool(ctx, "RAG_GEN_BREADTH", true)
}

// useLightGenSchema decides whether generation asks for the light "idea core"
// schema. Breadth forces it for any count; without breadth the legacy count>3
// compact threshold applies, so disabling the flag restores the old behaviour.
func useLightGenSchema(ctx context.Context, ovr *runtimecfg.Overrides, count int) bool {
	return genBreadthEnabled(ctx, ovr) || compactGenPrompt(count)
}

// genQualityFloor is the schema-completeness score below which a still thin,
// mechanism-less and plan-less hypothesis is archived by the quality gate
// (RAG_GEN_QUALITY_FLOOR, default 0.35).
func genQualityFloor(ctx context.Context, ovr *runtimecfg.Overrides) float64 {
	return ovr.GetFloat(ctx, "RAG_GEN_QUALITY_FLOOR", genQualityFloorDefault)
}

// enrichResult is the rich-fields payload the LLM emits during Stage-2
// enrichment. The field shapes mirror genItem so the generation cleaners
// (cleanCausalChain / cleanExperiment / cleanFeasibility) can be reused.
type enrichResult struct {
	CausalChain             []causalStep   `json:"causal_chain"`
	Experiment              *experimentVal `json:"experiment_plan"`
	Feasibility             []feasibility  `json:"feasibility"`
	MicrostructureMechanism string         `json:"microstructure_mechanism"`
	CompositionChange       string         `json:"composition_change"`
	ProcessChange           string         `json:"process_change"`
	CharacterizationMethods []string       `json:"characterization_methods"`
	TestMethods             []string       `json:"test_methods"`
	FailureModes            []string       `json:"failure_modes"`
}

// EnrichHypothesis deepens a hypothesis the caller owns: it reads the full text of
// its source documents, asks the LLM (one structured pass) to fill the rich
// passport fields, merges them into detail without clobbering existing values,
// recomputes ranking, and runs the quality gate that may archive junk. It re-reads
// the row first (keeping evidence with their ids), the same discipline as AssessTRL.
func (s *HypothesisService) EnrichHypothesis(ctx context.Context, ownerID, id string) (*commonv1.Hypothesis, error) {
	ctx, cancel := withDeadline(ctx, llmSinglePassTimeout)
	defer cancel()
	h, err := s.GetHypothesis(ctx, ownerID, false, id)
	if err != nil {
		return nil, err
	}
	docText := s.sourceDocumentsText(ctx, ownerID, h)
	resp, aerr := s.answerer.Answer(opCtx(ctx, "enrich"), &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   h.GetStatement(),              // short → supplementary retrieval
		Prompt:  buildEnrichPrompt(h, docText), // full source text → LLM only
		TopK:    enrichEvidenceTop,
	})
	if aerr != nil {
		return nil, aerr
	}
	res, perr := parseEnrich(resp.GetAnswer())
	if perr != nil {
		return nil, perr
	}
	model := resp.GetModel()
	h.Detail = mergeEnrichDetail(h.GetDetail(), res, model)

	score, missing, hasMechanism, hasPlan := detailSignals(h.GetDetail(), h.GetMeasurable())
	h.Assessment = mergeEnrichAssessment(h.GetAssessment(), score, missing, model)

	grounded := hasGroundedEvidence(h)
	duplicate := s.isDuplicateStatement(ctx, ownerID, id, h.GetStatement())
	reason, archived := qualityGate(score, hasMechanism, hasPlan, grounded, duplicate, genQualityFloor(ctx, s.ovr))
	// The gate culls machine-generated junk only — it must never revert a human
	// decision. An approved hypothesis keeps its status (it is still enriched).
	if archived && h.GetStatus() == statusApproved {
		archived, reason = false, ""
	}
	summary := "Обогащение по полному тексту источников"
	if archived {
		h.Status = statusArchived
		summary = "Отсев гипотезы: " + archiveReasonRU(reason)
		logger.From(ctx).Info().
			Str("hypothesis_id", id).Str("reason", reason).
			Msg("hypothesis archived by quality gate")
	}
	// Detail, schema and status just changed → refresh the transparent composite so
	// the board re-ranks on the enriched features, not stale ones.
	s.applyRanking(ctx, h)
	rev := &commonv1.HypothesisRevision{HypothesisId: id, Action: actionEnriched, EditorId: editorSystem, Summary: summary}
	if uerr := s.cat.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: h, Revision: rev}); uerr != nil {
		return nil, uerr
	}
	return s.GetHypothesis(ctx, ownerID, false, id)
}

// sourceDocumentsText concatenates the full text of the hypothesis's evidence
// documents (deduplicated, ordered by chunk index), capped at enrichMaxRunes. A
// nil chunk reader or any per-document error yields whatever text was gathered so
// far, so enrichment degrades to retrieval-only context rather than failing.
func (s *HypothesisService) sourceDocumentsText(ctx context.Context, ownerID string, h *commonv1.Hypothesis) string {
	if s.chunks == nil {
		return ""
	}
	seen := make(map[string]struct{})
	var b strings.Builder
	runes := 0
	for _, e := range h.GetEvidence() {
		docID := strings.TrimSpace(e.GetDocumentId())
		if docID == "" {
			continue
		}
		if _, ok := seen[docID]; ok {
			continue
		}
		seen[docID] = struct{}{}
		chunks, err := s.chunks.DocumentChunks(ctx, ownerID, docID)
		if err != nil {
			continue
		}
		for _, c := range chunks {
			text := strings.TrimSpace(c.GetText())
			if text == "" {
				continue
			}
			b.WriteString(text)
			b.WriteByte('\n')
			runes += len([]rune(text)) + 1
			if runes >= enrichMaxRunes {
				return capRunes(b.String(), enrichMaxRunes)
			}
		}
	}
	return capRunes(b.String(), enrichMaxRunes)
}

// capRunes truncates s to at most limit runes without splitting a rune.
func capRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}

// buildEnrichPrompt asks for ONLY the rich passport fields, grounded in the
// hypothesis and the full source text. It never asks the model to re-state title
// or scores — Stage 1 owns those.
func buildEnrichPrompt(h *commonv1.Hypothesis, docText string) string {
	var b strings.Builder
	b.WriteString("Гипотеза: «")
	b.WriteString(h.GetStatement())
	b.WriteString("».")
	if r := h.GetRationale(); r != "" {
		b.WriteString(" Обоснование: ")
		b.WriteString(r)
	}
	if d := compactText(h.GetDetail()); d != "" {
		b.WriteString("\nТекущий паспорт гипотезы (JSON): ")
		b.WriteString(d)
	}
	if docText != "" {
		b.WriteString("\n\nПолный текст документов-источников:\n")
		b.WriteString(docText)
	}
	b.WriteString("\n\nТы — ассистент НИОКР. Опираясь на гипотезу и приведённый выше полный текст источников, " +
		"дополни паспорт гипотезы недостающими деталями: причинно-следственная цепочка (для материаловедения: " +
		"состав → процесс → микроструктура → свойство → KPI; для обогащения руды и металлургии: " +
		"питание/сырьё → операция/аппарат → режим → технологический показатель → KPI); " +
		"план эксперимента (материалы, параметры процесса " +
		"с диапазонами, методы характеризации и испытаний, контроли, ИЗМЕРИМЫЙ критерий успеха, ориентировочные " +
		"стоимость и срок); реализуемость по аспектам (научная реализуемость, масштабирование, безопасность/экология, " +
		"цепочка поставок) с уровнем low/medium/high; микроструктурный механизм; изменения состава и процесса; методы " +
		"характеризации и испытаний; режимы отказа. Заполняй ТОЛЬКО на основе источников; если данных нет — оставь поле " +
		"пустым ([] или \"\"), не выдумывай.")
	b.WriteString("\nВерни СТРОГО JSON без markdown по схеме:\n")
	b.WriteString(`{"causal_chain":[{"stage":"состав|процесс|микроструктура|свойство|питание/сырьё|операция|режим|показатель|KPI",` +
		`"change":"что меняется"}],` +
		`"experiment_plan":{"experiment_type":"new_alloy|process_route|coating_corrosion|battery_material|` +
		`ore_beneficiation|metallurgy_process|generic",` +
		`"sections":[{"title":"этап","purpose":"зачем","items":["конкретное действие"]}],` +
		`"materials":["материал/реактив"],"process_parameters":[{"name":"параметр","range":"диапазон с единицами"}],` +
		`"characterization_methods":["SEM","XRD"],"test_methods":["метод испытания"],"controls":["базовый образец"],` +
		`"success_criteria":"измеримый критерий успеха","estimated_cost":"low|medium|high","estimated_time":"days|weeks|months",` +
		`"risks":["ключевой риск"],"variables":["управляемая переменная"],"methods":["метод"],"horizon":"срок"},` +
		`"feasibility":[{"aspect":"научная реализуемость|масштабирование|безопасность/экология|цепочка поставок",` +
		`"level":"low|medium|high","note":"кратко почему"}],` +
		`"microstructure_mechanism":"ожидаемый механизм","composition_change":"что меняем в составе",` +
		`"process_change":"какой техрежим/процесс меняем","characterization_methods":["SEM","TEM"],` +
		`"test_methods":["tensile test"],"failure_modes":["что может пойти не так"]}`)
	b.WriteString(untrustedContextInstruction)
	b.WriteString(langInstruction)
	b.WriteString("\nТолько JSON-объект.")
	return b.String()
}

// parseEnrich tolerantly extracts the JSON object from the model output (same
// {…} extraction + backslash repair as verify/plan).
func parseEnrich(answer string) (enrichResult, error) {
	start := strings.IndexByte(answer, '{')
	end := strings.LastIndexByte(answer, '}')
	if start < 0 || end <= start {
		return enrichResult{}, errors.New("no JSON object in enrich output")
	}
	raw := answer[start : end+1]
	var res enrichResult
	if err := jsonx.Unmarshal([]byte(raw), &res); err != nil {
		if err2 := jsonx.Unmarshal([]byte(repairJSONBackslashes(raw)), &res); err2 != nil {
			return enrichResult{}, fmt.Errorf("parse enrich result: %w", err)
		}
	}
	return res, nil
}

// mergeEnrichDetail fills the rich detail fields, setting each ONLY when it is
// absent or empty so generation and the experiment planner are never clobbered.
// It also records enrichment provenance under detail.enrichment.
func mergeEnrichDetail(detail string, res enrichResult, model string) string {
	m := map[string]any{}
	if detail != "" {
		_ = jsonx.Unmarshal([]byte(detail), &m)
	}
	setDetailStrIfAbsent(m, "microstructure_mechanism", compactText(res.MicrostructureMechanism))
	setDetailStrIfAbsent(m, "composition_change", compactText(res.CompositionChange))
	setDetailStrIfAbsent(m, "process_change", compactText(res.ProcessChange))
	setDetailSliceIfAbsent(m, "characterization_methods", compactSlice(res.CharacterizationMethods))
	setDetailSliceIfAbsent(m, "test_methods", compactSlice(res.TestMethods))
	setDetailSliceIfAbsent(m, "failure_modes", compactSlice(res.FailureModes))
	if _, ok := m["causal_chain"]; !ok {
		if chain := cleanCausalChain(res.CausalChain); len(chain) > 0 {
			m["causal_chain"] = chain
		}
	}
	if _, ok := m["experiment_plan"]; !ok {
		if plan := cleanExperiment(res.Experiment); plan != nil {
			m["experiment_plan"] = plan
		}
	}
	if _, ok := m["feasibility"]; !ok {
		if fz := cleanFeasibility(res.Feasibility); len(fz) > 0 {
			m["feasibility"] = fz
		}
	}
	m["enrichment"] = map[string]any{keyModel: model, "enriched_at": time.Now().UTC().Format(time.RFC3339)}
	b, err := jsonx.Marshal(m)
	if err != nil {
		return detail
	}
	return string(b)
}

// setDetailStrIfAbsent stores a non-empty val under key unless a non-empty value
// is already present.
func setDetailStrIfAbsent(m map[string]any, key, val string) {
	if val == "" {
		return
	}
	if cur, ok := m[key]; ok {
		if s, isStr := cur.(string); !isStr || strings.TrimSpace(s) != "" {
			return
		}
	}
	m[key] = val
}

// setDetailSliceIfAbsent stores a non-empty slice under key unless a non-empty
// array is already present.
func setDetailSliceIfAbsent(m map[string]any, key string, val []string) {
	if len(val) == 0 {
		return
	}
	if cur, ok := m[key]; ok {
		if arr, isArr := cur.([]any); isArr && len(arr) > 0 {
			return
		}
	}
	m[key] = val
}

// mergeEnrichAssessment recomputes assessment.schema from the enriched detail and
// records an enrichment marker (assessment.enrichment) that EnsureEnrich reads
// from board rows to know enrichment already ran. Other assessment fields are
// preserved.
func mergeEnrichAssessment(assessment string, score float64, missing []string, model string) string {
	m := map[string]any{}
	if assessment != "" {
		_ = jsonx.Unmarshal([]byte(assessment), &m)
	}
	m["schema"] = map[string]any{keyScore: score, "complete": len(missing) == 0, keyMissing: missing}
	m["enrichment"] = map[string]any{keyModel: model, "enriched_at": time.Now().UTC().Format(time.RFC3339)}
	b, err := jsonx.Marshal(m)
	if err != nil {
		return assessment
	}
	return string(b)
}

// detailSignals reconstructs the schema-completeness signals from the merged
// detail, reusing schemaCompleteness so the gate and the board agree.
func detailSignals(detail string, measurable bool) (float64, []string, bool, bool) {
	m := map[string]any{}
	if detail != "" {
		_ = jsonx.Unmarshal([]byte(detail), &m)
	}
	item := genItem{
		MaterialSystem:          mapString(m, "material_system"),
		CompositionChange:       mapString(m, "composition_change"),
		ProcessChange:           mapString(m, "process_change"),
		MicrostructureMechanism: mapString(m, "microstructure_mechanism"),
		TargetProperty:          mapString(m, "target_property"),
	}
	if arr, ok := m["causal_chain"].([]any); ok && len(arr) > 0 {
		item.CausalChain = []causalStep{{}} // only len>0 matters to schemaCompleteness
	}
	if plan, ok := m["experiment_plan"].(map[string]any); ok && planHasSuccessCriteria(plan) {
		item.Experiment = &experimentVal{SuccessCriteria: "set"}
	}
	score, missing := schemaCompleteness(item, measurable)
	hasMechanism := item.MicrostructureMechanism != "" || len(item.CausalChain) > 0
	hasPlan := item.Experiment != nil && item.Experiment.SuccessCriteria != ""
	return score, missing, hasMechanism, hasPlan
}

// planHasSuccessCriteria reports whether the plan carries a non-empty success
// criterion, tolerating both the string (generation) and []string (planner)
// storage shapes.
func planHasSuccessCriteria(plan map[string]any) bool {
	switch v := plan["success_criteria"].(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	default:
		return false
	}
}

// mapString reads a string value from a decoded JSON map (missing/typed-wrong ⇒ "").
func mapString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// hasGroundedEvidence reports whether any evidence row carries a real document id.
func hasGroundedEvidence(h *commonv1.Hypothesis) bool {
	for _, e := range h.GetEvidence() {
		if strings.TrimSpace(e.GetDocumentId()) != "" {
			return true
		}
	}
	return false
}

// qualityGate decides whether an enriched hypothesis is junk that should leave the
// board. A hypothesis is archived when it is an exact duplicate, or when it is
// still thin (schema below the floor) with neither a mechanism nor a plan, or when
// it is ungrounded and thin. The returned reason is logged and recorded.
func qualityGate(score float64, hasMechanism, hasPlan, grounded, duplicate bool, floor float64) (string, bool) {
	switch {
	case duplicate:
		return "duplicate statement", true
	case score < floor && !hasMechanism && !hasPlan:
		return "low schema completeness and no mechanism/plan after enrichment", true
	case !grounded && score < floor:
		return "no grounded evidence and low schema completeness", true
	default:
		return "", false
	}
}

// archiveReasonRU renders a user-facing (Russian) archive reason for the revision.
func archiveReasonRU(reason string) string {
	switch reason {
	case "duplicate statement":
		return "дубликат формулировки"
	case "low schema completeness and no mechanism/plan after enrichment":
		return "низкая полнота и нет механизма/плана"
	case "no grounded evidence and low schema completeness":
		return "нет привязки к документам и низкая полнота"
	default:
		return reason
	}
}

// isDuplicateStatement reports whether another live hypothesis of the same owner
// has the same normalized statement. Any catalog error yields false (best-effort).
func (s *HypothesisService) isDuplicateStatement(ctx context.Context, ownerID, id, statement string) bool {
	norm := normalizedStatement(statement)
	if norm == "" {
		return false
	}
	hs, err := s.cat.ListHypotheses(ctx, &dbv1.ListHypothesesRequest{OwnerId: ownerID})
	if err != nil {
		return false
	}
	for _, other := range hs {
		if other.GetId() == id {
			continue
		}
		if other.GetStatus() == statusArchived || other.GetStatus() == statusRejected {
			continue
		}
		if normalizedStatement(other.GetStatement()) != norm {
			continue
		}
		// Asymmetric tie-break: a hypothesis is a "duplicate to archive" only when a
		// same-statement twin has a smaller id. So the lowest id always survives, and
		// two identical statements enriched concurrently cannot archive each other
		// (each would otherwise see the other still live → total loss of both).
		if other.GetId() < id {
			return true
		}
	}
	return false
}

// normalizedStatement lowercases s and keeps only letters/digits as space-joined
// tokens, so trivial punctuation/whitespace differences do not hide a duplicate.
func normalizedStatement(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// enrichmentDone reports whether Stage-2 enrichment already ran, read from the
// assessment marker so EnsureEnrich can gate off board rows.
func enrichmentDone(assessment string) bool {
	if assessment == "" {
		return false
	}
	var m struct {
		Enrichment struct {
			EnrichedAt string `json:"enriched_at"`
		} `json:"enrichment"`
	}
	if err := jsonx.Unmarshal([]byte(assessment), &m); err != nil {
		return false
	}
	return m.Enrichment.EnrichedAt != ""
}
