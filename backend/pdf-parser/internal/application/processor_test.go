package application

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/example/pdf-parser/internal/domain"
	"github.com/example/pdf-parser/internal/platform/contracts"

	commonv1 "github.com/example/pdf-parser/internal/platform/genproto/common/v1"
)

// plainExtractor implements only domain.TextExtractor (no layout), so Process
// takes the plain-text branch and emits no page metadata.
type plainExtractor struct {
	text string
	err  error
}

func (e *plainExtractor) Extract(_ context.Context, _ []byte) (string, error) {
	return e.text, e.err
}

// layoutExtractor implements domain.LayoutExtractor, so Process takes the
// layout branch and renders page (and section) metadata.
type layoutExtractor struct {
	doc domain.ExtractedDocument
	err error
}

func (e *layoutExtractor) Extract(_ context.Context, _ []byte) (string, error) {
	return e.doc.Text, e.err
}

func (e *layoutExtractor) ExtractWithLayout(_ context.Context, _ []byte) (domain.ExtractedDocument, error) {
	return e.doc, e.err
}

// fakeStore serves canned bytes for GetBytes and records Put calls; either side
// can be told to fail to exercise the error branches.
type fakeStore struct {
	data    []byte
	getErr  error
	putErr  error
	putKey  string
	putBody string
	puts    int
}

func (s *fakeStore) GetBytes(_ context.Context, _ string) ([]byte, error) {
	return s.data, s.getErr
}

