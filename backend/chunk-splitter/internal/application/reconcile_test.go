package application

// Tests for §18 hardening on the REAL Process loop: a textMax truncation must
// leave a STRUCTURED, machine-parseable marker in the persisted status (so
// downstream can downweight the document), and a mid-batch store failure must
// drive the document to "failed" — never a clean "indexed" — keeping the partial
// dual-write observable.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/example/chunk-splitter/internal/infrastructure/splitter"
	"github.com/example/chunk-splitter/internal/platform/contracts"
	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

// statusCall records one UpdateDocumentStatus invocation.
type statusCall struct {
	status string
	msg    string
	count  int32
	hasCnt bool
}

// capturingStatus records every status transition so a test can inspect the
// terminal status and message.
type capturingStatus struct{ calls []statusCall }

func (c *capturingStatus) UpdateDocumentStatus(
	_ context.Context, _, status, message string, chunkCount *int32,
) error {
	call := statusCall{status: status, msg: message, count: 0, hasCnt: chunkCount != nil}
	if chunkCount != nil {
		call.count = *chunkCount
	}
	c.calls = append(c.calls, call)
	return nil
}

// last returns the final recorded status call.
func (c *capturingStatus) last() statusCall { return c.calls[len(c.calls)-1] }

// staticStore is an ObjectStore returning a fixed body for any key, to exercise
// the claim-check (text_object_key) path and its truncation.
type staticStore struct{ body []byte }

func (s staticStore) GetBytes(context.Context, string) ([]byte, error) { return s.body, nil }

// failingSearch fails every Index call, simulating a lexical-store outage after
// the vector upsert already landed (a partial dual-write).
type failingSearch struct{}

func (failingSearch) Index(context.Context, []SearchDoc) error {
	return errors.New("opensearch down")
}

func TestProcess_TruncationRecordsStructuredFlag(t *testing.T) {
	const textMax = 64 // bytes; tiny so a modest body overflows it
	big := []byte(strings.Repeat("материаловедение ", 200))
	status := &capturingStatus{}
	ix := New(
		splitter.NewRecursive(40, 8),
		status, dummyEmbedder{}, &capturingVectors{}, noopSearch{}, noopPub{},
		nil, staticStore{body: big}, // pacer, object store (claim-check text)
		nil, 0, // tokenizer (rune splitter), maxTokens (default)
		textMax, 8, // textMax (force truncation), batchSize
		1<<20, 10, // splitWindow, overlap
		true, // contextual headers on (default)
		nil,  // metrics
	)

	evt := &commonv1.DocumentParsed{
		DocumentId:    "doc-trunc",
		OwnerId:       "u1",
		Filename:      "big.txt",
		TextObjectKey: "blob/key",
	}
	if err := ix.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}

	final := status.last()
	if final.status != contracts.StatusIndexed {
		t.Fatalf("terminal status = %q, want %q", final.status, contracts.StatusIndexed)
	}
	// Structured, parseable marker plus the human note must both be present.
	if !strings.Contains(final.msg, "[truncated ") || !strings.Contains(final.msg, "bytes]") {
		t.Fatalf("status_msg missing structured truncation marker: %q", final.msg)
	}
	if !strings.Contains(final.msg, "усеч") {
		t.Fatalf("status_msg missing human truncation note: %q", final.msg)
	}
}

func TestProcess_PartialDualWriteMarksFailed(t *testing.T) {
	status := &capturingStatus{}
	ix := New(
		splitter.NewRecursive(40, 8),
		status, dummyEmbedder{}, &capturingVectors{}, failingSearch{}, noopPub{},
		nil, nil, // pacer, object store (inline text)
		nil, 0,
		0, 4,
		1<<20, 8,
		true, // contextual headers on (default)
		nil,
	)
	evt := &commonv1.DocumentParsed{
		DocumentId: "doc-partial",
		OwnerId:    "u1",
		Filename:   "x.txt",
		Text: "Достаточно длинный текст чтобы образовать хотя бы один чанк для индексации точно. " +
			"Второе предложение добавляет ещё немного слов для надёжности проверки здесь.",
	}
	err := ix.Process(context.Background(), evt)
	if err == nil {
		t.Fatal("expected Process to error on lexical-store failure")
	}
	final := status.last()
	if final.status != contracts.StatusFailed {
		t.Fatalf("terminal status = %q, want %q (partial dual-write must not be a clean indexed)",
			final.status, contracts.StatusFailed)
	}
}
