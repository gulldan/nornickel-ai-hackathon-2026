package vectorstore_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/example/rag-mvp/pkg/jsonx"
	"github.com/example/rag-mvp/pkg/vectorstore"
)

const testCollection = "chunks"

// recorder captures the method, path, content type and body of the last request.
type recorder struct {
	method      string
	path        string
	rawQuery    string
	contentType string
	body        []byte
}

// newServer starts an httptest server whose handler records the request and
// delegates the response to h.
func newServer(t *testing.T, rec *recorder, h func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.rawQuery = r.URL.RawQuery
		rec.contentType = r.Header.Get("Content-Type")
		rec.body, _ = io.ReadAll(r.Body)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestEnsureCollectionCreates creates the collection when it is missing and then
// installs the four payload indexes.
func TestEnsureCollectionCreates(t *testing.T) {
	var paths []string
	var indexFields []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet:
			// Report the collection as missing to force creation.
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/index"):
			body, _ := io.ReadAll(r.Body)
			var idx struct {
				FieldName string `json:"field_name"`
			}
			_ = jsonx.Unmarshal(body, &idx)
			indexFields = append(indexFields, idx.FieldName)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	if err := c.EnsureCollection(context.Background(), 768); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	wantCreate := "PUT /collections/" + testCollection
	found := false
	for _, p := range paths {
		if p == wantCreate {
			found = true
		}
	}
	if !found {
		t.Errorf("missing collection create %q in %v", wantCreate, paths)
	}
	want := []string{"owner_id", "document_id", "node_type", "chunk_index"}
	if len(indexFields) != len(want) {
		t.Fatalf("index fields = %v, want %v", indexFields, want)
	}
	for i, f := range want {
		if indexFields[i] != f {
			t.Errorf("index field[%d] = %q, want %q", i, indexFields[i], f)
		}
	}
}

// TestEnsureCollectionExists skips creation when the collection already exists
// but still ensures the payload indexes.
func TestEnsureCollectionExists(t *testing.T) {
	created := false
	indexes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/index"):
			indexes++
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			created = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	if err := c.EnsureCollection(context.Background(), 512); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if created {
		t.Error("EnsureCollection created the collection though it already exists")
	}
	if indexes != 4 {
		t.Errorf("payload index calls = %d, want 4", indexes)
	}
}

// TestEnsureCollectionIndexAlreadyExists tolerates the "already exists" answer
// from the payload-index call.
func TestEnsureCollectionIndexAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/index"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":{"error":"index already exists"}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	if err := c.EnsureCollection(context.Background(), 256); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
}

// TestEnsureCollectionCreateError surfaces a non-2xx from the create call.
func TestEnsureCollectionCreateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("create failed"))
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	err := c.EnsureCollection(context.Background(), 768)
	if err == nil {
		t.Fatal("EnsureCollection: want error, got nil")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error = %v, want status 500", err)
	}
}

// TestEnsureCollectionIndexError surfaces a non-"already exists" index failure.
func TestEnsureCollectionIndexError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/index"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("index broke"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	err := c.EnsureCollection(context.Background(), 768)
	if err == nil {
		t.Fatal("EnsureCollection: want error, got nil")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error = %v, want status 500", err)
	}
}

// TestUpsert sends the points payload to the wait=true endpoint.
func TestUpsert(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	points := []vectorstore.Point{
		{ID: "p1", Vector: []float32{0.1, 0.2}, Payload: map[string]any{"owner_id": "o1"}},
		{ID: "p2", Vector: []float32{0.3, 0.4}, Payload: map[string]any{"owner_id": "o2"}},
	}
	if err := c.Upsert(context.Background(), points); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if rec.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", rec.method)
	}
	if rec.path != "/collections/"+testCollection+"/points" {
		t.Errorf("path = %q, want /collections/%s/points", rec.path, testCollection)
	}
	if rec.rawQuery != "wait=true" {
		t.Errorf("query = %q, want wait=true", rec.rawQuery)
	}
	if rec.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", rec.contentType)
	}
	var got struct {
		Points []vectorstore.Point `json:"points"`
	}
	if err := jsonx.Unmarshal(rec.body, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got.Points) != 2 || got.Points[0].ID != "p1" || got.Points[1].ID != "p2" {
		t.Errorf("points = %+v, want p1,p2", got.Points)
	}
}

