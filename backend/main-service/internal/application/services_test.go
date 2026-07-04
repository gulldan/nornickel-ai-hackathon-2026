package application

import (
	"context"
	"errors"
	"testing"

	"github.com/example/main-service/internal/domain"
	"github.com/example/main-service/internal/platform/dbclient"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

// newHypService wires a HypothesisService over the given fakes (answerer may be nil
// when the flow under test never calls the LLM).
func newHypService(cat domain.HypothesisCatalog, a domain.Answerer) *HypothesisService {
	return NewHypothesisService(cat, a, nil, nil, nil, nil, nil)
}

// CreateKPI forces the owner id from the caller, never the request body.
func TestHypothesisService_CreateKPI_ForcesOwner(t *testing.T) {
	cat := newCatalog()
	svc := newHypService(cat, nil)
	k, err := svc.CreateKPI(context.Background(), "alice", &dbv1.CreateKPIRequest{Title: "t", OwnerId: "mallory"})
	if err != nil {
		t.Fatalf("CreateKPI: %v", err)
	}
	if k.GetOwnerId() != "alice" {
		t.Fatalf("owner = %q, want alice", k.GetOwnerId())
	}
}

// GetKPI returns ErrForbidden for a KPI owned by someone else and the row for the owner.
func TestHypothesisService_GetKPI_OwnerScoping(t *testing.T) {
	cat := newCatalog()
	cat.kpis["k1"] = &commonv1.KPI{Id: "k1", OwnerId: "alice"}
	svc := newHypService(cat, nil)

	if _, err := svc.GetKPI(context.Background(), "bob", "k1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign owner must be forbidden, got %v", err)
	}
	got, err := svc.GetKPI(context.Background(), "alice", "k1")
	if err != nil || got.GetId() != "k1" {
		t.Fatalf("owner read failed: %v / %v", got, err)
	}
	if _, err := svc.GetKPI(context.Background(), "alice", "missing"); !errors.Is(err, dbclient.ErrNotFound) {
		t.Fatalf("missing KPI must surface not-found, got %v", err)
	}
}

