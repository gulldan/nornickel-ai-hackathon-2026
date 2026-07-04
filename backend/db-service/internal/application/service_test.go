package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/example/db-service/internal/application"
	"github.com/example/db-service/internal/domain"
)

// errBoom is a sentinel repository failure used to assert that the application
// layer propagates storage errors unchanged (no wrapping, no swallowing).
var errBoom = errors.New("boom")

// ---- fake repositories ----
//
// Each fake records its last argument and returns a programmable error/result,
// so the table tests can assert both the defaulting the service applies before
// the repo call and the propagation of repo errors back to the caller.

type fakeUsers struct {
	created  *domain.User
	count    int
	countErr error
	createErr,
	getErr,
	listErr error
	got  *domain.User
	list []*domain.User
}

func (f *fakeUsers) Create(_ context.Context, u *domain.User) error {
	f.created = u
	return f.createErr
}

func (f *fakeUsers) GetByUsername(_ context.Context, _ string) (*domain.User, error) {
	return f.got, f.getErr
}
func (f *fakeUsers) List(_ context.Context) ([]*domain.User, error) { return f.list, f.listErr }
func (f *fakeUsers) Count(_ context.Context) (int, error)           { return f.count, f.countErr }

type fakeDocs struct {
	created   *domain.Document
	createErr error
	got       *domain.Document
	getErr    error
	byHash    *domain.Document
	byHashErr error
	list      []*domain.Document
	listErr   error
	statusErr error
	titleErr  error
	lastID,
	lastStatus,
	lastMsg,
	lastTitle string
	lastChunk *int
}

func (f *fakeDocs) Create(_ context.Context, d *domain.Document) error {
	f.created = d
	return f.createErr
}
func (f *fakeDocs) Get(_ context.Context, _ string) (*domain.Document, error) { return f.got, f.getErr }
func (f *fakeDocs) FindByHash(_ context.Context, _, _ string) (*domain.Document, error) {
	return f.byHash, f.byHashErr
}
func (f *fakeDocs) ListByOwner(_ context.Context, _ string) ([]*domain.Document, error) {
	return f.list, f.listErr
}

func (f *fakeDocs) UpdateStatus(_ context.Context, id, status, msg string, chunk *int) error {
	f.lastID, f.lastStatus, f.lastMsg, f.lastChunk = id, status, msg, chunk
	return f.statusErr
}

func (f *fakeDocs) UpdateTitle(_ context.Context, id, title string) error {
	f.lastID, f.lastTitle = id, title
	return f.titleErr
}

func (f *fakeDocs) UpdateKind(_ context.Context, id, _ string) error {
	f.lastID = id
	return nil
}

func (f *fakeDocs) UpdateMeta(_ context.Context, id, _, _, _ string) error {
	f.lastID = id
	return nil
}

type fakeChats struct {
	chat      *domain.Chat
	createErr error
	got       *domain.Chat
	getErr    error
	chats     []*domain.Chat
	listErr   error
	msg       *domain.Message
	msgErr    error
	msgs      []*domain.Message
	msgsErr   error
}

func (f *fakeChats) CreateChat(_ context.Context, c *domain.Chat) error {
	f.chat = c
	return f.createErr
}
func (f *fakeChats) GetChat(_ context.Context, _ string) (*domain.Chat, error) {
	return f.got, f.getErr
}
func (f *fakeChats) ListChats(
	_ context.Context, _ string, _, _ int, _ bool,
) ([]*domain.Chat, int64, error) {
	return f.chats, int64(len(f.chats)), f.listErr
}
func (f *fakeChats) AddMessage(_ context.Context, m *domain.Message) error {
	f.msg = m
	return f.msgErr
}
func (f *fakeChats) ListMessages(_ context.Context, _ string) ([]*domain.Message, error) {
	return f.msgs, f.msgsErr
}

type fakeModels struct {
	list      []*domain.Model
	listErr   error
	upserted  []*domain.Model
	upsertErr error
}

func (f *fakeModels) List(_ context.Context) ([]*domain.Model, error) { return f.list, f.listErr }
func (f *fakeModels) Upsert(_ context.Context, m *domain.Model) error {
	f.upserted = append(f.upserted, m)
	return f.upsertErr
}

type fakeKPIs struct {
	created   *domain.KPI
	createErr error
	got       *domain.KPI
	getErr    error
	list      []*domain.KPI
	listErr   error
	updated   *domain.KPI
	updateErr error
}

