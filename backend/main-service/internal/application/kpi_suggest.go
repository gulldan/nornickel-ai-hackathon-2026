// KPI suggestion: mine candidate R&D goals (KPIs) from a representative sample of
// the corpus. Authors of scientific papers optimize measurable properties
// (strength, recovery, yield, efficiency, porosity…); this surfaces those as
// measurable goals worth checking against the knowledge base. It is a read-only
// advisory step — it never creates KPIs, only proposes candidates the user can
// accept on /kpi. Kept in its own file so it evolves independently of generate.go.

package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	"github.com/example/main-service/internal/platform/jsonx"
)

const (
	// kpiSuggestMax caps the candidates returned (spec: 5–10 diverse goals).
	kpiSuggestMax = 10
	// kpiSuggestSampleMax bounds the representative documents fed to the LLM so
	// the prompt stays within a sane context budget (spec: 8–15 diverse docs).
	kpiSuggestSampleMax = 15
	// kpiSuggestPerCluster keeps the sample diverse: at most this many
	// representatives per topic cluster, so one large cluster cannot dominate.
	kpiSuggestPerCluster = 2
	// kpiSuggestTopK is the retrieval depth for the supplementary corpus context
	// the answerer prepends to the embedded sample.
	kpiSuggestTopK = 12
	// kpiSuggestTimeout bounds the single LLM pass (best-effort advisory).
	kpiSuggestTimeout = 90 * time.Second
	// kpiSuggestFallbackQuery biases retrieval toward measurable-property passages
	// when clustering has not run yet and no cluster labels are available.
	kpiSuggestFallbackQuery = "измеримые показатели прочность извлечение выход КПД эффективность " +
		"пористость коррозионная стойкость ёмкость производительность оптимизация улучшение"
)

// KPISuggestion is one candidate R&D goal mined from the corpus. It is advisory:
// the /kpi page pre-fills the "create goal" form from it, but nothing is
// persisted until the user accepts. Field names match the CreateKPI input so the
// frontend can pass it straight through.
type KPISuggestion struct {
	Title        string `json:"title"`
	Metric       string `json:"metric"`
	Unit         string `json:"unit"`
	Direction    string `json:"direction"` // increase | decrease | maintain
	FunctionArea string `json:"function_area"`
	Rationale    string `json:"rationale"`
}

// kpiSample is one representative document (with its topic) embedded in the
// suggestion prompt: a human-readable title, the topic it belongs to and a short
// content snippet the LLM extracts measurable goals from.
type kpiSample struct {
	title   string
	topic   string
	snippet string
}

// SuggestKPIs mines 5–10 candidate R&D goals (KPIs) from a representative,
// topic-diverse sample of the owner's corpus. It reuses the same VLLM answerer as
// generation: cluster representatives are embedded in the prompt and a broad
// corpus query supplies extra grounding. It is strictly advisory and best-effort:
// a missing corpus, an unavailable LLM or an unparseable reply all yield an empty
// list (never an error), so the /kpi page degrades to a friendly empty state
// instead of a 500.
func (s *HypothesisService) SuggestKPIs(ctx context.Context, ownerID string) ([]KPISuggestion, error) {
	if s.answerer == nil {
		return nil, nil
	}
	samples, query := s.kpiCorpusSample(ctx, ownerID)

	llmCtx, cancel := withDeadline(ctx, kpiSuggestTimeout)
	defer cancel()
	resp, err := s.answerer.Answer(opCtx(llmCtx, "kpi_suggest"), &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   query,
		Prompt:  buildKPISuggestPrompt(samples),
		TopK:    kpiSuggestTopK,
	})
	if err != nil || resp == nil {
		return nil, nil // LLM unavailable ⇒ empty list, not a failure
	}
	return dedupKPISuggestions(parseKPISuggestions(resp.GetAnswer())), nil
}

