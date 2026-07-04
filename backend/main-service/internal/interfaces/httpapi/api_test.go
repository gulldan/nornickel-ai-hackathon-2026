package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// Every business endpoint rejects an unauthenticated request at the middleware.
func TestAuth_MissingTokenRejected(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodGet, "/documents", "", "")
	if resp.code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", resp.code)
	}
	_ = resp.body
}

// ---- Documents ----

// Uploading a multipart file creates a document and returns 201.
func TestUploadDocument(t *testing.T) {
	h := newHarness(t)
	r := h.doMultipart(t, h.token(t, "alice"), "file", "note.txt", "hello world")
	if r.code != http.StatusCreated {
		t.Fatalf("upload = %d, want 201: %s", r.code, r.body)
	}
}

// An upload with no file field is a 400.
func TestUploadDocument_MissingFile(t *testing.T) {
	h := newHarness(t)
	r := h.doMultipart(t, h.token(t, "alice"), "", "", "")
	if r.code != http.StatusBadRequest {
		t.Fatalf("missing file = %d, want 400", r.code)
	}
}

// listDocuments returns the owner's documents.
func TestListDocuments(t *testing.T) {
	h := newHarness(t)
	h.docs.docs["d1"] = &commonv1.Document{Id: "d1", OwnerId: "alice", Filename: "a.pdf"}
	h.docs.docs["d2"] = &commonv1.Document{Id: "d2", OwnerId: "bob"}
	resp := h.do(t, http.MethodGet, "/documents", h.token(t, "alice"), "")
	if resp.code != http.StatusOK {
		t.Fatalf("status = %d", resp.code)
	}
	body := resp.body
	if !strings.Contains(body, "a.pdf") || strings.Contains(body, "\"d2\"") {
		t.Fatalf("list should be owner-scoped, got %s", body)
	}
}

// getDocument returns 404 for a foreign document and the row for the owner.
func TestGetDocument(t *testing.T) {
	h := newHarness(t)
	h.docs.docs["d1"] = &commonv1.Document{Id: "d1", OwnerId: "alice", Filename: "a.pdf"}
	if resp := h.do(t, http.MethodGet, "/documents/d1", h.token(t, "bob"), ""); resp.code != http.StatusNotFound {
		t.Fatalf("foreign doc = %d, want 404", resp.code)
	}
	resp := h.do(t, http.MethodGet, "/documents/d1", h.token(t, "alice"), "")
	if resp.code != http.StatusOK {
		t.Fatalf("owner doc = %d, want 200", resp.code)
	}
	_ = resp.body
}

// documentChunks returns the preview payload; an operator may read any doc.
func TestDocumentChunks(t *testing.T) {
	h := newHarness(t)
	h.docs.docs["d1"] = &commonv1.Document{Id: "d1", OwnerId: "alice"}
	// chunks come from a separate ChunkReader; the harness wires an empty one, so
	// the payload is the document header with an empty chunk list.
	resp := h.do(t, http.MethodGet, "/documents/d1/chunks", h.token(t, "alice"), "")
	if resp.code != http.StatusOK {
		t.Fatalf("owner chunks = %d, want 200: %s", resp.code, resp.body)
	}
	_ = resp.body
	// A non-owner without privilege is 404.
	if r := h.do(t, http.MethodGet, "/documents/d1/chunks", h.token(t, "bob"), ""); r.code != http.StatusNotFound {
		t.Fatalf("foreign chunks = %d, want 404", r.code)
	}
	// An operator may read any doc.
	if r := h.do(t, http.MethodGet, "/documents/d1/chunks", h.token(t, "bob", "operator"), ""); r.code != http.StatusOK {
		t.Fatalf("operator chunks = %d, want 200", r.code)
	}
}