func (f *fakeKPIs) Create(_ context.Context, k *domain.KPI) error {
	f.created = k
	return f.createErr
}
func (f *fakeKPIs) Get(_ context.Context, _ string) (*domain.KPI, error) { return f.got, f.getErr }
func (f *fakeKPIs) ListByOwner(_ context.Context, _ string) ([]*domain.KPI, error) {
	return f.list, f.listErr
}
func (f *fakeKPIs) Update(_ context.Context, k *domain.KPI) error {
	f.updated = k
	return f.updateErr
}

func (f *fakeKPIs) Delete(_ context.Context, _ string) error               { return nil }
func (f *fakeKPIs) AttachDocument(_ context.Context, _, _, _ string) error { return nil }
func (f *fakeKPIs) ListDocuments(_ context.Context, _ string) ([]*domain.KPIDocumentLink, error) {
	return nil, nil
}
func (f *fakeKPIs) DetachDocument(_ context.Context, _, _ string) error { return nil }

type fakeClusters struct {
	created   *domain.Cluster
	createErr error
	got       *domain.Cluster
	getErr    error
	list      []*domain.Cluster
	listErr   error
	updated   *domain.Cluster
	updateErr error
	deleted   int64
	deleteErr error
}

func (f *fakeClusters) Create(_ context.Context, c *domain.Cluster) error {
	f.created = c
	return f.createErr
}
func (f *fakeClusters) Get(_ context.Context, _ string) (*domain.Cluster, error) {
	return f.got, f.getErr
}
func (f *fakeClusters) ListByOwner(_ context.Context, _ string) ([]*domain.Cluster, error) {
	return f.list, f.listErr
}
func (f *fakeClusters) Update(_ context.Context, c *domain.Cluster) error {
	f.updated = c
	return f.updateErr
}
func (f *fakeClusters) DeleteByOwner(_ context.Context, _ string) (int64, error) {
	return f.deleted, f.deleteErr
}

// fakeRepos bundles every repository so a Service can be built with exactly the
// fakes a test cares about while the rest stay inert.
type fakeRepos struct {
	users    *fakeUsers
	docs     *fakeDocs
	chats    *fakeChats
	models   *fakeModels
	kpis     *fakeKPIs
	clusters *fakeClusters
	hyp      domain.HypothesisRepository
}

func newRepos() *fakeRepos {
	return &fakeRepos{
		users: &fakeUsers{}, docs: &fakeDocs{}, chats: &fakeChats{},
		models: &fakeModels{}, kpis: &fakeKPIs{}, clusters: &fakeClusters{}, hyp: &stubHypotheses{},
	}
}

func (r *fakeRepos) svc() *application.Service {
	return application.New(r.users, r.docs, r.chats, r.models, r.kpis, r.clusters, r.hyp, nil, nil)
}

// ---- users ----

// TestCreateUser covers required-field validation, the empty-roles default and
// repo error propagation.
func TestCreateUser(t *testing.T) {
	ctx := context.Background()

	if _, err := newRepos().svc().CreateUser(ctx, "", "h", nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty username: want ErrInvalidArgument, got %v", err)
	}
	if _, err := newRepos().svc().CreateUser(ctx, "u", "", nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty hash: want ErrInvalidArgument, got %v", err)
	}

	r := newRepos()
	u, err := r.svc().CreateUser(ctx, "alice", "hash", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == "" || u.CreatedAt.IsZero() || len(u.Roles) != 1 || u.Roles[0] != "user" {
		t.Fatalf("defaults not applied: %+v", u)
	}
	if r.users.created != u {
		t.Fatal("repo Create not called with the built user")
	}

	r = newRepos()
	got, err := r.svc().CreateUser(ctx, "bob", "h", []string{"admin"})
	if err != nil || len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("explicit roles overridden: %+v err=%v", got, err)
	}

	r = newRepos()
	r.users.createErr = errBoom
	if _, err := r.svc().CreateUser(ctx, "u", "h", nil); !errors.Is(err, errBoom) {
		t.Fatalf("create error not propagated: %v", err)
	}
}

// TestUserReads covers the pass-through user lookups.
func TestUserReads(t *testing.T) {
	ctx := context.Background()

	r := newRepos()
	r.users.got = &domain.User{ID: "u1"}
	got, err := r.svc().GetUserByUsername(ctx, "alice")
	if err != nil || got.ID != "u1" {
		t.Fatalf("get by username: %+v err=%v", got, err)
	}

	r = newRepos()
	r.users.list = []*domain.User{{ID: "u1"}, {ID: "u2"}}
	list, err := r.svc().ListUsers(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list users: %+v err=%v", list, err)
	}
}

