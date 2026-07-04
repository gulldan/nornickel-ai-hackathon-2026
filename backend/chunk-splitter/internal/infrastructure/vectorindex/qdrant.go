// Package vectorindex adapts platform/vectorstore.Qdrant to the application's
// VectorStore port. It maps the application's VectorPoint into the platform
// Point, building the payload (document_id, owner_id, filename, text,
// chunk_index, char_start, char_end, page_start, page_end, section_heading) the
// retrieval side expects.
package vectorindex

import (
	"context"

	"github.com/example/chunk-splitter/internal/application"
	"github.com/example/chunk-splitter/internal/platform/vectorstore"
)

// Adapter wraps a Qdrant client bound to a single collection.
type Adapter struct {
	client *vectorstore.Qdrant
}

// New builds an Adapter over the given Qdrant client.
func New(client *vectorstore.Qdrant) *Adapter {
	return &Adapter{client: client}
}

// Upsert maps application points to vectorstore points and upserts them.
func (a *Adapter) Upsert(ctx context.Context, points []application.VectorPoint) error {
	out := make([]vectorstore.Point, len(points))
	for i, p := range points {
		payload := map[string]any{
			"document_id": p.DocumentID,
			"owner_id":    p.OwnerID,
			"filename":    p.Filename,
			"text":        p.Text,
			"chunk_index": p.ChunkIndex,
			// [char_start, char_end) RUNE offsets of this chunk within the
			// document's full extracted text (end-exclusive); [0, 0] when the
			// span could not be located unambiguously (correct-or-absent).
			"char_start": p.CharStart,
			"char_end":   p.CharEnd,
			// 1-based page span and enclosing section heading (0/"" when the
			// parser supplied no page/section structure).
			"page_start":      p.PageStart,
			"page_end":        p.PageEnd,
			"section_heading": p.SectionHeading,
			// RAPTOR hierarchy tags (additive). For chunk leaves: node_type
			// "chunk", node_level 0, member_ids []. raptor-worker writes summary
			// points into the SAME collection with node_type "raptor_summary",
			// node_level >= 1 and the member chunk ids, so retrieval can collapse
			// the tree and expand a summary back to its leaves.
			"node_type":  p.NodeType,
			"node_level": p.NodeLevel,
			"member_ids": memberIDs(p.MemberIDs),
		}
		for key, value := range p.Metadata {
			if _, exists := payload[key]; !exists {
				payload[key] = value
			}
		}
		out[i] = vectorstore.Point{
			ID:      p.ID,
			Vector:  p.Vector,
			Payload: payload,
		}
	}
	return a.client.Upsert(ctx, out)
}

// memberIDs returns ids as-is, or an empty (non-nil) slice when there are none,
// so a chunk leaf's payload carries member_ids: [] (JSON array) rather than null —
// a uniform schema with the summary points raptor-worker writes.
func memberIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}