// kpiCorpusSample builds a topic-diverse sample of the corpus from cluster
// representatives (round-robin across clusters so no single topic dominates) plus
// a broad retrieval query from the cluster labels. When clustering has not run
// yet both may be empty; the caller then relies on retrieval alone with a generic
// measurable-property query.
func (s *HypothesisService) kpiCorpusSample(ctx context.Context, ownerID string) ([]kpiSample, string) {
	clusters, err := s.cat.ListClusters(ctx, ownerID)
	if err != nil || len(clusters) == 0 {
		return nil, kpiSuggestFallbackQuery
	}
	// Stable order so the sample (and query) are deterministic run to run and the
	// largest topics lead.
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].GetDocumentCount() != clusters[j].GetDocumentCount() {
			return clusters[i].GetDocumentCount() > clusters[j].GetDocumentCount()
		}
		return clusters[i].GetLabel() < clusters[j].GetLabel()
	})

	queryTerms := make([]string, 0, len(clusters))
	for _, c := range clusters {
		if label := compactText(c.GetLabel()); label != "" {
			queryTerms = append(queryTerms, label)
		}
	}

	samples := make([]kpiSample, 0, kpiSuggestSampleMax)
	seen := map[string]struct{}{}
	// Round-robin: take at most one representative per cluster per pass so many
	// topics are represented before any single cluster contributes a second doc.
	for pass := 0; pass < kpiSuggestPerCluster && len(samples) < kpiSuggestSampleMax; pass++ {
		for _, c := range clusters {
			if len(samples) >= kpiSuggestSampleMax {
				break
			}
			if sample, ok := kpiClusterSample(c, pass, seen); ok {
				samples = append(samples, sample)
			}
		}
	}
	query := strings.TrimSpace(strings.Join(uniqueStrings(queryTerms), " "))
	if query == "" {
		query = kpiSuggestFallbackQuery
	}
	return samples, query
}

// kpiClusterSample turns the pass-th representative of cluster c into a sample,
// recording its dedup key in seen. It returns ok=false when the cluster has no
// such representative, the snippet is empty, or the document was already taken.
func kpiClusterSample(c *commonv1.Cluster, pass int, seen map[string]struct{}) (kpiSample, bool) {
	reps := parseClusterRepresentatives(c.GetRepresentatives())
	if pass >= len(reps) {
		return kpiSample{}, false
	}
	rep := reps[pass]
	snippet := firstSentence(compactText(rep.Snippet), 400)
	if snippet == "" {
		return kpiSample{}, false
	}
	key := rep.DocumentID
	if key == "" {
		key = rep.Filename
	}
	if key == "" {
		key = snippet
	}
	if _, ok := seen[key]; ok {
		return kpiSample{}, false
	}
	seen[key] = struct{}{}
	return kpiSample{
		title:   kpiSampleTitle(rep.Filename, c),
		topic:   kpiClusterTopic(c),
		snippet: snippet,
	}, true
}

// kpiClusterTopic renders a cluster's label and keywords as a short topic hint.
func kpiClusterTopic(c *commonv1.Cluster) string {
	label := compactText(c.GetLabel())
	kw := compactText(strings.Join(c.GetKeywords(), ", "))
	switch {
	case label != "" && kw != "":
		return label + " (" + kw + ")"
	case label != "":
		return label
	default:
		return kw
	}
}

// kpiSampleTitle prefers a cleaned document filename as the sample title, falling
// back to the cluster label.
func kpiSampleTitle(filename string, c *commonv1.Cluster) string {
	if t := sourceTitle(&commonv1.Source{Filename: filename}); t != "" {
		return t
	}
	if l := compactText(c.GetLabel()); l != "" {
		return l
	}
	return "документ"
}

