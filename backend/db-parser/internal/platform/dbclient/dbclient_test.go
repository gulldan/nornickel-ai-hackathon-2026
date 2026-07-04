package dbclient_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/status"

	"github.com/example/db-parser/internal/platform/dbclient"

	commonv1 "github.com/example/db-parser/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/db-parser/internal/platform/genproto/db/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// fakeDB implements DbServiceServer. It echoes identifying request fields back
// into canned responses so proto->domain mapping can be asserted. When errCode
// is set every RPC returns that status, exercising error translation.
type fakeDB struct {
	dbv1.UnimplementedDbServiceServer
	errCode codes.Code
}

// fail returns the configured status error, or nil when no error is set.
func (f *fakeDB) fail() error {
	if f.errCode == codes.OK {
		return nil
	}
	return status.Error(f.errCode, "fake error")
}

func (f *fakeDB) GetUserByUsername(_ context.Context, r *dbv1.GetUserByUsernameRequest) (*commonv1.User, error) {
	return &commonv1.User{Id: "u1", Username: r.GetUsername()}, f.fail()
}

func (f *fakeDB) CreateUser(_ context.Context, r *dbv1.CreateUserRequest) (*commonv1.User, error) {
	return &commonv1.User{Id: "u2", Username: r.GetUsername(), Roles: r.GetRoles()}, f.fail()
}

func (f *fakeDB) ListUsers(_ context.Context, _ *dbv1.ListUsersRequest) (*dbv1.ListUsersResponse, error) {
	return &dbv1.ListUsersResponse{Users: []*commonv1.User{{Id: "u3"}}}, f.fail()
}

func (f *fakeDB) CreateDocument(_ context.Context, r *dbv1.CreateDocumentRequest) (*commonv1.Document, error) {
	return &commonv1.Document{Id: "d1", OwnerId: r.GetOwnerId(), Filename: r.GetFilename()}, f.fail()
}

func (f *fakeDB) GetDocument(_ context.Context, r *dbv1.GetDocumentRequest) (*commonv1.Document, error) {
	return &commonv1.Document{Id: r.GetId()}, f.fail()
}

func (f *fakeDB) FindDocumentByHash(_ context.Context, r *dbv1.FindDocumentByHashRequest) (*commonv1.Document, error) {
	return &commonv1.Document{Id: "d2", OwnerId: r.GetOwnerId(), ContentHash: r.GetContentHash()}, f.fail()
}

func (f *fakeDB) ListDocuments(_ context.Context, r *dbv1.ListDocumentsRequest) (*dbv1.ListDocumentsResponse, error) {
	return &dbv1.ListDocumentsResponse{Documents: []*commonv1.Document{{Id: "d3", OwnerId: r.GetOwnerId()}}}, f.fail()
}

func (f *fakeDB) UpdateDocumentStatus(
	_ context.Context, _ *dbv1.UpdateDocumentStatusRequest,
) (*dbv1.UpdateDocumentStatusResponse, error) {
	return &dbv1.UpdateDocumentStatusResponse{}, f.fail()
}

func (f *fakeDB) CreateChat(_ context.Context, r *dbv1.CreateChatRequest) (*commonv1.Chat, error) {
	return &commonv1.Chat{
		Id: "ch1", OwnerId: r.GetOwnerId(), Title: r.GetTitle(), Source: r.GetSource(),
	}, f.fail()
}

func (f *fakeDB) GetChat(_ context.Context, r *dbv1.GetChatRequest) (*commonv1.Chat, error) {
	return &commonv1.Chat{Id: r.GetId()}, f.fail()
}

func (f *fakeDB) ListChats(_ context.Context, r *dbv1.ListChatsRequest) (*dbv1.ListChatsResponse, error) {
	return &dbv1.ListChatsResponse{
		Chats: []*commonv1.Chat{{Id: "ch2", OwnerId: r.GetOwnerId()}}, Total: 1,
	}, f.fail()
}

