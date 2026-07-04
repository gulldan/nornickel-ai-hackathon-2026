// Package archivescan adapts the archive-scan HTTP service (Rust + libarchive)
// to domain.Extractor. The archive object is streamed from S3 into a multipart
// upload; archive-scan unpacks entries straight into the same S3 bucket under
// extracted/<extraction_id>/ and answers with per-entry metadata (sanitized
// path, detected MIME, stored S3 URI) — no extracted bytes travel through this
// worker.
package archivescan

import (
	"context"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/archive-worker/internal/domain"
	"github.com/example/archive-worker/internal/platform/jsonx"
)

// ObjectStore opens stored objects for streaming (satisfied by platform/storage).
type ObjectStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Limits caps archive-scan's extraction to defend against resource-exhaustion
// archives (oversize entries, total-size blowups, zip bombs, runaway CPU). The
// byte/ratio caps are enforced by archive-scan as bytes stream; ExtractTimeout
// additionally bounds the whole extraction on this side via a context deadline.
// A zero field disables that particular guard.
type Limits struct {
	MaxFileBytes   int64
	MaxTotalBytes  int64
	MaxRatio       float64
	ExtractTimeout time.Duration
}

// Client calls archive-scan over HTTP.
type Client struct {
	base   string
	store  ObjectStore
	httpc  *http.Client
	limits Limits
}

// New builds a Client. baseURL is like http://archive-scan:3000; timeout
// bounds one extraction round-trip (large archives take tens of minutes to
// unpack — the value comes from ARCHIVE_HTTP_TIMEOUT). limits carries the
// resource-exhaustion guards forwarded to archive-scan.
func New(baseURL string, store ObjectStore, timeout time.Duration, limits Limits) *Client {
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	return &Client{
		base:   strings.TrimRight(baseURL, "/"),
		store:  store,
		httpc:  &http.Client{Timeout: timeout},
		limits: limits,
	}
}

// formTrue is the multipart boolean literal archive-scan expects.
const formTrue = "true"

// wire mirrors the relevant slice of archive-scan's ExtractArchiveResult.
type wire struct {
	TotalFiles           int64 `json:"total_files"`
	StoredFiles          int64 `json:"stored_files"`
	StoredBytes          int64 `json:"stored_bytes"`
	SkippedOversizeFiles int64 `json:"skipped_oversize_files"`
	Entries              []struct {
		SanitizedPath string `json:"sanitized_path"`
		Row           struct {
			EntryKind       string `json:"entry_kind"`
			Mime            string `json:"mime"`
			IsNestedArchive bool   `json:"is_nested_archive"`
			FullB3          string `json:"full_b3"`
		} `json:"row"`
		StoredObject *struct {
			URI       string `json:"uri"`
			SizeBytes int64  `json:"size_bytes"`
		} `json:"stored_object"`
	} `json:"entries"`
}

