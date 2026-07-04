// Package domain holds main-service's commands and the ports the application
// layer depends on. main-service is an edge orchestrator (BFF): it has little
// state of its own and instead coordinates object storage, db-service, the
// LLM service and the message broker. Keeping these as small ports (and the
// concrete adapters in infrastructure) keeps the use cases transport-agnostic
// and unit-testable, mirroring the parser workers' DDD shape.
package domain

import (
	"context"
	"io"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/example/main-service/internal/platform/storage"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	llmv1 "github.com/example/main-service/internal/platform/genproto/llm/v1"
)

// UploadCommand carries everything needed to ingest a single uploaded file.
// It is built by the HTTP layer from the multipart request plus the caller's
// owner id (taken from the JWT claims). Body streams the file content (large
// uploads spill to disk in the multipart layer rather than living in RAM);
// Text is set only for text/plain uploads, which skip the parser tier and are
// dispatched straight to chunk-splitter with their content inline.
type UploadCommand struct {
	OwnerID  string
	Filename string
	Size     int64
	Body     io.Reader
	Text     string
	// Hash is the BLAKE3-256 hex of the body, computed by the HTTP layer in a
	// separate pass over the (seekable) upload before Body is handed to S3 —
	// the SDK needs a seekable stream for its integrity checksums.
	Hash string
	// Reuse makes a hash-hit return the already indexed document instead of
	// registering a duplicate row.
	Reuse bool
}

// AskCommand is a single chat turn: a user message in a chat the caller owns.
type AskCommand struct {
	OwnerID string
	ChatID  string
	Content string
}

// ObjectStore persists original file content as a stream (satisfied directly
// by platform/storage.Client, which hands it to the S3 SDK without buffering
// the whole object in memory).
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// MultipartStore drives browser-direct uploads: the server only orchestrates,
// part bytes travel straight to S3 by presigned URLs (satisfied directly by
// platform/storage.Client).
type MultipartStore interface {
	CreateMultipart(ctx context.Context, key, contentType string) (string, error)
	PresignUploadPart(ctx context.Context, key, uploadID string, partNumber int32, ttl time.Duration) (string, error)
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []storage.MultipartPart) error
	AbortMultipart(ctx context.Context, key, uploadID string) error
}

// SessionStore keeps upload-session state out of process memory so
// main-service stays stateless (satisfied by platform/valkey.Client).
type SessionStore interface {
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
	GetJSON(ctx context.Context, key string, dest any) (bool, error)
	Del(ctx context.Context, keys ...string) error
}

// DocumentCatalog records and queries document metadata in db-service
// (satisfied directly by platform/dbclient.Client, whose DTOs are the
// generated protobuf types).
type DocumentCatalog interface {
	CreateDocument(ctx context.Context, req *dbv1.CreateDocumentRequest) (*commonv1.Document, error)
	GetDocument(ctx context.Context, id string) (*commonv1.Document, error)
	FindDocumentByHash(ctx context.Context, ownerID, hash string) (*commonv1.Document, error)
	ListDocuments(ctx context.Context, ownerID string) ([]*commonv1.Document, error)
	UpdateDocumentStatus(ctx context.Context, id, status, message string, chunkCount *int32) error
}

// ChatCatalog records and queries chats and their messages in db-service
// (satisfied directly by platform/dbclient.Client).
type ChatCatalog interface {
	CreateChat(ctx context.Context, ownerID, title, source string) (*commonv1.Chat, error)
	GetChat(ctx context.Context, id string) (*commonv1.Chat, error)
	ListChats(ctx context.Context, ownerID string, limit, offset int, allOwners bool) ([]*commonv1.Chat, int64, error)
	AddMessage(
		ctx context.Context, chatID, role, content string, sources []*commonv1.Source, meta string,
	) (*commonv1.Message, error)
	ListMessages(ctx context.Context, chatID string) ([]*commonv1.Message, error)
	ListModels(ctx context.Context) ([]*commonv1.Model, error)
}

// UserCatalog lists accounts for the admin panel (satisfied directly by
// platform/dbclient.Client; password hashes are never populated).
type UserCatalog interface {
	ListUsers(ctx context.Context) ([]*commonv1.User, error)
}

// ChunkReader lists the indexed chunks of one document for the preview pane
// (satisfied directly by platform/ragclient.Client).
type ChunkReader interface {
	DocumentChunks(ctx context.Context, ownerID, documentID string) ([]*llmv1.DocumentChunk, error)
}

// HypothesisCatalog is the Hypothesis Factory store in db-service (satisfied
// directly by platform/dbclient.Client). Owner scoping is enforced by the
// application layer, not here.
type HypothesisCatalog interface {
	CreateKPI(ctx context.Context, req *dbv1.CreateKPIRequest) (*commonv1.KPI, error)
	GetKPI(ctx context.Context, id string) (*commonv1.KPI, error)
	ListKPIs(ctx context.Context, ownerID string) ([]*commonv1.KPI, error)
	UpdateKPI(ctx context.Context, kpi *commonv1.KPI) error
	DeleteKPI(ctx context.Context, id string) error
	AttachKPIDocument(ctx context.Context, kpiID, documentID, role string) error
	ListKPIDocuments(ctx context.Context, kpiID string) ([]*dbv1.KpiDocumentLink, error)
	DetachKPIDocument(ctx context.Context, kpiID, documentID string) error
	ListDocuments(ctx context.Context, ownerID string) ([]*commonv1.Document, error)

	CreateCluster(ctx context.Context, req *dbv1.CreateClusterRequest) (*commonv1.Cluster, error)
	GetCluster(ctx context.Context, id string) (*commonv1.Cluster, error)
	ListClusters(ctx context.Context, ownerID string) ([]*commonv1.Cluster, error)
	UpdateCluster(ctx context.Context, cluster *commonv1.Cluster) error
	DeleteClusters(ctx context.Context, ownerID string) error

	CreateHypothesis(ctx context.Context, req *dbv1.CreateHypothesisRequest) (*commonv1.Hypothesis, error)
	GetHypothesis(ctx context.Context, id string) (*commonv1.Hypothesis, error)
	ListHypotheses(ctx context.Context, req *dbv1.ListHypothesesRequest) ([]*commonv1.Hypothesis, error)
	UpdateHypothesis(ctx context.Context, req *dbv1.UpdateHypothesisRequest) error
	AddHypothesisRevision(ctx context.Context, rev *commonv1.HypothesisRevision) (*commonv1.HypothesisRevision, error)
	ListHypothesisRevisions(ctx context.Context, hypothesisID string) ([]*commonv1.HypothesisRevision, error)
	ListHypothesisEvidence(ctx context.Context, hypothesisID string) ([]*commonv1.HypothesisEvidence, error)
}

// EventPublisher emits protobuf messages onto the broker (satisfied by
// platform/messaging.Publisher). The application layer uses it to kick off the
// ingestion pipeline.
type EventPublisher interface {
	PublishProto(ctx context.Context, exchange, routingKey string, msg proto.Message) error
}

// Answerer asks the LLM service for a grounded answer to a query (satisfied by
// platform/ragclient.Client over gRPC).
type Answerer interface {
	Answer(ctx context.Context, req *commonv1.RagRequest) (*commonv1.RagResponse, error)
}