// TestUpsertEmpty is a no-op that issues no request for an empty batch.
func TestUpsertEmpty(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	if err := c.Upsert(context.Background(), nil); err != nil {
		t.Fatalf("Upsert(nil): %v", err)
	}
	if called {
		t.Error("Upsert(nil) issued a request, want none")
	}
}

// TestUpsertNon2xx propagates a non-2xx status from the upsert request.
func TestUpsertNon2xx(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("dim mismatch"))
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	err := c.Upsert(context.Background(), []vectorstore.Point{{ID: "p", Vector: []float32{1}}})
	if err == nil {
		t.Fatal("Upsert: want error, got nil")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Errorf("error = %v, want status 400", err)
	}
}

// TestSearchParsesHits parses scored points and sends a filter for eq matches.
func TestSearchParsesHits(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":[
			{"id":"p1","score":0.91,"payload":{"text":"alpha","owner_id":"o1"}},
			{"id":"p2","score":0.42,"payload":{"text":"beta"}}
		]}`))
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	hits, err := c.Search(context.Background(), []float32{0.1, 0.2}, 5, map[string]string{"owner_id": "o1"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/collections/"+testCollection+"/points/search" {
		t.Errorf("request = %s %s, want POST search path", rec.method, rec.path)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].ID != "p1" || hits[0].Score != 0.91 || hits[0].Payload["text"] != "alpha" {
		t.Errorf("hit[0] = %+v, want id=p1 score=0.91 text=alpha", hits[0])
	}
	if hits[1].ID != "p2" || hits[1].Score != 0.42 {
		t.Errorf("hit[1] = %+v, want id=p2 score=0.42", hits[1])
	}
	if !bytes.Contains(rec.body, []byte("owner_id")) || !bytes.Contains(rec.body, []byte(`"must"`)) {
		t.Errorf("search body missing owner_id must-filter: %s", rec.body)
	}
	if !bytes.Contains(rec.body, []byte(`"with_payload":true`)) {
		t.Errorf("search body missing with_payload: %s", rec.body)
	}
}

// TestSearchNoFilter omits the filter clause when no eq matches are given.
func TestSearchNoFilter(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":[]}`))
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	hits, err := c.Search(context.Background(), []float32{0.5}, 3, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("hits = %d, want 0", len(hits))
	}
	if bytes.Contains(rec.body, []byte(`"filter"`)) {
		t.Errorf("unfiltered search must not carry a filter clause: %s", rec.body)
	}
}

// TestSearchNon2xx propagates a non-2xx status from the search request.
func TestSearchNon2xx(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("search broke"))
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	_, err := c.Search(context.Background(), []float32{0.1}, 5, nil)
	if err == nil {
		t.Fatal("Search: want error, got nil")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error = %v, want status 500", err)
	}
}

// TestSearchMalformedJSON fails when the search response is not valid JSON.
func TestSearchMalformedJSON(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("nope"))
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	_, err := c.Search(context.Background(), []float32{0.1}, 5, nil)
	if err == nil {
		t.Fatal("Search: want decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error = %v, want decode error", err)
	}
}

