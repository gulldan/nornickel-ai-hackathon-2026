package application

// Integration regression guard that drives the REAL Indexer.Process loop (not a
// re-implementation) through fake stores and asserts the provenance written to
// the vector store: char offsets reproduce each chunk, and page-aware windowing
// keeps every chunk on a single page. This pins the production code path — e.g.
// deleting the cross-page carry reset would make a first-of-page chunk span two
// pages and fail here.

import (
	"context"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

type noopStatus struct{}

func (noopStatus) UpdateDocumentStatus(context.Context, string, string, string, *int32) error {
	return nil
}

type dummyEmbedder struct{}

func (dummyEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0.1}
	}
	return out, nil
}

type capturingVectors struct{ points []VectorPoint }

func (c *capturingVectors) Upsert(_ context.Context, pts []VectorPoint) error {
	c.points = append(c.points, pts...)
	return nil
}

type noopSearch struct{}

func (noopSearch) Index(context.Context, []SearchDoc) error { return nil }

type noopPub struct{}

func (noopPub) PublishProto(context.Context, string, string, proto.Message) error { return nil }

func TestProcess_PageProvenance_RealLoop(t *testing.T) {
	page1 := "Первая страница про буровые растворы и плотность жидкости, длинная фраза для нескольких чанков."
	page2 := "Вторая страница описывает коррозию насосно-компрессорных труб и защиту от сероводорода в скважине."
	page3 := "Третья страница посвящена композитным материалам и температурной стойкости при высоких нагрузках."
	full := page1 + "\n" + page2 + "\n" + page3

	r2 := utf8.RuneCountInString(page1 + "\n")
	r3 := utf8.RuneCountInString(page1 + "\n" + page2 + "\n")
	raw := mustJSON(t, []int{0, r2, r3})

	vecs := &capturingVectors{}
	ix := New(
		splitter.NewRecursive(50, 10),
		noopStatus{}, dummyEmbedder{}, vecs, noopSearch{}, noopPub{},
		nil, nil, // pacer, object store (text is inline → store unused)
		nil, 0, // tokenizer (nil → rune splitter, unchanged path), maxTokens (→ default)
		0, 8, // textMax (→ default), batchSize
		120, 10, // splitWindow (bytes), overlap (runes)
		true, // contextual headers on (default)
		nil,  // metrics
	)

	evt := &commonv1.DocumentParsed{
		DocumentId: "doc1",
		OwnerId:    "u1",
		Filename:   "report.pdf",
		Text:       full,
		Metadata:   map[string]string{"page_offsets": string(raw)},
	}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(vecs.points) == 0 {
		t.Fatal("no vector points captured — Process produced nothing")
	}

	spans, pages := 0, 0
	for _, p := range vecs.points {
		if p.CharStart == 0 && p.CharEnd == 0 {
			// Unlocated chunk: page annotation must also be unset.
			if p.PageStart != 0 || p.PageEnd != 0 {
				t.Fatalf("unlocated chunk carries page %d-%d: %q", p.PageStart, p.PageEnd, p.Text)
			}
			continue
		}
		// Offsets (from the real loop's baseRune + LocateOffsets) reproduce the chunk.
		if got, want := normWS(sliceRunesAbs(full, p.CharStart, p.CharEnd)), normWS(p.Text); got != want {
			t.Fatalf("Process offsets wrong: span %q != chunk %q", got, want)
		}
		spans++
		// Page-aware windowing: every chunk lands on exactly one in-range page.
		if p.PageStart != p.PageEnd {
			t.Fatalf("chunk spans pages %d..%d: %q", p.PageStart, p.PageEnd, p.Text)
		}
		if p.PageStart < 1 || p.PageStart > 3 {
			t.Fatalf("page %d out of range: %q", p.PageStart, p.Text)
		}
		pages++
	}
	if spans == 0 || pages == 0 {
		t.Fatal("nothing verified — expected located, page-annotated chunks")
	}
	t.Logf("real Process: %d offset spans reproduced, %d single-page chunks", spans, pages)
}
