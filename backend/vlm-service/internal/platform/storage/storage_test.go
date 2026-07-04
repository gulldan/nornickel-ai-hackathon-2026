package storage_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example/vlm-service/internal/platform/storage"
)

// fakeS3 emulates the handful of S3 operations the client performs. Routing is
// by method plus the presence of S3 query selectors (?cors, ?uploads, etc.).
type fakeS3 struct {
	// objects maps an object key to its stored body.
	objects map[string]string
	// missing, when set, makes GetObject answer 404 for that key.
	missing string
}

// handler routes a single S3 request to a minimal valid response.
func (f *fakeS3) handler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/b"), "/")
	switch {
	case r.Method == http.MethodPut && q.Has("cors"):
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPut && key == "":
		// CreateBucket on the path-style bucket root.
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodHead && key == "":
		// HeadBucket readiness probe.
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && q.Has("uploads"):
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>`+key+
			`</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<CompleteMultipartUploadResult><Bucket>b</Bucket><Key>`+key+
			`</Key><ETag>"final"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = string(body)
		w.Header().Set("ETag", `"etag-`+key+`"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet:
		if key == f.missing {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
				`<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
			return
		}
		body := f.objects[key]
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// newServerClient starts a fake S3 server and a client wired to it.
func newServerClient(t *testing.T) (*fakeS3, *storage.Client) {
	t.Helper()
	f := &fakeS3{objects: map[string]string{}, missing: ""}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := storage.New(context.Background(), storage.Config{
		Endpoint:       srv.URL,
		Region:         "us-east-1",
		AccessKey:      "x",
		SecretKey:      "y",
		Bucket:         "b",
		UsePathStyle:   true,
		PublicEndpoint: "",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return f, c
}

// TestConfigFromEnv reads the S3_* environment variables into a Config.
func TestConfigFromEnv(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("S3_REGION", "eu-west-1")
	t.Setenv("S3_ACCESS_KEY", "ak")
	t.Setenv("S3_SECRET_KEY", "sk")
	t.Setenv("S3_BUCKET", "files")
	t.Setenv("S3_USE_PATH_STYLE", "true")
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://cdn.example.com")
	cfg := storage.ConfigFromEnv()
	if cfg.Endpoint != "http://minio:9000" || cfg.Region != "eu-west-1" {
		t.Fatalf("endpoint/region = %q/%q", cfg.Endpoint, cfg.Region)
	}
	if cfg.AccessKey != "ak" || cfg.SecretKey != "sk" || cfg.Bucket != "files" {
		t.Fatalf("creds/bucket = %q/%q/%q", cfg.AccessKey, cfg.SecretKey, cfg.Bucket)
	}
	if !cfg.UsePathStyle || cfg.PublicEndpoint != "https://cdn.example.com" {
		t.Fatalf("pathStyle/public = %v/%q", cfg.UsePathStyle, cfg.PublicEndpoint)
	}
}

// TestNewAndPing constructs a client and verifies the readiness probe.
func TestNewAndPing(t *testing.T) {
	_, c := newServerClient(t)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// TestNewBucketError fails construction when the bucket cannot be created.
func TestNewBucketError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	_, err := storage.New(context.Background(), storage.Config{
		Endpoint: srv.URL, Region: "us-east-1", AccessKey: "x", SecretKey: "y",
		Bucket: "b", UsePathStyle: true, PublicEndpoint: "",
	})
	if err == nil {
		t.Fatal("expected an error when bucket creation fails")
	}
}

// TestNewCORSError fails construction when CreateBucket succeeds but CORS does not.
func TestNewCORSError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("cors") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	_, err := storage.New(context.Background(), storage.Config{
		Endpoint: srv.URL, Region: "us-east-1", AccessKey: "x", SecretKey: "y",
		Bucket: "b", UsePathStyle: true, PublicEndpoint: "",
	})
	if err == nil {
		t.Fatal("expected an error when CORS configuration fails")
	}
}

// TestNewBucketAlreadyOwned treats an existing-bucket response as success.
func TestNewBucketAlreadyOwned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodPut && q.Has("cors"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
				`<Error><Code>BucketAlreadyOwnedByYou</Code><Message>mine</Message></Error>`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	if _, err := storage.New(context.Background(), storage.Config{
		Endpoint: srv.URL, Region: "us-east-1", AccessKey: "x", SecretKey: "y",
		Bucket: "b", UsePathStyle: true, PublicEndpoint: "",
	}); err != nil {
		t.Fatalf("already-owned bucket should not fail: %v", err)
	}
}

// TestPutGet stores an object then reads it back via the streaming reader.
func TestPutGet(t *testing.T) {
	_, c := newServerClient(t)
	ctx := context.Background()
	if err := c.Put(ctx, "doc.txt", strings.NewReader("hello"), 5, "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, err := c.Get(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("body = %q, want %q", got, "hello")
	}
}

// TestPutUnknownSize stores an object with size -1 and no content type.
func TestPutUnknownSize(t *testing.T) {
	_, c := newServerClient(t)
	if err := c.Put(context.Background(), "blob", strings.NewReader("data"), -1, ""); err != nil {
		t.Fatalf("put: %v", err)
	}
}

// TestGetBytesWithContentLength reads a whole object using the ContentLength path.
func TestGetBytesWithContentLength(t *testing.T) {
	f, c := newServerClient(t)
	f.objects["sized"] = "abcdef"
	got, err := c.GetBytes(context.Background(), "sized")
	if err != nil {
		t.Fatalf("get bytes: %v", err)
	}
	if string(got) != "abcdef" {
		t.Fatalf("bytes = %q, want %q", got, "abcdef")
	}
}

// TestGetBytesEmpty falls back to the buffered copy when the object is empty.
func TestGetBytesEmpty(t *testing.T) {
	f, c := newServerClient(t)
	f.objects["empty"] = ""
	got, err := c.GetBytes(context.Background(), "empty")
	if err != nil {
		t.Fatalf("get bytes: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("bytes = %q, want empty", got)
	}
}

// TestGetMissing surfaces the typed ErrNotFound when the server answers 404.
func TestGetMissing(t *testing.T) {
	f, c := newServerClient(t)
	f.missing = "gone"
	if _, err := c.Get(context.Background(), "gone"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get missing = %v, want ErrNotFound", err)
	}
	if _, err := c.GetBytes(context.Background(), "gone"); err == nil {
		t.Fatal("expected an error for a missing object")
	}
}

// TestPresignGet builds an offline signed download URL containing the key.
func TestPresignGet(t *testing.T) {
	_, c := newServerClient(t)
	url, err := c.PresignGet(context.Background(), "report.pdf", time.Hour)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	if url == "" {
		t.Fatal("presigned URL is empty")
	}
	if !strings.Contains(url, "report.pdf") {
		t.Fatalf("presigned URL %q should contain the key", url)
	}
}

// TestMultipartLifecycle covers create, presign-part, and complete.
func TestMultipartLifecycle(t *testing.T) {
	_, c := newServerClient(t)
	ctx := context.Background()
	uploadID, err := c.CreateMultipart(ctx, "big.bin", "application/octet-stream")
	if err != nil {
		t.Fatalf("create multipart: %v", err)
	}
	if uploadID != "upload-1" {
		t.Fatalf("upload id = %q, want %q", uploadID, "upload-1")
	}
	url, err := c.PresignUploadPart(ctx, "big.bin", uploadID, 1, time.Hour)
	if err != nil {
		t.Fatalf("presign part: %v", err)
	}
	if !strings.Contains(url, "uploadId=upload-1") || !strings.Contains(url, "partNumber=1") {
		t.Fatalf("presigned part URL %q lacks the expected query", url)
	}
	parts := []storage.MultipartPart{{PartNumber: 1, ETag: `"etag-1"`}}
	if err = c.CompleteMultipart(ctx, "big.bin", uploadID, parts); err != nil {
		t.Fatalf("complete multipart: %v", err)
	}
}

// TestCreateMultipartNoContentType omits the content type on creation.
func TestCreateMultipartNoContentType(t *testing.T) {
	_, c := newServerClient(t)
	if _, err := c.CreateMultipart(context.Background(), "x.bin", ""); err != nil {
		t.Fatalf("create multipart: %v", err)
	}
}

// TestAbortMultipart discards an unfinished upload.
func TestAbortMultipart(t *testing.T) {
	_, c := newServerClient(t)
	if err := c.AbortMultipart(context.Background(), "big.bin", "upload-1"); err != nil {
		t.Fatalf("abort multipart: %v", err)
	}
}

// TestPublicEndpointPresigner signs URLs with the public endpoint when set.
func TestPublicEndpointPresigner(t *testing.T) {
	f := &fakeS3{objects: map[string]string{}, missing: ""}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := storage.New(context.Background(), storage.Config{
		Endpoint:       srv.URL,
		Region:         "us-east-1",
		AccessKey:      "x",
		SecretKey:      "y",
		Bucket:         "b",
		UsePathStyle:   true,
		PublicEndpoint: "https://cdn.example.com",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	url, err := c.PresignUploadPart(context.Background(), "big.bin", "upload-1", 2, time.Hour)
	if err != nil {
		t.Fatalf("presign part: %v", err)
	}
	if !strings.Contains(url, "cdn.example.com") {
		t.Fatalf("presigned part URL %q should use the public endpoint", url)
	}
}

// TestPutError surfaces an error when the server rejects the upload.
func TestPutError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow bucket+CORS setup, then fail the object PUT.
		if r.Method == http.MethodPut && r.URL.Query().Has("cors") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPut && strings.TrimPrefix(r.URL.Path, "/b") == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
	}))
	t.Cleanup(srv.Close)
	c, err := storage.New(context.Background(), storage.Config{
		Endpoint: srv.URL, Region: "us-east-1", AccessKey: "x", SecretKey: "y",
		Bucket: "b", UsePathStyle: true, PublicEndpoint: "",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if perr := c.Put(context.Background(), "k", strings.NewReader("v"), 1, ""); perr == nil {
		t.Fatal("expected a put error")
	}
}