// ---- documents ----

// TestCreateDocument covers required-field validation, defaulting to the
// uploaded status and error propagation.
func TestCreateDocument(t *testing.T) {
	ctx := context.Background()

	if _, err := newRepos().svc().CreateDocument(ctx, "", "f", "m", "k", "h", 1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty owner: want ErrInvalidArgument, got %v", err)
	}
	if _, err := newRepos().svc().CreateDocument(ctx, "o", "f", "m", "", "h", 1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty object_key: want ErrInvalidArgument, got %v", err)
	}

	r := newRepos()
	d, err := r.svc().CreateDocument(ctx, "owner", "paper.pdf", "application/pdf", "obj/1", "deadbeef", 42)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.ID == "" || d.Status != "uploaded" || d.Size != 42 || d.ContentHash != "deadbeef" || d.CreatedAt.IsZero() {
		t.Fatalf("document not built correctly: %+v", d)
	}

	r = newRepos()
	r.docs.createErr = errBoom
	if _, err := r.svc().CreateDocument(ctx, "o", "f", "m", "k", "h", 1); !errors.Is(err, errBoom) {
		t.Fatalf("create error not propagated: %v", err)
	}
}

// TestFindDocumentByHash covers the empty-argument short circuit (ErrNotFound
// without a repo call) and the delegated lookup.
func TestFindDocumentByHash(t *testing.T) {
	ctx := context.Background()

	if _, err := newRepos().svc().FindDocumentByHash(ctx, "", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty hash: want ErrNotFound, got %v", err)
	}

	r := newRepos()
	r.docs.byHash = &domain.Document{ID: "d1"}
	got, err := r.svc().FindDocumentByHash(ctx, "", "h")
	if err != nil || got.ID != "d1" {
		t.Fatalf("find by hash: %+v err=%v", got, err)
	}
}

// TestDocumentReadsAndStatus covers the pass-through reads and the status
// validation + delegation.
func TestDocumentReadsAndStatus(t *testing.T) {
	ctx := context.Background()

	r := newRepos()
	r.docs.got = &domain.Document{ID: "d1"}
	if got, err := r.svc().GetDocument(ctx, "d1"); err != nil || got.ID != "d1" {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	r = newRepos()
	r.docs.list = []*domain.Document{{ID: "d1"}}
	if list, err := r.svc().ListDocuments(ctx, "owner"); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}

	if err := newRepos().svc().UpdateDocumentStatus(ctx, "d1", "", "msg", nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty status: want ErrInvalidArgument, got %v", err)
	}

	r = newRepos()
	n := 7
	if err := r.svc().UpdateDocumentStatus(ctx, "d1", "indexed", "done", &n); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if r.docs.lastID != "d1" || r.docs.lastStatus != "indexed" || r.docs.lastChunk == nil || *r.docs.lastChunk != 7 {
		t.Fatalf("status args not forwarded: %+v", r.docs)
	}
}

// ---- chats and messages ----

// TestCreateChat covers required-owner validation, the default title and error
// propagation.
func TestCreateChat(t *testing.T) {
	ctx := context.Background()

	if _, err := newRepos().svc().CreateChat(ctx, "", "t", ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty owner: want ErrInvalidArgument, got %v", err)
	}

	r := newRepos()
	c, err := r.svc().CreateChat(ctx, "owner", "", "")
	if err != nil || c.ID == "" || c.Title != "New chat" || c.CreatedAt.IsZero() {
		t.Fatalf("default title not applied: %+v err=%v", c, err)
	}

	r = newRepos()
	c, err = r.svc().CreateChat(ctx, "owner", "Тема", "search")
	if err != nil || c.Title != "Тема" || c.Source != "search" {
		t.Fatalf("explicit title/source overridden: %+v err=%v", c, err)
	}

	r = newRepos()
	r.chats.createErr = errBoom
	if _, err := r.svc().CreateChat(ctx, "o", "t", ""); !errors.Is(err, errBoom) {
		t.Fatalf("create error not propagated: %v", err)
	}
}

// TestChatReads covers the pass-through chat lookups.
func TestChatReads(t *testing.T) {
	ctx := context.Background()

	r := newRepos()
	r.chats.got = &domain.Chat{ID: "c1"}
	if got, err := r.svc().GetChat(ctx, "c1"); err != nil || got.ID != "c1" {
		t.Fatalf("get chat: %+v err=%v", got, err)
	}

	r = newRepos()
	r.chats.chats = []*domain.Chat{{ID: "c1"}}
	if list, total, err := r.svc().ListChats(ctx, "owner", 0, 0, false); err != nil ||
		len(list) != 1 || total != 1 {
		t.Fatalf("list chats: %+v total=%d err=%v", list, total, err)
	}
}

