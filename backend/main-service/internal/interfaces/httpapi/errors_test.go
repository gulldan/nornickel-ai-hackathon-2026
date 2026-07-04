package httpapi_test

import (
	"errors"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// A generic catalog error maps to 500 across the read handlers.
func TestFail_GenericError500(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("db unavailable")
	h.cat.listErr = boom
	h.cat.listKPIErr = boom
	h.cat.listClErr = boom
	h.docs.listErr = boom
	h.chats.listErr = boom
	tok := h.token(t, "alice")

	for _, path := range []string{"/documents", "/chats", "/hypotheses", "/kpis", "/clusters"} {
		r := h.do(t, http.MethodGet, path, tok, "")
		if r.code != http.StatusInternalServerError {
			t.Fatalf("%s on error = %d, want 500", path, r.code)
		}
		_ = r.body
	}
}

// A ResourceExhausted gRPC error from llm-service maps to 503 (busy).
func TestFail_ResourceExhausted503(t *testing.T) {
	h := newHarness(t)
	h.cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	h.answerer.push(nil, status.Error(codes.ResourceExhausted, "queue full"))
	r := h.do(t, http.MethodPost, "/hypotheses/h1/verify", h.token(t, "alice"), "")
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("ResourceExhausted = %d, want 503", r.code)
	}
	_ = r.body
}

// A not-found from the catalog maps to 404 across the by-id reads.
func TestFail_NotFound404(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	paths := []string{"/documents/missing", "/hypotheses/missing", "/kpis/missing", "/clusters/missing", "/chats/missing"}
	for _, path := range paths {
		r := h.do(t, http.MethodGet, path, tok, "")
		if r.code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, r.code)
		}
		_ = r.body
	}
}

// updateOwned surfaces a bad body (400) and a not-found target (404).
func TestUpdateOwned_Errors(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	// Bad JSON on a PUT.
	if r := h.do(t, http.MethodPut, "/kpis/x", tok, `{not json`); r.code != http.StatusBadRequest {
		t.Fatalf("bad PUT body = %d, want 400", r.code)
	}
	// Updating a missing KPI is 404 (ownership check fails on the missing row).
	if r := h.do(t, http.MethodPut, "/kpis/missing", tok, `{"title":"x"}`); r.code != http.StatusNotFound {
		t.Fatalf("update missing kpi = %d, want 404", r.code)
	}
}

// The job endpoints answer 503 when no job service is configured.
func TestJobs_Unconfigured503(t *testing.T) {
	h := newHarness(t, withoutJobs())
	tok := h.token(t, "alice")
	enqueue := h.do(t, http.MethodPost, "/hypothesis-jobs", tok, `{"kind":"verify","hypothesis_id":"h"}`)
	if enqueue.code != http.StatusServiceUnavailable {
		t.Fatalf("enqueue unconfigured = %d, want 503", enqueue.code)
	}
	if r := h.do(t, http.MethodGet, "/hypothesis-jobs", tok, ""); r.code != http.StatusServiceUnavailable {
		t.Fatalf("list unconfigured = %d, want 503", r.code)
	}
	if r := h.do(t, http.MethodGet, "/hypothesis-jobs/x", tok, ""); r.code != http.StatusServiceUnavailable {
		t.Fatalf("get unconfigured = %d, want 503", r.code)
	}
}

// replaceClusters answers 503 when the publishing store is unavailable.
func TestReplaceClusters_Unconfigured503(t *testing.T) {
	h := newHarness(t, withoutMetrics())
	r := h.do(t, http.MethodPost, "/clusters/replace", h.token(t, "alice"), `{"clusters":[{"label":"A"}]}`)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("replace without store = %d, want 503", r.code)
	}
	_ = r.body
}

// listClusters tolerates a nil publish-version store (no filtering, plain list).
func TestListClusters_NilMetricsStore(t *testing.T) {
	h := newHarness(t, withoutMetrics())
	h.cat.clusters["c1"] = &commonv1.Cluster{Id: "c1", OwnerId: "alice", Label: "L"}
	r := h.do(t, http.MethodGet, "/clusters", h.token(t, "alice"), "")
	if r.code != http.StatusOK {
		t.Fatalf("list with nil store = %d, want 200", r.code)
	}
	_ = r.body
}

// A metrics-store read error on listClusters surfaces as 500.
func TestListClusters_PublishVersionError(t *testing.T) {
	h := newHarness(t)
	h.metrics.err = errors.New("valkey down")
	r := h.do(t, http.MethodGet, "/clusters", h.token(t, "alice"), "")
	if r.code != http.StatusInternalServerError {
		t.Fatalf("publish-version error = %d, want 500", r.code)
	}
	_ = r.body
}

// A bad-JSON body on the create/decode handlers is a 400.
func TestDecodeResource_BadBody(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	for _, path := range []string{"/hypotheses", "/kpis", "/clusters", "/hypotheses/generate"} {
		r := h.do(t, http.MethodPost, path, tok, `{bad`)
		if r.code != http.StatusBadRequest {
			t.Fatalf("%s bad body = %d, want 400", path, r.code)
		}
		_ = r.body
	}
}

// Storing an upload abort error surfaces; metrics.Set failure on trigger is a 500.
func TestTriggerWorker_StoreError(t *testing.T) {
	h := newHarness(t)
	h.metrics.err = errors.New("valkey down")
	r := h.do(t, http.MethodPost, "/admin/metrics/run", h.token(t, "root", "admin"), "")
	if r.code != http.StatusInternalServerError {
		t.Fatalf("trigger with store error = %d, want 500", r.code)
	}
	_ = r.body
}

// postMetrics surfaces a store failure as 500.
func TestPostMetrics_StoreError(t *testing.T) {
	h := newHarness(t)
	h.metrics.err = errors.New("valkey down")
	r := h.do(t, http.MethodPost, "/admin/metrics", h.token(t, "root", "admin"), `{"x":1}`)
	if r.code != http.StatusInternalServerError {
		t.Fatalf("post metrics store error = %d, want 500", r.code)
	}
	_ = r.body
}