func (f *fakeDB) AddMessage(_ context.Context, r *dbv1.AddMessageRequest) (*commonv1.Message, error) {
	return &commonv1.Message{
		Id: "m1", ChatId: r.GetChatId(), Role: r.GetRole(), Content: r.GetContent(), Meta: r.GetMeta(),
	}, f.fail()
}

func (f *fakeDB) ListMessages(_ context.Context, r *dbv1.ListMessagesRequest) (*dbv1.ListMessagesResponse, error) {
	return &dbv1.ListMessagesResponse{Messages: []*commonv1.Message{{Id: "m2", ChatId: r.GetChatId()}}}, f.fail()
}

func (f *fakeDB) ListModels(_ context.Context, _ *dbv1.ListModelsRequest) (*dbv1.ListModelsResponse, error) {
	return &dbv1.ListModelsResponse{Models: []*commonv1.Model{{Id: "mod1", Name: "gpt"}}}, f.fail()
}

func (f *fakeDB) CreateKPI(_ context.Context, r *dbv1.CreateKPIRequest) (*commonv1.KPI, error) {
	return &commonv1.KPI{Id: "k1", OwnerId: r.GetOwnerId(), Title: r.GetTitle()}, f.fail()
}

func (f *fakeDB) GetKPI(_ context.Context, r *dbv1.GetKPIRequest) (*commonv1.KPI, error) {
	return &commonv1.KPI{Id: r.GetId()}, f.fail()
}

func (f *fakeDB) ListKPIs(_ context.Context, r *dbv1.ListKPIsRequest) (*dbv1.ListKPIsResponse, error) {
	return &dbv1.ListKPIsResponse{Kpis: []*commonv1.KPI{{Id: "k2", OwnerId: r.GetOwnerId()}}}, f.fail()
}

func (f *fakeDB) UpdateKPI(_ context.Context, _ *dbv1.UpdateKPIRequest) (*dbv1.UpdateKPIResponse, error) {
	return &dbv1.UpdateKPIResponse{}, f.fail()
}

func (f *fakeDB) CreateCluster(_ context.Context, r *dbv1.CreateClusterRequest) (*commonv1.Cluster, error) {
	return &commonv1.Cluster{Id: "cl1", OwnerId: r.GetOwnerId(), Label: r.GetLabel()}, f.fail()
}

func (f *fakeDB) GetCluster(_ context.Context, r *dbv1.GetClusterRequest) (*commonv1.Cluster, error) {
	return &commonv1.Cluster{Id: r.GetId()}, f.fail()
}

func (f *fakeDB) ListClusters(_ context.Context, r *dbv1.ListClustersRequest) (*dbv1.ListClustersResponse, error) {
	return &dbv1.ListClustersResponse{Clusters: []*commonv1.Cluster{{Id: "cl2", OwnerId: r.GetOwnerId()}}}, f.fail()
}

func (f *fakeDB) UpdateCluster(_ context.Context, _ *dbv1.UpdateClusterRequest) (*dbv1.UpdateClusterResponse, error) {
	return &dbv1.UpdateClusterResponse{}, f.fail()
}

func (f *fakeDB) DeleteClusters(_ context.Context, _ *dbv1.DeleteClustersRequest) (*dbv1.DeleteClustersResponse, error) {
	return &dbv1.DeleteClustersResponse{}, f.fail()
}

func (f *fakeDB) CreateHypothesis(_ context.Context, r *dbv1.CreateHypothesisRequest) (*commonv1.Hypothesis, error) {
	h := r.GetHypothesis()
	return &commonv1.Hypothesis{Id: "h1", Title: h.GetTitle()}, f.fail()
}

func (f *fakeDB) GetHypothesis(_ context.Context, r *dbv1.GetHypothesisRequest) (*commonv1.Hypothesis, error) {
	return &commonv1.Hypothesis{Id: r.GetId()}, f.fail()
}

func (f *fakeDB) ListHypotheses(_ context.Context, r *dbv1.ListHypothesesRequest) (*dbv1.ListHypothesesResponse, error) {
	return &dbv1.ListHypothesesResponse{Hypotheses: []*commonv1.Hypothesis{{Id: "h2", OwnerId: r.GetOwnerId()}}}, f.fail()
}

