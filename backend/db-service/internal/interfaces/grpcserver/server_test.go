package grpcserver_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/db-service/internal/application"
	"github.com/example/db-service/internal/infrastructure/postgres"
	"github.com/example/db-service/internal/interfaces/grpcserver"

	commonv1 "github.com/example/db-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/db-service/internal/platform/genproto/db/v1"
)

// newClosedServer builds a server whose pool is already closed, so any handler
// that reaches storage fails with a non-domain error — exercising toStatus's
// codes.Internal fallback.
func newClosedServer(t *testing.T) *grpcserver.Server {
	t.Helper()
	dsn := os.Getenv("DB_TEST_DSN")
	if dsn == "" {
		t.Skip("set DB_TEST_DSN to run db-service grpc integration tests")
	}
	db, err := postgres.Connect(context.Background(), dsn, zerolog.Nop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	svc := application.New(
		postgres.NewUserRepo(db), postgres.NewDocumentRepo(db), postgres.NewChatRepo(db),
		postgres.NewModelRepo(db), postgres.NewKPIRepo(db), postgres.NewClusterRepo(db),
		postgres.NewHypothesisRepo(db),
		postgres.NewLLMUsageRepo(db), postgres.NewAppSettingsRepo(db),
	)
	db.Close()
	return grpcserver.New(svc)
}

// TestInternalErrorTranslation confirms a non-domain storage failure maps to
// codes.Internal rather than NotFound or InvalidArgument.
func TestInternalErrorTranslation(t *testing.T) {
	srv := newClosedServer(t)
	_, err := srv.ListUsers(context.Background(), &dbv1.ListUsersRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("storage failure: want Internal, got %v", err)
	}
}

// TestUserGRPC exercises the user handlers: create, lookup (with the password
// hash), and the listing that strips hashes for the admin UI.
func TestUserGRPC(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	username := "user-" + uuid.NewString()

	created, err := srv.CreateUser(ctx, &dbv1.CreateUserRequest{
		Username: username, PasswordHash: "hash", Roles: []string{"admin", "user"},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if created.GetId() == "" || created.GetPasswordHash() != "hash" || len(created.GetRoles()) != 2 {
		t.Fatalf("unexpected created user: %+v", created)
	}

	got, err := srv.GetUserByUsername(ctx, &dbv1.GetUserByUsernameRequest{Username: username})
	if err != nil || got.GetId() != created.GetId() || got.GetPasswordHash() != "hash" {
		t.Fatalf("get user: %+v err=%v", got, err)
	}

	list, err := srv.ListUsers(ctx, &dbv1.ListUsersRequest{})
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	found := false
	for _, u := range list.GetUsers() {
		if u.GetId() == created.GetId() {
			found = true
			if u.GetPasswordHash() != "" {
				t.Fatalf("ListUsers must strip the password hash, got %q", u.GetPasswordHash())
			}
		}
	}
	if !found {
		t.Fatalf("created user not in listing (n=%d)", len(list.GetUsers()))
	}

	// Missing username maps to codes.NotFound.
	_, err = srv.GetUserByUsername(ctx, &dbv1.GetUserByUsernameRequest{Username: "absent-" + uuid.NewString()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("get missing user: want NotFound, got %v", err)
	}
	// Missing required fields map to codes.InvalidArgument.
	if _, cerr := srv.CreateUser(ctx, &dbv1.CreateUserRequest{Username: ""}); status.Code(cerr) != codes.InvalidArgument {
		t.Fatalf("create invalid user: want InvalidArgument, got %v", cerr)
	}
}

// TestDocumentGRPC exercises the document handlers: create, get, hash lookup,
// listing, status update and the not-found/invalid-argument translations.
func TestDocumentGRPC(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	owner := uuid.NewString()
	hash := "h-" + uuid.NewString()

	created, err := srv.CreateDocument(ctx, &dbv1.CreateDocumentRequest{
		OwnerId: owner, Filename: "paper.pdf", MimeType: "application/pdf",
		ObjectKey: "k/" + uuid.NewString(), ContentHash: hash, Size: 99,
	})
	if err != nil || created.GetId() == "" || created.GetStatus() != "uploaded" {
		t.Fatalf("create document: %+v err=%v", created, err)
	}

	got, err := srv.GetDocument(ctx, &dbv1.GetDocumentRequest{Id: created.GetId()})
	if err != nil || got.GetSize() != 99 || got.GetContentHash() != hash {
		t.Fatalf("get document: %+v err=%v", got, err)
	}

	byHash, err := srv.FindDocumentByHash(ctx, &dbv1.FindDocumentByHashRequest{ContentHash: hash, OwnerId: owner})
	if err != nil || byHash.GetId() != created.GetId() {
		t.Fatalf("find by hash: %+v err=%v", byHash, err)
	}

	list, err := srv.ListDocuments(ctx, &dbv1.ListDocumentsRequest{OwnerId: owner})
	if err != nil || len(list.GetDocuments()) != 1 || list.GetDocuments()[0].GetId() != created.GetId() {
		t.Fatalf("list documents: %+v err=%v", list, err)
	}

	cc := int32(5)
	if _, uerr := srv.UpdateDocumentStatus(ctx, &dbv1.UpdateDocumentStatusRequest{
		Id: created.GetId(), Status: "indexed", Message: "done", ChunkCount: &cc,
	}); uerr != nil {
		t.Fatalf("update status: %v", uerr)
	}
	after, err := srv.GetDocument(ctx, &dbv1.GetDocumentRequest{Id: created.GetId()})
	if err != nil || after.GetStatus() != "indexed" || after.GetChunkCount() != 5 {
		t.Fatalf("status not persisted: %+v err=%v", after, err)
	}

	if _, gerr := srv.GetDocument(ctx, &dbv1.GetDocumentRequest{Id: uuid.NewString()}); status.Code(gerr) != codes.NotFound {
		t.Fatalf("get missing document: want NotFound, got %v", gerr)
	}
	if _, cerr := srv.CreateDocument(ctx, &dbv1.CreateDocumentRequest{OwnerId: ""}); status.Code(cerr) != codes.InvalidArgument {
		t.Fatalf("create invalid document: want InvalidArgument, got %v", cerr)
	}
}

// TestChatGRPC exercises the chat and message handlers, including structured
// sources round-tripping through the protobuf Source list.
func TestChatGRPC(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	owner := uuid.NewString()

	chat, err := srv.CreateChat(ctx, &dbv1.CreateChatRequest{OwnerId: owner, Title: "Тема"})
	if err != nil || chat.GetId() == "" || chat.GetTitle() != "Тема" {
		t.Fatalf("create chat: %+v err=%v", chat, err)
	}

	got, err := srv.GetChat(ctx, &dbv1.GetChatRequest{Id: chat.GetId()})
	if err != nil || got.GetId() != chat.GetId() {
		t.Fatalf("get chat: %+v err=%v", got, err)
	}

	chats, err := srv.ListChats(ctx, &dbv1.ListChatsRequest{OwnerId: owner})
	if err != nil || len(chats.GetChats()) != 1 {
		t.Fatalf("list chats: %+v err=%v", chats, err)
	}

	// A message with structured sources marshals on the way in and unmarshals
	// back into the protobuf Source list on the way out; meta rides as raw JSON.
	if _, aerr := srv.AddMessage(ctx, &dbv1.AddMessageRequest{
		ChatId: chat.GetId(), Role: "assistant", Content: "ответ",
		Sources: []*commonv1.Source{{DocumentId: "d1", Filename: "paper.pdf", ChunkId: "ch1", Score: 0.9}},
		Meta:    `{"model":"qwen","cached":false}`,
	}); aerr != nil {
		t.Fatalf("add message with sources: %v", aerr)
	}
	// A message without sources keeps them empty and reads back an empty meta.
	msg, err := srv.AddMessage(ctx, &dbv1.AddMessageRequest{ChatId: chat.GetId(), Role: "user", Content: "вопрос"})
	if err != nil || msg.GetId() == "" || msg.GetMeta() != "" {
		t.Fatalf("add message: %+v err=%v", msg, err)
	}

	msgs, err := srv.ListMessages(ctx, &dbv1.ListMessagesRequest{ChatId: chat.GetId()})
	if err != nil || len(msgs.GetMessages()) != 2 {
		t.Fatalf("list messages: %+v err=%v", msgs, err)
	}
	if got := msgs.GetMessages()[0].GetSources(); len(got) != 1 || got[0].GetDocumentId() != "d1" {
		t.Fatalf("sources not round-tripped through proto: %+v", got)
	}
	// JSONB round-trips normalize spacing/order; either spelling is the same doc.
	meta := msgs.GetMessages()[0].GetMeta()
	if meta != `{"model": "qwen", "cached": false}` && meta != `{"model":"qwen","cached":false}` {
		t.Fatalf("meta not round-tripped through proto: %q", meta)
	}

	if _, gerr := srv.GetChat(ctx, &dbv1.GetChatRequest{Id: uuid.NewString()}); status.Code(gerr) != codes.NotFound {
		t.Fatalf("get missing chat: want NotFound, got %v", gerr)
	}
	_, aerr := srv.AddMessage(ctx, &dbv1.AddMessageRequest{ChatId: chat.GetId(), Role: ""})
	if status.Code(aerr) != codes.InvalidArgument {
		t.Fatalf("add invalid message: want InvalidArgument, got %v", aerr)
	}
}

// TestListModelsGRPC exercises the model catalogue handler against the seeded
// (or empty) models table.
func TestListModelsGRPC(t *testing.T) {
	srv := newServer(t)
	if _, err := srv.ListModels(context.Background(), &dbv1.ListModelsRequest{}); err != nil {
		t.Fatalf("list models: %v", err)
	}
}

// TestKPIClusterGRPC exercises the KPI and cluster handlers end to end:
// create, get, list, update and the bulk delete, plus the not-found and
// invalid-argument translations.
func TestKPIClusterGRPC(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	owner := uuid.NewString()

	kpi, err := srv.CreateKPI(ctx, &dbv1.CreateKPIRequest{
		OwnerId: owner, Title: "Снизить отказы", Metric: "отказы", Unit: "шт",
		Direction: "decrease", Baseline: f64(100), Target: f64(80), FunctionArea: "ДОБЫЧА",
		Detail: `{"horizon":"2030"}`,
	})
	if err != nil || kpi.GetId() == "" || kpi.GetDirection() != "decrease" {
		t.Fatalf("create kpi: %+v err=%v", kpi, err)
	}
	gotKPI, err := srv.GetKPI(ctx, &dbv1.GetKPIRequest{Id: kpi.GetId()})
	if err != nil || gotKPI.GetTitle() != "Снизить отказы" || !strings.Contains(gotKPI.GetDetail(), "horizon") {
		t.Fatalf("get kpi: %+v err=%v", gotKPI, err)
	}
	kpis, err := srv.ListKPIs(ctx, &dbv1.ListKPIsRequest{OwnerId: owner})
	if err != nil || len(kpis.GetKpis()) != 1 {
		t.Fatalf("list kpis: %+v err=%v", kpis, err)
	}
	gotKPI.Status = "archived"
	gotKPI.Target = f64(70)
	if _, uerr := srv.UpdateKPI(ctx, &dbv1.UpdateKPIRequest{Kpi: gotKPI}); uerr != nil {
		t.Fatalf("update kpi: %v", uerr)
	}
	afterKPI, err := srv.GetKPI(ctx, &dbv1.GetKPIRequest{Id: kpi.GetId()})
	if err != nil || afterKPI.GetStatus() != "archived" || afterKPI.GetTarget() != 70 {
		t.Fatalf("kpi update not persisted: %+v err=%v", afterKPI, err)
	}

	cl, err := srv.CreateCluster(ctx, &dbv1.CreateClusterRequest{
		OwnerId: owner, Label: "Коррозия", Summary: "сводка", Keywords: []string{"коррозия"},
		Method: "hdbscan", ChunkCount: 10, DocumentCount: 3, Representatives: `[{"id":"d1"}]`,
		Params: `{"min_cluster_size":5}`,
	})
	if err != nil || cl.GetId() == "" || cl.GetChunkCount() != 10 {
		t.Fatalf("create cluster: %+v err=%v", cl, err)
	}
	gotCl, err := srv.GetCluster(ctx, &dbv1.GetClusterRequest{Id: cl.GetId()})
	if err != nil || gotCl.GetLabel() != "Коррозия" || !strings.Contains(gotCl.GetParams(), "min_cluster_size") {
		t.Fatalf("get cluster: %+v err=%v", gotCl, err)
	}
	clusters, err := srv.ListClusters(ctx, &dbv1.ListClustersRequest{OwnerId: owner})
	if err != nil || len(clusters.GetClusters()) != 1 {
		t.Fatalf("list clusters: %+v err=%v", clusters, err)
	}
	gotCl.Status = "archived"
	if _, uerr := srv.UpdateCluster(ctx, &dbv1.UpdateClusterRequest{Cluster: gotCl}); uerr != nil {
		t.Fatalf("update cluster: %v", uerr)
	}

	del, err := srv.DeleteClusters(ctx, &dbv1.DeleteClustersRequest{OwnerId: owner})
	if err != nil || del.GetDeleted() != 1 {
		t.Fatalf("delete clusters: deleted=%d err=%v", del.GetDeleted(), err)
	}

	// Error translations.
	if _, gerr := srv.GetKPI(ctx, &dbv1.GetKPIRequest{Id: uuid.NewString()}); status.Code(gerr) != codes.NotFound {
		t.Fatalf("get missing kpi: want NotFound, got %v", gerr)
	}
	if _, gerr := srv.GetCluster(ctx, &dbv1.GetClusterRequest{Id: uuid.NewString()}); status.Code(gerr) != codes.NotFound {
		t.Fatalf("get missing cluster: want NotFound, got %v", gerr)
	}
	if _, cerr := srv.CreateKPI(ctx, &dbv1.CreateKPIRequest{OwnerId: ""}); status.Code(cerr) != codes.InvalidArgument {
		t.Fatalf("create invalid kpi: want InvalidArgument, got %v", cerr)
	}
	if _, derr := srv.DeleteClusters(ctx, &dbv1.DeleteClustersRequest{OwnerId: ""}); status.Code(derr) != codes.InvalidArgument {
		t.Fatalf("delete with empty owner: want InvalidArgument, got %v", derr)
	}
}

// TestHypothesisEvidenceAndRevisionGRPC covers the standalone revision and
// evidence listing handlers and their invalid-argument translation.
func TestHypothesisEvidenceAndRevisionGRPC(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	owner := uuid.NewString()

	created, err := srv.CreateHypothesis(ctx, &dbv1.CreateHypothesisRequest{
		Hypothesis: &commonv1.Hypothesis{OwnerId: owner, Title: "t", Statement: "s"},
		Initial:    &commonv1.HypothesisRevision{Action: "created", Summary: "by run"},
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	rev, err := srv.AddHypothesisRevision(ctx, &dbv1.AddHypothesisRevisionRequest{
		Revision: &commonv1.HypothesisRevision{HypothesisId: created.GetId(), Action: "commented", Summary: "note"},
	})
	if err != nil || rev.GetId() == "" || rev.GetRevisionNo() != 2 {
		t.Fatalf("add revision: %+v err=%v", rev, err)
	}

	// An update without a revision message exercises the nil-revision path.
	if _, uerr := srv.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: created}); uerr != nil {
		t.Fatalf("update without revision: %v", uerr)
	}

	revs, err := srv.ListHypothesisRevisions(ctx, &dbv1.ListHypothesisRevisionsRequest{HypothesisId: created.GetId()})
	if err != nil || len(revs.GetRevisions()) != 2 {
		t.Fatalf("list revisions: %+v err=%v", revs, err)
	}

	ev, err := srv.ListHypothesisEvidence(ctx, &dbv1.ListHypothesisEvidenceRequest{HypothesisId: created.GetId()})
	if err != nil || len(ev.GetEvidence()) != 0 {
		t.Fatalf("list evidence: %+v err=%v", ev, err)
	}

	// A revision missing required fields maps to InvalidArgument.
	_, aerr := srv.AddHypothesisRevision(ctx, &dbv1.AddHypothesisRevisionRequest{
		Revision: &commonv1.HypothesisRevision{HypothesisId: "", Action: ""},
	})
	if status.Code(aerr) != codes.InvalidArgument {
		t.Fatalf("add invalid revision: want InvalidArgument, got %v", aerr)
	}
}
