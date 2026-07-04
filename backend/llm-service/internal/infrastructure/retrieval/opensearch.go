package retrieval

import (
	"context"
	"fmt"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/searchstore"
)

// OpenSearchRetriever is the lexical (BM25) half of hybrid retrieval. It runs a
// full-text match over the owner's chunks.
type OpenSearchRetriever struct {
	client *searchstore.OpenSearch
}

// NewOpenSearch adapts a searchstore.OpenSearch client to domain.Retriever.
func NewOpenSearch(client *searchstore.OpenSearch) *OpenSearchRetriever {
	return &OpenSearchRetriever{client: client}
}

// Retrieve runs a BM25 search scoped to the owner. The embedding is unused on
// the lexical side. An empty query yields no results.
func (r *OpenSearchRetriever) Retrieve(
	ctx context.Context,
	query string,
	_ []float32,
	f domain.RetrievalFilter,
	topN int,
) ([]domain.RetrievedChunk, error) {
	if query == "" || topN <= 0 {
		return nil, nil
	}
	hits, err := r.client.SearchFiltered(ctx, query, f.OwnerID, topN, f.ScopeDocumentIDs, f.ExcludeDocumentIDs)
	if err != nil {
		return nil, fmt.Errorf("opensearch search: %w", err)
	}
	chunks := make([]domain.RetrievedChunk, 0, len(hits))
	for _, h := range hits {
		chunks = append(chunks, domain.RetrievedChunk{
			ID:         h.ID,
			Text:       h.Text,
			DocumentID: h.DocumentID,
			Filename:   h.Filename,
			// chunk_index is not stored in the search index; it is backfilled
			// from the Qdrant payload during fusion when the chunk also matches
			// the vector search.
			Score: h.Score,
		})
	}
	return chunks, nil
}