func (f *fakeDB) UpdateHypothesis(
	_ context.Context, _ *dbv1.UpdateHypothesisRequest,
) (*dbv1.UpdateHypothesisResponse, error) {
	return &dbv1.UpdateHypothesisResponse{}, f.fail()
}

func (f *fakeDB) AddHypothesisRevision(
	_ context.Context, r *dbv1.AddHypothesisRevisionRequest,
) (*commonv1.HypothesisRevision, error) {
	rev := r.GetRevision()
	return &commonv1.HypothesisRevision{Id: "r1", HypothesisId: rev.GetHypothesisId()}, f.fail()
}

func (f *fakeDB) ListHypothesisRevisions(
	_ context.Context, r *dbv1.ListHypothesisRevisionsRequest,
) (*dbv1.ListHypothesisRevisionsResponse, error) {
	return &dbv1.ListHypothesisRevisionsResponse{
		Revisions: []*commonv1.HypothesisRevision{{Id: "r2", HypothesisId: r.GetHypothesisId()}},
	}, f.fail()
}

func (f *fakeDB) ListHypothesisEvidence(
	_ context.Context, r *dbv1.ListHypothesisEvidenceRequest,
) (*dbv1.ListHypothesisEvidenceResponse, error) {
	return &dbv1.ListHypothesisEvidenceResponse{
		Evidence: []*commonv1.HypothesisEvidence{{Id: "e1", HypothesisId: r.GetHypothesisId()}},
	}, f.fail()
}

