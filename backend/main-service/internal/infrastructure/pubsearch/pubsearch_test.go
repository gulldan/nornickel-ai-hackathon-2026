package pubsearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const oaFixture = `{
	"results": [{
		"title": "Ultrasound treatment on tailings to enhance copper flotation recovery",
		"publication_year": 2016,
		"doi": "https://doi.org/10.1016/j.mineng.2016.09.019",
		"primary_location": {"source": {"display_name": "Minerals Engineering"}},
		"abstract_inverted_index": {"copper": [2], "flotation": [1], "Enhancing": [0], "recovery": [3]}
	}]
}`

const crFixture = `{
	"message": {
		"items": [{
			"title": ["Study of Flotation Parameters for Copper Recovery"],
			"DOI": "10.21275/v5i3.nov161778",
			"container-title": ["International Journal of Science and Research"],
			"issued": {"date-parts": [[2016, 3, 5]]},
			"abstract": "<jats:title>Abstract</jats:title><jats:p>Copper &amp; nickel recovery.</jats:p>"
		}]
	}
}`

func TestAbstractFromInvertedIndex(t *testing.T) {
	idx := map[string][]int{
		"and":   {1},
		"grade": {0, 3},
		"yield": {2},
	}
	if got, want := abstractFromInverted(idx), "grade and yield grade"; got != want {
		t.Fatalf("abstract = %q, want %q", got, want)
	}
	if got := abstractFromInverted(nil); got != "" {
		t.Fatalf("nil index must yield empty abstract, got %q", got)
	}
}

func TestSearchOpenAlex(t *testing.T) {
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mailto") == "" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(oaFixture))
	}))
	defer oa.Close()
	cr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer cr.Close()

	c := newClient("dev@example.com", true, oa.URL, cr.URL)
	works, err := c.Search(t.Context(), "copper flotation", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(works) != 1 {
		t.Fatalf("works = %d, want 1", len(works))
	}
	w := works[0]
	if w.Source != SourceOpenAlex || w.Year != 2016 {
		t.Fatalf("unexpected work: %+v", w)
	}
	if w.DOI != "10.1016/j.mineng.2016.09.019" {
		t.Fatalf("doi prefix not stripped: %q", w.DOI)
	}
	if w.Venue != "Minerals Engineering" {
		t.Fatalf("venue = %q", w.Venue)
	}
	if w.Abstract != "Enhancing flotation copper recovery" {
		t.Fatalf("abstract = %q", w.Abstract)
	}
}

func TestSearchYearFilter(t *testing.T) {
	var gotFilter string
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		_, _ = w.Write([]byte(oaFixture))
	}))
	defer oa.Close()

	c := newClient("dev@example.com", true, oa.URL, "http://unused.invalid")
	c.recentYears = func(context.Context) int { return 5 }
	c.now = func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }
	if _, err := c.Search(t.Context(), "copper flotation", 5); err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "from_publication_date:2021-07-04"; gotFilter != want {
		t.Fatalf("openalex filter = %q, want %q", gotFilter, want)
	}
}

func TestSearchOpenAlexAPIKey(t *testing.T) {
	var gotKey, gotMailto string
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("api_key")
		gotMailto = r.URL.Query().Get("mailto")
		_, _ = w.Write([]byte(oaFixture))
	}))
	defer oa.Close()

	c := newClient("", true, oa.URL, "http://unused.invalid")
	c.apiKey = func(context.Context) string { return "secret-key" }
	works, err := c.Search(t.Context(), "copper flotation", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(works) != 1 || works[0].Source != SourceOpenAlex {
		t.Fatalf("api key must reach openalex without mailto: %+v", works)
	}
	if gotKey != "secret-key" {
		t.Fatalf("api_key = %q, want secret-key", gotKey)
	}
	if gotMailto != "" {
		t.Fatalf("mailto = %q, want empty", gotMailto)
	}
}

