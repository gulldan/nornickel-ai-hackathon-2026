// Package titler extracts a document's real article title from the opening of
// its text via the LLM generation backend. It satisfies application.Titler, so
// the documents page can show the article title instead of the uploaded
// filename. Extraction is best-effort: any failure yields "" and the UI falls
// back to the filename.
package titler

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/example/chunk-splitter/internal/platform/aiclients"
	"github.com/example/chunk-splitter/internal/platform/llmusage"
	"github.com/example/chunk-splitter/internal/platform/logger"
)

// titleOperation labels title-extraction spend in the shared LLM-usage ledger.
const titleOperation = "title_extract"

// openingRunes bounds how much of the document start the model sees: a title and
// its front-matter (authors, abstract heading) always live here, so sending more
// only spends tokens.
const openingRunes = 1500

// maxTitleRunes caps an accepted title; a longer reply is the model returning a
// sentence or the abstract rather than a title, so it is rejected.
const maxTitleRunes = 300

const systemPrompt = "You analyse the opening text of a document (often a scientific article). " +
	"Reply with EXACTLY two lines and nothing else:\n" +
	"TITLE: <the document's title as plain text in its original language, no quotes, " +
	"no markdown, no translation; NONE if the text has no discernible title>\n" +
	"KIND: <hypotheses if the document is essentially a list of ready-made hypotheses, " +
	"improvement ideas or brainstorm results (e.g. \"гипотезы по результатам мозгового " +
	"штурма\"); normal for everything else — articles, reports, data, methods>"

const query = "Return the document's title and kind."

const kindHypotheses = "hypotheses"

// Titler extracts article titles through an LLM generator.
type Titler struct {
	gen aiclients.Generator
	kv  llmusage.KV // usage ledger; nil disables cost accounting (best-effort)
}

// New builds a Titler over gen (a real generation backend; the caller enables
// title extraction only when one is configured). kv is the shared usage ledger
// used to account title-extraction spend; a nil kv disables that accounting.
func New(gen aiclients.Generator, kv llmusage.KV) *Titler {
	return &Titler{gen: gen, kv: kv}
}

// Extract returns the article title found at the start of text ("" when none
// can be determined) and the document kind ("hypotheses" for a ready-made
// hypotheses/brainstorm list, "" otherwise). It never returns an error —
// extraction must not fail ingestion.
func (t *Titler) Extract(ctx context.Context, text string) (string, string) {
	opening := strings.TrimSpace(headRunes(text, openingRunes))
	if opening == "" {
		return "", ""
	}
	resp, err := t.gen.Generate(ctx, aiclients.GenRequest{
		System:      systemPrompt,
		Context:     opening,
		Query:       query,
		MaxTokens:   128,
		Temperature: 0,
	})
	if err != nil {
		logger.From(ctx).Warn().Err(err).Msg("title extraction failed")
		return "", ""
	}
	if t.kv != nil {
		// The completion cost tokens whether or not sanitize keeps the title.
		_ = llmusage.Record(ctx, t.kv, resp.Model, titleOperation,
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.CostUSD)
	}
	return parseReply(resp.Text)
}

// parseReply splits the model's TITLE/KIND reply. A reply without the labelled
// lines is treated as a bare title (older models answer that way).
func parseReply(raw string) (string, string) {
	title, kind, labelled := "", "", false
	for _, line := range strings.Split(raw, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case len(l) >= 6 && strings.EqualFold(l[:6], "TITLE:"):
			title, labelled = strings.TrimSpace(l[6:]), true
		case len(l) >= 5 && strings.EqualFold(l[:5], "KIND:"):
			kind, labelled = strings.TrimSpace(l[5:]), true
		}
	}
	if !labelled {
		title = raw
	}
	if !strings.EqualFold(kind, kindHypotheses) {
		kind = ""
	} else {
		kind = kindHypotheses
	}
	return sanitize(title), kind
}

// headRunes returns up to n leading runes of s without splitting a rune.
func headRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) && count < n {
		_, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
		count++
	}
	return s[:i]
}

// sanitize normalises the model's reply into a single clean title line, or ""
// when it declined (NONE) or returned something that is not title-shaped.
func sanitize(raw string) string {
	title := strings.TrimSpace(raw)
	if nl := strings.IndexAny(title, "\r\n"); nl >= 0 {
		title = strings.TrimSpace(title[:nl])
	}
	title = strings.TrimSpace(strings.Trim(title, "\"'«»“”`"))
	if title == "" || strings.EqualFold(title, "NONE") {
		return ""
	}
	if utf8.RuneCountInString(title) > maxTitleRunes {
		return ""
	}
	return title
}
