// Auto-tagging of hypotheses with scientific-specialty taxonomies (ГРНТИ, ВАК
// specialties, Scopus ASJC). LLM zero-shot classification: the model
// is given the hypothesis plus the candidate code lists and picks the best
// matches; returned codes are validated against the embedded taxonomies (so
// hallucinated codes are dropped). The canonical names become hypothesis.tags
// (board facets) and the structured codes are kept under detail.classification.

package application

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

const tagsPerTaxonomy = 3

//go:embed taxonomy/grnti.json
var grntiRaw []byte

//go:embed taxonomy/vak.json
var vakRaw []byte

//go:embed taxonomy/asjc.json
var asjcRaw []byte

type taxEntry struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func loadTax(raw []byte) []taxEntry {
	var t []taxEntry
	_ = json.Unmarshal(raw, &t)
	return t
}

func taxIndex(entries []taxEntry) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[strings.TrimSpace(e.Code)] = e.Name
	}
	return m
}

type classification struct {
	ResearchType string     `json:"research_type"`
	Grnti        []taxEntry `json:"grnti"`
	Vak          []taxEntry `json:"vak"`
	Asjc         []taxEntry `json:"asjc"`
	Model        string     `json:"model"`
	TaggedAt     string     `json:"tagged_at"`
}

// TagHypothesis classifies a hypothesis the caller owns into ГРНТИ / ВАК / ASJC,
// validates the codes against the embedded taxonomies, and stores the result.
func (s *HypothesisService) TagHypothesis(ctx context.Context, ownerID, id string) (*commonv1.Hypothesis, error) {
	ctx, cancel := withDeadline(ctx, llmSinglePassTimeout)
	defer cancel()
	h, err := s.GetHypothesis(ctx, ownerID, false, id)
	if err != nil {
		return nil, err
	}
	resp, aerr := s.answerer.Answer(ctx, &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   h.GetTitle(), // minimal retrieval; classification is from the prompt
		Prompt:  buildTagPrompt(h),
		TopK:    1,
	})
	if aerr != nil {
		return nil, aerr
	}
	raw, perr := parseClassification(resp.GetAnswer())
	if perr != nil {
		return nil, perr
	}
	research := normalizeResearchTag(raw.ResearchType)
	if research == "" {
		research = inferResearchTag(
			h.GetMeasurable(),
			h.GetTitle(),
			h.GetStatement(),
			h.GetRationale(),
			h.GetSourceType(),
			h.GetDetail(),
		)
	}
	cls := classification{
		ResearchType: research,
		Grnti:        validate(raw.Grnti, taxIndex(loadTax(grntiRaw))),
		Vak:          validate(raw.Vak, taxIndex(loadTax(vakRaw))),
		Asjc:         validate(raw.Asjc, taxIndex(loadTax(asjcRaw))),
		Model:        resp.GetModel(),
		TaggedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	h.Tags = withResearchTag(mergeTags(h.GetTags(), tagLabels(cls)), cls.ResearchType)
	h.Detail = mergeClassification(h.GetDetail(), cls)
	rev := &commonv1.HypothesisRevision{
		Action: actionEdited, EditorId: editorSystem, Summary: "Проставлены теги и научные специальности (ГРНТИ/ВАК/Scopus)",
	}
	if uerr := s.cat.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: h, Revision: rev}); uerr != nil {
		return nil, uerr
	}
	return s.GetHypothesis(ctx, ownerID, false, id)
}

// validate keeps only codes present in the taxonomy, replaces the LLM's name with
// the canonical one, dedups, and caps the count.
func validate(picked []taxEntry, index map[string]string) []taxEntry {
	out := make([]taxEntry, 0, tagsPerTaxonomy)
	seen := map[string]bool{}
	for _, p := range picked {
		code := strings.TrimSpace(p.Code)
		name, ok := index[code]
		if !ok || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, taxEntry{Code: code, Name: name})
		if len(out) >= tagsPerTaxonomy {
			break
		}
	}
	return out
}

func tagLabels(cls classification) []string {
	labels := make([]string, 0, tagsPerTaxonomy*3)
	for _, e := range cls.Grnti {
		labels = append(labels, e.Name)
	}
	for _, e := range cls.Vak {
		labels = append(labels, e.Code+" "+e.Name)
	}
	for _, e := range cls.Asjc {
		labels = append(labels, e.Name)
	}
	return labels
}

func buildTagPrompt(h *commonv1.Hypothesis) string {
	var b strings.Builder
	b.WriteString("Гипотеза: «")
	b.WriteString(h.GetTitle())
	b.WriteString("». Суть: ")
	b.WriteString(h.GetStatement())
	b.WriteString("\n\nОпредели тип исследования и научные специальности этой гипотезы. Тип выбирай " +
		"строго один: «теоретическое исследование» — модели, методы, расчёты, симуляции, " +
		"теоретические обоснования; «практическое» — внедрение на производстве, опытная/промышленная " +
		"эксплуатация или описанный положительный/отрицательный эффект. Выбирай ТОЛЬКО из приведённых ниже " +
		"списков и возвращай существующие коды (до 3 на каждый классификатор, самые релевантные). " +
		"Не придумывай коды, которых нет в списке.\n")
	writeCandidates(&b, "ГРНТИ", loadTax(grntiRaw))
	writeCandidates(&b, "ВАК", loadTax(vakRaw))
	writeCandidates(&b, "Scopus ASJC", loadTax(asjcRaw))
	b.WriteString("\nВерни СТРОГО JSON без markdown: " +
		`{"research_type":"теоретическое исследование|практическое",` +
		`"grnti":[{"code":"","name":""}],"vak":[{"code":"","name":""}],` +
		`"asjc":[{"code":"","name":""}]}`)
	return b.String()
}

func writeCandidates(b *strings.Builder, title string, entries []taxEntry) {
	b.WriteString("\nКлассификатор ")
	b.WriteString(title)
	b.WriteString(":\n")
	for _, e := range entries {
		b.WriteString(e.Code)
		b.WriteString(" — ")
		b.WriteString(e.Name)
		b.WriteString("\n")
	}
}

func parseClassification(answer string) (classification, error) {
	start := strings.IndexByte(answer, '{')
	end := strings.LastIndexByte(answer, '}')
	if start < 0 || end <= start {
		return classification{}, errors.New("no JSON object in tag output")
	}
	raw := answer[start : end+1]
	var c classification
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		if err2 := json.Unmarshal([]byte(repairJSONBackslashes(raw)), &c); err2 != nil {
			return classification{}, fmt.Errorf("parse classification: %w", err)
		}
	}
	return c, nil
}

func mergeClassification(detail string, cls classification) string {
	m := map[string]any{}
	if detail != "" {
		_ = json.Unmarshal([]byte(detail), &m)
	}
	m["classification"] = cls
	b, err := json.Marshal(m)
	if err != nil {
		return detail
	}
	return string(b)
}
