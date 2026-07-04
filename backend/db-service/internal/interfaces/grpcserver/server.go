// Package grpcserver is db-service's delivery layer: it implements the generated
// DbService gRPC server and translates between domain entities and the protobuf
// DTOs. domain.ErrNotFound maps to codes.NotFound, domain.ErrInvalidArgument to
// codes.InvalidArgument; other errors to codes.Internal.
package grpcserver

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/db-service/internal/application"
	"github.com/example/db-service/internal/domain"
	"github.com/example/db-service/internal/platform/jsonx"

	commonv1 "github.com/example/db-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/db-service/internal/platform/genproto/db/v1"
)

// timeLayout formats domain timestamps as RFC3339 strings in protobuf DTOs.
const timeLayout = time.RFC3339

// Server implements dbv1.DbServiceServer over the application service.
type Server struct {
	dbv1.UnimplementedDbServiceServer
	svc *application.Service
}

// New builds the gRPC server.
func New(svc *application.Service) *Server {
	return &Server{UnimplementedDbServiceServer: dbv1.UnimplementedDbServiceServer{}, svc: svc}
}

func toStatus(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return status.Error(codes.NotFound, "not found")
	}
	if errors.Is(err, domain.ErrInvalidArgument) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// GetUserByUsername returns a user including the password hash.
func (s *Server) GetUserByUsername(
	ctx context.Context, req *dbv1.GetUserByUsernameRequest,
) (*commonv1.User, error) {
	u, err := s.svc.GetUserByUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, toStatus(err)
	}
	return userPB(u), nil
}

// CreateUser creates a user.
func (s *Server) CreateUser(ctx context.Context, req *dbv1.CreateUserRequest) (*commonv1.User, error) {
	u, err := s.svc.CreateUser(ctx, req.GetUsername(), req.GetPasswordHash(), req.GetRoles())
	if err != nil {
		return nil, toStatus(err)
	}
	return userPB(u), nil
}

// ListUsers returns every account. Password hashes are stripped: this listing
// feeds the admin UI, never credential checks.
func (s *Server) ListUsers(ctx context.Context, _ *dbv1.ListUsersRequest) (*dbv1.ListUsersResponse, error) {
	users, err := s.svc.ListUsers(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.User, 0, len(users))
	for _, u := range users {
		pb := userPB(u)
		pb.PasswordHash = ""
		out = append(out, pb)
	}
	return &dbv1.ListUsersResponse{Users: out}, nil
}

// CreateDocument creates a document in the uploaded state.
func (s *Server) CreateDocument(ctx context.Context, req *dbv1.CreateDocumentRequest) (*commonv1.Document, error) {
	d, err := s.svc.CreateDocument(ctx,
		req.GetOwnerId(), req.GetFilename(), req.GetMimeType(), req.GetObjectKey(),
		req.GetContentHash(), req.GetSize())
	if err != nil {
		return nil, toStatus(err)
	}
	return documentPB(d), nil
}

