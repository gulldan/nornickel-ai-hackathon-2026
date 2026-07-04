// Package application implements db-service's use cases against the domain
// repository ports. It owns id and timestamp generation and first-run seeding.
package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/example/db-service/internal/domain"
	"github.com/example/db-service/internal/platform/contracts"
)

// Service is the application facade over the repositories.
type Service struct {
	users      domain.UserRepository
	docs       domain.DocumentRepository
	chats      domain.ChatRepository
	models     domain.ModelRepository
	kpis       domain.KPIRepository
	clusters   domain.ClusterRepository
	hypotheses domain.HypothesisRepository
	llmUsage   domain.LLMUsageRepository
	settings   domain.AppSettingsRepository
}

// New wires the repositories into a Service.
func New(
	users domain.UserRepository,
	docs domain.DocumentRepository,
	chats domain.ChatRepository,
	models domain.ModelRepository,
	kpis domain.KPIRepository,
	clusters domain.ClusterRepository,
	hypotheses domain.HypothesisRepository,
	llmUsage domain.LLMUsageRepository,
	settings domain.AppSettingsRepository,
) *Service {
	return &Service{
		users:      users,
		docs:       docs,
		chats:      chats,
		models:     models,
		kpis:       kpis,
		clusters:   clusters,
		hypotheses: hypotheses,
		llmUsage:   llmUsage,
		settings:   settings,
	}
}

// SetLLMUsage mirrors a running daily usage total into the ledger.
func (s *Service) SetLLMUsage(ctx context.Context, row *domain.LLMUsageDaily) error {
	return s.llmUsage.SetDaily(ctx, row)
}

// ListLLMUsage returns the usage ledger for an inclusive date range.
func (s *Service) ListLLMUsage(ctx context.Context, from, to time.Time) ([]*domain.LLMUsageDaily, error) {
	return s.llmUsage.ListDaily(ctx, from, to)
}

// ListAppSettings returns every runtime override.
func (s *Service) ListAppSettings(ctx context.Context) ([]*domain.AppSetting, error) {
	return s.settings.List(ctx)
}

// SetAppSetting upserts a runtime override.
func (s *Service) SetAppSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("key is required: %w", domain.ErrInvalidArgument)
	}
	return s.settings.Upsert(ctx, key, value)
}

// DeleteAppSetting removes a runtime override.
func (s *Service) DeleteAppSetting(ctx context.Context, key string) error {
	if key == "" {
		return fmt.Errorf("key is required: %w", domain.ErrInvalidArgument)
	}
	return s.settings.Delete(ctx, key)
}

func now() time.Time { return time.Now().UTC() }

// CreateUser inserts a user, defaulting an empty role set to ["user"].
func (s *Service) CreateUser(ctx context.Context, username, passwordHash string, roles []string) (*domain.User, error) {
	if username == "" || passwordHash == "" {
		return nil, fmt.Errorf("username and password_hash are required: %w", domain.ErrInvalidArgument)
	}
	if len(roles) == 0 {
		roles = []string{"user"}
	}
	u := &domain.User{ID: uuid.NewString(), Username: username, PasswordHash: passwordHash, Roles: roles, CreatedAt: now()}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByUsername looks up a user by username.
func (s *Service) GetUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	return s.users.GetByUsername(ctx, username)
}

// ListUsers returns every account for the admin listing.
func (s *Service) ListUsers(ctx context.Context) ([]*domain.User, error) {
	return s.users.List(ctx)
}