// UpdateKPI rejects a foreign KPI and persists an owned one with a forced owner.
func TestHypothesisService_UpdateKPI(t *testing.T) {
	cat := newCatalog()
	cat.kpis["k1"] = &commonv1.KPI{Id: "k1", OwnerId: "alice"}
	svc := newHypService(cat, nil)

	if err := svc.UpdateKPI(context.Background(), "bob", &commonv1.KPI{Id: "k1"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign update must be forbidden, got %v", err)
	}
	if err := svc.UpdateKPI(context.Background(), "alice", &commonv1.KPI{Id: "k1", OwnerId: "x"}); err != nil {
		t.Fatalf("owner update: %v", err)
	}
	if cat.kpis["k1"].GetOwnerId() != "alice" {
		t.Fatalf("owner must be forced to alice, got %q", cat.kpis["k1"].GetOwnerId())
	}
}

// ListKPIs returns only the owner's rows.
func TestHypothesisService_ListKPIs(t *testing.T) {
	cat := newCatalog()
	cat.kpis["a"] = &commonv1.KPI{Id: "a", OwnerId: "alice"}
	cat.kpis["b"] = &commonv1.KPI{Id: "b", OwnerId: "bob"}
	svc := newHypService(cat, nil)
	ks, err := svc.ListKPIs(context.Background(), "alice")
	if err != nil {
		t.Fatalf("ListKPIs: %v", err)
	}
	if len(ks) != 1 || ks[0].GetId() != "a" {
		t.Fatalf("want only alice's KPI, got %v", ks)
	}
}

// Cluster CRUD enforces owner scoping the same way KPIs do.
func TestHypothesisService_Clusters(t *testing.T) {
	cat := newCatalog()
	svc := newHypService(cat, nil)
	c, err := svc.CreateCluster(context.Background(), "alice", &dbv1.CreateClusterRequest{Label: "L"})
	if err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if c.GetOwnerId() != "alice" {
		t.Fatalf("owner = %q", c.GetOwnerId())
	}
	if _, ferr := svc.GetCluster(context.Background(), "bob", c.GetId()); !errors.Is(ferr, ErrForbidden) {
		t.Fatalf("foreign cluster must be forbidden, got %v", ferr)
	}
	got, err := svc.GetCluster(context.Background(), "alice", c.GetId())
	if err != nil || got.GetId() != c.GetId() {
		t.Fatalf("owner cluster read: %v / %v", got, err)
	}
	cs, err := svc.ListClusters(context.Background(), "alice")
	if err != nil || len(cs) != 1 {
		t.Fatalf("ListClusters: %v / %d", err, len(cs))
	}
	if err := svc.UpdateCluster(context.Background(), "bob", &commonv1.Cluster{Id: c.GetId()}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign update must be forbidden, got %v", err)
	}
	if err := svc.UpdateCluster(context.Background(), "alice", &commonv1.Cluster{Id: c.GetId()}); err != nil {
		t.Fatalf("owner update: %v", err)
	}
	if err := svc.DeleteAllClusters(context.Background(), "alice"); err != nil {
		t.Fatalf("DeleteAllClusters: %v", err)
	}
	if cat.deleteClustersCount != 1 {
		t.Fatalf("delete should hit catalog once, got %d", cat.deleteClustersCount)
	}
}

// CreateHypothesis forces the owner and writes a composite via applyRanking.
func TestHypothesisService_CreateHypothesis_RanksAndScopes(t *testing.T) {
	cat := newCatalog()
	svc := newHypService(cat, nil)
	h, err := svc.CreateHypothesis(context.Background(), "alice", &dbv1.CreateHypothesisRequest{
		Hypothesis: &commonv1.Hypothesis{Title: "T", Statement: "S", ValueScore: f64(0.8)},
	})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if h.GetOwnerId() != "alice" {
		t.Fatalf("owner = %q", h.GetOwnerId())
	}
	if h.CompositeScore == nil {
		t.Fatal("applyRanking must set composite_score")
	}
}

// GetHypothesis / List / revisions / evidence enforce owner scoping.
func TestHypothesisService_HypothesisReads(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice"})
	cat.revisions["h1"] = []*commonv1.HypothesisRevision{{Id: "r"}}
	cat.evidence["h1"] = []*commonv1.HypothesisEvidence{{ChunkId: "c"}}
	svc := newHypService(cat, nil)

	if _, err := svc.GetHypothesis(context.Background(), "bob", false, "h1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign read must be forbidden, got %v", err)
	}
	if _, err := svc.GetHypothesis(context.Background(), "bob", true, "h1"); err != nil {
		t.Fatalf("privileged read must pass, got %v", err)
	}
	if _, err := svc.ListRevisions(context.Background(), "bob", false, "h1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign revisions must be forbidden, got %v", err)
	}
	revs, err := svc.ListRevisions(context.Background(), "alice", false, "h1")
	if err != nil || len(revs) != 1 {
		t.Fatalf("ListRevisions: %v / %d", err, len(revs))
	}
	ev, err := svc.ListEvidence(context.Background(), "alice", false, "h1")
	if err != nil || len(ev) != 1 {
		t.Fatalf("ListEvidence: %v / %d", err, len(ev))
	}
	if _, ferr := svc.ListEvidence(context.Background(), "bob", false, "h1"); !errors.Is(ferr, ErrForbidden) {
		t.Fatalf("foreign evidence must be forbidden, got %v", ferr)
	}
	hs, err := svc.ListHypotheses(context.Background(), "alice", false, &dbv1.ListHypothesesRequest{})
	if err != nil || len(hs) != 1 {
		t.Fatalf("ListHypotheses: %v / %d", err, len(hs))
	}
	if hs, err = svc.ListHypotheses(context.Background(), "bob", true, &dbv1.ListHypothesesRequest{}); err != nil || len(hs) != 1 {
		t.Fatalf("privileged ListHypotheses: %v / %d", err, len(hs))
	}
}