// TestScrollUnorderedPaginates walks next_page_offset across two pages: a full
// first page (256 points) forces a continuation that returns the remainder.
func TestScrollUnorderedPaginates(t *testing.T) {
	page := 0
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusOK)
		if page == 0 {
			page++
			_, _ = w.Write(scrollPageJSON(0, 256, `"next_page_offset":"cursor-1"`))
			return
		}
		_, _ = w.Write(scrollPageJSON(256, 2, `"next_page_offset":null`))
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	pts, err := c.Scroll(context.Background(), map[string]string{"document_id": "d1"}, "", 1000)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if len(pts) != 258 {
		t.Fatalf("points = %d, want 258", len(pts))
	}
	if pts[0].ID != "p0" || pts[257].ID != "p257" {
		t.Errorf("boundary points = %q..%q, want p0..p257", pts[0].ID, pts[257].ID)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2 pages", len(bodies))
	}
	// The second page must carry the offset returned by the first.
	if !bytes.Contains(bodies[1], []byte(`"offset":"cursor-1"`)) {
		t.Errorf("second page missing offset cursor: %s", bodies[1])
	}
	// The eq match must appear as a must filter.
	if !bytes.Contains(bodies[0], []byte("document_id")) {
		t.Errorf("scroll body missing document_id filter: %s", bodies[0])
	}
}

// TestScrollOrderedStripsBoundary continues an ordered scroll with an inclusive
// start_from and drops the duplicated boundary point on the second page.
func TestScrollOrderedStripsBoundary(t *testing.T) {
	page := 0
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusOK)
		if page == 0 {
			page++
			// Two points filling the requested page, so pagination continues.
			_, _ = w.Write([]byte(`{"result":{"points":[
				{"id":"p1","payload":{"chunk_index":0}},
				{"id":"p2","payload":{"chunk_index":1}}
			]}}`))
			return
		}
		// p2 repeats as the inclusive start_from boundary and must be stripped.
		_, _ = w.Write([]byte(`{"result":{"points":[
			{"id":"p2","payload":{"chunk_index":1}},
			{"id":"p3","payload":{"chunk_index":2}}
		]}}`))
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	pts, err := c.Scroll(context.Background(), map[string]string{"document_id": "d1"}, "chunk_index", 2)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("points = %d, want 2 (capped at limit)", len(pts))
	}
	if pts[0].ID != "p1" || pts[1].ID != "p2" {
		t.Errorf("points = %v, want p1,p2", []string{pts[0].ID, pts[1].ID})
	}
	if len(bodies) < 1 {
		t.Fatal("no scroll request recorded")
	}
	// The first ordered page asks for order_by on the configured field.
	if !bytes.Contains(bodies[0], []byte("order_by")) || !bytes.Contains(bodies[0], []byte("chunk_index")) {
		t.Errorf("ordered scroll missing order_by: %s", bodies[0])
	}
}

// TestScrollOrderedStartFromCursor sends the last point's order value as the
// inclusive start_from on the continuation page. A full first page (256 points)
// forces the continuation; the second page returns the stripped boundary plus
// the final point.
func TestScrollOrderedStartFromCursor(t *testing.T) {
	page := 0
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusOK)
		if page == 0 {
			page++
			// 256 points ordered by chunk_index 0..255; chunk_index 255 becomes
			// the inclusive start_from for the next page.
			_, _ = w.Write(scrollOrderedPageJSON(0, 256, ""))
			return
		}
		// The boundary point (chunk_index 255) repeats and is stripped; p256 is
		// the genuinely new tail.
		_, _ = w.Write(scrollOrderedPageJSON(255, 2, ""))
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	pts, err := c.Scroll(context.Background(), nil, "chunk_index", 300)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if len(pts) != 257 {
		t.Fatalf("points = %d, want 257 (256 + stripped tail)", len(pts))
	}
	if pts[256].ID != "p256" {
		t.Errorf("tail point = %q, want p256", pts[256].ID)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	// The continuation carries start_from set to the last collected order value.
	if !bytes.Contains(bodies[1], []byte(`"start_from":255`)) {
		t.Errorf("continuation start_from = wrong value: %s", bodies[1])
	}
}

