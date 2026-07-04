// Turning a Russian improvement goal into a concise English OpenAlex query.
// One LLM pass, best-effort: any failure returns "" so the caller falls back to
// the deterministic mineral-processing dictionary (pubsearch.QueryFromRu).

package application

import (
	"context"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	"github.com/example/main-service/internal/platform/jsonx"
)

const (
	pubQueryTimeout  = 20 * time.Second
	pubQueryMaxTerms = 8
	pubQueryMaxRunes = 90
)

func (s *HypothesisService) pubSearchQuery(ctx context.Context, ownerID, goal, constraints string) string {
	if s.answerer == nil || strings.TrimSpace(goal) == "" {
		return ""
	}
	llmCtx, cancel := withDeadline(ctx, pubQueryTimeout)
	defer cancel()
	resp, err := s.answerer.Answer(opCtx(llmCtx, "pub_query"), &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   firstSentence(compactText(goal), 160),
		Prompt:  buildPubQueryPrompt(goal, constraints),
		TopK:    0,
	})
	if err != nil || resp == nil {
		return ""
	}
	return sanitizePubQuery(resp.GetAnswer())
}

func buildPubQueryPrompt(goal, constraints string) string {
	var b strings.Builder
	b.WriteString("Ты строишь короткий поисковый запрос к OpenAlex (база научных статей) ")
	b.WriteString("по цели исследователя. Цель может быть на русском — переведи на английский ТОЛЬКО ")
	b.WriteString("суть научной темы.\n\nПравила выбора фокуса:\n")
	b.WriteString("- широкая научная область → каноническое название области;\n")
	b.WriteString("- метод внутри домена → \"метод + домен\", только если важно и то и другое;\n")
	b.WriteString("- объект и задача → \"объект + задача\";\n")
	b.WriteString("- много методов → общий родительский топик, а не перечисление методов;\n")
	b.WriteString("- много приложений → общий научный домен, а не список приложений;\n")
	b.WriteString("- сравнение подходов → общая исследовательская проблема;\n")
	b.WriteString("- запрос про state-of-the-art → задача или домен, без слов \"state of the art\";\n")
	b.WriteString("- запрос про обзор литературы → сама тема, без слов \"literature review\".\n\n")
	b.WriteString("Запрос: 2–6 слов, строчными, английский, без кавычек, без булевых операторов, ")
	b.WriteString("без авторов и годов, без мусора (\"papers\", \"methods\", \"review\", \"analysis\", ")
	b.WriteString("\"state of the art\"). Кратчайшая фраза, которую набрал бы эксперт домена.\n\n")
	b.WriteString("Примеры:\n")
	b.WriteString("- \"распознавание и верификация говорящего\" → speaker verification\n")
	b.WriteString("- \"x-vector, i-vector, d-vector для голосовой биометрии\" → speaker recognition\n")
	b.WriteString("- \"классификация зашифрованного трафика\" → encrypted traffic classification\n")
	b.WriteString("- \"методы генерации сетевого трафика\" → traffic generation\n")
	b.WriteString("- \"биоуголь для солонцеватых почв\" → biochar saline soils\n")
	b.WriteString("- \"графовые нейросети для обнаружения мошенничества\" → graph fraud detection\n")
	b.WriteString("- \"обзор методов knowledge graph completion\" → knowledge graph completion\n\n")
	b.WriteString("Цель:\n\"\"\"\n")
	b.WriteString(strings.TrimSpace(goal))
	b.WriteString("\n\"\"\"\n")
	if c := strings.TrimSpace(constraints); c != "" {
		b.WriteString("Ограничения (только контекст, обычно НЕ часть запроса): ")
		b.WriteString(c)
		b.WriteString("\n")
	}
	b.WriteString("\nВерни СТРОГО один JSON-объект без пояснений: {\"query\": \"<короткий английский запрос>\"}. ")
	b.WriteString("Если научной темы нет — верни {\"query\": \"\"}.")
	return b.String()
}

func sanitizePubQuery(answer string) string {
	raw := stripJSONFence(answer)
	if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
		var m struct {
			Query string `json:"query"`
		}
		if err := jsonx.Unmarshal([]byte(raw[i:j+1]), &m); err == nil {
			raw = m.Query
		}
	}
	q := strings.Trim(compactText(raw), "\"'`. ")
	fields := strings.Fields(q)
	if len(fields) > pubQueryMaxTerms {
		fields = fields[:pubQueryMaxTerms]
	}
	q = strings.Join(fields, " ")
	if r := []rune(q); len(r) > pubQueryMaxRunes {
		q = strings.TrimSpace(string(r[:pubQueryMaxRunes]))
	}
	if len([]rune(q)) < 3 {
		return ""
	}
	return q
}
