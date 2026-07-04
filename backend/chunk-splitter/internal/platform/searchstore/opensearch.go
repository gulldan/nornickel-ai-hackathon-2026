// Package searchstore talks to OpenSearch over REST. It provides the BM25
// lexical half of hybrid retrieval; chunk-splitter indexes chunks and
// llm-service queries them. The local cluster runs with security disabled, so no
// auth header is required.
package searchstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/example/chunk-splitter/internal/platform/jsonx"
)

// OpenSearch is a client bound to a single index.
type OpenSearch struct {
	base  string
	index string
	httpc *http.Client
}

// NewOpenSearch builds a client. baseURL is like http://opensearch:9200.
func NewOpenSearch(baseURL, index string) *OpenSearch {
	return &OpenSearch{base: baseURL, index: index, httpc: &http.Client{Timeout: 30 * time.Second}}
}

// OpenSearch field name / mapping type literals (shared to satisfy goconst).
const (
	osText       = "text"
	osKeyword    = "keyword"
	osType       = "type"
	osIndex      = "index"
	osFilter     = "filter"
	osAnalyzer   = "analyzer"
	osExact      = "exact"
	osDocumentID = "document_id"
)

// Doc is an indexed chunk.
type Doc struct {
	ID         string            `json:"-"`
	Text       string            `json:"text"`
	DocumentID string            `json:"document_id"`
	OwnerID    string            `json:"owner_id"`
	Filename   string            `json:"filename"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Hit is a single BM25 search result.
type Hit struct {
	ID         string
	Score      float64
	Text       string
	DocumentID string
	Filename   string
}

// refreshInterval trades search freshness for bulk-indexing throughput: newly
// indexed chunks become searchable within this window instead of OpenSearch
// refreshing segments every second while an ingest storm is running.
const refreshInterval = "5s"

// EnsureIndex creates the index with an explicit mapping if it is missing, and
// (idempotently) applies the dynamic ingest-friendly settings either way.
func (o *OpenSearch) EnsureIndex(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, o.base+"/"+o.index, nil)
	if err != nil {
		return fmt.Errorf("opensearch new head request: %w", err)
	}
	resp, err := o.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("opensearch head index: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		mapping := map[string]any{
			"settings": map[string]any{
				osIndex: map[string]any{"refresh_interval": refreshInterval},
				// Russian-first bilingual analysis. With the default analyzer BM25
				// only matches exact word forms — "балансир" misses "балансира". The
				// built-in snowball stemmers fold every token to its stem at both
				// index and query time so morphological variants collide; the en_*
				// pass does the same for any Latin text in the corpus. Cyrillic
				// tokens pass through the English filters untouched and vice versa,
				// so chaining is safe. Analysis is a STATIC setting (applied only at
				// creation) — it lives here, not in the dynamic reapply below; an
				// existing index must be dropped and reindexed to pick it up.
				"analysis": map[string]any{
					osFilter: map[string]any{
						"ru_stop":    map[string]any{osType: "stop", "stopwords": "_russian_"},
						"ru_stemmer": map[string]any{osType: "stemmer", "language": "russian"},
						"en_stop":    map[string]any{osType: "stop", "stopwords": "_english_"},
						"en_stemmer": map[string]any{osType: "stemmer", "language": "english"},
					},
					osAnalyzer: map[string]any{
						// cjk_bigram: китайский ищется биграммами, не-CJK токены проходят
						// фильтр нетронутыми (ru/en стемминг сохраняется).
						"ru_en": map[string]any{
							osType:      "custom",
							"tokenizer": "standard",
							osFilter: []any{
								"lowercase", "cjk_width", "cjk_bigram",
								"en_stop", "en_stemmer", "ru_stop", "ru_stemmer",
							},
						},
						// No stemming/stopwords: chemical formulas (LiFePO4), units (MPa,
						// S/cm) and alloy grades survive intact for exact lexical matching.
						osExact: map[string]any{
							osType:      "custom",
							"tokenizer": "whitespace",
							osFilter:    []any{"lowercase"},
						},
					},
				},
			},
			"mappings": map[string]any{
				"properties": map[string]any{
					osText: map[string]any{
						osType: osText, osAnalyzer: "ru_en",
						"fields": map[string]any{osExact: map[string]any{osType: osText, osAnalyzer: osExact}},
					},
					osDocumentID: map[string]any{osType: osKeyword},
					"owner_id":   map[string]any{osType: osKeyword},
					"filename":   map[string]any{osType: osKeyword},
				},
			},
		}
		return o.do(ctx, http.MethodPut, "/"+o.index, mapping, nil)
	}
	// The index predates this code: refresh_interval is a dynamic setting, so
	// align it on every boot.
	settings := map[string]any{osIndex: map[string]any{"refresh_interval": refreshInterval}}
	return o.do(ctx, http.MethodPut, "/"+o.index+"/_settings", settings, nil)
}

// Index upserts a chunk document under its id.
func (o *OpenSearch) Index(ctx context.Context, d Doc) error {
	return o.do(ctx, http.MethodPut, "/"+o.index+"/_doc/"+d.ID, d, nil)
}

// BulkIndex upserts a batch of chunk documents in one _bulk request — the
// ingest fast path: one HTTP round-trip and one refresh cycle for a whole
// document's chunks instead of a request per chunk.
func (o *OpenSearch) BulkIndex(ctx context.Context, docs []Doc) error {
	if len(docs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, d := range docs {
		meta, err := jsonx.Marshal(map[string]any{osIndex: map[string]any{"_index": o.index, "_id": d.ID}})
		if err != nil {
			return fmt.Errorf("encode bulk meta: %w", err)
		}
		body, err := jsonx.Marshal(d)
		if err != nil {
			return fmt.Errorf("encode bulk doc: %w", err)
		}
		buf.Write(meta)
		buf.WriteByte('\n')
		buf.Write(body)
		buf.WriteByte('\n')
	}
	var resp struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := o.doNDJSON(ctx, "/_bulk", buf.Bytes(), &resp); err != nil {
		return err
	}
	if resp.Errors {
		for _, item := range resp.Items {
			for _, r := range item {
				if r.Error != nil {
					return fmt.Errorf("opensearch bulk item: %s: %s", r.Error.Type, r.Error.Reason)
				}
			}
		}
		return errors.New("opensearch bulk reported errors")
	}
	return nil
}

// doNDJSON posts a newline-delimited body (the _bulk wire format) and decodes
// the JSON response into out.
func (o *OpenSearch) doNDJSON(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("opensearch new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := o.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("opensearch %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("opensearch %s: status %d: %s", path, resp.StatusCode, string(raw))
	}
	if out != nil {
		if derr := jsonx.NewDecoder(resp.Body).Decode(out); derr != nil {
			return fmt.Errorf("decode opensearch response: %w", derr)
		}
	}
	return nil
}

// Search runs a BM25 match over text. A non-empty ownerID scopes the search to
// one tenant; the empty string searches the shared corpus (no tenant filter).
func (o *OpenSearch) Search(ctx context.Context, query, ownerID string, size int) ([]Hit, error) {
	return o.SearchFiltered(ctx, query, ownerID, size, nil, nil)
}

// SearchFiltered is Search with optional document scoping: scopeDocIDs keeps
// only hits from those documents, excludeDocIDs drops hits from those.
func (o *OpenSearch) SearchFiltered(
	ctx context.Context, query, ownerID string, size int, scopeDocIDs, excludeDocIDs []string,
) ([]Hit, error) {
	boolQuery := map[string]any{
		// Match the stemmed text and the no-stemming text.exact (boosted) so chemical
		// formulas, units and alloy grades are found exactly, not folded by the stemmer.
		"must": []any{map[string]any{"multi_match": map[string]any{
			"query":  query,
			"fields": []any{osText, osText + ".exact^2"},
		}}},
	}
	filters := make([]any, 0, 2)
	if ownerID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"owner_id": ownerID}})
	}
	if len(scopeDocIDs) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{osDocumentID: scopeDocIDs}})
	}
	if len(filters) > 0 {
		boolQuery[osFilter] = filters
	}
	if len(excludeDocIDs) > 0 {
		boolQuery["must_not"] = []any{map[string]any{"terms": map[string]any{osDocumentID: excludeDocIDs}}}
	}
	body := map[string]any{
		"size":  size,
		"query": map[string]any{"bool": boolQuery},
	}
	var resp struct {
		Hits struct {
			Hits []struct {
				ID     string  `json:"_id"`
				Score  float64 `json:"_score"`
				Source Doc     `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := o.do(ctx, http.MethodPost, "/"+o.index+"/_search", body, &resp); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(resp.Hits.Hits))
	for _, h := range resp.Hits.Hits {
		hits = append(hits, Hit{
			ID:         h.ID,
			Score:      h.Score,
			Text:       h.Source.Text,
			DocumentID: h.Source.DocumentID,
			Filename:   h.Source.Filename,
		})
	}
	return hits, nil
}

// Ping checks cluster reachability for readiness probes.
func (o *OpenSearch) Ping(ctx context.Context) error {
	return o.do(ctx, http.MethodGet, "/", nil, nil)
}

func (o *OpenSearch) do(ctx context.Context, method, path string, in, out any) error {
	var reader io.Reader
	if in != nil {
		raw, err := jsonx.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.base+path, reader)
	if err != nil {
		return fmt.Errorf("opensearch new request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := o.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("opensearch %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("opensearch %s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}
	if out != nil {
		if derr := jsonx.NewDecoder(resp.Body).Decode(out); derr != nil {
			return fmt.Errorf("decode opensearch response: %w", derr)
		}
	}
	return nil
}
