// Package dbclient is the typed gRPC client for db-service (the BFF over
// PostgreSQL). auth-service, main-service and the workers use it instead of
// touching the database directly. The DTOs are the generated protobuf types, so
// client and server cannot drift. A gRPC NotFound is translated to ErrNotFound.
package dbclient

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/office-parser/internal/platform/grpcx"

	commonv1 "github.com/example/office-parser/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/office-parser/internal/platform/genproto/db/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// ErrNotFound is returned when db-service reports gRPC NotFound.
var ErrNotFound = errors.New("dbclient: not found")

// Client calls db-service over gRPC.
type Client struct {
	conn *grpc.ClientConn
	api  dbv1.DbServiceClient
}

// New dials db-service at addr (host:port). The connection is lazy.
func New(addr string) (*Client, error) {
	conn, err := grpcx.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("dial db-service: %w", err)
	}
	return &Client{conn: conn, api: dbv1.NewDbServiceClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close db connection: %w", err)
	}
	return nil
}

// Ping checks db-service health for readiness probes.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := healthpb.NewHealthClient(c.conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("db-service health: %w", err)
	}
	return nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}

// GetUserByUsername returns the user (including password hash) or ErrNotFound.
func (c *Client) GetUserByUsername(ctx context.Context, username string) (*commonv1.User, error) {
	u, err := c.api.GetUserByUsername(ctx, &dbv1.GetUserByUsernameRequest{Username: username})
	return u, mapErr(err)
}

// CreateUser inserts a user and returns the stored row.
func (c *Client) CreateUser(ctx context.Context, username, passwordHash string, roles []string) (*commonv1.User, error) {
	u, err := c.api.CreateUser(ctx, &dbv1.CreateUserRequest{
		Username: username, PasswordHash: passwordHash, Roles: roles,
	})
	return u, mapErr(err)
}

// ListUsers returns every account (password hashes are never populated).
func (c *Client) ListUsers(ctx context.Context) ([]*commonv1.User, error) {
	resp, err := c.api.ListUsers(ctx, &dbv1.ListUsersRequest{})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetUsers(), nil
}

// CreateDocument inserts a document row in the "uploaded" state.
func (c *Client) CreateDocument(ctx context.Context, req *dbv1.CreateDocumentRequest) (*commonv1.Document, error) {
	d, err := c.api.CreateDocument(ctx, req)
	return d, mapErr(err)
}

// FindDocumentByHash returns the newest non-failed document owned by ownerID with
// the given BLAKE3 content hash, or ErrNotFound. Scoping by owner keeps
// deduplication from linking one tenant's upload to another tenant's document.
func (c *Client) FindDocumentByHash(ctx context.Context, ownerID, hash string) (*commonv1.Document, error) {
	d, err := c.api.FindDocumentByHash(ctx, &dbv1.FindDocumentByHashRequest{OwnerId: ownerID, ContentHash: hash})
	return d, mapErr(err)
}

// GetDocument fetches a document by id.
func (c *Client) GetDocument(ctx context.Context, id string) (*commonv1.Document, error) {
	d, err := c.api.GetDocument(ctx, &dbv1.GetDocumentRequest{Id: id})
	return d, mapErr(err)
}

// ListDocuments returns an owner's documents, newest first.
func (c *Client) ListDocuments(ctx context.Context, ownerID string) ([]*commonv1.Document, error) {
	resp, err := c.api.ListDocuments(ctx, &dbv1.ListDocumentsRequest{OwnerId: ownerID})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetDocuments(), nil
}

// UpdateDocumentStatus advances a document's ingestion state. chunkCount may be nil.
func (c *Client) UpdateDocumentStatus(ctx context.Context, id, status, message string, chunkCount *int32) error {
	_, err := c.api.UpdateDocumentStatus(ctx, &dbv1.UpdateDocumentStatusRequest{
		Id: id, Status: status, Message: message, ChunkCount: chunkCount,
	})
	return mapErr(err)
}

