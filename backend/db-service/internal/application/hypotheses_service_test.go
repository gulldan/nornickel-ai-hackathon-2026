package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/example/db-service/internal/domain"
)

// ---- KPIs ----

// TestCreateKPI covers required-field validation, direction/status defaulting,
// id/timestamp assignment and error propagation.
func TestCreateKPI(t *testing.T) {
	ctx := context.Background()

	for _, k := range []*domain.KPI{{Title: "t"}, {OwnerID: "o"}} {
		if _, err := newRepos().svc().CreateKPI(ctx, k); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("missing field: want ErrInvalidArgument, got %v", err)
		}
	}

	r := newRepos()
	k, err := r.svc().CreateKPI(ctx, &domain.KPI{OwnerID: "o", Title: "Снизить отказы"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if k.ID == "" || k.Direction != domain.DirectionIncrease || k.Status != domain.StatusActive || k.CreatedAt.IsZero() {
		t.Fatalf("defaults not applied: %+v", k)
	}

	r = newRepos()
	k, err = r.svc().CreateKPI(ctx, &domain.KPI{OwnerID: "o", Title: "t", Direction: "decrease", Status: "archived"})
	if err != nil || k.Direction != "decrease" || k.Status != "archived" {
		t.Fatalf("explicit values overridden: %+v err=%v", k, err)
	}

	r = newRepos()
	r.kpis.createErr = errBoom
	if _, err := r.svc().CreateKPI(ctx, &domain.KPI{OwnerID: "o", Title: "t"}); !errors.Is(err, errBoom) {
		t.Fatalf("create error not propagated: %v", err)
	}
}

// TestKPIReads covers the get and list pass-throughs.
func TestKPIReads(t *testing.T) {
	ctx := context.Background()

	r := newRepos()
	r.kpis.got = &domain.KPI{ID: "k1"}
	if got, err := r.svc().GetKPI(ctx, "k1"); err != nil || got.ID != "k1" {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	r = newRepos()
	r.kpis.list = []*domain.KPI{{ID: "k1"}}
	if list, err := r.svc().ListKPIs(ctx, "owner"); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
}

// TestUpdateKPI covers the required-id check, status/direction defaulting on the
// passed entity and delegation.
func TestUpdateKPI(t *testing.T) {
	ctx := context.Background()

	if err := newRepos().svc().UpdateKPI(ctx, &domain.KPI{}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("missing id: want ErrInvalidArgument, got %v", err)
	}

	r := newRepos()
	k := &domain.KPI{ID: "k1"}
	if err := r.svc().UpdateKPI(ctx, k); err != nil {
		t.Fatalf("update: %v", err)
	}
	if k.Direction != domain.DirectionIncrease || k.Status != domain.StatusActive {
		t.Fatalf("defaults not applied on update: %+v", k)
	}
	if r.kpis.updated != k {
		t.Fatal("repo Update not called")
	}

	r = newRepos()
	r.kpis.updateErr = errBoom
	if err := r.svc().UpdateKPI(ctx, &domain.KPI{ID: "k1"}); !errors.Is(err, errBoom) {
		t.Fatalf("update error not propagated: %v", err)
	}
}

// ---- Clusters ----

// TestCreateCluster covers required-field validation, status defaulting and
// error propagation.
func TestCreateCluster(t *testing.T) {
	ctx := context.Background()

	for _, c := range []*domain.Cluster{{Label: "l"}, {OwnerID: "o"}} {
		if _, err := newRepos().svc().CreateCluster(ctx, c); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("missing field: want ErrInvalidArgument, got %v", err)
		}
	}

	r := newRepos()
	c, err := r.svc().CreateCluster(ctx, &domain.Cluster{OwnerID: "o", Label: "Коррозия"})
	if err != nil || c.ID == "" || c.Status != domain.StatusActive || c.CreatedAt.IsZero() {
		t.Fatalf("defaults not applied: %+v err=%v", c, err)
	}

	r = newRepos()
	r.clusters.createErr = errBoom
	if _, err := r.svc().CreateCluster(ctx, &domain.Cluster{OwnerID: "o", Label: "l"}); !errors.Is(err, errBoom) {
		t.Fatalf("create error not propagated: %v", err)
	}
}

// TestClusterReads covers the get and list pass-throughs.
func TestClusterReads(t *testing.T) {
	ctx := context.Background()

	r := newRepos()
	r.clusters.got = &domain.Cluster{ID: "c1"}
	if got, err := r.svc().GetCluster(ctx, "c1"); err != nil || got.ID != "c1" {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	r = newRepos()
	r.clusters.list = []*domain.Cluster{{ID: "c1"}}
	if list, err := r.svc().ListClusters(ctx, "owner"); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
}

// TestUpdateCluster covers the required-id check, status defaulting and
// delegation.
func TestUpdateCluster(t *testing.T) {
	ctx := context.Background()

	if err := newRepos().svc().UpdateCluster(ctx, &domain.Cluster{}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("missing id: want ErrInvalidArgument, got %v", err)
	}

	r := newRepos()
	c := &domain.Cluster{ID: "c1"}
	if err := r.svc().UpdateCluster(ctx, c); err != nil {
		t.Fatalf("update: %v", err)
	}
	if c.Status != domain.StatusActive || r.clusters.updated != c {
		t.Fatalf("status default / delegation wrong: %+v", c)
	}

	r = newRepos()
	r.clusters.updateErr = errBoom
	if err := r.svc().UpdateCluster(ctx, &domain.Cluster{ID: "c1"}); !errors.Is(err, errBoom) {
		t.Fatalf("update error not propagated: %v", err)
	}
}

// TestDeleteClusters covers the required-owner check, the returned count and
// error propagation.
func TestDeleteClusters(t *testing.T) {
	ctx := context.Background()

	if _, err := newRepos().svc().DeleteClusters(ctx, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty owner: want ErrInvalidArgument, got %v", err)
	}

	r := newRepos()
	r.clusters.deleted = 3
	if n, err := r.svc().DeleteClusters(ctx, "owner"); err != nil || n != 3 {
		t.Fatalf("delete: n=%d err=%v", n, err)
	}

	r = newRepos()
	r.clusters.deleteErr = errBoom
	if _, err := r.svc().DeleteClusters(ctx, "owner"); !errors.Is(err, errBoom) {
		t.Fatalf("delete error not propagated: %v", err)
	}
}

// ---- Hypotheses: defaulting, reads and revisions ----

// TestCreateHypothesisDefaults covers method/status defaulting, the id/timestamp
// stamping of the hypothesis, its evidence and the optional initial revision.
func TestCreateHypothesisDefaults(t *testing.T) {
	ctx := context.Background()
	svc := newSvcWithHypotheses(&stubHypotheses{})

	h := &domain.Hypothesis{
		OwnerID: "o", Title: "t", Statement: "s",
		Evidence: []*domain.Evidence{{Filename: "a.pdf"}, {Filename: "b.pdf", Stance: "contradicts"}},
	}
	initial := &domain.Revision{Summary: "by run"}
	out, err := svc.CreateHypothesis(ctx, h, initial)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.ID == "" || out.Method != domain.MethodClusterKPI || out.Status != domain.HypothesisGenerated {
		t.Fatalf("hypothesis defaults not applied: %+v", out)
	}
	if out.CreatedAt.IsZero() || out.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not stamped: %+v", out)
	}
	for i, e := range out.Evidence {
		if e.ID == "" || e.HypothesisID != out.ID || e.CreatedAt.IsZero() {
			t.Fatalf("evidence[%d] not stamped: %+v", i, e)
		}
	}
	if out.Evidence[0].Stance != domain.StanceSupports || out.Evidence[1].Stance != "contradicts" {
		t.Fatalf("evidence stance defaulting wrong: %+v", out.Evidence)
	}
	if initial.ID == "" || initial.HypothesisID != out.ID || initial.Action != domain.ActionCreated {
		t.Fatalf("initial revision not stamped: %+v", initial)
	}
}

// TestCreateHypothesisPropagatesRepoError confirms a repository failure surfaces.
func TestCreateHypothesisPropagatesRepoError(t *testing.T) {
	svc := newSvcWithHypotheses(&errHypotheses{})
	h := &domain.Hypothesis{OwnerID: "o", Title: "t", Statement: "s"}
	if _, err := svc.CreateHypothesis(context.Background(), h, nil); !errors.Is(err, errBoom) {
		t.Fatalf("create error not propagated: %v", err)
	}
}

// TestUpdateHypothesisDefaults covers method/status defaulting and revision
// stamping (with the generic edited action default).
func TestUpdateHypothesisDefaults(t *testing.T) {
	ctx := context.Background()
	stub := &stubHypotheses{}
	svc := newSvcWithHypotheses(stub)

	h := &domain.Hypothesis{ID: "h1"}
	rev := &domain.Revision{Summary: "tweak"}
	if err := svc.UpdateHypothesis(ctx, h, rev); err != nil {
		t.Fatalf("update: %v", err)
	}
	if h.Method != domain.MethodClusterKPI || h.Status != domain.HypothesisGenerated || h.UpdatedAt.IsZero() {
		t.Fatalf("defaults not applied: %+v", h)
	}
	if rev.ID == "" || rev.HypothesisID != "h1" || rev.Action != domain.ActionEdited {
		t.Fatalf("revision not stamped: %+v", rev)
	}
	if !stub.updated {
		t.Fatal("repo Update not called")
	}
}

// TestGetAndListHypotheses covers the read pass-through, including the
// all-owners listing (empty owner id).
func TestGetAndListHypotheses(t *testing.T) {
	ctx := context.Background()
	svc := newSvcWithHypotheses(&stubHypotheses{})

	if _, err := svc.GetHypothesis(ctx, "h1"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := svc.ListHypotheses(ctx, domain.HypothesisFilter{}); err != nil {
		t.Fatalf("list all owners: %v", err)
	}
	if _, err := svc.ListHypotheses(ctx, domain.HypothesisFilter{OwnerID: "o"}); err != nil {
		t.Fatalf("list: %v", err)
	}
}

// TestAddHypothesisRevision covers required-field validation, id/timestamp
// stamping and error propagation.
func TestAddHypothesisRevision(t *testing.T) {
	ctx := context.Background()

	bad := []*domain.Revision{{Action: "edited"}, {HypothesisID: "h1"}}
	for _, rev := range bad {
		svc := newSvcWithHypotheses(&stubHypotheses{})
		if _, err := svc.AddHypothesisRevision(ctx, rev); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("missing field: want ErrInvalidArgument, got %v", err)
		}
	}

	svc := newSvcWithHypotheses(&stubHypotheses{})
	rev := &domain.Revision{HypothesisID: "h1", Action: "commented"}
	out, err := svc.AddHypothesisRevision(ctx, rev)
	if err != nil || out.ID == "" || out.CreatedAt.IsZero() {
		t.Fatalf("add revision: %+v err=%v", out, err)
	}

	svc = newSvcWithHypotheses(&errHypotheses{})
	failing := &domain.Revision{HypothesisID: "h1", Action: "commented"}
	if _, err := svc.AddHypothesisRevision(ctx, failing); !errors.Is(err, errBoom) {
		t.Fatalf("add revision error not propagated: %v", err)
	}
}

// TestListHypothesisRevisionsAndEvidence covers the two listing pass-throughs.
func TestListHypothesisRevisionsAndEvidence(t *testing.T) {
	ctx := context.Background()
	svc := newSvcWithHypotheses(&stubHypotheses{})

	if _, err := svc.ListHypothesisRevisions(ctx, "h1"); err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if _, err := svc.ListHypothesisEvidence(ctx, "h1"); err != nil {
		t.Fatalf("list evidence: %v", err)
	}
}

// errHypotheses is a HypothesisRepository whose write paths always fail, so the
// service-level error-propagation branches can be exercised without a database.
type errHypotheses struct{ stubHypotheses }

func (errHypotheses) Create(_ context.Context, _ *domain.Hypothesis, _ *domain.Revision) error {
	return errBoom
}
func (errHypotheses) AddRevision(_ context.Context, _ *domain.Revision) error { return errBoom }
