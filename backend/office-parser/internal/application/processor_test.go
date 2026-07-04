package application_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/example/office-parser/internal/application"
	"github.com/example/office-parser/internal/domain"
	"github.com/example/office-parser/internal/platform/contracts"

	commonv1 "github.com/example/office-parser/internal/platform/genproto/common/v1"
)

// fakeExtractor is a scripted domain.TextExtractor.
type fakeExtractor struct {
	plain   bool
	result  domain.ExtractionResult
	err     error
	gotData []byte
}

func (f *fakeExtractor) Extract(_ context.Context, data []byte, _, _ string) (domain.ExtractionResult, error) {
	f.gotData = data
	return f.result, f.err
}

func (f *fakeExtractor) IsPlainText(_, _ string) bool { return f.plain }

// fakeStore is a scripted application.ObjectStore that records the claim-check
// Put it receives.
type fakeStore struct {
	bytes   []byte
	getErr  error
	putErr  error
	puts    []putCall
	putKey  string
	putBody string
}

type putCall struct {
	key         string
	body        string
	contentType string
}

func (f *fakeStore) GetBytes(_ context.Context, _ string) ([]byte, error) {
	return f.bytes, f.getErr
}

func (f *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	f.putKey = key
	b, _ := io.ReadAll(r)
	f.putBody = string(b)
	f.puts = append(f.puts, putCall{key: key, body: string(b), contentType: contentType})
	return f.putErr
}

// fakeStatus records the status transitions the processor requests.
type fakeStatus struct {
	mu       sync.Mutex
	statuses []string
	err      error
}

func (f *fakeStatus) UpdateDocumentStatus(_ context.Context, _, status, _ string, _ *int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, status)
	return f.err
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

// publication captures one PublishProto call.
type publication struct {
	exchange   string
	routingKey string
	msg        proto.Message
}

// fakePublisher records every published message and can fail messages sent to a
// chosen exchange.
type fakePublisher struct {
	mu           sync.Mutex
	calls        []publication
	failExchange string
	failErr      error
}

func (f *fakePublisher) PublishProto(_ context.Context, exchange, routingKey string, msg proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publication{exchange: exchange, routingKey: routingKey, msg: msg})
	if f.failExchange != "" && exchange == f.failExchange {
		return f.failErr
	}
	return nil
}

// parsed returns the first DocumentParsed published to the chunk route, or nil.
func (f *fakePublisher) parsed() *commonv1.DocumentParsed {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.exchange == contracts.ExchangeIngestion && c.routingKey == contracts.RouteChunk {
			if p, ok := c.msg.(*commonv1.DocumentParsed); ok {
				return p
			}
		}
	}
	return nil
}

// failedEvents counts IngestionEvent broadcasts carrying the failed status.
func (f *fakePublisher) failedEvents() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		ev, ok := c.msg.(*commonv1.IngestionEvent)
		if ok && c.exchange == contracts.ExchangeEvents && ev.GetStatus() == contracts.StatusFailed {
			n++
		}
	}
	return n
}

// uploaded builds a DocumentUploaded event for the table cases.
func uploaded() *commonv1.DocumentUploaded {
	return &commonv1.DocumentUploaded{
		DocumentId: "doc-1", OwnerId: "owner-1", ObjectKey: "raw/doc-1",
		Filename: "report.docx", MimeType: "", Size: 0, TraceId: "trace-1", ArchiveDepth: 0,
	}
}

