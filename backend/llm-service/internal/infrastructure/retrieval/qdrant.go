// Package retrieval contains the two adapters that implement domain.Retriever:
// a vector retriever over Qdrant and a lexical (BM25) retriever over OpenSearch.
// Together they form the platform's hybrid retrieval. Both treat a missing
// collection/index as "no results" so llm-service degrades gracefully before
// chunk-splitter has provisioned the backing stores.
package retrieval

import (
	"context"
	"fmt"

	"github.com/example/llm-service/internal/domain"
	"github.com/example/llm-service/internal/platform/vectorstore"
)

// QdrantRetriever is the vector half of hybrid retrieval. It searches the
// owner's chunk embeddings by cosine similarity and reads the standard payload
// (document_id, owner_id, filename, text, chunk_index) written by chunk-splitter.
type QdrantRetriever struct {
	client *vectorstore.Qdrant
}

const payloadDocumentID = "document_id"

// NewQdrant adapts a vectorstore.Qdrant client to domain.Retriever.
func NewQdrant(client *vectorstore.Qdrant) *QdrantRetriever {
	return &QdrantRetriever{client: client}
}

// Retrieve runs a cosine-similarity search. A non-empty ownerID scopes the
// search to one tenant; the empty string searches the shared corpus. The query
// string is unused here — the application layer already embedded it. A nil or
// empty embedding yields no results (nothing to search with).
func (r *QdrantRetriever) Retrieve(
	ctx context.Context,
	_ string,
	embedding []float32,
	f domain.RetrievalFilter,
	topN int,
) ([]domain.RetrievedChunk, error) {
	if len(embedding) == 0 || topN <= 0 {
		return nil, nil
	}
	filter := vectorstore.SearchFilter{}
	if f.OwnerID != "" {
		filter.Eq = map[string]string{"owner_id": f.OwnerID}
	}
	if len(f.ScopeDocumentIDs) > 0 {
		filter.AnyOf = map[string][]string{payloadDocumentID: f.ScopeDocumentIDs}
	}
	if len(f.ExcludeDocumentIDs) > 0 {
		filter.NoneOf = map[string][]string{payloadDocumentID: f.ExcludeDocumentIDs}
	}
	hits, err := r.client.SearchFiltered(ctx, embedding, topN, filter)
	if err != nil {
		return nil, fmt.Errorf("qdrant search: %w", err)
	}
	chunks := make([]domain.RetrievedChunk, 0, len(hits))
	for _, h := range hits {
		chunks = append(chunks, chunkFromPayload(h.ID, float64(h.Score), h.Payload))
	}
	return chunks, nil
}

// chunkFromPayload builds a RetrievedChunk from a Qdrant point id, score and
// payload. It reads the standard chunk provenance plus the RAPTOR tags
// (node_type/node_level/member_ids/members) so a retrieved summary node can be
// expanded to its real leaf chunks for citation upstream.
func chunkFromPayload(id string, score float64, payload map[string]any) domain.RetrievedChunk {
	return domain.RetrievedChunk{
		ID:             id,
		Text:           payloadString(payload, "text"),
		DocumentID:     payloadString(payload, payloadDocumentID),
		Filename:       payloadString(payload, "filename"),
		ChunkIndex:     payloadInt(payload, "chunk_index"),
		Score:          score,
		CharStart:      payloadInt(payload, "char_start"),
		CharEnd:        payloadInt(payload, "char_end"),
		PageStart:      payloadInt(payload, "page_start"),
		PageEnd:        payloadInt(payload, "page_end"),
		SectionHeading: payloadString(payload, "section_heading"),
		NodeType:       payloadString(payload, "node_type"),
		NodeLevel:      payloadInt(payload, "node_level"),
		MemberIDs:      payloadStrings(payload, "member_ids"),
		Members:        payloadMembers(payload, "members"),
	}
}

// payloadStrings reads a string array from a Qdrant payload (e.g. member_ids).
// JSON arrays decode to []any through the generic map; non-string elements are
// skipped. Absent/odd values yield nil.
func payloadStrings(payload map[string]any, key string) []string {
	if payload == nil {
		return nil
	}
	raw, ok := payload[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// payloadMembers reads a RAPTOR summary's denormalised leaf chunks (the "members"
// array) into RetrievedChunks carrying full citation provenance. Each element is
// itself a payload object; absent/odd values yield nil.
func payloadMembers(payload map[string]any, key string) []domain.RetrievedChunk {
	if payload == nil {
		return nil
	}
	raw, ok := payload[key].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]domain.RetrievedChunk, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, chunkFromPayload(payloadString(m, "id"), 0, m))
	}
	return out
}

// payloadString reads a string field from a Qdrant payload, tolerating absence
// and non-string values.
func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

// payloadInt reads an integer field from a Qdrant payload. JSON numbers decode
// to float64 through the generic map, so both float64 and int are handled.
func payloadInt(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	switch v := payload[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