// UpdateHypothesis re-ranks and rejects a foreign row; AddRevision is owner-scoped.
func TestHypothesisService_UpdateAndAddRevision(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice"})
	svc := newHypService(cat, nil)

	req := &dbv1.UpdateHypothesisRequest{Hypothesis: &commonv1.Hypothesis{Id: "h1", ValueScore: f64(0.9)}}
	if err := svc.UpdateHypothesis(context.Background(), "alice", req); err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}
	if cat.hypotheses["h1"].CompositeScore == nil {
		t.Fatal("update must recompute composite_score")
	}
	foreign := &dbv1.UpdateHypothesisRequest{Hypothesis: &commonv1.Hypothesis{Id: "h1"}}
	if err := svc.UpdateHypothesis(context.Background(), "bob", foreign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign update must be forbidden, got %v", err)
	}

	rev := &commonv1.HypothesisRevision{HypothesisId: "h1", Action: "edited"}
	if _, err := svc.AddRevision(context.Background(), "alice", rev); err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	if _, err := svc.AddRevision(context.Background(), "bob", rev); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign revision must be forbidden, got %v", err)
	}
}

// Scoring weights round-trip through the store and fall back to defaults.
func TestHypothesisService_ScoringWeights(t *testing.T) {
	cat := newCatalog()
	// No store ⇒ defaults, Set is a no-op.
	noStore := newHypService(cat, nil)
	if got := noStore.GetScoringWeights(context.Background(), "alice"); got != DefaultWeights() {
		t.Fatalf("no store must yield defaults, got %+v", got)
	}
	if err := noStore.SetScoringWeights(context.Background(), "alice", DefaultWeights()); err != nil {
		t.Fatalf("no-store Set must be a no-op, got %v", err)
	}

	store := newWeights()
	svc := NewHypothesisService(cat, nil, store, nil, nil, nil, nil)
	custom := ScoringWeights{KPIFit: 0.5, Evidence: 0.5}
	if err := svc.SetScoringWeights(context.Background(), "alice", custom); err != nil {
		t.Fatalf("SetScoringWeights: %v", err)
	}
	if got := svc.GetScoringWeights(context.Background(), "alice"); got != custom {
		t.Fatalf("stored weights = %+v, want %+v", got, custom)
	}
	// A store error falls back to defaults rather than failing.
	store.getErr = errors.New("boom")
	if got := svc.GetScoringWeights(context.Background(), "alice"); got != DefaultWeights() {
		t.Fatalf("store error must fall back to defaults, got %+v", got)
	}
}

// Runtime settings round-trip through the store and fall back to defaults.
func TestHypothesisService_RuntimeSettings(t *testing.T) {
	cat := newCatalog()
	noStore := newHypService(cat, nil)
	if got := noStore.GetRuntimeSettings(context.Background(), "alice"); got != DefaultHypothesisRuntimeSettings() {
		t.Fatalf("no store must yield defaults, got %+v", got)
	}
	if err := noStore.SetRuntimeSettings(context.Background(), "alice", DefaultHypothesisRuntimeSettings()); err != nil {
		t.Fatalf("no-store Set must be a no-op, got %v", err)
	}

	store := newRuntimeSettings()
	svc := NewHypothesisService(cat, nil, nil, store, nil, nil, nil)
	custom := HypothesisRuntimeSettings{
		DefaultGenerateCount:   6,
		ClusterGenerateCount:   4,
		DirectionGenerateCount: 4,
		GenerationTimeoutSec:   120,
		ReadyTRLMin:            5,
		ReadyScoreMin:          65,
		RiskScoreMin:           75,
		GraphDirectionLimit:    8,
	}
	if err := svc.SetRuntimeSettings(context.Background(), "alice", custom); err != nil {
		t.Fatalf("SetRuntimeSettings: %v", err)
	}
	if got := svc.GetRuntimeSettings(context.Background(), "alice"); got != custom {
		t.Fatalf("stored settings = %+v, want %+v", got, custom)
	}
	store.getErr = errors.New("boom")
	if got := svc.GetRuntimeSettings(context.Background(), "alice"); got != DefaultHypothesisRuntimeSettings() {
		t.Fatalf("store error must fall back to defaults, got %+v", got)
	}
}

// ---- ChatService ----