// documentContent streams the stored bytes with the right headers.
func TestDocumentContent(t *testing.T) {
	h := newHarness(t)
	h.docs.docs["d1"] = &commonv1.Document{
		Id: "d1", OwnerId: "alice", ObjectKey: "k", MimeType: "application/pdf", Filename: "a.pdf",
	}
	h.store.objects["k"] = []byte("PDFBYTES")
	resp := h.do(t, http.MethodGet, "/documents/d1/content", h.token(t, "alice"), "")
	if resp.code != http.StatusOK {
		t.Fatalf("content = %d, want 200", resp.code)
	}
	if ct := resp.header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type = %q", ct)
	}
	if body := resp.body; body != "PDFBYTES" {
		t.Fatalf("body = %q", body)
	}
	// The object vanished from the store: a stable 404, not a leaky 500.
	h.docs.docs["d2"] = &commonv1.Document{Id: "d2", OwnerId: "alice", ObjectKey: "missing"}
	r := h.do(t, http.MethodGet, "/documents/d2/content", h.token(t, "alice"), "")
	if r.code != http.StatusNotFound {
		t.Fatalf("missing object = %d, want 404: %s", r.code, r.body)
	}
	if !strings.Contains(r.body, "original file is not available") {
		t.Fatalf("missing object body = %q", r.body)
	}
}

// documentChunks surfaces the shared-corpus flag (any user may preview any doc).
func TestDocumentChunks_SharedCorpus(t *testing.T) {
	h := newHarness(t, withSharedCorpus(true))
	h.docs.docs["d1"] = &commonv1.Document{Id: "d1", OwnerId: "alice"}
	if r := h.do(t, http.MethodGet, "/documents/d1/chunks", h.token(t, "bob"), ""); r.code != http.StatusOK {
		t.Fatalf("shared corpus must let any user preview, got %d", r.code)
	}
}

// ---- Chats ----

// Chat create/list/get/messages round-trip and the post-message path calls the LLM.
func TestChats_Lifecycle(t *testing.T) {
	h := newHarness(t)
	h.answerer.push(&commonv1.RagResponse{Answer: "grounded", Model: "m"}, nil)
	tok := h.token(t, "alice")

	resp := h.do(t, http.MethodPost, "/chats", tok, `{"title":"hi"}`)
	if resp.code != http.StatusCreated {
		t.Fatalf("create chat = %d", resp.code)
	}
	_ = resp.body

	if r := h.do(t, http.MethodGet, "/chats", tok, ""); r.code != http.StatusOK {
		t.Fatalf("list chats = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodGet, "/chats/chat-hi", tok, ""); r.code != http.StatusOK {
		t.Fatalf("get chat = %d", r.code)
	} else {
		_ = r.body
	}
	if r := h.do(t, http.MethodGet, "/chats/chat-hi/messages", tok, ""); r.code != http.StatusOK {
		t.Fatalf("list messages = %d", r.code)
	} else {
		_ = r.body
	}
	r := h.do(t, http.MethodPost, "/chats/chat-hi/messages", tok, `{"content":"q?"}`)
	if r.code != http.StatusCreated {
		t.Fatalf("post message = %d: %s", r.code, r.body)
	}
	_ = r.body
}

// Posting an empty message body is a 400.
func TestChats_PostMessage_Empty(t *testing.T) {
	h := newHarness(t)
	h.chats.chats["c"] = &commonv1.Chat{Id: "c", OwnerId: "alice"}
	r := h.do(t, http.MethodPost, "/chats/c/messages", h.token(t, "alice"), `{"content":""}`)
	if r.code != http.StatusBadRequest {
		t.Fatalf("empty content = %d, want 400", r.code)
	}
	_ = r.body
}

// Malformed JSON on create is a 400.
func TestChats_CreateBadJSON(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodPost, "/chats", h.token(t, "alice"), `{not json`)
	if r.code != http.StatusBadRequest {
		t.Fatalf("bad json = %d, want 400", r.code)
	}
	_ = r.body
}

// listModels returns the catalogue.
func TestListModels(t *testing.T) {
	h := newHarness(t)
	h.chats.models = []*commonv1.Model{{Id: "m1", Name: "Model One"}}
	r := h.do(t, http.MethodGet, "/models", h.token(t, "alice"), "")
	if r.code != http.StatusOK {
		t.Fatalf("models = %d", r.code)
	}
	if !strings.Contains(r.body, "Model One") {
		t.Fatal("model name must appear")
	}
}

