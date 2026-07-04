package pubsearch

import (
	"context"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/example/main-service/internal/platform/jsonx"
)

type crDate struct {
	DateParts [][]int `json:"date-parts"`
}

type crItem struct {
	Title          []string `json:"title"`
	Abstract       string   `json:"abstract"`
	DOI            string   `json:"DOI"`
	ContainerTitle []string `json:"container-title"`
	Issued         crDate   `json:"issued"`
}

type crMessage struct {
	Items []crItem `json:"items"`
}

type crResponse struct {
	Message crMessage `json:"message"`
}

func (c *Client) searchCrossref(ctx context.Context, query string, limit, recentYears int) ([]Work, error) {
	q := url.Values{
		"query":  {query},
		"rows":   {strconv.Itoa(limit)},
		"select": {"title,abstract,DOI,container-title,issued"},
	}
	if m := c.mailto(ctx); m != "" {
		q.Set("mailto", m)
	}
	if from := c.fromDate(recentYears); from != "" {
		q.Set("filter", "from-pub-date:"+from)
	}
	body, err := c.getJSON(ctx, c.crBase+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var resp crResponse
	if uerr := jsonx.Unmarshal(body, &resp); uerr != nil {
		return nil, uerr
	}
	works := make([]Work, 0, len(resp.Message.Items))
	for _, it := range resp.Message.Items {
		works = append(works, Work{
			Title:    first(it.Title),
			Year:     issuedYear(it.Issued),
			DOI:      it.DOI,
			Venue:    first(it.ContainerTitle),
			Abstract: stripJATS(it.Abstract),
			Source:   SourceCrossref,
		})
	}
	return works, nil
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func issuedYear(d crDate) int {
	if len(d.DateParts) == 0 || len(d.DateParts[0]) == 0 {
		return 0
	}
	return d.DateParts[0][0]
}

func stripJATS(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
			b.WriteByte(' ')
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
	return strings.TrimPrefix(out, "Abstract ")
}
