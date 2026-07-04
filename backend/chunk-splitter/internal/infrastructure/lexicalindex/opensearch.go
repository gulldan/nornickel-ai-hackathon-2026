// Package lexicalindex adapts platform/searchstore.OpenSearch to the
// application's SearchStore port. It maps the application's SearchDocs into the
// platform Docs and indexes a whole document's chunks in one _bulk request.
package lexicalindex

import (
	"context"

	"github.com/example/chunk-splitter/internal/application"
	"github.com/example/chunk-splitter/internal/platform/searchstore"
)

// Adapter wraps an OpenSearch client bound to a single index.
type Adapter struct {
	client *searchstore.OpenSearch
}

// New builds an Adapter over the given OpenSearch client.
func New(client *searchstore.OpenSearch) *Adapter {
	return &Adapter{client: client}
}

// Index maps the application SearchDocs to searchstore Docs and bulk-indexes
// them in a single request. A detected section is carried in Metadata so BM25
// hits expose the same section provenance as the vector store.
func (a *Adapter) Index(ctx context.Context, docs []application.SearchDoc) error {
	out := make([]searchstore.Doc, 0, len(docs))
	for _, d := range docs {
		meta := copyMetadata(d.Metadata)
		if d.Section != "" {
			if meta == nil {
				meta = map[string]string{}
			}
			meta["section"] = d.Section
		}
		out = append(out, searchstore.Doc{
			ID:         d.ID,
			Text:       d.Text,
			DocumentID: d.DocumentID,
			OwnerID:    d.OwnerID,
			Filename:   d.Filename,
			Metadata:   meta,
		})
	}
	return a.client.BulkIndex(ctx, out)
}

func copyMetadata(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