// ---- Upload sessions ----

// The browser-direct upload session flow: begin → part URLs → complete → abort.
func TestUploadSessions(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, "alice")
	beginBody := `{"filename":"big.bin","size":20971520,"mime_type":"application/octet-stream"}`
	resp := h.do(t, http.MethodPost, "/uploads", tok, beginBody)
	if resp.code != http.StatusCreated {
		t.Fatalf("begin = %d: %s", resp.code, resp.body)
	}
	body := resp.body
	id := extractJSONString(t, body, "upload_id")

	if r := h.do(t, http.MethodGet, "/uploads/"+id+"/parts?from=1&count=2", tok, ""); r.code != http.StatusOK {
		t.Fatalf("part urls = %d", r.code)
	} else {
		_ = r.body
	}
	// Complete with the right number of parts.
	complete := `{"parts":[{"part_number":1,"etag":"e1"},{"part_number":2,"etag":"e2"}]}`
	if r := h.do(t, http.MethodPost, "/uploads/"+id+"/complete", tok, complete); r.code != http.StatusCreated {
		t.Fatalf("complete = %d: %s", r.code, r.body)
	} else {
		_ = r.body
	}
}

// Begin rejects a missing filename / non-positive size.
func TestUploadSessions_BeginValidation(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodPost, "/uploads", h.token(t, "alice"), `{"filename":"","size":0}`)
	if r.code != http.StatusBadRequest {
		t.Fatalf("invalid begin = %d, want 400", r.code)
	}
	_ = r.body
}

// Abort on a missing session is idempotent (204).
func TestUploadSessions_Abort(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodDelete, "/uploads/missing", h.token(t, "alice"), "")
	if r.code != http.StatusNoContent {
		t.Fatalf("abort missing = %d, want 204", r.code)
	}
	_ = r.body
}

// ---- Admin panels ----

// adminUsers requires the admin role.
func TestAdminUsers_RoleGate(t *testing.T) {
	h := newHarness(t)
	h.users.users = []*commonv1.User{{Id: "u", Username: "admin"}}
	if r := h.do(t, http.MethodGet, "/admin/users", h.token(t, "alice"), ""); r.code != http.StatusForbidden {
		t.Fatalf("non-admin = %d, want 403", r.code)
	}
	r := h.do(t, http.MethodGet, "/admin/users", h.token(t, "root", "admin"), "")
	if r.code != http.StatusOK {
		t.Fatalf("admin = %d, want 200", r.code)
	}
	_ = r.body
}

// adminDocuments is open to admins and operators.
func TestAdminDocuments_RoleGate(t *testing.T) {
	h := newHarness(t)
	h.docs.docs["d"] = &commonv1.Document{Id: "d", OwnerId: "x"}
	if r := h.do(t, http.MethodGet, "/admin/documents", h.token(t, "alice"), ""); r.code != http.StatusForbidden {
		t.Fatalf("non-privileged = %d, want 403", r.code)
	}
	if r := h.do(t, http.MethodGet, "/admin/documents", h.token(t, "op", "operator"), ""); r.code != http.StatusOK {
		t.Fatalf("operator = %d, want 200", r.code)
	}
}

// Metrics: store (admin), read (admin/operator), trigger runs.
func TestMetrics(t *testing.T) {
	h := newHarness(t)
	admin := h.token(t, "root", "admin")

	// Post a scorecard.
	if r := h.do(t, http.MethodPost, "/admin/metrics", admin, `{"score":0.9}`); r.code != http.StatusOK {
		t.Fatalf("post metrics = %d: %s", r.code, r.body)
	}
	// Invalid JSON is rejected.
	if r := h.do(t, http.MethodPost, "/admin/metrics", admin, `not json`); r.code != http.StatusBadRequest {
		t.Fatalf("invalid scorecard = %d, want 400", r.code)
	}
	// Read it back.
	r := h.do(t, http.MethodGet, "/admin/metrics", admin, "")
	if r.code != http.StatusOK || !strings.Contains(r.body, "scorecard") {
		t.Fatalf("get metrics did not return the stored scorecard")
	}
	// Trigger an eval run and an ITC run.
	if r := h.do(t, http.MethodPost, "/admin/metrics/run", admin, ""); r.code != http.StatusAccepted {
		t.Fatalf("metrics run = %d, want 202", r.code)
	}
	if r := h.do(t, http.MethodPost, "/admin/itc/run", admin, ""); r.code != http.StatusAccepted {
		t.Fatalf("itc run = %d, want 202", r.code)
	}
}

