// Package storage is a thin wrapper over the official AWS SDK for Go v2 S3
// client — the platform's object store for original files and presigned
// downloads. It works against real AWS S3 (leave S3_ENDPOINT empty) or any
// S3-compatible endpoint (set S3_ENDPOINT + path-style), e.g. the SeaweedFS
// container named localstack in local development.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/example/main-service/internal/platform/config"
)

// Client wraps an S3 client bound to a single bucket.
type Client struct {
	api    *s3.Client
	bucket string
	// presignAPI, when set, is used ONLY to sign presigned URLs with the public
	// endpoint (S3_PUBLIC_ENDPOINT); inter-service traffic uses the internal api.
	presignAPI *s3.Client
}

// Config holds the connection parameters (sourced from env vars).
type Config struct {
	Endpoint     string // empty => real AWS S3; otherwise an S3-compatible URL
	Region       string
	AccessKey    string
	SecretKey    string
	Bucket       string
	UsePathStyle bool
	// PublicEndpoint is the S3 address reachable from the BROWSER (presigned URLs
	// are signed with it). Empty = same as Endpoint.
	PublicEndpoint string
}

// ConfigFromEnv reads the standard S3_* environment variables.
func ConfigFromEnv() Config {
	return Config{
		Endpoint:     config.Get("S3_ENDPOINT", ""),
		Region:       config.Get("S3_REGION", "us-east-1"),
		AccessKey:    config.Get("S3_ACCESS_KEY", ""),
		SecretKey:    config.Get("S3_SECRET_KEY", ""),
		Bucket:       config.Get("S3_BUCKET", "documents"),
		UsePathStyle: config.GetBool("S3_USE_PATH_STYLE", false),

		PublicEndpoint: config.Get("S3_PUBLIC_ENDPOINT", ""),
	}
}

// New builds the S3 client and ensures the bucket exists.
func New(ctx context.Context, cfg Config) (*Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	api := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})
	c := &Client{api: api, bucket: cfg.Bucket, presignAPI: nil}
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		c.presignAPI = s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.PublicEndpoint)
			o.UsePathStyle = cfg.UsePathStyle
		})
	}
	if err = c.ensureBucket(ctx); err != nil {
		return nil, err
	}
	if err = c.ensureCORS(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// ensureCORS allows browsers to PUT multipart parts straight to the bucket.
// ETag must be exposed: completion needs the part ETags, and without
// exposeHeaders the browser cannot read them. Idempotent. An S3 backend that
// does not implement the CORS API (older SeaweedFS/MinIO builds) must not
// brick the whole service: only browser-direct uploads degrade, so
// NotImplemented is tolerated.
func (c *Client) ensureCORS(ctx context.Context) error {
	_, err := c.api.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String(c.bucket),
		CORSConfiguration: &s3types.CORSConfiguration{
			CORSRules: []s3types.CORSRule{{
				AllowedOrigins: []string{"*"},
				AllowedMethods: []string{"PUT", "GET", "HEAD"},
				AllowedHeaders: []string{"*"},
				ExposeHeaders:  []string{"ETag"},
				MaxAgeSeconds:  aws.Int32(3600),
			}},
		},
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotImplemented" {
			return nil
		}
		return fmt.Errorf("configure bucket cors: %w", err)
	}
	return nil
}

func (c *Client) ensureBucket(ctx context.Context) error {
	_, err := c.api.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(c.bucket)})
	if err == nil {
		return nil
	}
	var owned *s3types.BucketAlreadyOwnedByYou
	var exists *s3types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return nil
	}
	return fmt.Errorf("create bucket %s: %w", c.bucket, err)
}

// Put stores an object. Pass size = -1 for unknown length.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	in := &s3.PutObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key), Body: r}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if _, err := c.api.PutObject(ctx, in); err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	return nil
}

// ErrNotFound marks a read of an object key that does not exist in the bucket.
var ErrNotFound = errors.New("object not found")

