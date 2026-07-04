// Upload sessions: the browser uploads large files in parts directly to S3 via
// presigned URLs; main-service only orchestrates (create session → issue URLs →
// complete → hand the object to the pipeline). Session state lives in Valkey, so
// main-service stays stateless and horizontally scalable.

package application

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"lukechampine.com/blake3"

	"github.com/example/main-service/internal/domain"
	"github.com/example/main-service/internal/platform/logger"
	"github.com/example/main-service/internal/platform/storage"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"

	"github.com/google/uuid"
)

// ErrSessionNotFound is returned for unknown/expired/foreign upload sessions.
var ErrSessionNotFound = errors.New("upload session not found")

const (
	// sessionTTL bounds how long an unfinished upload may live.
	sessionTTL = 48 * time.Hour
	// partURLTTL bounds one presigned part link.
	partURLTTL = 2 * time.Hour
	// minPartSize is S3's floor for non-final parts.
	minPartSize = 16 << 20
	// maxParts keeps the count comfortably under S3's 10k limit.
	maxParts = 9000
	// maxPartBatch bounds one PartURLs call.
	maxPartBatch = 500
)

// UploadSession is the persisted state of one in-flight multipart upload.
type UploadSession struct {
	ID        string `json:"id"`
	OwnerID   string `json:"owner_id"`
	Filename  string `json:"filename"`
	MIMEType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	ObjectKey string `json:"object_key"`
	S3Upload  string `json:"s3_upload_id"`
	PartSize  int64  `json:"part_size"`
	PartCount int32  `json:"part_count"`
	Reuse     bool   `json:"reuse,omitempty"`
}

// PartURL is one presigned link the browser PUTs a part to.
type PartURL struct {
	PartNumber int32
	URL        string
}

// UploadSessionService orchestrates browser-direct multipart uploads.
type UploadSessionService struct {
	multipart domain.MultipartStore
	objects   domain.ObjectStore
	sessions  domain.SessionStore
	ingestion *IngestionService
	// hashMaxBytes bounds server-side dedup hashing on completion: larger files
	// skip the hash (archive-entry dedup still works via entry hashes).
	hashMaxBytes int64
	maxBytes     int64
}

// NewUploadSessionService wires the dependencies. hashMaxBytes/maxBytes <= 0
// fall back to 1 GiB and 200 GiB.
func NewUploadSessionService(
	multipart domain.MultipartStore,
	objects domain.ObjectStore,
	sessions domain.SessionStore,
	ingestion *IngestionService,
	hashMaxBytes, maxBytes int64,
) *UploadSessionService {
	if hashMaxBytes <= 0 {
		hashMaxBytes = 1 << 30
	}
	if maxBytes <= 0 {
		maxBytes = 200 << 30
	}
	return &UploadSessionService{
		multipart:    multipart,
		objects:      objects,
		sessions:     sessions,
		ingestion:    ingestion,
		hashMaxBytes: hashMaxBytes,
		maxBytes:     maxBytes,
	}
}

func sessionKey(ownerID, id string) string { return "upload:" + ownerID + ":" + id }

// Begin opens a session: starts a multipart upload in S3 and computes the part
// layout. Part size is chosen so the part count stays ≤ maxParts.
func (s *UploadSessionService) Begin(
	ctx context.Context, ownerID, filename, mimeType string, size int64, reuse bool,
) (*UploadSession, error) {
	if size <= 0 {
		return nil, errors.New("size must be positive")
	}
	if size > s.maxBytes {
		return nil, fmt.Errorf("файл больше предела %d ГиБ", s.maxBytes>>30)
	}

	partSize := int64(minPartSize)
	if need := (size + maxParts - 1) / maxParts; need > partSize {
		// Round up to 8 MiB to keep the part size "round".
		const step = 8 << 20
		partSize = (need + step - 1) / step * step
	}
	partCount := int32((size + partSize - 1) / partSize)

	id := uuid.NewString()
	key := fmt.Sprintf("uploads/%s/%s/%s", ownerID, id, filename)
	s3id, err := s.multipart.CreateMultipart(ctx, key, mimeType)
	if err != nil {
		return nil, fmt.Errorf("begin multipart: %w", err)
	}

	sess := &UploadSession{
		ID: id, OwnerID: ownerID, Filename: filename, MIMEType: mimeType,
		Size: size, ObjectKey: key, S3Upload: s3id,
		PartSize: partSize, PartCount: partCount, Reuse: reuse,
	}
	if serr := s.sessions.SetJSON(ctx, sessionKey(ownerID, id), sess, sessionTTL); serr != nil {
		_ = s.multipart.AbortMultipart(ctx, key, s3id)
		return nil, fmt.Errorf("persist session: %w", serr)
	}
	logger.From(ctx).Info().
		Str("upload_id", id).Int64("size", size).
		Int64("part_size", partSize).Int32("parts", partCount).
		Msg("upload session opened")
	return sess, nil
}

