package application

import (
	"context"
	"errors"
	"testing"

	"github.com/example/main-service/internal/platform/storage"
)

func newUploads(mp *fakeMultipart, store *fakeObjectStore, sess *fakeSessions, ing *IngestionService) *UploadSessionService {
	return NewUploadSessionService(mp, store, sess, ing, 0, 0)
}

func ingestionForUploads() (*IngestionService, *fakePublisher) {
	pub := &fakePublisher{}
	return newIngestion(newObjectStore(), newDocs(), pub, &fakeChunks{}), pub
}

// Begin opens a multipart session, persists it and computes a sane part layout.
func TestUploads_Begin_Success(t *testing.T) {
	mp, sess := &fakeMultipart{}, newSessions()
	ing, _ := ingestionForUploads()
	svc := newUploads(mp, newObjectStore(), sess, ing)

	s, err := svc.Begin(context.Background(), "alice", "big.bin", "application/octet-stream", 100<<20, false)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if s.PartCount < 1 || s.PartSize < minPartSize {
		t.Fatalf("bad layout: %+v", s)
	}
	if len(sess.data) != 1 {
		t.Fatalf("session must be persisted, got %d", len(sess.data))
	}
}

// A very large size forces a larger-than-minimum part size to stay under maxParts.
func TestUploads_Begin_LargePartSize(t *testing.T) {
	svc := newUploads(&fakeMultipart{}, newObjectStore(), newSessions(), mustIngestion())
	// Above maxParts*minPartSize (~140 GiB) the part size must grow, yet stay under the 200 GiB cap.
	huge := int64(160) << 30
	s, err := svc.Begin(context.Background(), "alice", "huge.bin", "", huge, false)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if s.PartSize <= minPartSize {
		t.Fatalf("huge upload should grow the part size, got %d", s.PartSize)
	}
	if s.PartCount > maxParts {
		t.Fatalf("part count %d must stay under %d", s.PartCount, maxParts)
	}
}

// Begin rejects non-positive and over-limit sizes.
func TestUploads_Begin_BadSize(t *testing.T) {
	svc := newUploads(&fakeMultipart{}, newObjectStore(), newSessions(), mustIngestion())
	if _, err := svc.Begin(context.Background(), "a", "f", "", 0, false); err == nil {
		t.Fatal("zero size must error")
	}
	if _, err := svc.Begin(context.Background(), "a", "f", "", (200<<30)+1, false); err == nil {
		t.Fatal("over-limit size must error")
	}
}

// A persist failure aborts the just-opened S3 upload.
func TestUploads_Begin_PersistFailureAborts(t *testing.T) {
	mp := &fakeMultipart{}
	sess := newSessions()
	sess.setErr = errors.New("valkey down")
	svc := newUploads(mp, newObjectStore(), sess, mustIngestion())
	if _, err := svc.Begin(context.Background(), "a", "f", "", 1<<20, false); err == nil {
		t.Fatal("persist failure must surface")
	}
	if mp.abortCalls != 1 {
		t.Fatalf("persist failure must abort the S3 upload, got %d aborts", mp.abortCalls)
	}
}

// CreateMultipart failure surfaces.
func TestUploads_Begin_CreateError(t *testing.T) {
	mp := &fakeMultipart{createErr: errors.New("s3 down")}
	svc := newUploads(mp, newObjectStore(), newSessions(), mustIngestion())
	if _, err := svc.Begin(context.Background(), "a", "f", "", 1<<20, false); err == nil {
		t.Fatal("create multipart error must surface")
	}
}

// PartURLs issues links for an in-range window and clamps the batch.
func TestUploads_PartURLs(t *testing.T) {
	mp, sess := &fakeMultipart{}, newSessions()
	svc := newUploads(mp, newObjectStore(), sess, mustIngestion())
	s, err := svc.Begin(context.Background(), "alice", "f.bin", "", 200<<20, false)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	urls, err := svc.PartURLs(context.Background(), "alice", s.ID, 1, 2)
	if err != nil {
		t.Fatalf("PartURLs: %v", err)
	}
	if len(urls) == 0 || urls[0].PartNumber != 1 {
		t.Fatalf("unexpected urls: %+v", urls)
	}
	// Out-of-range start errors.
	if _, err := svc.PartURLs(context.Background(), "alice", s.ID, s.PartCount+5, 1); err == nil {
		t.Fatal("out-of-range part must error")
	}
	// Unknown session is not found.
	if _, err := svc.PartURLs(context.Background(), "alice", "nope", 1, 1); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown session must be not-found, got %v", err)
	}
	// A presign failure surfaces.
	mp.presignErr = errors.New("presign down")
	if _, err := svc.PartURLs(context.Background(), "alice", s.ID, 1, 1); err == nil {
		t.Fatal("presign failure must surface")
	}
}

