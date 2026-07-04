// Package domain holds db-service's entities and the repository ports the
// application layer depends on. It is free of transport (gRPC) and storage (SQL)
// concerns. Statuses are plain strings (see platform/contracts.Status*).
package domain

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by repositories when an entity does not exist; the
// gRPC layer maps it to codes.NotFound.
var ErrNotFound = errors.New("not found")

// ErrInvalidArgument wraps application-layer validation failures (missing
// required fields, out-of-range values); the gRPC layer maps it to
// codes.InvalidArgument. Wrap with fmt.Errorf("...: %w", domain.ErrInvalidArgument).
var ErrInvalidArgument = errors.New("invalid argument")

// User is an account that can authenticate and own documents and chats.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Roles        []string
	CreatedAt    time.Time
}

// Document tracks an ingested file and its progress through the pipeline.
type Document struct {
	ID         string
	OwnerID    string
	Filename   string
	MIMEType   string
	Size       int64
	ObjectKey  string
	Status     string
	StatusMsg  string
	ChunkCount int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// ContentHash is the BLAKE3-256 hex of the file bytes ("" = unknown).
	ContentHash string
	// Title is the real article title extracted from the text ("" = not yet
	// extracted; the UI falls back to the filename).
	Title string
	// Parser-extracted metadata ("" = unknown): Author/PublishedAt come from
	// email headers or PDF docinfo; SourceRef labels the origin ("email", "pdf").
	Author      string
	PublishedAt string
	SourceRef   string
	// Kind is the document class from indexing ("" = regular; "hypotheses" =
	// ready-made hypotheses/brainstorm notes, excluded from hypothesis retrieval).
	Kind string
}

// Chat is a conversation thread owned by a user. Source is the page the
// conversation started from ("search"; "" on legacy rows); OwnerUsername is
// resolved on reads for history views and never stored on the row.
type Chat struct {
	ID            string
	OwnerID       string
	OwnerUsername string
	Title         string
	Source        string
	CreatedAt     time.Time
}

// Message is a single turn in a chat. Sources is the JSON-encoded citation
// list; Meta is the JSON provenance envelope ({model, cached, trace}) written
// for assistant turns (empty/'{}' for user messages and legacy rows).
type Message struct {
	ID        string
	ChatID    string
	Role      string
	Content   string
	Sources   []byte
	Meta      []byte
	CreatedAt time.Time
}

// Model is metadata about an AI backend (the GPU table from the architecture).
type Model struct {
	ID      string
	Name    string
	Role    string
	Backend string
}

// UserRepository persists users.
type UserRepository interface {
	Create(ctx context.Context, u *User) error
	GetByUsername(ctx context.Context, username string) (*User, error)
	List(ctx context.Context) ([]*User, error)
	Count(ctx context.Context) (int, error)
}

// DocumentRepository persists documents and their status transitions.
type DocumentRepository interface {
	Create(ctx context.Context, d *Document) error
	Get(ctx context.Context, id string) (*Document, error)
	FindByHash(ctx context.Context, ownerID, hash string) (*Document, error)
	ListByOwner(ctx context.Context, ownerID string) ([]*Document, error)
	UpdateStatus(ctx context.Context, id, status, msg string, chunkCount *int) error
	UpdateTitle(ctx context.Context, id, title string) error
	UpdateKind(ctx context.Context, id, kind string) error
	UpdateMeta(ctx context.Context, id, author, publishedAt, sourceRef string) error
}

// ChatRepository persists chats and their messages.
type ChatRepository interface {
	CreateChat(ctx context.Context, c *Chat) error
	GetChat(ctx context.Context, id string) (*Chat, error)
	ListChats(ctx context.Context, ownerID string, limit, offset int, allOwners bool) ([]*Chat, int64, error)
	AddMessage(ctx context.Context, m *Message) error
	ListMessages(ctx context.Context, chatID string) ([]*Message, error)
}