// Get opens an object for reading. The caller must close the reader. A missing
// key surfaces as ErrNotFound so callers can answer 404 instead of 500.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("get object %s: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	return out.Body, nil
}

// isNotFound reports whether err is the backend's missing-object answer
// (NoSuchKey from GetObject, NotFound from HEAD-shaped responses).
func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	code := apiErr.ErrorCode()
	return code == "NoSuchKey" || code == "NotFound"
}

// GetBytes reads an entire object into memory. The buffer is preallocated from
// ContentLength, since on large objects a doubling bytes.Buffer would peak at
// nearly 2x the object size.
func (c *Client) GetBytes(ctx context.Context, key string) ([]byte, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	if n := aws.ToInt64(out.ContentLength); n > 0 {
		buf := make([]byte, n)
		if _, rerr := io.ReadFull(out.Body, buf); rerr != nil {
			return nil, fmt.Errorf("read object %s: %w", key, rerr)
		}
		return buf, nil
	}
	var buf bytes.Buffer
	if _, err = io.Copy(&buf, out.Body); err != nil {
		return nil, fmt.Errorf("read object %s: %w", key, err)
	}
	return buf.Bytes(), nil
}

// PresignGet returns a time-limited download URL for the object.
func (c *Client) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	ps := s3.NewPresignClient(c.api)
	req, err := ps.PresignGetObject(ctx,
		&s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)},
		s3.WithPresignExpires(ttl),
	)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return req.URL, nil
}

// Ping verifies connectivity for readiness probes.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)}); err != nil {
		return fmt.Errorf("ping object store: %w", err)
	}
	return nil
}

// ---- Multipart upload (large files straight from the browser) -------------
//
// The browser must not push tens of gigabytes through the edge: main-service
// creates a multipart session, hands out presigned part URLs (signed with the
// public endpoint), the browser PUTs parts straight to S3, then the session
// completes and the object enters the normal pipeline. This needs bucket CORS
// (see New).

// MultipartPart identifies one uploaded part for completion.
type MultipartPart struct {
	PartNumber int32
	ETag       string
}

// CreateMultipart starts a multipart upload for key and returns the S3 upload id.
func (c *Client) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	out, err := c.api.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", fmt.Errorf("create multipart %s: %w", key, err)
	}
	return aws.ToString(out.UploadId), nil
}

// PresignUploadPart returns a time-limited URL the browser PUTs one part to.
func (c *Client) PresignUploadPart(
	ctx context.Context, key, uploadID string, partNumber int32, ttl time.Duration,
) (string, error) {
	presigner := c.presigner()
	req, err := presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(c.bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(partNumber),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign part %d of %s: %w", partNumber, key, err)
	}
	return req.URL, nil
}

// CompleteMultipart finalises the upload from the collected part ETags.
func (c *Client) CompleteMultipart(
	ctx context.Context, key, uploadID string, parts []MultipartPart,
) error {
	completed := make([]s3types.CompletedPart, 0, len(parts))
	for _, p := range parts {
		completed = append(completed, s3types.CompletedPart{
			PartNumber: aws.Int32(p.PartNumber),
			ETag:       aws.String(p.ETag),
		})
	}
	_, err := c.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(c.bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return fmt.Errorf("complete multipart %s: %w", key, err)
	}
	return nil
}

// AbortMultipart discards an unfinished upload (cancel/cleanup).
func (c *Client) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := c.api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("abort multipart %s: %w", key, err)
	}
	return nil
}

// presigner builds a presign client. When PublicEndpoint differs from the
// internal endpoint (docker network vs browser), URLs are signed with the public
// host — the S3 signature includes the host, which must match where the browser
// will go.
func (c *Client) presigner() *s3.PresignClient {
	if c.presignAPI != nil {
		return s3.NewPresignClient(c.presignAPI)
	}
	return s3.NewPresignClient(c.api)
}
