// KPI prompt parsing: the create-goal dialog sends one free-text prompt (the
// experts asked for a single field, no dropdowns); this turns it into the
// structured goal the DB stores. One LLM pass, best-effort: any failure falls
// back to a deterministic split so the endpoint never errors.

package application

import (
	"context"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	"github.com/example/main-service/internal/platform/jsonx"
)

const (
	// kpiParseTimeout bounds the single LLM pass.
	kpiParseTimeout = 45 * time.Second
	// kpiParseTopK keeps a little corpus grounding without inflating latency.
	kpiParseTopK = 4
)

// KPIParseResult is the structured goal extracted from a free-text prompt.
// Field names match kpiInput so the frontend passes it straight to POST /kpis.
type KPIParseResult struct {
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Metric       string   `json:"metric"`
	Unit         string   `json:"unit"`
	Direction    string   `json:"direction"`
	FunctionArea string   `json:"function_area"`
	Constraints  string   `json:"constraints"`
	Baseline     *float64 `json:"baseline"`
	Target       *float64 `json:"target"`
}

// ParseKPIPrompt extracts a structured goal from one free-text prompt. It never
// fails: when the LLM is unavailable or unparseable the first line becomes the
// title and the full prompt the description.
func (s *HypothesisService) ParseKPIPrompt(ctx context.Context, ownerID, prompt string) KPIParseResult {
	fallback := kpiParseFallback(prompt)
	if s.answerer == nil || strings.TrimSpace(prompt) == "" {
		return fallback
	}
	llmCtx, cancel := withDeadline(ctx, kpiParseTimeout)
	defer cancel()
	resp, err := s.answerer.Answer(opCtx(llmCtx, "kpi_parse"), &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   firstSentence(compactText(prompt), 200),
		Prompt:  buildKPIParsePrompt(prompt),
		TopK:    kpiParseTopK,
	})
	if err != nil || resp == nil {
		return fallback
	}
	parsed, ok := parseKPIParseAnswer(resp.GetAnswer())
	if !ok {
		return fallback
	}
	if parsed.Title == "" {
		parsed.Title = fallback.Title
	}
	if parsed.Description == "" {
		parsed.Description = prompt
	}
	parsed.Direction = normalizeKPIDirection(parsed.Direction, parsed.Title+" "+parsed.Metric+" "+prompt)
	return parsed
}

func buildKPIParsePrompt(prompt string) string {
	var b strings.Builder
	b.WriteString("Ты помогаешь инженеру сформулировать цель улучшения производства ")
	b.WriteString("(обогащение руды, металлургия). Разбери его формулировку на структурные поля.\n\n")
	b.WriteString("Формулировка инженера:\n\"\"\"\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\"\"\"\n\n")
	b.WriteString("Верни СТРОГО один JSON-объект без пояснений:\n")
	b.WriteString(`{"title": "короткое название цели (до 90 символов, по-русски)",` + "\n")
	b.WriteString(` "description": "суть цели своими словами, 1-3 предложения",` + "\n")
	b.WriteString(` "metric": "измеряемая величина, например: извлечение меди",` + "\n")
	b.WriteString(` "unit": "единица измерения, например: %",` + "\n")
	b.WriteString(` "direction": "increase | decrease | maintain",` + "\n")
	b.WriteString(` "function_area": "область, например: флотация, плавка, измельчение",` + "\n")
	b.WriteString(` "baseline": число — текущее значение показателя, или null если не названо,` + "\n")
	b.WriteString(` "target": число — целевое значение показателя, или null если не названо,` + "\n")
	b.WriteString(` "constraints": "все ограничения из формулировки (бюджет, оборудование, сбыт, сроки)` +
		` одной строкой; пустая строка если их нет"}` + "\n\n")
	b.WriteString("Не выдумывай ограничения и числа, которых нет в формулировке.")
	return b.String()
}

func parseKPIParseAnswer(answer string) (KPIParseResult, bool) {
	var out KPIParseResult
	raw := stripJSONFence(answer)
	if start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); start >= 0 && end > start {
		raw = raw[start : end+1]
	}
	if err := jsonx.Unmarshal([]byte(raw), &out); err != nil {
		return KPIParseResult{}, false
	}
	out.Title = firstSentence(compactText(out.Title), 120)
	out.Metric = compactText(out.Metric)
	out.Unit = compactText(out.Unit)
	out.FunctionArea = compactText(out.FunctionArea)
	out.Constraints = strings.TrimSpace(out.Constraints)
	out.Description = strings.TrimSpace(out.Description)
	return out, out.Title != "" || out.Metric != "" || out.Description != ""
}

// kpiParseFallback splits the prompt deterministically: first line (or
// sentence) is the title, the whole prompt the description.
func kpiParseFallback(prompt string) KPIParseResult {
	trimmed := strings.TrimSpace(prompt)
	title := trimmed
	if i := strings.IndexByte(trimmed, '\n'); i > 0 {
		title = trimmed[:i]
	}
	title = firstSentence(compactText(title), 120)
	if title == "" {
		title = "Новая цель"
	}
	return KPIParseResult{
		Title:       title,
		Description: trimmed,
		Direction:   normalizeKPIDirection("", trimmed),
	}
}
