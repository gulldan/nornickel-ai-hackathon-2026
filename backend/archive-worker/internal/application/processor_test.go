package application

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/example/archive-worker/internal/domain"
	"github.com/example/archive-worker/internal/platform/contracts"

	commonv1 "github.com/example/archive-worker/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/archive-worker/internal/platform/genproto/db/v1"
)

// fakeExtractor returns a canned extraction (or error).
type fakeExtractor struct {
	res *domain.Extraction
	err error
}

func (f *fakeExtractor) Extract(_ context.Context, _, _, _ string) (*domain.Extraction, error) {
	return f.res, f.err
}

// fakeCatalog records calls and can be told to fail CreateDocument for a path.
// Entries register concurrently, so the fake is mutex-guarded.
type fakeCatalog struct {
	mu         sync.Mutex
	created    int
	failFor    map[string]bool // filename -> CreateDocument returns error
	hashes     map[string]bool // hashes that already exist (duplicates)
	statusByID map[string]string
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{
		failFor:    map[string]bool{},
		hashes:     map[string]bool{},
		statusByID: map[string]string{},
	}
}

func (f *fakeCatalog) CreateDocument(_ context.Context, req *dbv1.CreateDocumentRequest) (*commonv1.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFor[req.GetFilename()] {
		return nil, errors.New("db down")
	}
	f.created++
	return &commonv1.Document{
		Id:        "doc-" + req.GetFilename(),
		OwnerId:   req.GetOwnerId(),
		Filename:  req.GetFilename(),
		MimeType:  req.GetMimeType(),
		Size:      req.GetSize(),
		ObjectKey: req.GetObjectKey(),
	}, nil
}