// newTestClient starts a real loopback gRPC server with fake and health
// services and returns a connected client.
func newTestClient(t *testing.T, fake *fakeDB) *dbclient.Client {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()
	dbv1.RegisterDbServiceServer(s, fake)
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	client, err := dbclient.New(lis.Addr().String())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestNewDialError checks that New surfaces a dial error for an invalid target.
func TestNewDialError(t *testing.T) {
	if _, err := dbclient.New("\x00invalid"); err == nil {
		t.Fatalf("expected dial error for invalid target")
	}
}

// TestMethods drives every wrapper method against the fake and asserts a
// representative field of each result, covering proto->domain mapping.
func TestMethods(t *testing.T) {
	client := newTestClient(t, &fakeDB{})
	ctx := context.Background()
	chunkCount := int32(3)

	if u, err := client.GetUserByUsername(ctx, "alice"); err != nil || u.GetUsername() != "alice" {
		t.Fatalf("GetUserByUsername = %v, %v", u, err)
	}
	if u, err := client.CreateUser(ctx, "bob", "hash", []string{"admin"}); err != nil ||
		u.GetUsername() != "bob" || len(u.GetRoles()) != 1 {
		t.Fatalf("CreateUser = %v, %v", u, err)
	}
	if us, err := client.ListUsers(ctx); err != nil || len(us) != 1 || us[0].GetId() != "u3" {
		t.Fatalf("ListUsers = %v, %v", us, err)
	}
	if d, err := client.CreateDocument(ctx, &dbv1.CreateDocumentRequest{OwnerId: "o1", Filename: "f.pdf"}); err != nil ||
		d.GetOwnerId() != "o1" || d.GetFilename() != "f.pdf" {
		t.Fatalf("CreateDocument = %v, %v", d, err)
	}
	if d, err := client.GetDocument(ctx, "doc-id"); err != nil || d.GetId() != "doc-id" {
		t.Fatalf("GetDocument = %v, %v", d, err)
	}
	if d, err := client.FindDocumentByHash(ctx, "o2", "hash3"); err != nil ||
		d.GetOwnerId() != "o2" || d.GetContentHash() != "hash3" {
		t.Fatalf("FindDocumentByHash = %v, %v", d, err)
	}
	if ds, err := client.ListDocuments(ctx, "o3"); err != nil || len(ds) != 1 || ds[0].GetOwnerId() != "o3" {
		t.Fatalf("ListDocuments = %v, %v", ds, err)
	}
	if err := client.UpdateDocumentStatus(ctx, "doc", "ready", "ok", &chunkCount); err != nil {
		t.Fatalf("UpdateDocumentStatus = %v", err)
	}
	if ch, err := client.CreateChat(ctx, "o4", "title", "search"); err != nil ||
		ch.GetOwnerId() != "o4" || ch.GetTitle() != "title" || ch.GetSource() != "search" {
		t.Fatalf("CreateChat = %v, %v", ch, err)
	}
	if ch, err := client.GetChat(ctx, "chat-id"); err != nil || ch.GetId() != "chat-id" {
		t.Fatalf("GetChat = %v, %v", ch, err)
	}
	if chs, total, err := client.ListChats(ctx, "o5", 10, 0, false); err != nil ||
		total != 1 || len(chs) != 1 || chs[0].GetOwnerId() != "o5" {
		t.Fatalf("ListChats = %v, %d, %v", chs, total, err)
	}
	if m, err := client.AddMessage(ctx, "chat", "user", "hi", nil, `{"model":"m"}`); err != nil ||
		m.GetChatId() != "chat" || m.GetContent() != "hi" || m.GetMeta() != `{"model":"m"}` {
		t.Fatalf("AddMessage = %v, %v", m, err)
	}
	if ms, err := client.ListMessages(ctx, "chat-x"); err != nil || len(ms) != 1 || ms[0].GetChatId() != "chat-x" {
		t.Fatalf("ListMessages = %v, %v", ms, err)
	}
	if mo, err := client.ListModels(ctx); err != nil || len(mo) != 1 || mo[0].GetName() != "gpt" {
		t.Fatalf("ListModels = %v, %v", mo, err)
	}
	if k, err := client.CreateKPI(ctx, &dbv1.CreateKPIRequest{OwnerId: "o6", Title: "kpi"}); err != nil ||
		k.GetOwnerId() != "o6" || k.GetTitle() != "kpi" {
		t.Fatalf("CreateKPI = %v, %v", k, err)
	}
	if k, err := client.GetKPI(ctx, "kpi-id"); err != nil || k.GetId() != "kpi-id" {
		t.Fatalf("GetKPI = %v, %v", k, err)
	}
	if ks, err := client.ListKPIs(ctx, "o7"); err != nil || len(ks) != 1 || ks[0].GetOwnerId() != "o7" {
		t.Fatalf("ListKPIs = %v, %v", ks, err)
	}
	if err := client.UpdateKPI(ctx, &commonv1.KPI{Id: "k"}); err != nil {
		t.Fatalf("UpdateKPI = %v", err)
	}
	if cl, err := client.CreateCluster(ctx, &dbv1.CreateClusterRequest{OwnerId: "o8", Label: "lab"}); err != nil ||
		cl.GetOwnerId() != "o8" || cl.GetLabel() != "lab" {
		t.Fatalf("CreateCluster = %v, %v", cl, err)
	}
	if cl, err := client.GetCluster(ctx, "cl-id"); err != nil || cl.GetId() != "cl-id" {
		t.Fatalf("GetCluster = %v, %v", cl, err)
	}
	if cls, err := client.ListClusters(ctx, "o9"); err != nil || len(cls) != 1 || cls[0].GetOwnerId() != "o9" {
		t.Fatalf("ListClusters = %v, %v", cls, err)
	}
	if err := client.UpdateCluster(ctx, &commonv1.Cluster{Id: "c"}); err != nil {
		t.Fatalf("UpdateCluster = %v", err)
	}
	if err := client.DeleteClusters(ctx, "o10"); err != nil {
		t.Fatalf("DeleteClusters = %v", err)
	}
	if h, err := client.CreateHypothesis(ctx, &dbv1.CreateHypothesisRequest{
		Hypothesis: &commonv1.Hypothesis{Title: "hyp"},
	}); err != nil || h.GetTitle() != "hyp" {
		t.Fatalf("CreateHypothesis = %v, %v", h, err)
	}
	if h, err := client.GetHypothesis(ctx, "hyp-id"); err != nil || h.GetId() != "hyp-id" {
		t.Fatalf("GetHypothesis = %v, %v", h, err)
	}
	if hs, err := client.ListHypotheses(ctx, &dbv1.ListHypothesesRequest{OwnerId: "o11"}); err != nil ||
		len(hs) != 1 || hs[0].GetOwnerId() != "o11" {
		t.Fatalf("ListHypotheses = %v, %v", hs, err)
	}
	if err := client.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{
		Hypothesis: &commonv1.Hypothesis{Id: "h"},
	}); err != nil {
		t.Fatalf("UpdateHypothesis = %v", err)
	}
	if r, err := client.AddHypothesisRevision(ctx, &commonv1.HypothesisRevision{HypothesisId: "hyp-2"}); err != nil ||
		r.GetHypothesisId() != "hyp-2" {
		t.Fatalf("AddHypothesisRevision = %v, %v", r, err)
	}
	if rs, err := client.ListHypothesisRevisions(ctx, "hyp-3"); err != nil ||
		len(rs) != 1 || rs[0].GetHypothesisId() != "hyp-3" {
		t.Fatalf("ListHypothesisRevisions = %v, %v", rs, err)
	}
	if ev, err := client.ListHypothesisEvidence(ctx, "hyp-4"); err != nil ||
		len(ev) != 1 || ev[0].GetHypothesisId() != "hyp-4" {
		t.Fatalf("ListHypothesisEvidence = %v, %v", ev, err)
	}
}

