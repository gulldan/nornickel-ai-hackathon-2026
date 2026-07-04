package lexicalindex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/chunk-splitter/internal/application"
	"github.com/example/chunk-splitter/internal/platform/searchstore"
)

// Index маппит SearchDoc в _bulk-запрос платформенного клиента; секция уезжает
// в metadata, пустая — опускается.
func TestAdapterIndex(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_bulk") {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
		}
		_, _ = w.Write([]byte(`{"errors":false}`))
	}))
	defer srv.Close()

	a := New(searchstore.NewOpenSearch(srv.URL, "chunks"))
	err := a.Index(context.Background(), []application.SearchDoc{
		{ID: "c1", Text: "текст", DocumentID: "d1", OwnerID: "o1", Filename: "f.pdf", Section: "Методы"},
		{ID: "c2", Text: "ещё", DocumentID: "d1", OwnerID: "o1", Filename: "f.pdf"},
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !strings.Contains(body, `"c1"`) || !strings.Contains(body, "Методы") {
		t.Fatalf("bulk body missing docs/section: %s", body)
	}
}