// /system/activity reports the pipeline heartbeat to any signed-in role:
// present keys are parsed, absent workers/epochs read as nulls, and the
// itc/eval plain-text last_status backfills the state.
func TestSystemActivity(t *testing.T) {
	h := newHarness(t)
	h.metrics.data["rag:corpus_epoch:shared"] = "42"
	h.metrics.data["rag:clusters:last_epoch"] = "41"
	h.metrics.data["rag:worker:clusters:status"] = `{"state":"running","epoch":42,"progress_done":3,` +
		`"progress_total":10,"updated_at":"2026-07-01T00:00:00Z","last_error":"","last_run_seconds":12.5}`
	h.metrics.data["rag:itc:last_status"] = "exit=0 2026-07-01T00:00:00Z reason=cron"

	r := h.do(t, http.MethodGet, "/system/activity", h.token(t, "alice"), "")
	if r.code != http.StatusOK {
		t.Fatalf("activity = %d: %s", r.code, r.body)
	}
	var got struct {
		CorpusEpoch *int64 `json:"corpus_epoch"`
		Workers     []struct {
			Name           string   `json:"name"`
			State          *string  `json:"state"`
			Epoch          *int64   `json:"epoch"`
			ProgressDone   *int64   `json:"progress_done"`
			LastRunSeconds *float64 `json:"last_run_seconds"`
		} `json:"workers"`
		LastEpochs map[string]*int64 `json:"last_epochs"`
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatalf("decode: %v: %s", err, r.body)
	}
	if got.CorpusEpoch == nil || *got.CorpusEpoch != 42 {
		t.Fatalf("corpus_epoch = %v, want 42", got.CorpusEpoch)
	}
	if got.LastEpochs["clusters"] == nil || *got.LastEpochs["clusters"] != 41 || got.LastEpochs["raptor"] != nil {
		t.Fatalf("last_epochs = %v", got.LastEpochs)
	}
	byName := map[string]int{}
	for i, w := range got.Workers {
		byName[w.Name] = i
	}
	cl := got.Workers[byName["clusters"]]
	if cl.State == nil || *cl.State != "running" || cl.Epoch == nil || *cl.Epoch != 42 ||
		cl.ProgressDone == nil || *cl.ProgressDone != 3 {
		t.Fatalf("clusters worker = %+v", cl)
	}
	if cl.LastRunSeconds == nil || *cl.LastRunSeconds != 12.5 {
		t.Fatalf("clusters last_run_seconds = %v, want 12.5", cl.LastRunSeconds)
	}
	if disc := got.Workers[byName["discovery"]]; disc.State != nil || disc.Epoch != nil || disc.LastRunSeconds != nil {
		t.Fatalf("silent worker must read as nulls: %+v", disc)
	}
	if itc := got.Workers[byName["itc"]]; itc.State == nil || !strings.HasPrefix(*itc.State, "exit=0") {
		t.Fatalf("itc state fallback = %+v", itc)
	}
}

// getMetrics returns a null scorecard when none has been stored.
func TestGetMetrics_Empty(t *testing.T) {
	h := newHarness(t)
	r := h.do(t, http.MethodGet, "/admin/metrics", h.token(t, "root", "admin"), "")
	if body := r.body; !strings.Contains(body, "null") {
		t.Fatalf("empty metrics should be null, got %s", body)
	}
}