// Complete finalizes the object and hands it to the pipeline, then drops the session.
func TestUploads_Complete_Success(t *testing.T) {
	mp, sess := &fakeMultipart{}, newSessions()
	ing, pub := ingestionForUploads()
	svc := newUploads(mp, newObjectStore(), sess, ing)
	s, err := svc.Begin(context.Background(), "alice", "f.bin", "application/octet-stream", 20<<20, false)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	parts := make([]storage.MultipartPart, s.PartCount)
	for i := range parts {
		parts[i] = storage.MultipartPart{PartNumber: int32(i + 1), ETag: "etag"}
	}
	doc, _, err := svc.Complete(context.Background(), "alice", s.ID, parts)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if doc == nil {
		t.Fatal("Complete must return a document")
	}
	if pub.count() != 1 {
		t.Fatalf("Complete should dispatch one pipeline event, got %d", pub.count())
	}
	if sess.delCnt == 0 {
		t.Fatal("Complete must drop the session")
	}
}

// Complete rejects a part-count mismatch and a completion failure.
func TestUploads_Complete_Errors(t *testing.T) {
	mp, sess := &fakeMultipart{}, newSessions()
	svc := newUploads(mp, newObjectStore(), sess, mustIngestion())
	s, _ := svc.Begin(context.Background(), "alice", "f.bin", "", 20<<20, false)
	if _, _, err := svc.Complete(context.Background(), "alice", s.ID, nil); err == nil {
		t.Fatal("part-count mismatch must error")
	}
	parts := make([]storage.MultipartPart, s.PartCount)
	mp.completeErr = errors.New("complete down")
	if _, _, err := svc.Complete(context.Background(), "alice", s.ID, parts); err == nil {
		t.Fatal("completion failure must surface")
	}
}

// Abort discards the session and the S3 upload, and is idempotent when missing.
func TestUploads_Abort(t *testing.T) {
	mp, sess := &fakeMultipart{}, newSessions()
	svc := newUploads(mp, newObjectStore(), sess, mustIngestion())
	s, _ := svc.Begin(context.Background(), "alice", "f.bin", "", 20<<20, false)
	if err := svc.Abort(context.Background(), "alice", s.ID); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if mp.abortCalls != 1 {
		t.Fatalf("abort must hit S3, got %d", mp.abortCalls)
	}
	// Unknown session ⇒ idempotent no-op (no error).
	if err := svc.Abort(context.Background(), "alice", "gone"); err != nil {
		t.Fatalf("missing session must be a no-op, got %v", err)
	}
}

// Complete hashes the object when within the limit so a later identical upload dedups.
func TestUploads_Complete_HashesForDedup(t *testing.T) {
	mp, sess := &fakeMultipart{}, newSessions()
	store := newObjectStore()
	docs, pub := newDocs(), &fakePublisher{}
	ing := newIngestion(store, docs, pub, &fakeChunks{})
	svc := NewUploadSessionService(mp, store, sess, ing, 1<<30, 0)
	s, err := svc.Begin(context.Background(), "alice", "f.bin", "application/octet-stream", 20<<20, false)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Seed the object the session will hash on completion.
	store.objects[s.ObjectKey] = []byte("payload")
	parts := make([]storage.MultipartPart, s.PartCount)
	if _, _, err := svc.Complete(context.Background(), "alice", s.ID, parts); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(docs.byHash) == 0 {
		t.Fatal("Complete within hash limit must record a content hash for dedup")
	}
}

func mustIngestion() *IngestionService {
	return newIngestion(newObjectStore(), newDocs(), &fakePublisher{}, &fakeChunks{})
}