func TestSearchFallbackToCrossref(t *testing.T) {
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer oa.Close()
	cr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(crFixture))
	}))
	defer cr.Close()

	c := newClient("dev@example.com", true, oa.URL, cr.URL)
	works, err := c.Search(t.Context(), "copper flotation", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(works) != 1 {
		t.Fatalf("works = %d, want 1", len(works))
	}
	w := works[0]
	if w.Source != SourceCrossref || w.Year != 2016 || w.DOI != "10.21275/v5i3.nov161778" {
		t.Fatalf("unexpected work: %+v", w)
	}
	if w.Abstract != "Copper & nickel recovery." {
		t.Fatalf("jats not stripped: %q", w.Abstract)
	}
}

func TestSearchNoMailtoSkipsOpenAlex(t *testing.T) {
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer oa.Close()
	var crHits atomic.Int32
	cr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		crHits.Add(1)
		_, _ = w.Write([]byte(crFixture))
	}))
	defer cr.Close()

	c := newClient("", true, oa.URL, cr.URL)
	works, err := c.Search(t.Context(), "copper flotation", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(works) != 1 || works[0].Source != SourceCrossref {
		t.Fatalf("unexpected works: %+v", works)
	}
	if crHits.Load() != 1 {
		t.Fatalf("crossref hits = %d, want 1", crHits.Load())
	}
}

func TestSearchCache(t *testing.T) {
	var hits atomic.Int32
	cr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(crFixture))
	}))
	defer cr.Close()

	c := newClient("", true, "http://unused.invalid", cr.URL)
	for range 3 {
		if _, err := c.Search(t.Context(), "copper flotation", 5); err != nil {
			t.Fatalf("search: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hits = %d, want 1 (cache miss on repeat)", hits.Load())
	}
}

func TestSearchDisabledOrEmptyQuery(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(crFixture))
	}))
	defer srv.Close()

	disabled := newClient("dev@example.com", false, srv.URL, srv.URL)
	if works, err := disabled.Search(t.Context(), "copper", 5); err != nil || works != nil {
		t.Fatalf("disabled client: works=%v err=%v", works, err)
	}
	enabled := newClient("dev@example.com", true, srv.URL, srv.URL)
	if works, err := enabled.Search(t.Context(), "   ", 5); err != nil || works != nil {
		t.Fatalf("blank query: works=%v err=%v", works, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("network hits = %d, want 0", hits.Load())
	}
}

func TestStripJATS(t *testing.T) {
	in := "<jats:title>Abstract</jats:title>\n<jats:p>Copper &amp; nickel  losses</jats:p>"
	if got, want := stripJATS(in), "Copper & nickel losses"; got != want {
		t.Fatalf("stripJATS = %q, want %q", got, want)
	}
	if got := stripJATS(""); got != "" {
		t.Fatalf("empty input: %q", got)
	}
}

func TestQueryFromRu(t *testing.T) {
	cases := []struct {
		name  string
		goal  string
		extra string
		want  []string
	}{
		{name: "flotation copper", goal: "Снизить потери меди при флотации", extra: "хвосты обогащения",
			want: []string{"losses", "copper", "flotation", "tailings", "beneficiation", "mineral processing"}},
		{name: "metallurgy", goal: "Переработка шлаков плавки", extra: "выщелачивание штейна",
			want: []string{"slag", "smelting", "leaching", "matte", "extractive metallurgy"}},
		{name: "latin passthrough", goal: "Дозировка xanthate при аэрации пульпы", extra: "",
			want: []string{"aeration", "pulp density", "xanthate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QueryFromRu(tc.goal, tc.extra)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("query %q missing %q", got, want)
				}
			}
		})
	}

	metallurgy := QueryFromRu(cases[1].goal, cases[1].extra)
	if strings.Contains(metallurgy, cases[0].want[5]) {
		t.Fatalf("metallurgy query %q must not add the beneficiation tail", metallurgy)
	}
	if got := QueryFromRu("Как улучшить показатели", ""); got != "" {
		t.Fatalf("no domain terms must yield empty query, got %q", got)
	}
}