// TestProcessExtractInline extracts text and publishes it inline, advancing the
// document to the parsed state.
func TestProcessExtractInline(t *testing.T) {
	ext := &fakeExtractor{result: domain.ExtractionResult{Text: "hello world"}}
	store := &fakeStore{bytes: []byte("rawbytes")}
	status := &fakeStatus{}
	pub := &fakePublisher{}
	p := application.New(ext, store, status, pub)

	if err := p.Process(context.Background(), uploaded()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	parsed := pub.parsed()
	if parsed == nil {
		t.Fatalf("no DocumentParsed published")
	}
	if parsed.GetText() != "hello world" || parsed.GetTextObjectKey() != "" {
		t.Fatalf("parsed text=%q key=%q, want inline %q", parsed.GetText(), parsed.GetTextObjectKey(), "hello world")
	}
	if parsed.GetSource() != "office" || parsed.GetDocumentId() != "doc-1" {
		t.Fatalf("parsed source=%q id=%q, want office/doc-1", parsed.GetSource(), parsed.GetDocumentId())
	}
	if !status.seen(contracts.StatusParsed) {
		t.Fatalf("status %q never set; saw %v", contracts.StatusParsed, status.statuses)
	}
	if string(ext.gotData) != "rawbytes" {
		t.Fatalf("extractor got %q, want downloaded bytes", ext.gotData)
	}
}

// TestProcessStoresParserSidecars keeps workbook parser artifacts addressable
// through metadata while the searchable text still goes downstream inline.
func TestProcessStoresParserSidecars(t *testing.T) {
	ext := &fakeExtractor{result: domain.ExtractionResult{
		Text:     "anchored markdown",
		Metadata: map[string]string{"workbook_mode": "anchored_markdown"},
		Sidecars: []domain.SidecarArtifact{{
			Name:        "workbook.sidecar.json",
			ContentType: "application/json",
			Text:        `{"cells":[]}`,
		}},
	}}
	store := &fakeStore{bytes: []byte("raw")}
	pub := &fakePublisher{}
	p := application.New(ext, store, &fakeStatus{}, pub)

	if err := p.Process(context.Background(), uploaded()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(store.puts) != 1 {
		t.Fatalf("Put calls = %d, want 1", len(store.puts))
	}
	got := store.puts[0]
	if got.key != "parsed/doc-1.workbook.sidecar.json" ||
		got.body != `{"cells":[]}` || got.contentType != "application/json" {
		t.Fatalf("sidecar Put = %+v, want workbook sidecar JSON", got)
	}
	parsed := pub.parsed()
	if parsed == nil {
		t.Fatalf("no DocumentParsed published")
	}
	if parsed.GetMetadata()["workbook_mode"] != "anchored_markdown" {
		t.Fatalf("workbook_mode metadata = %q, want anchored_markdown", parsed.GetMetadata()["workbook_mode"])
	}
	if parsed.GetMetadata()["workbook_sidecar_object_key"] != "parsed/doc-1.workbook.sidecar.json" {
		t.Fatalf("sidecar object metadata = %q", parsed.GetMetadata()["workbook_sidecar_object_key"])
	}
	if parsed.GetMetadata()["workbook_sidecar_content_type"] != "application/json" {
		t.Fatalf("sidecar content type metadata = %q", parsed.GetMetadata()["workbook_sidecar_content_type"])
	}
}

// TestProcessPlainTextByReference hands a plain-text source downstream by object
// key without downloading or extracting it.
func TestProcessPlainTextByReference(t *testing.T) {
	ext := &fakeExtractor{plain: true}
	store := &fakeStore{getErr: errors.New("must not download")}
	pub := &fakePublisher{}
	p := application.New(ext, store, &fakeStatus{}, pub)

	evt := uploaded()
	evt.Filename = "notes.txt"
	if err := p.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	parsed := pub.parsed()
	if parsed == nil {
		t.Fatalf("no DocumentParsed published")
	}
	if parsed.GetTextObjectKey() != evt.GetObjectKey() || parsed.GetText() != "" {
		t.Fatalf("parsed key=%q text=%q, want claim-check %q", parsed.GetTextObjectKey(), parsed.GetText(), evt.GetObjectKey())
	}
	if ext.gotData != nil {
		t.Fatalf("extractor was called for plain text")
	}
}

// TestProcessOffloadsLargeText stores oversized extracted text as a claim check
// and clears the inline text field.
func TestProcessOffloadsLargeText(t *testing.T) {
	big := strings.Repeat("a", (4<<20)+1)
	ext := &fakeExtractor{result: domain.ExtractionResult{Text: big}}
	store := &fakeStore{bytes: []byte("raw")}
	pub := &fakePublisher{}
	p := application.New(ext, store, &fakeStatus{}, pub)

	if err := p.Process(context.Background(), uploaded()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if store.putKey != "parsed/doc-1.txt" || store.putBody != big {
		t.Fatalf("Put key=%q bodyLen=%d, want parsed/doc-1.txt len=%d", store.putKey, len(store.putBody), len(big))
	}
	parsed := pub.parsed()
	if parsed == nil || parsed.GetText() != "" || parsed.GetTextObjectKey() != "parsed/doc-1.txt" {
		t.Fatalf("parsed text/key wrong after offload: %+v", parsed)
	}
}

// TestProcessErrors covers the failure branches: the first attempt only
// requeues (no failed status or event yet, so a transient blip never surfaces
// in the UI), while the redelivered (final) attempt marks the document failed
// and emits the failed event.
func TestProcessErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func() (*application.Processor, *fakeStatus, *fakePublisher)
	}{
		{
			name: "download fails",
			setup: func() (*application.Processor, *fakeStatus, *fakePublisher) {
				status := &fakeStatus{}
				pub := &fakePublisher{}
				p := application.New(&fakeExtractor{}, &fakeStore{getErr: errors.New("s3 down")}, status, pub)
				return p, status, pub
			},
		},
		{
			name: "extract fails",
			setup: func() (*application.Processor, *fakeStatus, *fakePublisher) {
				status := &fakeStatus{}
				pub := &fakePublisher{}
				ext := &fakeExtractor{err: errors.New("parse boom")}
				p := application.New(ext, &fakeStore{bytes: []byte("raw")}, status, pub)
				return p, status, pub
			},
		},
		{
			name: "offload put fails",
			setup: func() (*application.Processor, *fakeStatus, *fakePublisher) {
				status := &fakeStatus{}
				pub := &fakePublisher{}
				ext := &fakeExtractor{result: domain.ExtractionResult{Text: strings.Repeat("b", (4<<20)+1)}}
				store := &fakeStore{bytes: []byte("raw"), putErr: errors.New("disk full")}
				p := application.New(ext, store, status, pub)
				return p, status, pub
			},
		},
		{
			name: "sidecar put fails",
			setup: func() (*application.Processor, *fakeStatus, *fakePublisher) {
				status := &fakeStatus{}
				pub := &fakePublisher{}
				ext := &fakeExtractor{result: domain.ExtractionResult{
					Text: "ok",
					Sidecars: []domain.SidecarArtifact{{
						Name:        "workbook.sidecar.json",
						ContentType: "application/json",
						Text:        `{}`,
					}},
				}}
				store := &fakeStore{bytes: []byte("raw"), putErr: errors.New("s3 down")}
				p := application.New(ext, store, status, pub)
				return p, status, pub
			},
		},
		{
			name: "publish parsed fails",
			setup: func() (*application.Processor, *fakeStatus, *fakePublisher) {
				status := &fakeStatus{}
				pub := &fakePublisher{failExchange: contracts.ExchangeIngestion, failErr: errors.New("broker down")}
				ext := &fakeExtractor{result: domain.ExtractionResult{Text: "ok"}}
				p := application.New(ext, &fakeStore{bytes: []byte("raw")}, status, pub)
				return p, status, pub
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, status, pub := tc.setup()
			if err := p.Process(context.Background(), uploaded()); err == nil {
				t.Fatalf("Process expected error, got nil")
			}
			if status.seen(contracts.StatusFailed) {
				t.Fatalf("first attempt must not mark failed; saw %v", status.statuses)
			}
			if n := pub.failedEvents(); n != 0 {
				t.Fatalf("first attempt emitted %d failed events, want 0", n)
			}

			p, status, pub = tc.setup()
			redelivered := contracts.WithRedelivered(context.Background(), true)
			if err := p.Process(redelivered, uploaded()); err == nil {
				t.Fatalf("Process expected error, got nil")
			}
			if !status.seen(contracts.StatusFailed) {
				t.Fatalf("status %q never set; saw %v", contracts.StatusFailed, status.statuses)
			}
			if n := pub.failedEvents(); n != 1 {
				t.Fatalf("final attempt emitted %d failed events, want 1", n)
			}
		})
	}
}

// TestProcessToleratesBestEffortFailures keeps succeeding when status updates
// and event broadcasts fail, since both are best-effort.
func TestProcessToleratesBestEffortFailures(t *testing.T) {
	ext := &fakeExtractor{result: domain.ExtractionResult{Text: "ok"}}
	store := &fakeStore{bytes: []byte("raw")}
	status := &fakeStatus{err: errors.New("db down")}
	// Fail only the fanout events exchange (best-effort), not the chunk route.
	pub := &fakePublisher{failExchange: contracts.ExchangeEvents, failErr: errors.New("events down")}
	p := application.New(ext, store, status, pub)

	if err := p.Process(context.Background(), uploaded()); err != nil {
		t.Fatalf("Process should tolerate best-effort failures, got %v", err)
	}
	if pub.parsed() == nil {
		t.Fatalf("DocumentParsed not published despite best-effort failures")
	}
}