// ModelRepository persists AI model metadata.
type ModelRepository interface {
	List(ctx context.Context) ([]*Model, error)
	Upsert(ctx context.Context, m *Model) error
}

// ---- Hypothesis Factory ----
// Entities and ports for the Hypothesis Factory. They persist to the tables from
// migrations/00002_hypothesis_factory.sql and mirror docs/schemas/hypothesis/v1.
// Pointer fields are nullable in storage (scores stay nil until the scoring
// stage runs); the []byte fields are raw JSONB blobs that mirror the JSON Schema
// sub-objects.

// Default enum values applied by the application layer. Stored as plain strings;
// mirror the SQL CHECK constraints and schemas/hypothesis/v1.
const (
	StatusActive         = "active"      // KPI / cluster
	DirectionIncrease    = "increase"    // KPI
	HypothesisGenerated  = "generated"   // freshly generated hypothesis
	MethodClusterKPI     = "cluster_kpi" // default generation method
	StanceSupports       = "supports"    // default evidence stance
	ActionCreated        = "created"     // initial revision
	ActionEdited         = "edited"      // generic edit revision
	KPIDocumentRoleInput = "input"       // default kpi_documents role
)

// KPI is a target metric a generation run aims at — the "KPI" in "cluster × KPI".
type KPI struct {
	ID           string
	OwnerID      string
	Title        string
	Description  string
	Metric       string // the measured quantity, e.g. "стоимость ГРП"
	Unit         string
	Direction    string   // increase | decrease | maintain
	Baseline     *float64 // nil = unknown / qualitative KPI
	Target       *float64
	FunctionArea string // domain facet, e.g. "ДОБЫЧА"
	Status       string // active | archived
	Detail       []byte // JSONB: constraints, horizon, weighting
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Cluster is a thematic grouping of the corpus — the "cluster" in "cluster × KPI".
// Chunk-level membership lives in the vector store; this row is the header.
type Cluster struct {
	ID              string
	OwnerID         string
	Label           string // LLM-generated theme name
	Summary         string
	Keywords        []string
	Method          string // hdbscan | kmeans | ...
	ChunkCount      int
	DocumentCount   int
	Representatives []byte // JSONB: top docs/snippets for explainability
	Params          []byte // JSONB: clustering params for reproducibility
	Status          string // active | archived
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Hypothesis is a generated, ranked, expert-correctable research hypothesis.
type Hypothesis struct {
	ID               string
	OwnerID          string
	RunID            string // groups one generation batch
	Title            string
	Statement        string // the testable claim
	Rationale        string // обоснование
	Method           string // cluster_kpi | transfer | gap | combination | manual
	Status           string // draft | generated | under_review | approved | rejected | archived
	KPIID            *string
	PrimaryClusterID *string
	TRL              *int     // 1..9; rises with concreteness / quantitative params
	NoveltyScore     *float64 // 0..1
	RiskScore        *float64 // 0..1, higher = riskier
	ValueScore       *float64 // 0..1
	ConfidenceScore  *float64 // 0..1
	CompositeScore   *float64 // ranking output (weighting lives in the service)
	Measurable       bool     // false = scientific hypothesis without a measurable effect
	Organization     string
	FunctionArea     string
	SourceType       string
	Location         string
	Tags             []string
	Assessment       []byte // JSONB: novelty/risk/value/confidence/trl detail
	Detail           []byte // JSONB: verification, quantitative_parameters, application_potential, drivers
	Generation       []byte // JSONB: model, prompt_version, inputs, reasoning_trace
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// Evidence is populated by Get and filled by the caller before Create; it is
	// left empty by List to keep board queries cheap.
	Evidence []*Evidence
}

// Evidence grounds a hypothesis in a corpus chunk or document. filename and
// snippet are denormalized so the citation survives the source's deletion.
type Evidence struct {
	ID           string
	HypothesisID string
	DocumentID   *string // nil once the source document is deleted
	ChunkID      string  // Qdrant/OpenSearch id; "" = document-level
	Filename     string
	Snippet      string
	Stance       string // supports | contradicts | context
	Score        *float64
	Ord          int // display order within the hypothesis
	CreatedAt    time.Time
	// Provenance for grounded citations: how the snippet relates to the
	// hypothesis (conditions → effect, with numbers) and where it sits in the
	// source document so the UI can jump to the exact place.
	Relation       string
	PageStart      int // 1-based page span in the source document; 0 = unknown
	PageEnd        int
	SectionHeading string
	Origin         string // "" | input | knowledge | web
}

// Revision is one entry in a hypothesis's append-only edit/audit log — the
// provenance behind the "expert correction" requirement.
type Revision struct {
	ID           string
	HypothesisID string
	RevisionNo   int
	EditorID     string // "" = system
	Action       string // created | edited | status_changed | score_override | approved | rejected | commented
	Summary      string
	Patch        []byte // JSONB: {"before": ..., "after": ...}
	CreatedAt    time.Time
}

// HypothesisFilter narrows and orders a board listing. Zero-valued fields are
// ignored, so the empty filter lists an owner's hypotheses, highest-ranked first.
type HypothesisFilter struct {
	OwnerID      string
	Status       string
	KPIID        string
	ClusterID    string
	FunctionArea string
	SourceType   string
	Organization string
	MinTRL       int      // 0 = unset
	MaxTRL       int      // 0 = unset
	Tags         []string // matches hypotheses carrying ALL listed tags
	DocumentIDs  []string // matches hypotheses with evidence from ANY listed document
	OrderBy      string   // composite_score (default) | created_at | trl | novelty | value
	Limit        int      // 0 = no limit
	Offset       int
}

// KPIRepository persists target KPIs and their input-document links.
type KPIRepository interface {
	Create(ctx context.Context, k *KPI) error
	Get(ctx context.Context, id string) (*KPI, error)
	ListByOwner(ctx context.Context, ownerID string) ([]*KPI, error)
	Update(ctx context.Context, k *KPI) error
	Delete(ctx context.Context, id string) error
	AttachDocument(ctx context.Context, kpiID, documentID, role string) error
	ListDocuments(ctx context.Context, kpiID string) ([]*KPIDocumentLink, error)
	DetachDocument(ctx context.Context, kpiID, documentID string) error
}

// KPIDocumentLink is one document attached to a goal as its input data.
type KPIDocumentLink struct {
	Document   *Document
	Role       string // input | reference
	AttachedAt time.Time
}

// ClusterRepository persists corpus clusters.
type ClusterRepository interface {
	Create(ctx context.Context, c *Cluster) error
	Get(ctx context.Context, id string) (*Cluster, error)
	ListByOwner(ctx context.Context, ownerID string) ([]*Cluster, error)
	Update(ctx context.Context, c *Cluster) error
	DeleteByOwner(ctx context.Context, ownerID string) (int64, error)
}

// HypothesisRepository persists hypotheses, their evidence and their revisions.
type HypothesisRepository interface {
	// Create inserts a hypothesis with its Evidence and an optional initial
	// revision in one transaction.
	Create(ctx context.Context, h *Hypothesis, initial *Revision) error
	// Get returns a hypothesis with its Evidence loaded, or ErrNotFound.
	Get(ctx context.Context, id string) (*Hypothesis, error)
	// List returns the board projection (without Evidence) for a filter.
	List(ctx context.Context, f HypothesisFilter) ([]*Hypothesis, error)
	// Update persists the mutable columns and appends an optional revision in
	// one transaction.
	Update(ctx context.Context, h *Hypothesis, rev *Revision) error
	ListEvidence(ctx context.Context, hypothesisID string) ([]*Evidence, error)
	AddRevision(ctx context.Context, rev *Revision) error
	ListRevisions(ctx context.Context, hypothesisID string) ([]*Revision, error)
}