// CreateDocument inserts a document row in the "uploaded" state.
func (s *Service) CreateDocument(
	ctx context.Context, ownerID, filename, mimeType, objectKey, contentHash string, size int64,
) (*domain.Document, error) {
	if ownerID == "" || objectKey == "" {
		return nil, fmt.Errorf("owner_id and object_key are required: %w", domain.ErrInvalidArgument)
	}
	d := &domain.Document{
		ID: uuid.NewString(), OwnerID: ownerID, Filename: filename, MIMEType: mimeType,
		Size: size, ObjectKey: objectKey, Status: contracts.StatusUploaded, StatusMsg: "",
		ChunkCount: 0, CreatedAt: now(), UpdatedAt: now(), ContentHash: contentHash,
	}
	if err := s.docs.Create(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// FindDocumentByHash returns the owner's newest non-failed document with the hash.
func (s *Service) FindDocumentByHash(ctx context.Context, ownerID, hash string) (*domain.Document, error) {
	if hash == "" {
		return nil, domain.ErrNotFound
	}
	return s.docs.FindByHash(ctx, ownerID, hash)
}

// GetDocument fetches a document by id.
func (s *Service) GetDocument(ctx context.Context, id string) (*domain.Document, error) {
	return s.docs.Get(ctx, id)
}

// ListDocuments returns an owner's documents.
func (s *Service) ListDocuments(ctx context.Context, ownerID string) ([]*domain.Document, error) {
	return s.docs.ListByOwner(ctx, ownerID)
}

// UpdateDocumentStatus advances a document's ingestion state.
func (s *Service) UpdateDocumentStatus(ctx context.Context, id, status, msg string, chunkCount *int) error {
	if status == "" {
		return fmt.Errorf("status is required: %w", domain.ErrInvalidArgument)
	}
	return s.docs.UpdateStatus(ctx, id, status, msg, chunkCount)
}

// SetDocumentTitle records the real article title extracted from a document.
func (s *Service) SetDocumentTitle(ctx context.Context, id, title string) error {
	if id == "" {
		return fmt.Errorf("id is required: %w", domain.ErrInvalidArgument)
	}
	return s.docs.UpdateTitle(ctx, id, title)
}

// SetDocumentKind records the document class determined during indexing.
func (s *Service) SetDocumentKind(ctx context.Context, id, kind string) error {
	if id == "" {
		return fmt.Errorf("id is required: %w", domain.ErrInvalidArgument)
	}
	return s.docs.UpdateKind(ctx, id, kind)
}

// SetDocumentMeta records a document's parser-extracted metadata.
func (s *Service) SetDocumentMeta(ctx context.Context, id, author, publishedAt, sourceRef string) error {
	if id == "" {
		return fmt.Errorf("id is required: %w", domain.ErrInvalidArgument)
	}
	return s.docs.UpdateMeta(ctx, id, author, publishedAt, sourceRef)
}

// CreateChat starts a new conversation, defaulting an empty title. source
// marks the page the conversation started from ("search"); optional.
func (s *Service) CreateChat(ctx context.Context, ownerID, title, source string) (*domain.Chat, error) {
	if ownerID == "" {
		return nil, fmt.Errorf("owner_id is required: %w", domain.ErrInvalidArgument)
	}
	if title == "" {
		title = "New chat"
	}
	c := &domain.Chat{ID: uuid.NewString(), OwnerID: ownerID, Title: title, Source: source, CreatedAt: now()}
	if err := s.chats.CreateChat(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// GetChat fetches a chat by id.
func (s *Service) GetChat(ctx context.Context, id string) (*domain.Chat, error) {
	return s.chats.GetChat(ctx, id)
}

// ListChats returns chats newest first: the owner's, or everyone's for the
// admin history view. limit <= 0 means "no limit"; total counts the scope.
func (s *Service) ListChats(
	ctx context.Context, ownerID string, limit, offset int, allOwners bool,
) ([]*domain.Chat, int64, error) {
	return s.chats.ListChats(ctx, ownerID, limit, offset, allOwners)
}

// AddMessage appends a message to a chat. sources and meta are opaque JSON;
// meta carries the answer provenance envelope and may be empty.
func (s *Service) AddMessage(
	ctx context.Context, chatID, role, content string, sources, meta []byte,
) (*domain.Message, error) {
	if role == "" || content == "" {
		return nil, fmt.Errorf("role and content are required: %w", domain.ErrInvalidArgument)
	}
	if len(meta) > 0 && !json.Valid(meta) {
		return nil, fmt.Errorf("meta must be valid JSON: %w", domain.ErrInvalidArgument)
	}
	m := &domain.Message{
		ID: uuid.NewString(), ChatID: chatID, Role: role, Content: content,
		Sources: sources, Meta: meta, CreatedAt: now(),
	}
	if err := s.chats.AddMessage(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// ListMessages returns a chat's messages in chronological order.
func (s *Service) ListMessages(ctx context.Context, chatID string) ([]*domain.Message, error) {
	return s.chats.ListMessages(ctx, chatID)
}

// ListModels returns the AI model catalogue.
func (s *Service) ListModels(ctx context.Context) ([]*domain.Model, error) {
	return s.models.List(ctx)
}

// Seed creates the bootstrap admin (if no users exist) and upserts the model
// catalogue. Safe to call on every startup.
func (s *Service) Seed(ctx context.Context, adminUsername, adminPasswordHash string, models []*domain.Model) error {
	count, err := s.users.Count(ctx)
	if err != nil {
		return err
	}
	if count == 0 && adminUsername != "" && adminPasswordHash != "" {
		if _, cerr := s.CreateUser(ctx, adminUsername, adminPasswordHash, []string{"admin", "user"}); cerr != nil {
			return cerr
		}
	}
	for _, m := range models {
		if uerr := s.models.Upsert(ctx, m); uerr != nil {
			return uerr
		}
	}
	return nil
}