// CreateChat / ListChats / GetChat enforce owner scoping.
func TestChatService_CRUDAndScoping(t *testing.T) {
	chats := newChats()
	svc := NewChatService(chats, nil)
	c, err := svc.CreateChat(context.Background(), "alice", "hi", "search")
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if _, ferr := svc.GetChat(context.Background(), "bob", c.GetId()); !errors.Is(ferr, ErrForbidden) {
		t.Fatalf("foreign chat must be forbidden, got %v", ferr)
	}
	got, err := svc.GetChat(context.Background(), "alice", c.GetId())
	if err != nil || got.GetId() != c.GetId() {
		t.Fatalf("owner chat read: %v / %v", got, err)
	}
	list, total, err := svc.ListChats(context.Background(), "alice", 0, 0, false)
	if err != nil || len(list) != 1 || total != 1 {
		t.Fatalf("ListChats: %v / %d / %d", err, len(list), total)
	}
	if _, err := svc.ListMessages(context.Background(), "bob", c.GetId()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign messages must be forbidden, got %v", err)
	}
	if _, err := svc.ListMessages(context.Background(), "alice", c.GetId()); err != nil {
		t.Fatalf("owner messages: %v", err)
	}
}

// ListModels passes through to the catalog.
func TestChatService_ListModels(t *testing.T) {
	chats := newChats()
	chats.models = []*commonv1.Model{{Id: "m"}}
	svc := NewChatService(chats, nil)
	ms, err := svc.ListModels(context.Background())
	if err != nil || len(ms) != 1 {
		t.Fatalf("ListModels: %v / %d", err, len(ms))
	}
}

// Ask persists the user turn, calls the LLM and stores the grounded answer.
func TestChatService_Ask_Success(t *testing.T) {
	chats := newChats()
	chats.chats["c1"] = &commonv1.Chat{Id: "c1", OwnerId: "alice"}
	ans := newAnswerer(reply("grounded answer", src("d1", "snippet")))
	svc := NewChatService(chats, ans)

	msg, err := svc.Ask(context.Background(), domain.AskCommand{OwnerID: "alice", ChatID: "c1", Content: "q?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if msg.GetRole() != "assistant" || msg.GetContent() != "grounded answer" {
		t.Fatalf("unexpected assistant message: %+v", msg)
	}
	if chats.addMsgCalls != 2 {
		t.Fatalf("Ask must persist user + assistant turns, got %d", chats.addMsgCalls)
	}
	turns := chats.messages["c1"]
	if turns[0].GetMeta() != "" {
		t.Fatalf("user turn meta = %q, want empty", turns[0].GetMeta())
	}
	if got := msg.GetMeta(); got != `{"model":"fake-model","cached":false}` {
		t.Fatalf("assistant meta = %q, want the provenance envelope", got)
	}
}

// Ask refuses to write into a chat the caller does not own.
func TestChatService_Ask_Forbidden(t *testing.T) {
	chats := newChats()
	chats.chats["c1"] = &commonv1.Chat{Id: "c1", OwnerId: "alice"}
	svc := NewChatService(chats, newAnswerer(reply("x")))
	_, err := svc.Ask(context.Background(), domain.AskCommand{OwnerID: "bob", ChatID: "c1", Content: "q"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign Ask must be forbidden, got %v", err)
	}
	if chats.addMsgCalls != 0 {
		t.Fatal("forbidden Ask must not persist any message")
	}
}

// Ask surfaces an LLM error after persisting the user turn.
func TestChatService_Ask_LLMError(t *testing.T) {
	chats := newChats()
	chats.chats["c1"] = &commonv1.Chat{Id: "c1", OwnerId: "alice"}
	svc := NewChatService(chats, newAnswerer(failReply(errors.New("rag down"))))
	if _, err := svc.Ask(context.Background(), domain.AskCommand{OwnerID: "alice", ChatID: "c1", Content: "q"}); err == nil {
		t.Fatal("LLM error must surface")
	}
}

// ---- AdminService ----

// ListUsers passes through to the user catalog and surfaces errors.
func TestAdminService_ListUsers(t *testing.T) {
	users := &fakeUsers{users: []*commonv1.User{{Id: "u"}}}
	svc := NewAdminService(users)
	got, err := svc.ListUsers(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("ListUsers: %v / %d", err, len(got))
	}
	users.err = errors.New("db down")
	if _, err := svc.ListUsers(context.Background()); err == nil {
		t.Fatal("error must surface")
	}
}
