package application

import (
	"context"
	"strings"
	"testing"

	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

func TestProcess_DelimitedCSVNormalizesRows(t *testing.T) {
	vecs := &capturingVectors{}
	ix := New(
		splitter.NewRecursive(500, 0),
		noopStatus{}, dummyEmbedder{}, vecs, noopSearch{}, noopPub{},
		nil, nil,
		nil, 0,
		0, 8,
		1000, 0,
		true,
		nil,
	)
	evt := &commonv1.DocumentParsed{
		DocumentId: "doc-csv",
		OwnerId:    "u1",
		Filename:   "steel.csv",
		MimeType:   "text/plain; charset=utf-8",
		Text: "formula,yield strength,tensile strength\n" +
			"Fe0.620C0.000953Mn0.000521Si0.00102Cr0.000110Ni0.192Mo0.0176V0.000112Nb0.0000616Co0.146Al0.00318Ti0.0185,2411.5,2473.5\n",
	}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(vecs.points) == 0 {
		t.Fatal("expected indexed vector points")
	}
	found := false
	for _, point := range vecs.points {
		if strings.Contains(point.Text, "source_uri=steel.csv#rows=2") &&
			strings.Contains(point.Text, "yield strength=2411.5") &&
			strings.Contains(point.Text, "tensile strength=2473.5") {
			found = true
			break
		}
	}
	if !found {
		for i, point := range vecs.points {
			t.Logf("point %d: %q", i, point.Text)
		}
		t.Fatal("CSV row evidence was not present in indexed chunks")
	}
}

func TestProcess_NonDelimitedTextStaysPlain(t *testing.T) {
	vecs := &capturingVectors{}
	ix := New(
		splitter.NewRecursive(500, 0),
		noopStatus{}, dummyEmbedder{}, vecs, noopSearch{}, noopPub{},
		nil, nil,
		nil, 0,
		0, 8,
		1000, 0,
		true,
		nil,
	)
	evt := &commonv1.DocumentParsed{
		DocumentId: "doc-text",
		OwnerId:    "u1",
		Filename:   "notes.txt",
		MimeType:   "text/plain",
		Text:       "hello\nworld\n",
	}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(vecs.points) == 0 {
		t.Fatal("expected indexed vector points")
	}
	if strings.Contains(vecs.points[0].Text, "source_uri=") {
		t.Fatalf("plain text unexpectedly transformed: %q", vecs.points[0].Text)
	}
}