// SetDocumentTitle stores the real article title extracted from a document.
func (c *Client) SetDocumentTitle(ctx context.Context, id, title string) error {
	_, err := c.api.SetDocumentTitle(ctx, &dbv1.SetDocumentTitleRequest{Id: id, Title: title})
	return mapErr(err)
}

// SetDocumentKind stores the document class determined during indexing.
func (c *Client) SetDocumentKind(ctx context.Context, id, kind string) error {
	_, err := c.api.SetDocumentKind(ctx, &dbv1.SetDocumentKindRequest{Id: id, Kind: kind})
	return mapErr(err)
}

// SetDocumentMeta stores a document's parser-extracted metadata.
func (c *Client) SetDocumentMeta(ctx context.Context, id, author, publishedAt, sourceRef string) error {
	_, err := c.api.SetDocumentMeta(ctx, &dbv1.SetDocumentMetaRequest{
		Id: id, Author: author, PublishedAt: publishedAt, SourceRef: sourceRef,
	})
	return mapErr(err)
}

// CreateChat starts a new conversation. source marks the page it started from.
func (c *Client) CreateChat(ctx context.Context, ownerID, title, source string) (*commonv1.Chat, error) {
	ch, err := c.api.CreateChat(ctx, &dbv1.CreateChatRequest{OwnerId: ownerID, Title: title, Source: source})
	return ch, mapErr(err)
}

// GetChat fetches a chat by id.
func (c *Client) GetChat(ctx context.Context, id string) (*commonv1.Chat, error) {
	ch, err := c.api.GetChat(ctx, &dbv1.GetChatRequest{Id: id})
	return ch, mapErr(err)
}

// ListChats returns chats newest first with the total count in scope.
// limit <= 0 — full list; allOwners — admin history across users.
func (c *Client) ListChats(
	ctx context.Context, ownerID string, limit, offset int, allOwners bool,
) ([]*commonv1.Chat, int64, error) {
	resp, err := c.api.ListChats(ctx, &dbv1.ListChatsRequest{
		OwnerId: ownerID, Limit: int32(limit), Offset: int32(offset), AllOwners: allOwners,
	})
	if err != nil {
		return nil, 0, mapErr(err)
	}
	return resp.GetChats(), resp.GetTotal(), nil
}

// AddMessage appends a message to a chat. meta is an optional raw-JSON
// provenance envelope ({model, cached, trace}); pass "" for user messages.
func (c *Client) AddMessage(
	ctx context.Context, chatID, role, content string, sources []*commonv1.Source, meta string,
) (*commonv1.Message, error) {
	m, err := c.api.AddMessage(ctx, &dbv1.AddMessageRequest{
		ChatId: chatID, Role: role, Content: content, Sources: sources, Meta: meta,
	})
	return m, mapErr(err)
}

// ListMessages returns a chat's messages in chronological order.
func (c *Client) ListMessages(ctx context.Context, chatID string) ([]*commonv1.Message, error) {
	resp, err := c.api.ListMessages(ctx, &dbv1.ListMessagesRequest{ChatId: chatID})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetMessages(), nil
}

// ListModels returns the configured AI model metadata.
func (c *Client) ListModels(ctx context.Context) ([]*commonv1.Model, error) {
	resp, err := c.api.ListModels(ctx, &dbv1.ListModelsRequest{})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetModels(), nil
}

// ---- Hypothesis Factory ----

// CreateKPI inserts a KPI and returns the stored row.
func (c *Client) CreateKPI(ctx context.Context, req *dbv1.CreateKPIRequest) (*commonv1.KPI, error) {
	k, err := c.api.CreateKPI(ctx, req)
	return k, mapErr(err)
}

// GetKPI fetches a KPI by id.
func (c *Client) GetKPI(ctx context.Context, id string) (*commonv1.KPI, error) {
	k, err := c.api.GetKPI(ctx, &dbv1.GetKPIRequest{Id: id})
	return k, mapErr(err)
}

