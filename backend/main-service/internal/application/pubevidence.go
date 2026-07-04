package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	"github.com/example/main-service/internal/platform/logger"
)

const (
	pubWorksLimit          = 5
	pubAbstractMaxRunes    = 400
	pubFreshnessWindowYear = 5
)

// ExternalWork is one open-literature match (world practice) attached to a
// generation run.
type ExternalWork struct {
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	DOI      string `json:"doi,omitempty"`
	Venue    string `json:"venue,omitempty"`
	Abstract string `json:"-"`
	Source   string `json:"source,omitempty"`
}

// PubSearcher finds open publications for a goal/constraints pair; nil-safe
// integration point wired via SetPubSearcher.
type PubSearcher interface {
	SearchWorks(ctx context.Context, query, goal, constraints string, limit int) ([]ExternalWork, error)
}

// SetPubSearcher enables the open-literature evidence step of generation.
func (s *HypothesisService) SetPubSearcher(p PubSearcher) { s.pubs = p }

// PubIngestor feeds abstract-bearing publications back into the corpus so they
// become retrievable evidence for later generations.
type PubIngestor interface {
	IngestTextIfNew(ctx context.Context, ownerID, filename, content string) error
}

// SetPubIngestor enables corpus back-fill of found publications.
func (s *HypothesisService) SetPubIngestor(p PubIngestor) { s.pubIngest = p }

func (s *HypothesisService) externalWorks(
	ctx context.Context, kpi *commonv1.KPI, constraints string,
) []ExternalWork {
	if s.pubs == nil {
		return nil
	}
	goal := strings.TrimSpace(kpi.GetTitle() + " " + kpi.GetMetric() + " " + kpi.GetDescription())
	query := s.pubSearchQuery(ctx, kpi.GetOwnerId(), goal, constraints)
	works, err := s.pubs.SearchWorks(ctx, query, goal, constraints, pubWorksLimit)
	if err != nil {
		return nil
	}
	if s.pubIngest != nil && kpi.GetOwnerId() != "" {
		go s.ingestWorks(context.WithoutCancel(ctx), kpi.GetOwnerId(), works)
	}
	return works
}

func (s *HypothesisService) ingestWorks(ctx context.Context, ownerID string, works []ExternalWork) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for _, w := range works {
		if strings.TrimSpace(w.Abstract) == "" {
			continue
		}
		if err := s.pubIngest.IngestTextIfNew(ctx, ownerID, workFilename(w), workDocument(w)); err != nil {
			logger.From(ctx).Warn().Err(err).Str("doi", w.DOI).Msg("publication back-fill failed")
		}
	}
}

func workFilename(w ExternalWork) string {
	base := w.DOI
	if base == "" {
		base = w.Title
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(base) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteRune('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return "pub_" + slug + ".txt"
}

func workDocument(w ExternalWork) string {
	var b strings.Builder
	b.WriteString(w.Title + "\n")
	if w.Venue != "" {
		b.WriteString(w.Venue)
	}
	if w.Year > 0 {
		fmt.Fprintf(&b, " (%d)", w.Year)
	}
	if w.DOI != "" {
		b.WriteString(" doi:" + w.DOI)
	}
	b.WriteString("\n\n" + strings.TrimSpace(w.Abstract) + "\n")
	return b.String()
}

func worldPracticeNote(works []ExternalWork) string {
	if len(works) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nОткрытые публикации (мировая практика). Используй их для звена «мировая практика» " +
		"в rationale и ссылайся как [P1], [P2]…; не выдумывай источники сверх списка:\n")
	for i, w := range works {
		fmt.Fprintf(&b, "[P%d] %s", i+1, w.Title)
		if w.Year > 0 {
			fmt.Fprintf(&b, " (%d)", w.Year)
		}
		if w.Venue != "" {
			fmt.Fprintf(&b, ", %s", w.Venue)
		}
		if w.DOI != "" {
			fmt.Fprintf(&b, ", doi:%s", w.DOI)
		}
		if a := compactText(w.Abstract); a != "" {
			r := []rune(a)
			if len(r) > pubAbstractMaxRunes {
				a = string(r[:pubAbstractMaxRunes]) + "…"
			}
			b.WriteString(" — ")
			b.WriteString(a)
		}
		b.WriteString("\n")
	}
	if _, rationale, ok := topicFreshness(works, time.Now().Year()); ok {
		b.WriteString("Свежесть темы по годам публикаций (учитывай при оценке новизны): " + rationale + "\n")
	}
	return b.String()
}

// topicFreshness turns the publication-year spread of the world-practice works
// into a 0..1 novelty signal: a burst of recent papers reads as an active,
// novel frontier; a set clustered at the far edge of the lookback window reads
// as stale. Mirrors the itc-worker momentum weights (0.6 recency + 0.4 recent
// share). ok is false when no work carries a usable year.
func topicFreshness(works []ExternalWork, nowYear int) (float64, string, bool) {
	years := make([]int, 0, len(works))
	for _, w := range works {
		if w.Year > 0 && w.Year <= nowYear {
			years = append(years, w.Year)
		}
	}
	if len(years) == 0 {
		return 0, "", false
	}
	windowStart := nowYear - pubFreshnessWindowYear
	span := float64(nowYear - windowStart)
	recent, sumRecency := 0, 0.0
	ymin, ymax := years[0], years[0]
	for _, y := range years {
		sumRecency += clamp01(float64(y-windowStart) / span)
		if y >= nowYear-1 {
			recent++
		}
		if y < ymin {
			ymin = y
		}
		if y > ymax {
			ymax = y
		}
	}
	meanRecency := sumRecency / float64(len(years))
	recentShare := float64(recent) / float64(len(years))
	score := clamp01(0.6*meanRecency + 0.4*recentShare)
	return score, freshnessRationale(score, recent, len(years), ymin, ymax, nowYear), true
}

func freshnessRationale(score float64, recent, total, ymin, ymax, nowYear int) string {
	years := strconv.Itoa(ymin)
	if ymax != ymin {
		years += "–" + strconv.Itoa(ymax)
	}
	tone := "поток публикаций затухает — новизна низкая"
	switch {
	case score >= 0.66:
		tone = "тема активно развивается — новизна высокая"
	case score >= 0.33:
		tone = "умеренная активность по теме — новизна средняя"
	}
	return fmt.Sprintf("Свежих за последние 2 года (%d–%d): %d из %d; годы найденных публикаций — %s. %s.",
		nowYear-1, nowYear, recent, total, years, tone)
}