// GetDocument fetches a document by id.
func (s *Server) GetDocument(ctx context.Context, req *dbv1.GetDocumentRequest) (*commonv1.Document, error) {
	d, err := s.svc.GetDocument(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return documentPB(d), nil
}

// FindDocumentByHash returns the newest non-failed document with the hash,
// scoped to req.owner_id when set (empty keeps the shared-corpus scan).
func (s *Server) FindDocumentByHash(
	ctx context.Context, req *dbv1.FindDocumentByHashRequest,
) (*commonv1.Document, error) {
	d, err := s.svc.FindDocumentByHash(ctx, req.GetOwnerId(), req.GetContentHash())
	if err != nil {
		return nil, toStatus(err)
	}
	return documentPB(d), nil
}

// ListDocuments lists an owner's documents.
func (s *Server) ListDocuments(
	ctx context.Context, req *dbv1.ListDocumentsRequest,
) (*dbv1.ListDocumentsResponse, error) {
	docs, err := s.svc.ListDocuments(ctx, req.GetOwnerId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.Document, 0, len(docs))
	for _, d := range docs {
		out = append(out, documentPB(d))
	}
	return &dbv1.ListDocumentsResponse{Documents: out}, nil
}

// UpdateDocumentStatus advances a document's status.
func (s *Server) UpdateDocumentStatus(
	ctx context.Context, req *dbv1.UpdateDocumentStatusRequest,
) (*dbv1.UpdateDocumentStatusResponse, error) {
	var chunk *int
	if req.ChunkCount != nil {
		v := int(req.GetChunkCount())
		chunk = &v
	}
	if err := s.svc.UpdateDocumentStatus(ctx, req.GetId(), req.GetStatus(), req.GetMessage(), chunk); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.UpdateDocumentStatusResponse{}, nil
}

// SetDocumentTitle records the real article title extracted from a document.
func (s *Server) SetDocumentTitle(
	ctx context.Context, req *dbv1.SetDocumentTitleRequest,
) (*dbv1.SetDocumentTitleResponse, error) {
	if err := s.svc.SetDocumentTitle(ctx, req.GetId(), req.GetTitle()); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.SetDocumentTitleResponse{}, nil
}

// SetDocumentKind records the document class determined during indexing.
func (s *Server) SetDocumentKind(
	ctx context.Context, req *dbv1.SetDocumentKindRequest,
) (*dbv1.SetDocumentKindResponse, error) {
	if err := s.svc.SetDocumentKind(ctx, req.GetId(), req.GetKind()); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.SetDocumentKindResponse{}, nil
}

// SetDocumentMeta records a document's parser-extracted metadata.
func (s *Server) SetDocumentMeta(
	ctx context.Context, req *dbv1.SetDocumentMetaRequest,
) (*dbv1.SetDocumentMetaResponse, error) {
	err := s.svc.SetDocumentMeta(ctx, req.GetId(), req.GetAuthor(), req.GetPublishedAt(), req.GetSourceRef())
	if err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.SetDocumentMetaResponse{}, nil
}

// CreateChat starts a conversation.
func (s *Server) CreateChat(ctx context.Context, req *dbv1.CreateChatRequest) (*commonv1.Chat, error) {
	c, err := s.svc.CreateChat(ctx, req.GetOwnerId(), req.GetTitle(), req.GetSource())
	if err != nil {
		return nil, toStatus(err)
	}
	return chatPB(c), nil
}

// GetChat fetches a chat by id.
func (s *Server) GetChat(ctx context.Context, req *dbv1.GetChatRequest) (*commonv1.Chat, error) {
	c, err := s.svc.GetChat(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return chatPB(c), nil
}

// ListChats lists chats: the owner's, or everyone's for the admin history view.
func (s *Server) ListChats(ctx context.Context, req *dbv1.ListChatsRequest) (*dbv1.ListChatsResponse, error) {
	chats, total, err := s.svc.ListChats(
		ctx, req.GetOwnerId(), int(req.GetLimit()), int(req.GetOffset()), req.GetAllOwners(),
	)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.Chat, 0, len(chats))
	for _, c := range chats {
		out = append(out, chatPB(c))
	}
	return &dbv1.ListChatsResponse{Chats: out, Total: total}, nil
}

// AddMessage appends a message to a chat.
func (s *Server) AddMessage(ctx context.Context, req *dbv1.AddMessageRequest) (*commonv1.Message, error) {
	var sources []byte
	if len(req.GetSources()) > 0 {
		raw, err := jsonx.Marshal(req.GetSources())
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		sources = raw
	}
	m, err := s.svc.AddMessage(ctx, req.GetChatId(), req.GetRole(), req.GetContent(), sources, []byte(req.GetMeta()))
	if err != nil {
		return nil, toStatus(err)
	}
	return messagePB(m), nil
}

// ListMessages lists a chat's messages.
func (s *Server) ListMessages(
	ctx context.Context, req *dbv1.ListMessagesRequest,
) (*dbv1.ListMessagesResponse, error) {
	msgs, err := s.svc.ListMessages(ctx, req.GetChatId())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messagePB(m))
	}
	return &dbv1.ListMessagesResponse{Messages: out}, nil
}

// ListModels returns the AI model catalogue.
func (s *Server) ListModels(ctx context.Context, _ *dbv1.ListModelsRequest) (*dbv1.ListModelsResponse, error) {
	models, err := s.svc.ListModels(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*commonv1.Model, 0, len(models))
	for _, m := range models {
		out = append(out, &commonv1.Model{Id: m.ID, Name: m.Name, Role: m.Role, Backend: m.Backend})
	}
	return &dbv1.ListModelsResponse{Models: out}, nil
}

func userPB(u *domain.User) *commonv1.User {
	return &commonv1.User{
		Id: u.ID, Username: u.Username, PasswordHash: u.PasswordHash, Roles: u.Roles,
		CreatedAt: u.CreatedAt.Format(timeLayout),
	}
}

func documentPB(d *domain.Document) *commonv1.Document {
	return &commonv1.Document{
		Id: d.ID, OwnerId: d.OwnerID, Filename: d.Filename, MimeType: d.MIMEType, Size: d.Size,
		ObjectKey: d.ObjectKey, Status: d.Status, StatusMsg: d.StatusMsg, ChunkCount: int32(d.ChunkCount),
		CreatedAt: d.CreatedAt.Format(timeLayout), UpdatedAt: d.UpdatedAt.Format(timeLayout),
		ContentHash: d.ContentHash, Title: d.Title,
		Author: d.Author, PublishedAt: d.PublishedAt, SourceRef: d.SourceRef, Kind: d.Kind,
	}
}

func chatPB(c *domain.Chat) *commonv1.Chat {
	return &commonv1.Chat{
		Id: c.ID, OwnerId: c.OwnerID, OwnerUsername: c.OwnerUsername,
		Title: c.Title, Source: c.Source, CreatedAt: c.CreatedAt.Format(timeLayout),
	}
}

func messagePB(m *domain.Message) *commonv1.Message {
	msg := &commonv1.Message{
		Id: m.ID, ChatId: m.ChatID, Role: m.Role, Content: m.Content,
		CreatedAt: m.CreatedAt.Format(timeLayout), Sources: nil,
	}
	if len(m.Sources) > 0 {
		var sources []*commonv1.Source
		if jsonx.Unmarshal(m.Sources, &sources) == nil {
			msg.Sources = sources
		}
	}
	// The empty-object default means "no provenance": keep it "" on the wire.
	if meta := string(m.Meta); meta != "" && meta != "{}" {
		msg.Meta = meta
	}
	return msg
}