// TestAddMessage covers required-field validation, opaque sources forwarding and
// error propagation.
func TestAddMessage(t *testing.T) {
	ctx := context.Background()

	if _, err := newRepos().svc().AddMessage(ctx, "c1", "", "hi", nil, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty role: want ErrInvalidArgument, got %v", err)
	}
	if _, err := newRepos().svc().AddMessage(ctx, "c1", "user", "", nil, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty content: want ErrInvalidArgument, got %v", err)
	}
	badMeta := []byte(`{oops`)
	if _, err := newRepos().svc().AddMessage(ctx, "c1", "user", "hi", nil, badMeta); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid meta: want ErrInvalidArgument, got %v", err)
	}

	r := newRepos()
	m, err := r.svc().AddMessage(ctx, "c1", "assistant", "answer", []byte(`[{"id":"s1"}]`), []byte(`{"model":"m1"}`))
	if err != nil || m.ID == "" || m.ChatID != "c1" ||
		string(m.Sources) != `[{"id":"s1"}]` || string(m.Meta) != `{"model":"m1"}` {
		t.Fatalf("message not built correctly: %+v err=%v", m, err)
	}

	r = newRepos()
	r.chats.msgErr = errBoom
	if _, err := r.svc().AddMessage(ctx, "c1", "user", "hi", nil, nil); !errors.Is(err, errBoom) {
		t.Fatalf("add message error not propagated: %v", err)
	}

	r = newRepos()
	r.chats.msgs = []*domain.Message{{ID: "m1"}}
	if list, err := r.svc().ListMessages(ctx, "c1"); err != nil || len(list) != 1 {
		t.Fatalf("list messages: %+v err=%v", list, err)
	}
}

// ---- models and seed ----

// TestListModels covers the catalogue pass-through.
func TestListModels(t *testing.T) {
	r := newRepos()
	r.models.list = []*domain.Model{{ID: "m1"}}
	list, err := r.svc().ListModels(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list models: %+v err=%v", list, err)
	}
}

// TestSeed covers admin creation only on an empty user table, the model upserts
// and the propagation of count/upsert errors.
func TestSeed(t *testing.T) {
	ctx := context.Background()
	models := []*domain.Model{{ID: "m1"}, {ID: "m2"}}

	// Empty table: admin is created and all models upserted.
	r := newRepos()
	if err := r.svc().Seed(ctx, "admin", "hash", models); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if r.users.created == nil || len(r.users.created.Roles) != 2 {
		t.Fatalf("admin not created with admin+user roles: %+v", r.users.created)
	}
	if len(r.models.upserted) != 2 {
		t.Fatalf("models not upserted: %+v", r.models.upserted)
	}

	// Non-empty table: no admin is created, models still upserted.
	r = newRepos()
	r.users.count = 1
	if err := r.svc().Seed(ctx, "admin", "hash", models); err != nil {
		t.Fatalf("seed with existing users: %v", err)
	}
	if r.users.created != nil {
		t.Fatalf("admin created over a non-empty table: %+v", r.users.created)
	}

	// Empty table but blank admin credentials: skip admin creation.
	r = newRepos()
	if err := r.svc().Seed(ctx, "", "", models); err != nil {
		t.Fatalf("seed with blank admin: %v", err)
	}
	if r.users.created != nil {
		t.Fatalf("admin created from blank credentials: %+v", r.users.created)
	}

	// Count error aborts before any upsert.
	r = newRepos()
	r.users.countErr = errBoom
	if err := r.svc().Seed(ctx, "admin", "h", models); !errors.Is(err, errBoom) {
		t.Fatalf("count error not propagated: %v", err)
	}

	// Upsert error is propagated.
	r = newRepos()
	r.users.count = 1
	r.models.upsertErr = errBoom
	if err := r.svc().Seed(ctx, "admin", "h", models); !errors.Is(err, errBoom) {
		t.Fatalf("upsert error not propagated: %v", err)
	}

	// Admin create error during seed is propagated.
	r = newRepos()
	r.users.createErr = errBoom
	if err := r.svc().Seed(ctx, "admin", "h", models); !errors.Is(err, errBoom) {
		t.Fatalf("admin create error not propagated: %v", err)
	}
}
