package pubsearch

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/example/main-service/internal/platform/jsonx"
)

type oaSource struct {
	DisplayName string `json:"display_name"`
}

type oaLocation struct {
	Source *oaSource `json:"source"`
}

type oaWork struct {
	Title                 string           `json:"title"`
	Year                  int              `json:"publication_year"`
	DOI                   string           `json:"doi"`
	PrimaryLocation       *oaLocation      `json:"primary_location"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
}

type oaResponse struct {
	Results []oaWork `json:"results"`
}

func (w oaWork) venue() string {
	if w.PrimaryLocation == nil || w.PrimaryLocation.Source == nil {
		return ""
	}
	return w.PrimaryLocation.Source.DisplayName
}

func (c *Client) searchOpenAlex(ctx context.Context, query string, limit, recentYears int) ([]Work, error) {
	mailto := c.mailto(ctx)
	apiKey := c.apiKey(ctx)
	if mailto == "" && apiKey == "" {
		return nil, nil
	}
	q := url.Values{
		"search":   {query},
		"per-page": {strconv.Itoa(limit)},
	}
	if apiKey != "" {
		q.Set("api_key", apiKey)
	}
	if mailto != "" {
		q.Set("mailto", mailto)
	}
	if from := c.fromDate(recentYears); from != "" {
		q.Set("filter", "from_publication_date:"+from)
	}
	body, err := c.getJSON(ctx, c.oaBase+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var resp oaResponse
	if uerr := jsonx.Unmarshal(body, &resp); uerr != nil {
		return nil, uerr
	}
	works := make([]Work, 0, len(resp.Results))
	for _, r := range resp.Results {
		works = append(works, Work{
			Title:    r.Title,
			Year:     r.Year,
			DOI:      strings.TrimPrefix(r.DOI, "https://doi.org/"),
			Venue:    r.venue(),
			Abstract: abstractFromInverted(r.AbstractInvertedIndex),
			Source:   SourceOpenAlex,
		})
	}
	return works, nil
}

func abstractFromInverted(idx map[string][]int) string {
	if len(idx) == 0 {
		return ""
	}
	type posWord struct {
		pos  int
		word string
	}
	pairs := make([]posWord, 0, len(idx))
	for word, positions := range idx {
		for _, p := range positions {
			pairs = append(pairs, posWord{pos: p, word: word})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].pos < pairs[j].pos })
	words := make([]string, len(pairs))
	for i, p := range pairs {
		words[i] = p.word
	}
	return strings.Join(words, " ")
}
