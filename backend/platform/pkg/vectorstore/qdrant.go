// Package vectorstore talks to Qdrant over its REST API (no heavy gRPC/protobuf
// client dependency). It stores chunk embeddings and serves cosine-similarity
// search — the vector half of the platform's hybrid retrieval.
package vectorstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/example/rag-mvp/pkg/jsonx"
)

// Qdrant payload/query literal keys (shared to satisfy goconst).
const (
	qKey       = "key"
	qDirection = "direction"
	qAsc       = "asc"
	qMatch     = "match"
)

// Qdrant is a client bound to a single collection.
type Qdrant struct {
	base       string
	collection string
	httpc      *http.Client
}

// NewQdrant builds a client. baseURL is like http://qdrant:6333.
func NewQdrant(baseURL, collection string) *Qdrant {
	return &Qdrant{base: baseURL, collection: collection, httpc: &http.Client{Timeout: 30 * time.Second}}
}

// Point is a vector with its associated payload (metadata).
type Point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// SearchHit is a single nearest-neighbour result.
type SearchHit struct {
	ID      string         `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload"`
}

// EnsureCollection creates the collection with the given vector dimension and
// cosine distance if it does not already exist, and ensures the payload index
// behind the platform's owner-scoped searches either way.
func (q *Qdrant) EnsureCollection(ctx context.Context, dim int) error {
	if err := q.do(ctx, http.MethodGet, "/collections/"+q.collection, nil, nil); err != nil {
		// No quantization: it materially degrades retrieval quality on this corpus,
		// so the collection stores full-precision vectors with default HNSW.
		body := map[string]any{"vectors": map[string]any{"size": dim, "distance": "Cosine"}}
		if cerr := q.do(ctx, http.MethodPut, "/collections/"+q.collection, body, nil); cerr != nil {
			return cerr
		}
	}
	// Every search filters on owner_id (tenant isolation) and the chunk reader
	// filters on document_id. Without keyword payload indexes Qdrant has to
	// read each candidate's payload off storage — effectively a scan that grows
	// with the corpus — so the indexes are part of the collection contract, not
	// an optimisation.
	if err := q.ensurePayloadIndex(ctx, "owner_id", "keyword"); err != nil {
		return err
	}
	if err := q.ensurePayloadIndex(ctx, "document_id", "keyword"); err != nil {
		return err
	}
	// Keyword index on node_type so retrieval can separate the RAPTOR layers
	// (raw "chunk" leaves vs "raptor_summary" nodes, all in this one collection)
	// without scanning payloads. Backward-compatible: points written before this
	// key simply have no node_type and match nothing when filtered on it.
	if err := q.ensurePayloadIndex(ctx, "node_type", "keyword"); err != nil {
		return err
	}
	// Integer index on chunk_index lets the chunk reader scroll a document's
	// points in their original order (Qdrant order_by requires a range index).
	return q.ensurePayloadIndex(ctx, "chunk_index", "integer")
}