// TestNotFoundTranslation checks that a gRPC NotFound becomes ErrNotFound across
// the single-result and list code paths.
func TestNotFoundTranslation(t *testing.T) {
	client := newTestClient(t, &fakeDB{errCode: codes.NotFound})
	ctx := context.Background()

	if _, err := client.GetDocument(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("GetDocument err = %v, want ErrNotFound", err)
	}
	if _, err := client.GetUserByUsername(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("GetUserByUsername err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListUsers(ctx); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListUsers err = %v, want ErrNotFound", err)
	}
	if err := client.UpdateKPI(ctx, &commonv1.KPI{}); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("UpdateKPI err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListHypothesisEvidence(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListHypothesisEvidence err = %v, want ErrNotFound", err)
	}

	// Remaining list paths: confirm each returns the translated error on failure.
	if _, err := client.ListDocuments(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListDocuments err = %v, want ErrNotFound", err)
	}
	if _, _, err := client.ListChats(ctx, "x", 0, 0, false); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListChats err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListMessages(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListMessages err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListModels(ctx); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListModels err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListKPIs(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListKPIs err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListClusters(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListClusters err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListHypotheses(ctx, &dbv1.ListHypothesesRequest{}); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListHypotheses err = %v, want ErrNotFound", err)
	}
	if _, err := client.ListHypothesisRevisions(ctx, "x"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListHypothesisRevisions err = %v, want ErrNotFound", err)
	}
}

// TestOtherErrorPassThrough checks that non-NotFound codes are returned verbatim.
func TestOtherErrorPassThrough(t *testing.T) {
	client := newTestClient(t, &fakeDB{errCode: codes.Internal})
	ctx := context.Background()

	_, err := client.GetDocument(ctx, "x")
	if err == nil || errors.Is(err, dbclient.ErrNotFound) {
		t.Fatalf("GetDocument err = %v, want non-nil non-ErrNotFound", err)
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("status code = %v, want Internal", status.Code(err))
	}
	if _, err := client.ListDocuments(ctx, "o"); err == nil || errors.Is(err, dbclient.ErrNotFound) {
		t.Errorf("ListDocuments err = %v, want non-nil non-ErrNotFound", err)
	}
}

// TestPing verifies Ping succeeds against a SERVING health server.
func TestPing(t *testing.T) {
	client := newTestClient(t, &fakeDB{})
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestPingAfterClose verifies Ping fails once the connection is closed.
func TestPingAfterClose(t *testing.T) {
	client := newTestClient(t, &fakeDB{})
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Ping(context.Background()); err == nil {
		t.Fatalf("expected Ping to fail after Close")
	}
}