// buildKPISuggestPrompt asks the model to extract measurable R&D goals from the
// embedded representative works (and the retrieved context above), returning a
// strict JSON array.
func buildKPISuggestPrompt(samples []kpiSample) string {
	var b strings.Builder
	b.WriteString("Ты — аналитик НИОКР. В научных статьях авторы улучшают измеримые показатели " +
		"(прочность, извлечение, выход, КПД, пористость, коррозионная стойкость, ёмкость, " +
		"стоимость, производительность…). Ниже — репрезентативная выборка работ из базы знаний, " +
		"плюс выдержки из документов в контексте выше. Извлеки measurable цели R&D (KPI), которые " +
		"стоит проверять по этой базе: что именно в этих работах оптимизируют или улучшают.\n\n")
	b.WriteString("Требования: (1) верни от 5 до 10 разных целей; (2) каждая цель — измеримый " +
		"показатель с направлением изменения (increase — увеличить, decrease — снизить, maintain — " +
		"удержать); (3) metric — конкретная величина (напр. «предел текучести», «нефтеотдача», " +
		"«удельная ёмкость»), unit — единица измерения, если она ясна («МПа», «%», «мА·ч/г»), иначе " +
		"пустая строка; (4) function_area — область (напр. «Жаропрочные сплавы», «Катодные " +
		"материалы», «Разработка нефтяных пластов»); (5) rationale — из каких работ и почему следует " +
		"эта цель, кратко; (6) объединяй похожие цели и не повторяйся; (7) не выдумывай показателей, " +
		"которых нет в работах.\n\n")
	if len(samples) > 0 {
		b.WriteString("Работы:\n")
		for i, s := range samples {
			fmt.Fprintf(&b, "%d. %s", i+1, s.title)
			if s.topic != "" {
				b.WriteString(" — тема: " + s.topic)
			}
			if s.snippet != "" {
				b.WriteString(" — " + s.snippet)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	b.WriteString("Верни СТРОГО JSON-массив без markdown и пояснений, каждый объект по схеме:\n")
	b.WriteString(`[{"title":"краткая формулировка цели","metric":"измеримый показатель",` +
		`"unit":"единица или пустая строка","direction":"increase|decrease|maintain",` +
		`"function_area":"область","rationale":"из каких работ и почему"}]`)
	b.WriteString(untrustedContextInstruction)
	b.WriteString(langInstruction)
	b.WriteString("\nТолько JSON-массив.")
	return b.String()
}

// parseKPISuggestions tolerantly extracts the candidate array from the model
// output: a bare array, a fenced array or an array embedded in prose/object.
func parseKPISuggestions(answer string) []KPISuggestion {
	candidates := append([]string{stripJSONFence(answer)}, jsonArrayCandidates(answer)...)
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var items []KPISuggestion
		// Require a titled item, not just len>0: jsonx (sonic) ignores unknown
		// fields, so a decoy/reasoning array (e.g. [{"topic":…}] inside <think>)
		// unmarshals into title-less structs and would otherwise mask the real
		// array that closes later in the output.
		if err := jsonx.Unmarshal([]byte(raw), &items); err == nil && hasTitledSuggestion(items) {
			return items
		}
	}
	return nil
}

// hasTitledSuggestion reports whether any parsed candidate carries a real title.
func hasTitledSuggestion(items []KPISuggestion) bool {
	for _, it := range items {
		if compactText(it.Title) != "" {
			return true
		}
	}
	return false
}

// dedupKPISuggestions normalizes each candidate, drops near-duplicates (by
// title+metric) and empty titles, and clamps to kpiSuggestMax.
func dedupKPISuggestions(items []KPISuggestion) []KPISuggestion {
	out := make([]KPISuggestion, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		it.Title = compactText(it.Title)
		it.Metric = compactText(it.Metric)
		it.Unit = compactText(it.Unit)
		it.FunctionArea = compactText(it.FunctionArea)
		it.Rationale = compactText(it.Rationale)
		it.Direction = normalizeKPIDirection(it.Direction, it.Title+" "+it.Metric)
		if it.Title == "" {
			continue
		}
		key := kpiSuggestionKey(it)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, it)
		if len(out) >= kpiSuggestMax {
			break
		}
	}
	return out
}

func kpiSuggestionKey(it KPISuggestion) string {
	base := strings.ToLower(it.Title)
	if it.Metric != "" {
		base += "|" + strings.ToLower(it.Metric)
	}
	return strings.Join(strings.Fields(base), " ")
}

// Canonical KPI directions, the only values a suggestion's Direction may hold.
const (
	kpiDirIncrease = "increase"
	kpiDirDecrease = "decrease"
	kpiDirMaintain = "maintain"
)

// normalizeKPIDirection maps the model's direction (or, failing that, the goal
// wording) onto the canonical increase|decrease|maintain vocabulary.
func normalizeKPIDirection(dir, context string) string {
	switch canon := strings.ToLower(compactText(dir)); canon {
	case kpiDirIncrease, kpiDirDecrease, kpiDirMaintain:
		return canon
	}
	hay := strings.ToLower(dir + " " + context)
	switch {
	case containsAny(hay, "decrease", "reduc", "lower", "сниз", "уменьш", "сократ", "минимиз", "удешев"):
		return kpiDirDecrease
	case containsAny(hay, "increase", "raise", "improv", "maximi", "повыс", "увелич", "улучш", "рост", "максимиз"):
		return kpiDirIncrease
	case containsAny(hay, "maintain", "stable", "поддерж", "сохран", "удерж", "стабил"):
		return kpiDirMaintain
	default:
		return kpiDirIncrease
	}
}
