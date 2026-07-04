package application

// Regression guard for chunk-splitter idempotency under AMQP redelivery: chunk
// point IDs must be a deterministic function of (document_id, absolute
// chunk_index), and the vector point and lexical document for one chunk must
// share that ID — so re-processing the SAME DocumentParsed OVERWRITES the
// existing records in Qdrant and OpenSearch instead of inserting duplicates.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

// capturingSearch records the SearchDocs (with their IDs) handed to the store so
// the test can assert vector/lexical ID parity. noopSearch (in the integration
// test) discards them, so a dedicated capturing double is needed here.
type capturingSearch struct{ docs []SearchDoc }

func (c *capturingSearch) Index(_ context.Context, docs []SearchDoc) error {
	c.docs = append(c.docs, docs...)
	return nil
}

// chunkPointID is a pure UUIDv5 of (document_id, absolute chunk_index): stable
// across calls, sensitive to both inputs, and a valid UUID.
func TestChunkPointID_Deterministic(t *testing.T) {
	a := chunkPointID("doc1", 0)
	if a != chunkPointID("doc1", 0) {
		t.Fatal("same (document_id, chunk_index) produced different IDs")
	}
	if a == chunkPointID("doc1", 1) {
		t.Fatal("different chunk_index produced the same ID")
	}
	if a == chunkPointID("doc2", 0) {
		t.Fatal("different document_id produced the same ID")
	}
	if _, err := uuid.Parse(a); err != nil {
		t.Fatalf("chunk point ID is not a valid UUID: %q (%v)", a, err)
	}
}

// runProcess drives the real Process loop over a multi-chunk document, capturing
// the vector points and search docs emitted, so a test can compare two runs.
func runProcess(t *testing.T) (*capturingVectors, *capturingSearch) {
	t.Helper()
	// Long enough to force multiple chunks (and multiple windows: splitWindow is
	// small), so absolute vs per-window chunk_index actually matters.
	text := "Idempotency requires deterministic identifiers across redeliveries. " +
		"Каждый чанк получает стабильный идентификатор по документу и индексу. " +
		"Повторная доставка обязана перезаписать, а не задвоить точки в хранилищах."
	vecs := &capturingVectors{}
	srch := &capturingSearch{}
	ix := New(
		splitter.NewRecursive(40, 8),
		noopStatus{}, dummyEmbedder{}, vecs, srch, noopPub{},
		nil, nil, // pacer, object store
		nil, 0, // tokenizer (rune splitter), maxTokens (default)
		0, 4, // textMax (default), batchSize
		90, 8, // splitWindow (bytes, forces several windows), overlap (runes)
		true, // contextual headers on (default)
		nil,  // metrics
	)
	evt := &commonv1.DocumentParsed{
		DocumentId: "doc-idem",
		OwnerId:    "u1",
		Filename:   "x.txt",
		Text:       text,
	}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(vecs.points) == 0 || len(srch.docs) == 0 {
		t.Fatal("Process produced no points/docs")
	}
	return vecs, srch
}

// TestProcess_IdempotentIDs pins the redelivery-safety contract on the REAL
// Process loop: two runs of the same event yield the same per-chunk IDs (so the
// stores overwrite), and within a run the vector point and search doc for each
// chunk carry the same ID (so hybrid retrieval can fuse them).
func TestProcess_IdempotentIDs(t *testing.T) {
	v1, s1 := runProcess(t)
	v2, s2 := runProcess(t)

	if len(v1.points) != len(v2.points) {
		t.Fatalf("chunk count not stable across runs: %d vs %d", len(v1.points), len(v2.points))
	}
	if len(v1.points) != len(s1.docs) {
		t.Fatalf("vector/search count mismatch: %d points, %d docs", len(v1.points), len(s1.docs))
	}

	seen := make(map[string]bool, len(v1.points))
	for i := range v1.points {
		id := v1.points[i].ID
		// Re-delivery: same chunk → same ID (overwrites the prior record).
		if id != v2.points[i].ID {
			t.Fatalf("chunk %d ID changed between runs: %q != %q", i, id, v2.points[i].ID)
		}
		// Vector point and lexical document for one chunk share the ID.
		if id != s1.docs[i].ID {
			t.Fatalf("chunk %d: vector ID %q != search ID %q", i, id, s1.docs[i].ID)
		}
		if id != s2.docs[i].ID {
			t.Fatalf("chunk %d: vector ID %q != search ID %q (run 2)", i, id, s2.docs[i].ID)
		}
		// Distinct chunks get distinct IDs (no collisions across windows).
		if seen[id] {
			t.Fatalf("duplicate chunk ID %q across chunks", id)
		}
		seen[id] = true
		// The ID matches the pure helper for this chunk's ABSOLUTE index.
		if want := chunkPointID("doc-idem", v1.points[i].ChunkIndex); id != want {
			t.Fatalf("chunk %d ID %q != chunkPointID(doc, %d)=%q", i, id, v1.points[i].ChunkIndex, want)
		}
	}
}
