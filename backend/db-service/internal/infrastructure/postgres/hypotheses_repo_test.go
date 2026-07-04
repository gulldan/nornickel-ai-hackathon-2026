package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/example/db-service/internal/domain"
	"github.com/example/db-service/internal/infrastructure/postgres"
)

// newTestDB connects to DB_TEST_DSN and migrates it, or skips when unset.
func newTestDB(t *testing.T) *postgres.DB {
	t.Helper()
	dsn := os.Getenv("DB_TEST_DSN")
	if dsn == "" {
		t.Skip("set DB_TEST_DSN to run db-service postgres integration tests")
	}
	db, err := postgres.Connect(context.Background(), dsn, zerolog.Nop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if merr := db.Migrate(context.Background()); merr != nil {
		t.Fatalf("migrate: %v", merr)
	}
	t.Cleanup(db.Close)
	return db
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }
func ptrS(v string) *string   { return &v }

// seedDoc inserts a document so evidence has a real FK target.
func seedDoc(t *testing.T, db *postgres.DB, owner string) string {
	t.Helper()
	d := &domain.Document{
		ID: uuid.NewString(), OwnerID: owner, Filename: "paper.pdf", MIMEType: "application/pdf",
		ObjectKey: "k/" + uuid.NewString(), Status: "indexed", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := postgres.NewDocumentRepo(db).Create(context.Background(), d); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	return d.ID
}

// TestKPIRepo exercises the KPI repository against a live PostgreSQL.
func TestKPIRepo(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := postgres.NewKPIRepo(db)
	owner := uuid.NewString()

	k := &domain.KPI{
		ID: uuid.NewString(), OwnerID: owner, Title: "Снизить стоимость ГРП", Description: "удельные затраты",
		Metric: "стоимость ГРП", Unit: "руб/операция", Direction: "decrease", Baseline: ptrF(1000), Target: ptrF(800),
		FunctionArea: "ДОБЫЧА", Status: "active", Detail: []byte(`{"horizon":"2030"}`),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, k); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.Get(ctx, k.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != k.Title || got.Direction != "decrease" || got.Baseline == nil || *got.Baseline != 1000 {
		t.Fatalf("unexpected kpi: %+v", got)
	}

	got.Status = "archived"
	got.Target = ptrF(700)
	if uerr := repo.Update(ctx, got); uerr != nil {
		t.Fatalf("update: %v", uerr)
	}
	list, err := repo.ListByOwner(ctx, owner)
	if err != nil || len(list) != 1 || list[0].Status != "archived" || *list[0].Target != 700 {
		t.Fatalf("list after update: %+v err=%v", list, err)
	}
}

// TestClusterRepo exercises the cluster repository against a live PostgreSQL.
func TestClusterRepo(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := postgres.NewClusterRepo(db)
	owner := uuid.NewString()

	c := &domain.Cluster{
		ID: uuid.NewString(), OwnerID: owner, Label: "Очистка возвратной жидкости ГРП", Summary: "замкнутый цикл",
		Keywords: []string{"ГРП", "вода"}, Method: "hdbscan", ChunkCount: 42, DocumentCount: 7,
		Representatives: []byte(`[{"document_id":"d1"}]`), Params: []byte(`{"min_cluster_size":5}`),
		Status: "active", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Label != c.Label || len(got.Keywords) != 2 || got.ChunkCount != 42 {
		t.Fatalf("unexpected cluster: %+v", got)
	}
	if !strings.Contains(string(got.Params), "min_cluster_size") {
		t.Fatalf("params not round-tripped: %s", got.Params)
	}
}

// TestHypothesisRepo covers create (with evidence + initial revision), get,
// filtered list, update (+revision), revision numbering and evidence loading.
func TestHypothesisRepo(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := postgres.NewHypothesisRepo(db)
	owner := uuid.NewString()
	docID := seedDoc(t, db, owner)

	kpi := &domain.KPI{
		ID: uuid.NewString(), OwnerID: owner, Title: "Снизить отказы НКТ", Status: "active",
		Direction: "decrease", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := postgres.NewKPIRepo(db).Create(ctx, kpi); err != nil {
		t.Fatalf("seed kpi: %v", err)
	}
	cl := &domain.Cluster{
		ID: uuid.NewString(), OwnerID: owner, Label: "Коррозия НКТ", Status: "active",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := postgres.NewClusterRepo(db).Create(ctx, cl); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}

	now := time.Now().UTC()
	hid := uuid.NewString()
	h := &domain.Hypothesis{
		ID: hid, OwnerID: owner, RunID: uuid.NewString(), Title: "Композитные НКТ снижают отказы",
		Statement: "Применение композитных НКТ снизит число отказов на 20%", Rationale: "композиты стойки к H2S",
		Method: "cluster_kpi", Status: "generated", KPIID: ptrS(kpi.ID), PrimaryClusterID: ptrS(cl.ID),
		TRL: ptrI(7), NoveltyScore: ptrF(0.8), RiskScore: ptrF(0.3), ValueScore: ptrF(0.9), ConfidenceScore: ptrF(0.6),
		Measurable: true, Organization: "PetroChina", FunctionArea: "ДОБЫЧА", SourceType: "literature",
		Location: "Сычуань", Tags: []string{"ГРП", "композит"},
		Assessment: []byte(`{"novelty":{"score":0.8}}`), Detail: []byte(`{"drivers":["стойкость"]}`),
		Generation: []byte(`{"model":"owl-alpha"}`), CreatedAt: now, UpdatedAt: now,
		Evidence: []*domain.Evidence{
			{
				ID: uuid.NewString(), HypothesisID: hid, DocumentID: ptrS(docID), ChunkID: "ch1",
				Filename: "paper.pdf", Snippet: "композит стоек к H2S", Stance: "supports", Score: ptrF(0.71),
				Ord: 0, CreatedAt: now,
			},
			{
				ID: uuid.NewString(), HypothesisID: hid, Filename: "report.pdf",
				Snippet: "ограничение по температуре", Stance: "context", Ord: 1, CreatedAt: now,
			},
		},
	}
	initial := &domain.Revision{
		ID: uuid.NewString(), HypothesisID: hid, EditorID: "", Action: "created",
		Summary: "generated by run", Patch: []byte(`{}`), CreatedAt: now,
	}
	if err := repo.Create(ctx, h, initial); err != nil {
		t.Fatalf("create: %v", err)
	}
	if initial.RevisionNo != 1 {
		t.Fatalf("initial revision_no = %d, want 1", initial.RevisionNo)
	}

	got, err := repo.Get(ctx, hid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TRL == nil || *got.TRL != 7 || got.NoveltyScore == nil || *got.NoveltyScore != 0.8 {
		t.Fatalf("scores not round-tripped: trl=%v novelty=%v", got.TRL, got.NoveltyScore)
	}
	if !got.Measurable || len(got.Tags) != 2 || !strings.Contains(string(got.Assessment), "novelty") {
		t.Fatalf("fields not round-tripped: %+v", got)
	}
	if len(got.Evidence) != 2 || got.Evidence[0].Stance != "supports" || got.Evidence[1].Ord != 1 {
		t.Fatalf("evidence not loaded in order: %+v", got.Evidence)
	}
	if got.Evidence[0].DocumentID == nil || got.Evidence[1].DocumentID != nil {
		t.Fatalf("evidence doc linkage wrong: %+v", got.Evidence)
	}

	checkHypothesisFilters(t, repo, owner, hid)

	// Update: approve + set composite score, with an audit revision.
	got.Status = "approved"
	got.CompositeScore = ptrF(0.84)
	rev := &domain.Revision{
		ID: uuid.NewString(), HypothesisID: hid, EditorID: "expert-1", Action: "approved",
		Summary: "looks solid", Patch: []byte(`{"status":{"after":"approved"}}`), CreatedAt: time.Now().UTC(),
	}
	if uerr := repo.Update(ctx, got, rev); uerr != nil {
		t.Fatalf("update: %v", uerr)
	}
	if rev.RevisionNo != 2 {
		t.Fatalf("update revision_no = %d, want 2", rev.RevisionNo)
	}
	after, err := repo.Get(ctx, hid)
	if err != nil || after.Status != "approved" || after.CompositeScore == nil || *after.CompositeScore != 0.84 {
		t.Fatalf("update not persisted: %+v err=%v", after, err)
	}

	// A standalone revision gets the next number.
	standalone := &domain.Revision{
		ID: uuid.NewString(), HypothesisID: hid, EditorID: "expert-1", Action: "commented",
		Summary: "watch temperature limits", Patch: []byte(`{}`), CreatedAt: time.Now().UTC(),
	}
	if aerr := repo.AddRevision(ctx, standalone); aerr != nil {
		t.Fatalf("add revision: %v", aerr)
	}
	revs, err := repo.ListRevisions(ctx, hid)
	if err != nil || len(revs) != 3 || revs[2].RevisionNo != 3 || revs[2].Action != "commented" {
		t.Fatalf("revisions: %+v err=%v", revs, err)
	}
}

// checkHypothesisFilters verifies the board listing honours filters and ordering.
func checkHypothesisFilters(t *testing.T, repo *postgres.HypothesisRepo, owner, hid string) {
	t.Helper()
	ctx := context.Background()
	cases := []struct {
		name string
		f    domain.HypothesisFilter
		want bool
	}{
		{"owner only", domain.HypothesisFilter{OwnerID: owner}, true},
		{"status match", domain.HypothesisFilter{OwnerID: owner, Status: "generated"}, true},
		{"status miss", domain.HypothesisFilter{OwnerID: owner, Status: "rejected"}, false},
		{"trl in range", domain.HypothesisFilter{OwnerID: owner, MinTRL: 7, MaxTRL: 9}, true},
		{"trl above range", domain.HypothesisFilter{OwnerID: owner, MinTRL: 8}, false},
		{"tag match", domain.HypothesisFilter{OwnerID: owner, Tags: []string{"композит"}}, true},
		{"tag miss", domain.HypothesisFilter{OwnerID: owner, Tags: []string{"отсутствует"}}, false},
		{"order by trl", domain.HypothesisFilter{OwnerID: owner, OrderBy: "trl"}, true},
	}
	for _, tc := range cases {
		list, err := repo.List(ctx, tc.f)
		if err != nil {
			t.Fatalf("list %q: %v", tc.name, err)
		}
		found := false
		for _, x := range list {
			if x.ID == hid {
				found = true
			}
			if len(x.Evidence) != 0 {
				t.Fatalf("list %q should not load evidence", tc.name)
			}
		}
		if found != tc.want {
			t.Fatalf("list %q: found=%v want=%v (n=%d)", tc.name, found, tc.want, len(list))
		}
	}
}

// TestHypothesisRepoNotFound and constraint enforcement.
func TestHypothesisRepoNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := postgres.NewHypothesisRepo(db)

	if _, err := repo.Get(ctx, uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get missing: got %v, want ErrNotFound", err)
	}

	// CHECK constraint: TRL must be 1..9.
	owner := uuid.NewString()
	bad := &domain.Hypothesis{
		ID: uuid.NewString(), OwnerID: owner, Title: "t", Statement: "s", Method: "cluster_kpi",
		Status: "generated", TRL: ptrI(10), Measurable: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, bad, nil); err == nil {
		t.Fatal("create with trl=10 should violate the CHECK constraint")
	}
}