func (s *UploadSessionService) load(ctx context.Context, ownerID, id string) (*UploadSession, error) {
	var sess UploadSession
	found, err := s.sessions.GetJSON(ctx, sessionKey(ownerID, id), &sess)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if !found {
		return nil, ErrSessionNotFound
	}
	return &sess, nil
}

// PartURLs issues presigned links for parts [from, from+count).
func (s *UploadSessionService) PartURLs(
	ctx context.Context, ownerID, id string, from, count int32,
) ([]PartURL, error) {
	sess, err := s.load(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	if from < 1 || from > sess.PartCount {
		return nil, fmt.Errorf("part %d is out of range 1..%d", from, sess.PartCount)
	}
	if count < 1 {
		count = 1
	}
	if count > maxPartBatch {
		count = maxPartBatch
	}
	if from+count-1 > sess.PartCount {
		count = sess.PartCount - from + 1
	}
	urls := make([]PartURL, 0, count)
	for n := from; n < from+count; n++ {
		u, perr := s.multipart.PresignUploadPart(ctx, sess.ObjectKey, sess.S3Upload, n, partURLTTL)
		if perr != nil {
			return nil, fmt.Errorf("presign part %d: %w", n, perr)
		}
		urls = append(urls, PartURL{PartNumber: n, URL: u})
	}
	return urls, nil
}

// Complete finalizes the object in S3, hashes it for dedup when its size is
// reasonable, and hands the document to the normal pipeline.
func (s *UploadSessionService) Complete(
	ctx context.Context, ownerID, id string, parts []storage.MultipartPart,
) (*commonv1.Document, bool, error) {
	sess, err := s.load(ctx, ownerID, id)
	if err != nil {
		return nil, false, err
	}
	if int32(len(parts)) != sess.PartCount {
		return nil, false, fmt.Errorf("ожидалось %d частей, получено %d", sess.PartCount, len(parts))
	}
	if cerr := s.multipart.CompleteMultipart(ctx, sess.ObjectKey, sess.S3Upload, parts); cerr != nil {
		return nil, false, fmt.Errorf("complete multipart: %w", cerr)
	}

	contentHash := ""
	if sess.Size <= s.hashMaxBytes {
		if h, herr := s.hashObject(ctx, sess.ObjectKey); herr != nil {
			logger.From(ctx).Warn().Err(herr).Msg("hash uploaded object failed; skipping dedup")
		} else {
			contentHash = h
		}
	}

	doc, existed, err := s.ingestion.FinalizeObject(ctx, FinalizeRequest{
		OwnerID:       ownerID,
		Filename:      sess.Filename,
		MIMEType:      sess.MIMEType,
		ObjectKey:     sess.ObjectKey,
		Size:          sess.Size,
		ContentHash:   contentHash,
		InlineText:    "",
		ReuseExisting: sess.Reuse,
	})
	if err != nil {
		return nil, false, err
	}
	_ = s.sessions.Del(ctx, sessionKey(ownerID, id))
	return doc, existed, nil
}

// Abort discards the session and the unfinished S3 upload.
func (s *UploadSessionService) Abort(ctx context.Context, ownerID, id string) error {
	sess, err := s.load(ctx, ownerID, id)
	if errors.Is(err, ErrSessionNotFound) {
		return nil // идемпотентно: отменять нечего
	}
	if err != nil {
		return err
	}
	if aerr := s.multipart.AbortMultipart(ctx, sess.ObjectKey, sess.S3Upload); aerr != nil {
		return aerr
	}
	_ = s.sessions.Del(ctx, sessionKey(ownerID, id))
	return nil
}

// hashObject streams the stored object through BLAKE3.
func (s *UploadSessionService) hashObject(ctx context.Context, key string) (string, error) {
	rc, err := s.objects.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("open object: %w", err)
	}
	defer func() { _ = rc.Close() }()
	hasher := blake3.New(32, nil)
	if _, err := io.Copy(hasher, rc); err != nil {
		return "", fmt.Errorf("hash object: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