// TestScrollOrderedNoBoundaryKey stops when the order field is absent from the
// last point's payload so the cursor cannot advance.
func TestScrollOrderedNoBoundaryKey(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		// A full page (forces a continuation attempt) but no chunk_index payload,
		// so advance cannot read the next start_from and pagination halts.
		_, _ = w.Write([]byte(`{"result":{"points":[
			{"id":"p1","payload":{"text":"a"}},
			{"id":"p2","payload":{"text":"b"}}
		]}}`))
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	pts, err := c.Scroll(context.Background(), nil, "chunk_index", 2)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("points = %d, want 2", len(pts))
	}
	if calls != 1 {
		t.Errorf("scroll requests = %d, want 1 (cursor cannot advance)", calls)
	}
}

// TestScrollStopsOnShortPage stops when a page returns fewer points than
// requested.
func TestScrollStopsOnShortPage(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"points":[
			{"id":"p1","payload":{"text":"a"}}
		],"next_page_offset":"p1"}}`))
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	pts, err := c.Scroll(context.Background(), nil, "", 50)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("points = %d, want 1", len(pts))
	}
	if calls != 1 {
		t.Errorf("scroll requests = %d, want 1 (short page ends scan)", calls)
	}
}

// TestScrollError propagates a non-2xx status from a scroll page.
func TestScrollError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("scroll broke"))
	}))
	t.Cleanup(srv.Close)
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	_, err := c.Scroll(context.Background(), nil, "", 10)
	if err == nil {
		t.Fatal("Scroll: want error, got nil")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error = %v, want status 500", err)
	}
}

// TestPing succeeds against a reachable readyz endpoint.
func TestPing(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if rec.method != http.MethodGet || rec.path != "/readyz" {
		t.Errorf("request = %s %s, want GET /readyz", rec.method, rec.path)
	}
}

// TestPingUnreachable surfaces a transport error from an unreachable instance.
func TestPingUnreachable(t *testing.T) {
	c := vectorstore.NewQdrant("http://127.0.0.1:0", testCollection)
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping: want transport error, got nil")
	}
}

// TestContextCancelled aborts a request when the context is already cancelled.
func TestContextCancelled(t *testing.T) {
	var rec recorder
	srv := newServer(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":[]}`))
	})
	c := vectorstore.NewQdrant(srv.URL, testCollection)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Search(ctx, []float32{0.1}, 5, nil); err == nil {
		t.Fatal("Search: want context error, got nil")
	}
}

// scrollPageJSON builds a scroll result body with n points starting at index
// start (ids p<start>..) and the given trailing result fields (e.g. an offset).
func scrollPageJSON(start, n int, trailer string) []byte {
	var b strings.Builder
	b.WriteString(`{"result":{"points":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		id := start + i
		b.WriteString(`{"id":"p`)
		b.WriteString(strconv.Itoa(id))
		b.WriteString(`","payload":{"text":"t`)
		b.WriteString(strconv.Itoa(id))
		b.WriteString(`"}}`)
	}
	b.WriteString(`]`)
	if trailer != "" {
		b.WriteByte(',')
		b.WriteString(trailer)
	}
	b.WriteString(`}}`)
	return []byte(b.String())
}

// scrollOrderedPageJSON builds a scroll result body with n points whose ids and
// chunk_index payload both run from start, for ordered-scroll pagination tests.
func scrollOrderedPageJSON(start, n int, trailer string) []byte {
	var b strings.Builder
	b.WriteString(`{"result":{"points":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		id := start + i
		b.WriteString(`{"id":"p`)
		b.WriteString(strconv.Itoa(id))
		b.WriteString(`","payload":{"chunk_index":`)
		b.WriteString(strconv.Itoa(id))
		b.WriteString(`}}`)
	}
	b.WriteString(`]`)
	if trailer != "" {
		b.WriteByte(',')
		b.WriteString(trailer)
	}
	b.WriteString(`}}`)
	return []byte(b.String())
}
