package retrieval

import (
	"context"
	"fmt"
	"sort"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/vectorstore"
)

// chunkScrollLimit bounds one document's chunk listing; documents beyond it
// are truncated rather than failing the preview (a 3000-chunk view is already
// far past what the reader pane can usefully show). Chunks arrive ordered by
// chunk_index, so the cap is a stable document prefix.
const chunkScrollLimit = 3000

// ChunkReader implements domain.ChunkSource over Qdrant's scroll API: the
// vector store's payload (text + chunk_index) doubles as the platform's
// canonical store of extracted document content.
type ChunkReader struct {
	client *vectorstore.Qdrant
}

// NewChunkReader adapts a vectorstore.Qdrant client to domain.ChunkSource.
func NewChunkReader(client *vectorstore.Qdrant) *ChunkReader {
	return &ChunkReader{client: client}
}

// DocumentChunks returns the document's chunks ordered by chunk_index.
// document_id selects the document; a non-empty ownerID additionally pins the
// tenant (the empty string reads the shared corpus).
func (r *ChunkReader) DocumentChunks(ctx context.Context, ownerID, documentID string) ([]domain.StoredChunk, error) {
	eq := map[string]string{payloadDocumentID: documentID}
	if ownerID != "" {
		eq["owner_id"] = ownerID
	}
	points, err := r.client.Scroll(ctx, eq, "chunk_index", chunkScrollLimit)
	if err != nil {
		return nil, fmt.Errorf("scroll chunks: %w", err)
	}
	chunks := make([]domain.StoredChunk, 0, len(points))
	for _, p := range points {
		chunks = append(chunks, domain.StoredChunk{
			ID:    p.ID,
			Index: payloadInt(p.Payload, "chunk_index"),
			Text:  payloadString(p.Payload, "text"),
		})
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	return chunks, nil
}