// ListKPIs returns an owner's KPIs.
func (c *Client) ListKPIs(ctx context.Context, ownerID string) ([]*commonv1.KPI, error) {
	resp, err := c.api.ListKPIs(ctx, &dbv1.ListKPIsRequest{OwnerId: ownerID})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetKpis(), nil
}

// SetLLMUsageDaily mirrors a running daily usage total into the ledger.
func (c *Client) SetLLMUsageDaily(
	ctx context.Context, day, model, operation string,
	requests, promptTokens, completionTokens, costNanoUSD int64,
) error {
	_, err := c.api.SetLLMUsageDaily(ctx, &dbv1.SetLLMUsageDailyRequest{Row: &dbv1.LLMUsageDailyRow{
		Day:              day,
		Model:            model,
		Operation:        operation,
		Requests:         requests,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostNanoUsd:      costNanoUSD,
	}})
	if err != nil {
		return mapErr(err)
	}
	return nil
}

// ListAppSettings returns every global runtime setting override.
func (c *Client) ListAppSettings(ctx context.Context) (map[string]string, error) {
	resp, err := c.api.ListAppSettings(ctx, &dbv1.ListAppSettingsRequest{})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[string]string, len(resp.GetSettings()))
	for _, s := range resp.GetSettings() {
		out[s.GetKey()] = s.GetValue()
	}
	return out, nil
}

// SetAppSetting upserts a global runtime setting override.
func (c *Client) SetAppSetting(ctx context.Context, key, value string) error {
	_, err := c.api.SetAppSetting(ctx, &dbv1.SetAppSettingRequest{Setting: &dbv1.AppSetting{Key: key, Value: value}})
	if err != nil {
		return mapErr(err)
	}
	return nil
}

// DeleteAppSetting removes a global runtime setting override.
func (c *Client) DeleteAppSetting(ctx context.Context, key string) error {
	_, err := c.api.DeleteAppSetting(ctx, &dbv1.DeleteAppSettingRequest{Key: key})
	if err != nil {
		return mapErr(err)
	}
	return nil
}

// ListLLMUsageDaily returns the usage ledger for an inclusive [fromDay, toDay] range.
func (c *Client) ListLLMUsageDaily(ctx context.Context, fromDay, toDay string) ([]*dbv1.LLMUsageDailyRow, error) {
	resp, err := c.api.ListLLMUsageDaily(ctx, &dbv1.ListLLMUsageDailyRequest{FromDay: fromDay, ToDay: toDay})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetRows(), nil
}

// UpdateKPI persists a full KPI.
func (c *Client) UpdateKPI(ctx context.Context, kpi *commonv1.KPI) error {
	_, err := c.api.UpdateKPI(ctx, &dbv1.UpdateKPIRequest{Kpi: kpi})
	return mapErr(err)
}

// DeleteKPI removes a goal; linked hypotheses keep living with kpi_id nulled.
func (c *Client) DeleteKPI(ctx context.Context, id string) error {
	_, err := c.api.DeleteKPI(ctx, &dbv1.DeleteKPIRequest{Id: id})
	return mapErr(err)
}

// AttachKPIDocument links a document to a goal as its input data.
func (c *Client) AttachKPIDocument(ctx context.Context, kpiID, documentID, role string) error {
	_, err := c.api.AttachKpiDocument(ctx, &dbv1.AttachKpiDocumentRequest{KpiId: kpiID, DocumentId: documentID, Role: role})
	return mapErr(err)
}

// ListKPIDocuments returns the documents attached to a goal.
func (c *Client) ListKPIDocuments(ctx context.Context, kpiID string) ([]*dbv1.KpiDocumentLink, error) {
	resp, err := c.api.ListKpiDocuments(ctx, &dbv1.ListKpiDocumentsRequest{KpiId: kpiID})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetLinks(), nil
}

// DetachKPIDocument unlinks a document from a goal.
func (c *Client) DetachKPIDocument(ctx context.Context, kpiID, documentID string) error {
	_, err := c.api.DetachKpiDocument(ctx, &dbv1.DetachKpiDocumentRequest{KpiId: kpiID, DocumentId: documentID})
	return mapErr(err)
}