func (f *fakeCatalog) FindDocumentByHash(_ context.Context, _, hash string) (*commonv1.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hashes[hash] {
		return &commonv1.Document{Id: "existing"}, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeCatalog) UpdateDocumentStatus(_ context.Context, id, status, _ string, _ *int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusByID[id] = status
	return nil
}

// fakePublisher can fail publishing to a specific child routing key (simulating a
// missing publisher-confirm) while letting event broadcasts succeed.
type fakePublisher struct {
	mu            sync.Mutex
	published     int
	failForFiles  map[string]bool // child DocumentUploaded.Filename -> publish error
	publishErrAll bool
}

func (f *fakePublisher) PublishProto(_ context.Context, exchange, _ string, msg proto.Message) error {
	if exchange == contracts.ExchangeEvents {
		return nil // best-effort events always succeed in the fake
	}
	if f.publishErrAll {
		return errors.New("broker did not confirm")
	}
	if du, ok := msg.(*commonv1.DocumentUploaded); ok && f.failForFiles[du.GetFilename()] {
		return errors.New("broker did not confirm")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published++
	return nil
}

func archiveEvt() *commonv1.DocumentUploaded {
	return &commonv1.DocumentUploaded{
		DocumentId: "archive-1",
		OwnerId:    "owner-1",
		ObjectKey:  "uploads/archive.zip",
		Filename:   "archive.zip",
		MimeType:   "application/zip",
	}
}

func entries(paths ...string) []domain.Entry {
	out := make([]domain.Entry, 0, len(paths))
	for _, p := range paths {
		out = append(out, domain.Entry{Path: p, ObjectKey: "extracted/" + p, MIMEType: "text/plain", Size: 10})
	}
	return out
}

// One failing entry must NOT dead-letter the whole archive: the survivors are
// registered and Process returns nil with the archive marked indexed.
func TestProcess_OneFailingEntryDoesNotFailArchive(t *testing.T) {
	cat := newFakeCatalog()
	cat.failFor["b.txt"] = true // CreateDocument blows up for one entry only
	pub := &fakePublisher{failForFiles: map[string]bool{}}
	ext := &fakeExtractor{res: &domain.Extraction{Entries: entries("a.txt", "b.txt", "c.txt")}}

	p := New(ext, cat, pub, 0)
	if err := p.Process(context.Background(), archiveEvt()); err != nil {
		t.Fatalf("Process returned error, want nil (archive must not be dead-lettered): %v", err)
	}
	if cat.created != 2 {
		t.Errorf("created = %d, want 2 (a.txt, c.txt)", cat.created)
	}
	if pub.published != 2 {
		t.Errorf("published children = %d, want 2", pub.published)
	}
	if got := cat.statusByID["archive-1"]; got != contracts.StatusIndexed {
		t.Errorf("archive status = %q, want %q", got, contracts.StatusIndexed)
	}
}

// A publisher-confirm failure on the hot path is a per-entry failure, not an
// archive failure.
func TestProcess_PublishConfirmFailureIsPerEntry(t *testing.T) {
	cat := newFakeCatalog()
	pub := &fakePublisher{failForFiles: map[string]bool{"b.txt": true}}
	ext := &fakeExtractor{res: &domain.Extraction{Entries: entries("a.txt", "b.txt")}}

	p := New(ext, cat, pub, 0)
	if err := p.Process(context.Background(), archiveEvt()); err != nil {
		t.Fatalf("Process returned error, want nil: %v", err)
	}
	// Both docs were created; only one publish was confirmed.
	if cat.created != 2 {
		t.Errorf("created = %d, want 2", cat.created)
	}
	if pub.published != 1 {
		t.Errorf("published = %d, want 1 (b.txt publish failed)", pub.published)
	}
	if got := cat.statusByID["archive-1"]; got != contracts.StatusIndexed {
		t.Errorf("archive status = %q, want %q", got, contracts.StatusIndexed)
	}
}

// Failure to extract the archive itself MUST dead-letter (return error) and mark
// the archive failed.
func TestProcess_ExtractFailureDeadLetters(t *testing.T) {
	cat := newFakeCatalog()
	pub := &fakePublisher{}
	ext := &fakeExtractor{err: errors.New("archive-scan 500")}

	p := New(ext, cat, pub, 0)
	if err := p.Process(context.Background(), archiveEvt()); err == nil {
		t.Fatal("Process returned nil, want error (archive extraction failed -> DLX)")
	}
	if got := cat.statusByID["archive-1"]; got != contracts.StatusFailed {
		t.Errorf("archive status = %q, want %q", got, contracts.StatusFailed)
	}
}

// Skipped (unparseable) entries surfaced by the extractor are folded into the
// archive's skipped count and do not fail the archive.
func TestProcess_SkippedEntriesCountedNotFailed(t *testing.T) {
	cat := newFakeCatalog()
	pub := &fakePublisher{}
	ext := &fakeExtractor{res: &domain.Extraction{Entries: entries("a.txt"), Skipped: 3}}

	p := New(ext, cat, pub, 0)
	if err := p.Process(context.Background(), archiveEvt()); err != nil {
		t.Fatalf("Process returned error, want nil: %v", err)
	}
	if cat.created != 1 {
		t.Errorf("created = %d, want 1", cat.created)
	}
	if got := cat.statusByID["archive-1"]; got != contracts.StatusIndexed {
		t.Errorf("archive status = %q, want %q", got, contracts.StatusIndexed)
	}
}

// Too many entries fails the archive before any registration.
func TestProcess_TooManyEntriesFails(t *testing.T) {
	cat := newFakeCatalog()
	pub := &fakePublisher{}
	ext := &fakeExtractor{res: &domain.Extraction{Entries: entries("a.txt", "b.txt", "c.txt")}}

	p := New(ext, cat, pub, 1) // cap of 1, three entries
	if err := p.Process(context.Background(), archiveEvt()); err == nil {
		t.Fatal("Process returned nil, want error (entry count over limit)")
	}
	if cat.created != 0 {
		t.Errorf("created = %d, want 0", cat.created)
	}
}
