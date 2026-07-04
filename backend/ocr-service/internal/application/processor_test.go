package application_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/example/ocr-service/internal/application"
	"github.com/example/ocr-service/internal/domain"
	"github.com/example/ocr-service/internal/platform/contracts"

	commonv1 "github.com/example/ocr-service/internal/platform/genproto/common/v1"
)

// fakeExtractor is a scripted domain.TextExtractor.
type fakeExtractor struct {
	res domain.Extraction
	err error
}

func (f *fakeExtractor) Extract(_ context.Context, _ []byte, _ string) (domain.Extraction, error) {
	return f.res, f.err
}

// fakeStore serves canned bytes and never offloads in these tests.
type fakeStore struct {
	data   []byte
	getErr error
}

func (f *fakeStore) GetBytes(_ context.Context, _ string) ([]byte, error) { return f.data, f.getErr }

func (f *fakeStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) error {
	return nil
}

// fakeStatus records status transitions.
type fakeStatus struct {
	mu       sync.Mutex
	statuses []string
}

func (f *fakeStatus) UpdateDocumentStatus(_ context.Context, _, status, _ string, _ *int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeStatus) seen(status string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.statuses {
		if s == status {
			return true
		}
	}
	return false
}

// fakePublisher records publishes and can fail a chosen exchange.
type fakePublisher struct {
	mu           sync.Mutex
	parsed       *commonv1.DocumentParsed
	events       []*commonv1.IngestionEvent
	failExchange string
}

func (f *fakePublisher) PublishProto(_ context.Context, exchange, _ string, msg proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failExchange != "" && exchange == f.failExchange {
		return errors.New("publish boom")
	}
	switch m := msg.(type) {
	case *commonv1.DocumentParsed:
		f.parsed = m
	case *commonv1.IngestionEvent:
		f.events = append(f.events, m)
	}
	return nil
}

// event returns the first recorded event with the given status, or nil.
func (f *fakePublisher) event(status string) *commonv1.IngestionEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events {
		if ev.GetStatus() == status {
			return ev
		}
	}
	return nil
}

func uploaded() *commonv1.DocumentUploaded {
	return &commonv1.DocumentUploaded{
		DocumentId: "doc-1", OwnerId: "owner-1", ObjectKey: "raw/doc-1",
		Filename: "scan.pdf", MimeType: "application/pdf", TraceId: "trace-1",
	}
}

// Per-page recognition yields joined text, the chunk-splitter page metadata
// (empty pages keep offsets strictly increasing) and OCR-stage progress events.
func TestProcessPagesCarryOffsets(t *testing.T) {
	ext := &fakeExtractor{res: domain.Extraction{Pages: []string{"Первая", "", "третья"}}}
	status := &fakeStatus{}
	pub := &fakePublisher{}
	p := application.New(ext, &fakeStore{data: []byte("pdf")}, status, pub)

	if err := p.Process(context.Background(), uploaded()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if pub.parsed == nil {
		t.Fatal("no DocumentParsed published")
	}
	if got := pub.parsed.GetText(); got != "Первая\n\n\n\nтретья" {
		t.Fatalf("text = %q, want pages joined with the separator", got)
	}
	md := pub.parsed.GetMetadata()
	// Rune offsets: "Первая"=6 runes, separator=2, empty page, separator=2.
	if md["page_offsets"] != "[0,8,10]" || md["page_count"] != "3" {
		t.Fatalf("page metadata = %v, want offsets [0,8,10] and count 3", md)
	}
	if !status.seen(contracts.StatusOCR) || !status.seen(contracts.StatusParsed) {
		t.Fatalf("statuses = %v, want ocr then parsed", status.statuses)
	}
	start := pub.event(contracts.StatusOCR)
	if start == nil || start.GetMessage() != "Распознавание сканов" {
		t.Fatalf("OCR start event = %+v", start)
	}
	done := pub.events[1] // start, progress, parsed
	if done.GetStatus() != contracts.StatusOCR || done.GetStageCurrent() != 3 || done.GetStageTotal() != 3 {
		t.Fatalf("OCR progress event = %+v, want 3/3", done)
	}
}

// Without page structure the backend's flat text is forwarded with no page
// metadata and no progress event.
func TestProcessFlatTextNoMetadata(t *testing.T) {
	ext := &fakeExtractor{res: domain.Extraction{Text: "flat scan text"}}
	pub := &fakePublisher{}
	p := application.New(ext, &fakeStore{data: []byte("img")}, &fakeStatus{}, pub)

	if err := p.Process(context.Background(), uploaded()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if pub.parsed.GetText() != "flat scan text" || pub.parsed.GetMetadata() != nil {
		t.Fatalf("parsed = %+v, want flat text and nil metadata", pub.parsed)
	}
	for _, ev := range pub.events {
		if ev.GetStageTotal() != 0 {
			t.Fatalf("unexpected progress event: %+v", ev)
		}
	}
}

// A first-attempt failure only requeues; the redelivered attempt marks failed.
func TestProcessFailOnlyOnRedelivery(t *testing.T) {
	ext := &fakeExtractor{err: errors.New("ocr down")}
	status := &fakeStatus{}
	pub := &fakePublisher{}
	p := application.New(ext, &fakeStore{data: []byte("pdf")}, status, pub)

	if err := p.Process(context.Background(), uploaded()); err == nil {
		t.Fatal("Process expected error, got nil")
	}
	if status.seen(contracts.StatusFailed) || pub.event(contracts.StatusFailed) != nil {
		t.Fatal("first attempt must not surface failed")
	}

	redelivered := contracts.WithRedelivered(context.Background(), true)
	if err := p.Process(redelivered, uploaded()); err == nil {
		t.Fatal("Process expected error, got nil")
	}
	if !status.seen(contracts.StatusFailed) || pub.event(contracts.StatusFailed) == nil {
		t.Fatal("final attempt must mark the document failed")
	}
}