// CreateCluster inserts a cluster and returns the stored row.
func (c *Client) CreateCluster(ctx context.Context, req *dbv1.CreateClusterRequest) (*commonv1.Cluster, error) {
	cl, err := c.api.CreateCluster(ctx, req)
	return cl, mapErr(err)
}

// GetCluster fetches a cluster by id.
func (c *Client) GetCluster(ctx context.Context, id string) (*commonv1.Cluster, error) {
	cl, err := c.api.GetCluster(ctx, &dbv1.GetClusterRequest{Id: id})
	return cl, mapErr(err)
}

// ListClusters returns an owner's clusters.
func (c *Client) ListClusters(ctx context.Context, ownerID string) ([]*commonv1.Cluster, error) {
	resp, err := c.api.ListClusters(ctx, &dbv1.ListClustersRequest{OwnerId: ownerID})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetClusters(), nil
}

// UpdateCluster persists a full cluster.
func (c *Client) UpdateCluster(ctx context.Context, cluster *commonv1.Cluster) error {
	_, err := c.api.UpdateCluster(ctx, &dbv1.UpdateClusterRequest{Cluster: cluster})
	return mapErr(err)
}

// DeleteClusters removes all of an owner's clusters.
func (c *Client) DeleteClusters(ctx context.Context, ownerID string) error {
	_, err := c.api.DeleteClusters(ctx, &dbv1.DeleteClustersRequest{OwnerId: ownerID})
	return mapErr(err)
}

// CreateHypothesis inserts a hypothesis with its evidence and optional initial revision.
func (c *Client) CreateHypothesis(
	ctx context.Context, req *dbv1.CreateHypothesisRequest,
) (*commonv1.Hypothesis, error) {
	h, err := c.api.CreateHypothesis(ctx, req)
	return h, mapErr(err)
}

// GetHypothesis fetches a hypothesis (with evidence) by id.
func (c *Client) GetHypothesis(ctx context.Context, id string) (*commonv1.Hypothesis, error) {
	h, err := c.api.GetHypothesis(ctx, &dbv1.GetHypothesisRequest{Id: id})
	return h, mapErr(err)
}

// ListHypotheses returns the board projection for a filter.
func (c *Client) ListHypotheses(
	ctx context.Context, req *dbv1.ListHypothesesRequest,
) ([]*commonv1.Hypothesis, error) {
	resp, err := c.api.ListHypotheses(ctx, req)
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetHypotheses(), nil
}

// UpdateHypothesis persists a full hypothesis and an optional audit revision.
func (c *Client) UpdateHypothesis(ctx context.Context, req *dbv1.UpdateHypothesisRequest) error {
	_, err := c.api.UpdateHypothesis(ctx, req)
	return mapErr(err)
}

// AddHypothesisRevision appends an audit/edit entry.
func (c *Client) AddHypothesisRevision(
	ctx context.Context, rev *commonv1.HypothesisRevision,
) (*commonv1.HypothesisRevision, error) {
	r, err := c.api.AddHypothesisRevision(ctx, &dbv1.AddHypothesisRevisionRequest{Revision: rev})
	return r, mapErr(err)
}

// ListHypothesisRevisions returns a hypothesis's revisions in order.
func (c *Client) ListHypothesisRevisions(
	ctx context.Context, hypothesisID string,
) ([]*commonv1.HypothesisRevision, error) {
	resp, err := c.api.ListHypothesisRevisions(ctx, &dbv1.ListHypothesisRevisionsRequest{HypothesisId: hypothesisID})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetRevisions(), nil
}

// ListHypothesisEvidence returns a hypothesis's evidence in display order.
func (c *Client) ListHypothesisEvidence(
	ctx context.Context, hypothesisID string,
) ([]*commonv1.HypothesisEvidence, error) {
	resp, err := c.api.ListHypothesisEvidence(ctx, &dbv1.ListHypothesisEvidenceRequest{HypothesisId: hypothesisID})
	if err != nil {
		return nil, mapErr(err)
	}
	return resp.GetEvidence(), nil
}