// Extract streams the archive into archive-scan and maps the response.
func (c *Client) Extract(
	ctx context.Context, objectKey, filename, extractionID string,
) (*domain.Extraction, error) {
	// Bound the whole extraction with a deadline so a CPU/IO-bomb archive cannot
	// pin the worker even if archive-scan's own guard fails to fire. archive-scan
	// receives the same budget via the extract_timeout_secs form field.
	if c.limits.ExtractTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.limits.ExtractTimeout)
		defer cancel()
	}

	obj, err := c.store.Get(ctx, objectKey)
	if err != nil {
		return nil, fmt.Errorf("open archive object: %w", err)
	}
	defer func() { _ = obj.Close() }()

	// Stream the multipart body through a pipe so the archive is never buffered.
	pr, pw := io.Pipe()
	form := multipart.NewWriter(pw)
	go func() { _ = pw.CloseWithError(c.writeArchiveForm(form, obj, filename, extractionID)) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/extract/upload", pr)
	if err != nil {
		return nil, fmt.Errorf("new extract request: %w", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call archive-scan: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("archive-scan: status %d: %s", resp.StatusCode, string(raw))
	}

	var w wire
	if derr := jsonx.NewDecoder(resp.Body).Decode(&w); derr != nil {
		return nil, fmt.Errorf("decode archive-scan response: %w", derr)
	}
	return mapExtraction(&w)
}

// writeArchiveForm streams the archive object and metadata fields into the
// multipart form, returning the first error so the pipe closes with it. The
// resource-exhaustion guards are sent as form fields (0 disables a guard).
func (c *Client) writeArchiveForm(
	form *multipart.Writer, obj io.Reader, filename, extractionID string,
) error {
	part, err := form.CreateFormFile("archive", filename)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, obj); err != nil {
		return fmt.Errorf("copy archive into form: %w", err)
	}
	if err := writeFields(form, map[string]string{
		"extraction_id":        extractionID,
		"include_entries":      formTrue,
		"fast_only":            formTrue,
		"full_hash":            formTrue,
		"max_file_bytes":       strconv.FormatInt(maxNonNeg(c.limits.MaxFileBytes), 10),
		"max_total_bytes":      strconv.FormatInt(maxNonNeg(c.limits.MaxTotalBytes), 10),
		"max_ratio":            strconv.FormatFloat(maxNonNegFloat(c.limits.MaxRatio), 'f', -1, 64),
		"extract_timeout_secs": strconv.FormatInt(int64(c.limits.ExtractTimeout/time.Second), 10),
	}); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return fmt.Errorf("close multipart form: %w", err)
	}
	return nil
}

// maxNonNeg clamps negatives to 0 so a misconfigured cap reads as "disabled"
// rather than a confusing negative on the wire.
func maxNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// maxNonNegFloat clamps non-finite or negative ratios to 0 (disabled).
func maxNonNegFloat(v float64) float64 {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// mapExtraction maps the archive-scan response to a domain.Extraction. Entries
// with no stored object or an unparseable URI are skipped; if every stored entry
// has an unparseable URI it errors rather than silently dropping the archive.
func mapExtraction(w *wire) (*domain.Extraction, error) {
	out := &domain.Extraction{
		Entries:         make([]domain.Entry, 0, len(w.Entries)),
		TotalFiles:      w.TotalFiles,
		StoredBytes:     w.StoredBytes,
		SkippedOversize: int(w.SkippedOversizeFiles),
		Skipped:         0,
	}
	stored := 0
	for _, e := range w.Entries {
		if e.StoredObject == nil {
			continue
		}
		stored++
		key, err := objectKeyFromURI(e.StoredObject.URI)
		if err != nil {
			out.Skipped++
			continue
		}
		out.Entries = append(out.Entries, domain.Entry{
			Path:          e.SanitizedPath,
			ObjectKey:     key,
			MIMEType:      e.Row.Mime,
			Size:          e.StoredObject.SizeBytes,
			NestedArchive: e.Row.IsNestedArchive,
			Hash:          e.Row.FullB3,
		})
	}
	if stored > 0 && out.Skipped == stored {
		return nil, fmt.Errorf("archive-scan: all %d stored entries have an unparseable stored object uri", stored)
	}
	return out, nil
}

// writeFields adds plain form fields to the multipart writer.
func writeFields(form *multipart.Writer, fields map[string]string) error {
	for k, v := range fields {
		if err := form.WriteField(k, v); err != nil {
			return fmt.Errorf("write field %s: %w", k, err)
		}
	}
	return nil
}

// objectKeyFromURI turns "s3://bucket/key/with/slashes" into the bare key.
func objectKeyFromURI(uri string) (string, error) {
	rest, ok := strings.CutPrefix(uri, "s3://")
	if !ok {
		return "", fmt.Errorf("unexpected stored object uri %q (want s3://...)", uri)
	}
	_, key, ok := strings.Cut(rest, "/")
	if !ok || key == "" {
		return "", fmt.Errorf("stored object uri %q has no key", uri)
	}
	return key, nil
}
