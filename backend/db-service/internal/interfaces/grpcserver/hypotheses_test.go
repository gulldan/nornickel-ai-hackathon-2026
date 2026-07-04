package grpcserver_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/example/db-service/internal/application"
	"github.com/example/db-service/internal/infrastructure/postgres"
	"github.com/example/db-service/internal/interfaces/grpcserver"

	commonv1 "github.com/example/db-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/db-service/internal/platform/genproto/db/v1"
)

// newServer builds the gRPC server over a live PostgreSQL, or skips.
func newServer(t *testing.T) *grpcserver.Server {
	t.Helper()
	dsn := os.Getenv("DB_TEST_DSN")
	if dsn == "" {
		t.Skip("set DB_TEST_DSN to run db-service grpc integration tests")
	}
	db, err := postgres.Connect(context.Background(), dsn, zerolog.Nop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if merr := db.Migrate(context.Background()); merr != nil {
		t.Fatalf("migrate: %v", merr)
	}
	t.Cleanup(db.Close)
	svc := application.New(
		postgres.NewUserRepo(db), postgres.NewDocumentRepo(db), postgres.NewChatRepo(db),
		postgres.NewModelRepo(db), postgres.NewKPIRepo(db), postgres.NewClusterRepo(db),
		postgres.NewHypothesisRepo(db),
		postgres.NewLLMUsageRepo(db), postgres.NewAppSettingsRepo(db),
	)
	return grpcserver.New(svc)
}

func f64(v float64) *float64 { return &v }
func i32(v int32) *int32     { return &v }

// TestHypothesisGRPC exercises the hypothesis-factory handlers end to end:
// proto<->domain mapping, the application layer and PostgreSQL.
func TestHypothesisGRPC(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	owner := uuid.NewString()

	kpi, err := srv.CreateKPI(ctx, &dbv1.CreateKPIRequest{
		OwnerId: owner, Title: "Снизить отказы НКТ", Direction: "decrease", Baseline: f64(100), Target: f64(80),
	})
	if err != nil || kpi.GetId() == "" {
		t.Fatalf("create kpi: %v (%v)", err, kpi)
	}
	cl, err := srv.CreateCluster(ctx, &dbv1.CreateClusterRequest{
		OwnerId: owner, Label: "Коррозия НКТ", Keywords: []string{"коррозия"},
	})
	if err != nil || cl.GetId() == "" {
		t.Fatalf("create cluster: %v", err)
	}

	created, err := srv.CreateHypothesis(ctx, &dbv1.CreateHypothesisRequest{
		Hypothesis: &commonv1.Hypothesis{
			OwnerId: owner, RunId: uuid.NewString(), Title: "Композитные НКТ снижают отказы",
			Statement: "Композитные НКТ снизят отказы на 20%", Rationale: "стойкость к H2S",
			Method: "cluster_kpi", KpiId: &kpi.Id, PrimaryClusterId: &cl.Id, Trl: i32(7),
			NoveltyScore: f64(0.8), RiskScore: f64(0.3), ValueScore: f64(0.9), ConfidenceScore: f64(0.6),
			Measurable: true, Organization: "PetroChina", FunctionArea: "ДОБЫЧА", SourceType: "literature",
			Tags: []string{"композит"}, Assessment: `{"novelty":{"score":0.8}}`,
			Evidence: []*commonv1.HypothesisEvidence{
				{ChunkId: "ch1", Filename: "paper.pdf", Snippet: "композит стоек к H2S", Stance: "supports", Score: f64(0.71)},
			},
		},
		Initial: &commonv1.HypothesisRevision{Action: "created", Summary: "by run"},
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	if created.GetId() == "" || created.GetTrl() != 7 || len(created.GetEvidence()) != 1 {
		t.Fatalf("unexpected created hypothesis: %+v", created)
	}
	if created.GetEvidence()[0].GetId() == "" {
		t.Fatal("evidence id not assigned server-side")
	}

	got, err := srv.GetHypothesis(ctx, &dbv1.GetHypothesisRequest{Id: created.GetId()})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetStatus() != "generated" || got.GetNoveltyScore() != 0.8 || got.GetKpiId() != kpi.GetId() {
		t.Fatalf("get mapping wrong: %+v", got)
	}
	if !strings.Contains(got.GetAssessment(), "novelty") || len(got.GetEvidence()) != 1 {
		t.Fatalf("get detail wrong: %+v", got)
	}

	list, err := srv.ListHypotheses(ctx, &dbv1.ListHypothesesRequest{
		OwnerId: owner, MinTrl: 7, Tags: []string{"композит"}, OrderBy: "trl",
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !containsID(list.GetHypotheses(), created.GetId()) {
		t.Fatalf("list did not return the hypothesis (n=%d)", len(list.GetHypotheses()))
	}

	got.Status = "approved"
	got.CompositeScore = f64(0.84)
	if _, uerr := srv.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{
		Hypothesis: got,
		Revision:   &commonv1.HypothesisRevision{Action: "approved", EditorId: "expert-1", Summary: "ok"},
	}); uerr != nil {
		t.Fatalf("update: %v", uerr)
	}
	after, err := srv.GetHypothesis(ctx, &dbv1.GetHypothesisRequest{Id: created.GetId()})
	if err != nil || after.GetStatus() != "approved" || after.GetCompositeScore() != 0.84 {
		t.Fatalf("update not persisted: %+v err=%v", after, err)
	}
	revs, err := srv.ListHypothesisRevisions(ctx, &dbv1.ListHypothesisRevisionsRequest{HypothesisId: created.GetId()})
	if err != nil || len(revs.GetRevisions()) != 2 {
		t.Fatalf("revisions: %+v err=%v", revs, err)
	}
}

func containsID(hs []*commonv1.Hypothesis, id string) bool {
	for _, h := range hs {
		if h.GetId() == id {
			return true
		}
	}
	return false
}
