// Competitor analysis grounded in the knowledge base: for a hypothesis, retrieve
// topic-relevant corpus passages and have the LLM extract competing / alternative
// approaches and organisations mentioned there (with their strengths, weaknesses
// and maturity), citing the source. Stored under detail.competitors. Corpus-only
// by design — no external web search.

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

const competitorEvidenceTop = 8

type competitor struct {
	Name       string   `json:"name"`
	Approach   string   `json:"approach"`
	Strengths  []string `json:"strengths"`
	Weaknesses []string `json:"weaknesses"`
	Maturity   string   `json:"maturity"`
	Source     string   `json:"source"`
}

type competitorResult struct {
	Summary     string       `json:"summary"`
	Competitors []competitor `json:"competitors"`
}

// AnalyzeCompetitors finds competing approaches for a hypothesis the caller owns,
// grounded only in the corpus, and stores them under detail.competitors.
func (s *HypothesisService) AnalyzeCompetitors(ctx context.Context, ownerID, id string) (*commonv1.Hypothesis, error) {
	ctx, cancel := withDeadline(ctx, llmSinglePassTimeout)
	defer cancel()
	h, err := s.GetHypothesis(ctx, ownerID, false, id)
	if err != nil {
		return nil, err
	}
	resp, aerr := s.answerer.Answer(ctx, &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   competitorQuery(h),       // short topic → retrieval + rerank
		Prompt:  buildCompetitorPrompt(h), // long instruction → LLM only
		TopK:    competitorEvidenceTop,
	})
	if aerr != nil {
		return nil, aerr
	}
	res, perr := parseCompetitors(resp.GetAnswer())
	if perr != nil {
		return nil, perr
	}
	h.Detail = mergeCompetitors(h.GetDetail(), res, resp.GetModel())
	rev := &commonv1.HypothesisRevision{
		Action: actionEdited, EditorId: editorSystem,
		Summary: fmt.Sprintf("Анализ конкурентов по базе знаний: найдено %d", len(res.Competitors)),
	}
	if uerr := s.cat.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: h, Revision: rev}); uerr != nil {
		return nil, uerr
	}
	return s.GetHypothesis(ctx, ownerID, false, id)
}

func competitorQuery(h *commonv1.Hypothesis) string {
	q := h.GetTitle()
	if fa := h.GetFunctionArea(); fa != "" {
		q += " " + fa
	}
	return q
}

func buildCompetitorPrompt(h *commonv1.Hypothesis) string {
	var b strings.Builder
	b.WriteString("Гипотеза/технология: «")
	b.WriteString(h.GetTitle())
	b.WriteString("». Суть: ")
	b.WriteString(h.GetStatement())
	b.WriteString("\n\nТы — аналитик НИОКР. Опираясь ТОЛЬКО на приведённый выше контекст (выдержки из " +
		"документов базы знаний), выдели КОНКУРИРУЮЩИЕ или альтернативные подходы, технологии и организации, " +
		"которые решают ту же задачу. Для каждого укажи: название/организацию, суть подхода, сильные и слабые " +
		"стороны, зрелость (стадия/УГТ, если есть), и источник (имя документа). НЕ выдумывай конкурентов, " +
		"которых нет в контексте; если их нет — верни пустой список.")
	b.WriteString(untrustedContextInstruction)
	b.WriteString(langInstruction)
	b.WriteString("\nВерни СТРОГО JSON без markdown:\n")
	b.WriteString(`{"summary":"краткий вывод о конкурентном поле","competitors":[` +
		`{"name":"","approach":"","strengths":["..."],"weaknesses":["..."],"maturity":"","source":""}]}`)
	return b.String()
}

func parseCompetitors(answer string) (competitorResult, error) {
	start := strings.IndexByte(answer, '{')
	end := strings.LastIndexByte(answer, '}')
	if start < 0 || end <= start {
		return competitorResult{}, errors.New("no JSON object in competitor output")
	}
	raw := answer[start : end+1]
	var r competitorResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		if err2 := json.Unmarshal([]byte(repairJSONBackslashes(raw)), &r); err2 != nil {
			return competitorResult{}, fmt.Errorf("parse competitors: %w", err)
		}
	}
	return r, nil
}

// mergeCompetitors stores the competitor analysis under detail.competitors,
// preserving the other detail fields.
func mergeCompetitors(detail string, res competitorResult, model string) string {
	m := map[string]any{}
	if detail != "" {
		_ = json.Unmarshal([]byte(detail), &m)
	}
	items := make([]map[string]any, 0, len(res.Competitors))
	for _, c := range res.Competitors {
		items = append(items, map[string]any{
			keyName: c.Name, "approach": c.Approach, "strengths": c.Strengths,
			"weaknesses": c.Weaknesses, "maturity": c.Maturity, "source": c.Source,
		})
	}
	m["competitors"] = map[string]any{
		"summary": res.Summary, keyItems: items,
		keyModel: model, "analyzed_at": time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(m)
	if err != nil {
		return detail
	}
	return string(b)
}
