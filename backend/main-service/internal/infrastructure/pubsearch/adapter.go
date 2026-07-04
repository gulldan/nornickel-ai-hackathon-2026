package pubsearch

import (
	"context"
	"strings"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/platform/runtimecfg"
)

// Adapter binds the pubsearch client to the application.PubSearcher port.
type Adapter struct{ c *Client }

// NewAdapter builds the adapter; ovr supplies the runtime PUBSEARCH_* toggles.
func NewAdapter(ovr *runtimecfg.Overrides) *Adapter { return &Adapter{c: New(ovr)} }

// SearchWorks runs the caller's query (an LLM-focused English query) against the
// open databases, falling back to the deterministic mineral-processing
// dictionary when that query is empty.
func (a *Adapter) SearchWorks(
	ctx context.Context, query, goal, constraints string, limit int,
) ([]application.ExternalWork, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		q = QueryFromRu(goal, constraints)
	}
	if q == "" {
		return nil, nil
	}
	works, err := a.c.Search(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.ExternalWork, 0, len(works))
	for _, w := range works {
		out = append(out, application.ExternalWork{
			Title: w.Title, Year: w.Year, DOI: w.DOI, Venue: w.Venue,
			Abstract: w.Abstract, Source: w.Source,
		})
	}
	return out, nil
}