func (s *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	if s.putErr != nil {
		return s.putErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.puts++
	s.putKey = key
	s.putBody = string(b)
	return nil
}

// fakeStatus records the last status set per document id.
type fakeStatus struct {
	statusByID map[string]string
	err        error
}

func newFakeStatus() *fakeStatus {
	return &fakeStatus{statusByID: map[string]string{}, err: nil}
}

func (s *fakeStatus) UpdateDocumentStatus(_ context.Context, id, status, _ string, _ *int32) error {
	s.statusByID[id] = status
	return s.err
}

// fakePublisher records published messages by routing key and can be told to
// fail publishes to the chunk or OCR routes while letting event broadcasts pass.
type fakePublisher struct {
	chunkMsg  *commonv1.DocumentParsed
	ocrMsg    *commonv1.DocumentUploaded
	events    int
	failOn    map[string]error // routing key -> error
	eventsErr error            // returned for every events-exchange publish
}

func newFakePublisher() *fakePublisher {
	return &fakePublisher{chunkMsg: nil, ocrMsg: nil, events: 0, failOn: map[string]error{}, eventsErr: nil}
}

func (p *fakePublisher) PublishProto(_ context.Context, exchange, routingKey string, msg proto.Message) error {
	if exchange == contracts.ExchangeEvents {
		p.events++
		return p.eventsErr
	}
	if err := p.failOn[routingKey]; err != nil {
		return err
	}
	switch routingKey {
	case contracts.RouteChunk:
		if dp, ok := msg.(*commonv1.DocumentParsed); ok {
			p.chunkMsg = dp
		}
	case contracts.RouteParseOCR:
		if du, ok := msg.(*commonv1.DocumentUploaded); ok {
			p.ocrMsg = du
		}
	default:
	}
	return nil
}

// redeliveredCtx marks the context as the broker's final (redelivered)
// attempt, the only one allowed to surface a failed status.
func redeliveredCtx() context.Context {
	return contracts.WithRedelivered(context.Background(), true)
}

func uploadEvt() *commonv1.DocumentUploaded {
	return &commonv1.DocumentUploaded{
		DocumentId: "doc-1",
		OwnerId:    "owner-1",
		ObjectKey:  "uploads/a.pdf",
		Filename:   "a.pdf",
		MimeType:   "application/pdf",
		TraceId:    "trace-1",
	}
}

// A PDF with a text layer is parsed, published inline to the chunk route, and the
// document is marked parsed. Layout metadata (page/section offsets) is carried.
func TestProcess_LayoutSuccessInline(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	pub := newFakePublisher()
	ext := &layoutExtractor{doc: domain.ExtractedDocument{
		Text:           "Введение\nГлава 2",
		PageOffsets:    []int{0, 9},
		SectionOffsets: []domain.SectionOffset{{Rune: 0, Heading: "Введение"}},
	}}

	p := New(ext, store, status, pub)
	if err := p.Process(context.Background(), uploadEvt()); err != nil {
		t.Fatalf("Process returned error, want nil: %v", err)
	}
	if pub.chunkMsg == nil {
		t.Fatal("nothing published to chunk route")
	}
	if pub.chunkMsg.GetText() != "Введение\nГлава 2" {
		t.Errorf("chunk text = %q, want the extracted text inline", pub.chunkMsg.GetText())
	}
	if pub.chunkMsg.GetTextObjectKey() != "" {
		t.Errorf("text_object_key = %q, want empty for inline text", pub.chunkMsg.GetTextObjectKey())
	}
	if pub.chunkMsg.GetSource() != "pdf" {
		t.Errorf("source = %q, want pdf", pub.chunkMsg.GetSource())
	}
	md := pub.chunkMsg.GetMetadata()
	if md["page_count"] != "2" {
		t.Errorf("page_count = %q, want 2", md["page_count"])
	}
	if md["page_offsets"] != "[0,9]" {
		t.Errorf("page_offsets = %q, want [0,9]", md["page_offsets"])
	}
	if !strings.Contains(md["section_offsets"], "Введение") {
		t.Errorf("section_offsets = %q, want to contain the heading", md["section_offsets"])
	}
	if got := status.statusByID["doc-1"]; got != contracts.StatusParsed {
		t.Errorf("final status = %q, want %q", got, contracts.StatusParsed)
	}
	if store.puts != 0 {
		t.Errorf("Put called %d times, want 0 for inline text", store.puts)
	}
}

// A PDF with no text layer is a scan: it is re-routed to OCR and the document
// moves to the ocr status rather than lingering in parsing.
func TestProcess_NoTextLayerRoutesToOCR(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	pub := newFakePublisher()
	ext := &plainExtractor{text: "   \n  "} // whitespace only -> treated as empty

	p := New(ext, store, status, pub)
	if err := p.Process(context.Background(), uploadEvt()); err != nil {
		t.Fatalf("Process returned error, want nil: %v", err)
	}
	if pub.ocrMsg == nil {
		t.Fatal("event not routed to OCR")
	}
	if pub.ocrMsg.GetDocumentId() != "doc-1" {
		t.Errorf("OCR event document id = %q, want doc-1", pub.ocrMsg.GetDocumentId())
	}
	if pub.chunkMsg != nil {
		t.Error("scan must not be published to chunk route")
	}
	if got := status.statusByID["doc-1"]; got != contracts.StatusOCR {
		t.Errorf("status = %q, want %q after the OCR handoff", got, contracts.StatusOCR)
	}
}

// Text larger than the inline ceiling is offloaded to object storage and the
// chunk message carries a text_object_key claim check instead of the text.
func TestProcess_LargeTextOffloaded(t *testing.T) {
	big := strings.Repeat("a", inlineTextMax+1)
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	pub := newFakePublisher()
	ext := &plainExtractor{text: big}

	p := New(ext, store, status, pub)
	if err := p.Process(context.Background(), uploadEvt()); err != nil {
		t.Fatalf("Process returned error, want nil: %v", err)
	}
	if store.puts != 1 {
		t.Fatalf("Put called %d times, want 1 for offloaded text", store.puts)
	}
	if store.putKey != "parsed/doc-1.txt" {
		t.Errorf("offload key = %q, want parsed/doc-1.txt", store.putKey)
	}
	if store.putBody != big {
		t.Errorf("offloaded body length = %d, want %d", len(store.putBody), len(big))
	}
	if pub.chunkMsg == nil {
		t.Fatal("nothing published to chunk route")
	}
	if pub.chunkMsg.GetText() != "" {
		t.Error("chunk text must be empty when offloaded")
	}
	if pub.chunkMsg.GetTextObjectKey() != "parsed/doc-1.txt" {
		t.Errorf("text_object_key = %q, want parsed/doc-1.txt", pub.chunkMsg.GetTextObjectKey())
	}
}

// A download failure returns an error on both attempts, but only the
// redelivered (final) attempt marks the document failed: the first failure is
// requeued by the broker and must not surface in the UI.
func TestProcess_DownloadFailure(t *testing.T) {
	store := &fakeStore{getErr: errors.New("s3 down")}
	status := newFakeStatus()
	pub := newFakePublisher()
	ext := &plainExtractor{text: "irrelevant"}

	p := New(ext, store, status, pub)
	if err := p.Process(context.Background(), uploadEvt()); err == nil {
		t.Fatal("Process returned nil, want error on download failure")
	}
	if got := status.statusByID["doc-1"]; got == contracts.StatusFailed {
		t.Error("first attempt must not mark the document failed")
	}
	if err := p.Process(redeliveredCtx(), uploadEvt()); err == nil {
		t.Fatal("Process returned nil, want error on download failure")
	}
	if got := status.statusByID["doc-1"]; got != contracts.StatusFailed {
		t.Errorf("status = %q, want %q", got, contracts.StatusFailed)
	}
}

// An extraction failure on the final (redelivered) attempt dead-letters the
// message and marks the document failed.
func TestProcess_ExtractFailure(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	pub := newFakePublisher()
	ext := &layoutExtractor{err: errors.New("tika 500")}

	p := New(ext, store, status, pub)
	if err := p.Process(redeliveredCtx(), uploadEvt()); err == nil {
		t.Fatal("Process returned nil, want error on extract failure")
	}
	if got := status.statusByID["doc-1"]; got != contracts.StatusFailed {
		t.Errorf("status = %q, want %q", got, contracts.StatusFailed)
	}
}

// A failure routing a scan to OCR dead-letters the message on the final attempt.
func TestProcess_OCRPublishFailure(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	pub := newFakePublisher()
	pub.failOn[contracts.RouteParseOCR] = errors.New("broker did not confirm")
	ext := &plainExtractor{text: ""}

	p := New(ext, store, status, pub)
	if err := p.Process(redeliveredCtx(), uploadEvt()); err == nil {
		t.Fatal("Process returned nil, want error when OCR publish fails")
	}
	if got := status.statusByID["doc-1"]; got != contracts.StatusFailed {
		t.Errorf("status = %q, want %q", got, contracts.StatusFailed)
	}
}

// A failure offloading oversized text to object storage dead-letters the
// message on the final attempt.
func TestProcess_OffloadStoreFailure(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF"), putErr: errors.New("put failed")}
	status := newFakeStatus()
	pub := newFakePublisher()
	ext := &plainExtractor{text: strings.Repeat("b", inlineTextMax+1)}

	p := New(ext, store, status, pub)
	if err := p.Process(redeliveredCtx(), uploadEvt()); err == nil {
		t.Fatal("Process returned nil, want error when offload Put fails")
	}
	if got := status.statusByID["doc-1"]; got != contracts.StatusFailed {
		t.Errorf("status = %q, want %q", got, contracts.StatusFailed)
	}
}

// A failure publishing the parsed text to the chunk route dead-letters the
// message on the final attempt.
func TestProcess_ChunkPublishFailure(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	pub := newFakePublisher()
	pub.failOn[contracts.RouteChunk] = errors.New("broker did not confirm")
	ext := &plainExtractor{text: "some text"}

	p := New(ext, store, status, pub)
	if err := p.Process(redeliveredCtx(), uploadEvt()); err == nil {
		t.Fatal("Process returned nil, want error when chunk publish fails")
	}
	if got := status.statusByID["doc-1"]; got != contracts.StatusFailed {
		t.Errorf("status = %q, want %q", got, contracts.StatusFailed)
	}
}

// A status-update error is non-fatal: Process still succeeds and publishes,
// exercising the warn-and-continue branch in setStatus.
func TestProcess_StatusUpdateErrorIsNonFatal(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	status.err = errors.New("db unreachable")
	pub := newFakePublisher()
	ext := &plainExtractor{text: "hello"}

	p := New(ext, store, status, pub)
	if err := p.Process(context.Background(), uploadEvt()); err != nil {
		t.Fatalf("Process returned error, want nil despite status update failure: %v", err)
	}
	if pub.chunkMsg == nil {
		t.Error("parsed text should still be published when status update fails")
	}
}

// A failure broadcasting an ingestion event is non-fatal: emit warns and
// Process still completes the parse-and-publish flow.
func TestProcess_EventEmitErrorIsNonFatal(t *testing.T) {
	store := &fakeStore{data: []byte("%PDF")}
	status := newFakeStatus()
	pub := newFakePublisher()
	pub.eventsErr = errors.New("events exchange down")
	ext := &plainExtractor{text: "hello"}

	p := New(ext, store, status, pub)
	if err := p.Process(context.Background(), uploadEvt()); err != nil {
		t.Fatalf("Process returned error, want nil despite event emit failure: %v", err)
	}
	if pub.chunkMsg == nil {
		t.Error("parsed text should still be published when event emit fails")
	}
	if pub.events == 0 {
		t.Error("expected at least one events-exchange publish attempt")
	}
}

// pageMetadata returns nil (so the keys are omitted) when there are no page
// offsets, and omits section_offsets when there are no sections.
func TestPageMetadata(t *testing.T) {
	if md := pageMetadata(domain.ExtractedDocument{Text: "x"}); md != nil {
		t.Errorf("pageMetadata with no offsets = %v, want nil", md)
	}
	md := pageMetadata(domain.ExtractedDocument{Text: "x", PageOffsets: []int{0}})
	if md == nil {
		t.Fatal("pageMetadata with offsets returned nil")
	}
	if _, ok := md["section_offsets"]; ok {
		t.Error("section_offsets must be omitted when there are no sections")
	}
	if md["page_count"] != "1" {
		t.Errorf("page_count = %q, want 1", md["page_count"])
	}
}