// ensurePayloadIndex creates a payload index on field with the given schema
// (keyword/integer/...), tolerating the "already exists" answer so boot stays
// idempotent.
func (q *Qdrant) ensurePayloadIndex(ctx context.Context, field, schema string) error {
	body := map[string]any{"field_name": field, "field_schema": schema}
	err := q.do(ctx, http.MethodPut, "/collections/"+q.collection+"/index?wait=true", body, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// Upsert inserts or updates points, waiting for the operation to be applied.
func (q *Qdrant) Upsert(ctx context.Context, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	body := map[string]any{"points": points}
	return q.do(ctx, http.MethodPut, "/collections/"+q.collection+"/points?wait=true", body, nil)
}

// SearchFilter describes payload conditions for Search: exact matches (Eq),
// match-any allowlists (AnyOf) and match-any denylists (NoneOf) per field.
type SearchFilter struct {
	Eq     map[string]string
	AnyOf  map[string][]string
	NoneOf map[string][]string
}

func (f SearchFilter) empty() bool {
	return len(f.Eq) == 0 && len(f.AnyOf) == 0 && len(f.NoneOf) == 0
}

// Search returns the top-limit nearest points, optionally filtered by exact
// payload matches (e.g. {"owner_id": "..."}) so tenants never see each other's data.
func (q *Qdrant) Search(ctx context.Context, vector []float32, limit int, eq map[string]string) ([]SearchHit, error) {
	return q.SearchFiltered(ctx, vector, limit, SearchFilter{Eq: eq})
}

// SearchFiltered is Search with the full filter shape: exact matches plus
// per-field allow/deny document lists (used to scope retrieval to or away
// from a goal's input documents).
func (q *Qdrant) SearchFiltered(ctx context.Context, vector []float32, limit int, f SearchFilter) ([]SearchHit, error) {
	body := map[string]any{"vector": vector, "limit": limit, "with_payload": true}
	if !f.empty() {
		must := make([]map[string]any, 0, len(f.Eq)+len(f.AnyOf))
		for k, v := range f.Eq {
			must = append(must, map[string]any{qKey: k, qMatch: map[string]any{"value": v}})
		}
		for k, vals := range f.AnyOf {
			if len(vals) > 0 {
				must = append(must, map[string]any{qKey: k, qMatch: map[string]any{"any": vals}})
			}
		}
		filter := map[string]any{"must": must}
		mustNot := make([]map[string]any, 0, len(f.NoneOf))
		for k, vals := range f.NoneOf {
			if len(vals) > 0 {
				mustNot = append(mustNot, map[string]any{qKey: k, qMatch: map[string]any{"any": vals}})
			}
		}
		if len(mustNot) > 0 {
			filter["must_not"] = mustNot
		}
		body["filter"] = filter
	}
	var resp struct {
		Result []SearchHit `json:"result"`
	}
	if err := q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/search", body, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// ScrollPoint is one stored point returned by Scroll (no similarity score).
type ScrollPoint struct {
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload"`
}

// scrollPageSize bounds one scroll request; pagination assembles the rest.
const scrollPageSize = 256

// scrollCursor keeps pagination state across scroll pages: ordered mode walks
// an inclusive start_from value, unordered mode walks next_page_offset.
type scrollCursor struct {
	orderBy   string
	startFrom any
	offset    any
}

// body assembles one page request. requested may exceed want by one in ordered
// continuations: start_from is inclusive, so the boundary point comes back
// again and is stripped by the caller.
func (c *scrollCursor) body(must []map[string]any, want int) (map[string]any, int) {
	requested := want
	body := map[string]any{
		"filter":       map[string]any{"must": must},
		"with_payload": true,
	}
	switch {
	case c.orderBy != "" && c.startFrom != nil:
		requested = want + 1
		body["order_by"] = map[string]any{qKey: c.orderBy, qDirection: qAsc, "start_from": c.startFrom}
	case c.orderBy != "":
		body["order_by"] = map[string]any{qKey: c.orderBy, qDirection: qAsc}
	case c.offset != nil:
		body["offset"] = c.offset
	}
	body["limit"] = requested
	return body, requested
}

// advance moves the cursor past the points collected so far; ok=false means
// pagination cannot continue.
func (c *scrollCursor) advance(collected []ScrollPoint, nextPageOffset any) bool {
	if c.orderBy == "" {
		c.offset = nextPageOffset
		return nextPageOffset != nil
	}
	if len(collected) == 0 {
		return false
	}
	v, ok := collected[len(collected)-1].Payload[c.orderBy]
	c.startFrom = v
	return ok
}

// Scroll lists points whose payload matches eq exactly, ordered by the
// orderBy payload field ascending (it must carry a range-capable index — see
// EnsureCollection), up to limit.
func (q *Qdrant) Scroll(ctx context.Context, eq map[string]string, orderBy string, limit int) ([]ScrollPoint, error) {
	must := make([]map[string]any, 0, len(eq))
	for k, v := range eq {
		must = append(must, map[string]any{qKey: k, qMatch: map[string]any{"value": v}})
	}

	out := make([]ScrollPoint, 0, min(limit, scrollPageSize))
	cur := scrollCursor{orderBy: orderBy, startFrom: nil, offset: nil}
	for len(out) < limit {
		body, requested := cur.body(must, min(scrollPageSize, limit-len(out)))
		var resp struct {
			Result struct {
				Points         []ScrollPoint `json:"points"`
				NextPageOffset any           `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/scroll", body, &resp); err != nil {
			return nil, err
		}
		got := len(resp.Result.Points)
		pts := resp.Result.Points
		if orderBy != "" && cur.startFrom != nil && got > 0 {
			pts = pts[1:] // граничный дубликат включительного start_from
		}
		out = append(out, pts...)
		if got < requested || !cur.advance(out, resp.Result.NextPageOffset) {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Ping checks reachability for readiness probes.
func (q *Qdrant) Ping(ctx context.Context) error {
	return q.do(ctx, http.MethodGet, "/readyz", nil, nil)
}

func (q *Qdrant) do(ctx context.Context, method, path string, in, out any) error {
	var reader io.Reader
	if in != nil {
		raw, err := jsonx.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, q.base+path, reader)
	if err != nil {
		return fmt.Errorf("qdrant new request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := q.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("qdrant %s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}
	if out != nil {
		if derr := jsonx.NewDecoder(resp.Body).Decode(out); derr != nil {
			return fmt.Errorf("decode qdrant response: %w", derr)
		}
	}
	return nil
}
