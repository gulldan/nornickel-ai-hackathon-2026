package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/interfaces/httpapi"
	"github.com/example/main-service/internal/platform/dbclient"
	"github.com/example/main-service/internal/platform/jwt"
	"github.com/example/main-service/internal/platform/storage"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	llmv1 "github.com/example/main-service/internal/platform/genproto/llm/v1"
)

// jsonMarshal / jsonUnmarshal alias encoding/json so the fakes mirror the stores.
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// jwtSecret is the shared HS256 secret used to sign test tokens.
const jwtSecret = "test-secret-32-bytes-long-padding!"

// newManager builds a JWT manager matching the middleware/token signing in tests.
func newManager() *jwt.Manager { return jwt.NewManager(jwtSecret, "rag-platform", time.Hour) }

// harness bundles a wired API, its fakes and an authenticated test server.
type harness struct {
	api          *httpapi.API
	server       *httptest.Server
	mgr          *jwt.Manager
	cat          *fakeCatalog
	docs         *fakeDocs
	chats        *fakeChats
	users        *fakeUsers
	store        *fakeObjectStore
	pub          *fakePublisher
	answerer     *scriptedAnswerer
	metrics      *fakeMetrics
	weights      *fakeWeights
	settings     *fakeRuntimeSettings
	graph        *fakeGraph
	jobStore     *fakeJobStore
	sharedCorpus bool
	nilMetrics   bool
	nilJobs      bool
}

// option mutates the harness build before the services are wired.
type option func(*harness)

func withSharedCorpus(on bool) option { return func(h *harness) { h.sharedCorpus = on } }

// withoutMetrics builds the API with a nil metrics store (cluster publishing /
// ITC trigger unavailable).
func withoutMetrics() option { return func(h *harness) { h.nilMetrics = true } }

// withoutJobs builds the API with a nil hypothesis-job service.
func withoutJobs() option { return func(h *harness) { h.nilJobs = true } }

func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	h := &harness{
		cat:      newCatalog(),
		docs:     newDocs(),
		chats:    newChats(),
		users:    &fakeUsers{},
		store:    newObjectStore(),
		pub:      &fakePublisher{},
		answerer: newAnswerer(),
		metrics:  newMetrics(),
		weights:  newWeights(),
		settings: newRuntimeSettings(),
		graph:    newGraph(),
		jobStore: newJobStore(),
	}
	for _, opt := range opts {
		opt(h)
	}
	ingestion := application.NewIngestionService(h.store, h.docs, h.pub, &fakeChunks{})
	chat := application.NewChatService(h.chats, h.answerer)
	admin := application.NewAdminService(h.users)
	uploads := application.NewUploadSessionService(&fakeMultipart{}, h.store, newSessions(), ingestion, 0, 0)
	hyp := application.NewHypothesisService(h.cat, h.answerer, h.weights, h.settings, nil, h.graph, &fakeChunks{})
	jobs := application.NewHypothesisJobService(hyp, h.jobStore)
	if h.nilJobs {
		jobs = nil
	}
	var metrics *fakeMetrics
	if !h.nilMetrics {
		metrics = h.metrics
	}

	h.mgr = newManager()
	// A nil metrics store must be passed as a true nil interface, not a typed nil.
	if metrics == nil {
		h.api = httpapi.New(ingestion, chat, admin, uploads, hyp, jobs, nil, nil, nil, 50, h.sharedCorpus)
	} else {
		h.api = httpapi.New(ingestion, chat, admin, uploads, hyp, jobs, metrics, nil, nil, 50, h.sharedCorpus)
	}

	mux := http.NewServeMux()
	h.api.Routes(mux)
	h.server = httptest.NewServer(h.mgr.Middleware(mux))
	t.Cleanup(h.server.Close)
	return h
}

// token mints a signed Bearer token for a user with the given roles.
func (h *harness) token(t *testing.T, userID string, roles ...string) string {
	t.Helper()
	tok, _, err := h.mgr.Issue(userID, userID, roles)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

// resp is a fully-consumed HTTP response (body read and closed eagerly so test
// call sites never have to manage the body lifecycle).
type resp struct {
	code   int
	body   string
	header http.Header
}

// do runs an authenticated request and returns the consumed response.
func (h *harness) do(t *testing.T, method, path, token, body string) resp {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, h.server.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.run(t, req)
}

// run executes a prepared request and returns its fully-consumed response (body
// read and closed in this frame so call sites never manage the body).
func (h *harness) run(t *testing.T, req *http.Request) resp {
	t.Helper()
	out, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = out.Body.Close() }()
	b, rerr := io.ReadAll(out.Body)
	if rerr != nil {
		t.Fatalf("read body: %v", rerr)
	}
	return resp{code: out.StatusCode, body: string(b), header: out.Header}
}

// doMultipart posts a single-file multipart form (the "file" field) and returns
// the consumed response. A blank field name omits the file part entirely.
func (h *harness) doMultipart(t *testing.T, token, field, filename, content string) resp {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if field != "" {
		fw, err := mw.CreateFormFile(field, filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, werr := fw.Write([]byte(content)); werr != nil {
			t.Fatalf("write form file: %v", werr)
		}
	} else {
		_ = mw.WriteField("other", "x")
	}
	_ = mw.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.server.URL+"/documents", &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return h.run(t, req)
}

// extractJSONString decodes a top-level string field from a JSON body.
func extractJSONString(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("field %q missing or not a string in %s", key, body)
	}
	return s
}

// ---- fakes (httpapi-local implementations of the domain ports) ----

type fakeMetrics struct {
	mu   sync.Mutex
	data map[string]string
	err  error
}

func newMetrics() *fakeMetrics { return &fakeMetrics{data: map[string]string{}} }

func (f *fakeMetrics) Set(_ context.Context, key, val string, _ time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	f.data[key] = val
	f.mu.Unlock()
	return nil
}

func (f *fakeMetrics) Get(_ context.Context, key string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	f.mu.Lock()
	v, ok := f.data[key]
	f.mu.Unlock()
	return v, ok, nil
}

type fakeChunks struct {
	chunks map[string][]*llmv1.DocumentChunk
	err    error
}

func (f *fakeChunks) DocumentChunks(_ context.Context, _, documentID string) ([]*llmv1.DocumentChunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.chunks[documentID], nil
}

type fakePublisher struct {
	mu     sync.Mutex
	routes []string
}

func (f *fakePublisher) PublishProto(_ context.Context, _, routingKey string, _ proto.Message) error {
	f.mu.Lock()
	f.routes = append(f.routes, routingKey)
	f.mu.Unlock()
	return nil
}

type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newObjectStore() *fakeObjectStore { return &fakeObjectStore{objects: map[string][]byte{}} }

func (f *fakeObjectStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.objects[key] = data
	f.mu.Unlock()
	return nil
}

func (f *fakeObjectStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	data, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

type fakeMultipart struct{}

func (f *fakeMultipart) CreateMultipart(_ context.Context, _, _ string) (string, error) {
	return "s3-id", nil
}

func (f *fakeMultipart) PresignUploadPart(
	_ context.Context, _, _ string, _ int32, _ time.Duration,
) (string, error) {
	return "https://s3/part", nil
}

func (f *fakeMultipart) CompleteMultipart(_ context.Context, _, _ string, _ []storage.MultipartPart) error {
	return nil
}

func (f *fakeMultipart) AbortMultipart(_ context.Context, _, _ string) error { return nil }

type fakeSessions struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newSessions() *fakeSessions { return &fakeSessions{data: map[string][]byte{}} }

func (f *fakeSessions) SetJSON(_ context.Context, key string, v any, _ time.Duration) error {
	b, err := jsonMarshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.data[key] = b
	f.mu.Unlock()
	return nil
}

func (f *fakeSessions) GetJSON(_ context.Context, key string, dest any) (bool, error) {
	f.mu.Lock()
	b, ok := f.data[key]
	f.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, jsonUnmarshal(b, dest)
}

func (f *fakeSessions) Del(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.data, k)
	}
	return nil
}

type fakeUsers struct {
	users []*commonv1.User
	err   error
}

func (f *fakeUsers) ListUsers(_ context.Context) ([]*commonv1.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.users, nil
}

type fakeWeights struct {
	mu sync.Mutex
	w  map[string]*application.ScoringWeights
}

func newWeights() *fakeWeights { return &fakeWeights{w: map[string]*application.ScoringWeights{}} }

func (f *fakeWeights) Get(_ context.Context, ownerID string) (*application.ScoringWeights, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w[ownerID], nil
}

func (f *fakeWeights) Set(_ context.Context, ownerID string, w application.ScoringWeights) error {
	f.mu.Lock()
	f.w[ownerID] = &w
	f.mu.Unlock()
	return nil
}

type fakeRuntimeSettings struct {
	mu       sync.Mutex
	settings map[string]*application.HypothesisRuntimeSettings
}

func newRuntimeSettings() *fakeRuntimeSettings {
	return &fakeRuntimeSettings{settings: map[string]*application.HypothesisRuntimeSettings{}}
}

func (f *fakeRuntimeSettings) Get(_ context.Context, ownerID string) (*application.HypothesisRuntimeSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings[ownerID], nil
}

func (f *fakeRuntimeSettings) Set(_ context.Context, ownerID string, settings application.HypothesisRuntimeSettings) error {
	f.mu.Lock()
	f.settings[ownerID] = &settings
	f.mu.Unlock()
	return nil
}

type fakeGraph struct {
	edges map[string][]application.KGEdge
}

func newGraph() *fakeGraph { return &fakeGraph{edges: map[string][]application.KGEdge{}} }

func (f *fakeGraph) AddEdges(_ context.Context, ownerID string, edges []application.KGEdge) error {
	f.edges[ownerID] = append(f.edges[ownerID], edges...)
	return nil
}

func (f *fakeGraph) Edges(_ context.Context, ownerID string) ([]application.KGEdge, error) {
	return f.edges[ownerID], nil
}

type fakeJobStore struct {
	mu    sync.Mutex
	json  map[string][]byte
	lists map[string][]string
	nx    map[string]bool
	// claimGate, when non-nil, blocks the SetNX claim until it is closed, so a
	// test can keep the detached job runner parked while it renders the enqueue
	// response (the runner and the handler share the returned *HypothesisJob).
	claimGate chan struct{}
}

func newJobStore() *fakeJobStore {
	return &fakeJobStore{json: map[string][]byte{}, lists: map[string][]string{}, nx: map[string]bool{}}
}

func (f *fakeJobStore) SetJSON(_ context.Context, key string, v any, _ time.Duration) error {
	b, err := jsonMarshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.json[key] = b
	f.mu.Unlock()
	return nil
}

func (f *fakeJobStore) GetJSON(_ context.Context, key string, dest any) (bool, error) {
	f.mu.Lock()
	b, ok := f.json[key]
	f.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, jsonUnmarshal(b, dest)
}

func (f *fakeJobStore) SetNX(_ context.Context, key, _ string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	gate := f.claimGate
	f.mu.Unlock()
	if gate != nil {
		<-gate // park the detached runner until the test releases it
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nx[key] {
		return false, nil
	}
	f.nx[key] = true
	return true, nil
}

func (f *fakeJobStore) LPush(_ context.Context, key string, values ...string) error {
	f.mu.Lock()
	f.lists[key] = append(append([]string{}, values...), f.lists[key]...)
	f.mu.Unlock()
	return nil
}

func (f *fakeJobStore) LTrim(_ context.Context, _ string, _, _ int64) error { return nil }

func (f *fakeJobStore) LRange(_ context.Context, key string, _, _ int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.lists[key]...), nil
}

// scriptedAnswerer returns queued replies in order, repeating the last one.
type scriptedAnswerer struct {
	mu      sync.Mutex
	replies []*commonv1.RagResponse
	errs    []error
	calls   int
}

func newAnswerer() *scriptedAnswerer { return &scriptedAnswerer{} }

func (s *scriptedAnswerer) push(resp *commonv1.RagResponse, err error) {
	s.replies = append(s.replies, resp)
	s.errs = append(s.errs, err)
}

func (s *scriptedAnswerer) Answer(_ context.Context, _ *commonv1.RagRequest) (*commonv1.RagResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.calls
	s.calls++
	if len(s.replies) == 0 {
		return &commonv1.RagResponse{Answer: "", Model: "fake"}, nil
	}
	if idx >= len(s.replies) {
		idx = len(s.replies) - 1
	}
	return s.replies[idx], s.errs[idx]
}

// fakeCatalog is an in-memory HypothesisCatalog for the httpapi layer.
type fakeCatalog struct {
	mu         sync.Mutex
	kpis       map[string]*commonv1.KPI
	clusters   map[string]*commonv1.Cluster
	hypotheses map[string]*commonv1.Hypothesis
	revisions  map[string][]*commonv1.HypothesisRevision
	evidence   map[string][]*commonv1.HypothesisEvidence
	listErr    error
	listKPIErr error
	listClErr  error
	kpiDocs    map[string][]*dbv1.KpiDocumentLink
}

func newCatalog() *fakeCatalog {
	return &fakeCatalog{
		kpis:       map[string]*commonv1.KPI{},
		clusters:   map[string]*commonv1.Cluster{},
		hypotheses: map[string]*commonv1.Hypothesis{},
		revisions:  map[string][]*commonv1.HypothesisRevision{},
		evidence:   map[string][]*commonv1.HypothesisEvidence{},
	}
}

func (c *fakeCatalog) CreateKPI(_ context.Context, req *dbv1.CreateKPIRequest) (*commonv1.KPI, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := &commonv1.KPI{Id: "kpi-" + req.GetTitle(), OwnerId: req.GetOwnerId(), Title: req.GetTitle(), Metric: req.GetMetric()}
	c.kpis[k.GetId()] = k
	return k, nil
}

func (c *fakeCatalog) GetKPI(_ context.Context, id string) (*commonv1.KPI, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.kpis[id]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return k, nil
}

func (c *fakeCatalog) ListKPIs(_ context.Context, ownerID string) ([]*commonv1.KPI, error) {
	if c.listKPIErr != nil {
		return nil, c.listKPIErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*commonv1.KPI, 0, len(c.kpis))
	for _, k := range c.kpis {
		if k.GetOwnerId() == ownerID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (c *fakeCatalog) UpdateKPI(_ context.Context, kpi *commonv1.KPI) error {
	c.mu.Lock()
	c.kpis[kpi.GetId()] = kpi
	c.mu.Unlock()
	return nil
}

func (c *fakeCatalog) DeleteKPI(_ context.Context, id string) error {
	delete(c.kpis, id)
	return nil
}

func (c *fakeCatalog) AttachKPIDocument(_ context.Context, kpiID, documentID, role string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kpiDocs == nil {
		c.kpiDocs = map[string][]*dbv1.KpiDocumentLink{}
	}
	c.kpiDocs[kpiID] = append(c.kpiDocs[kpiID], &dbv1.KpiDocumentLink{
		Document: &commonv1.Document{Id: documentID}, Role: role,
	})
	return nil
}

func (c *fakeCatalog) ListKPIDocuments(_ context.Context, kpiID string) ([]*dbv1.KpiDocumentLink, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kpiDocs[kpiID], nil
}

func (c *fakeCatalog) ListDocuments(_ context.Context, _ string) ([]*commonv1.Document, error) {
	return nil, nil
}

func (c *fakeCatalog) DetachKPIDocument(_ context.Context, kpiID, documentID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.kpiDocs[kpiID][:0]
	for _, l := range c.kpiDocs[kpiID] {
		if l.GetDocument().GetId() != documentID {
			kept = append(kept, l)
		}
	}
	c.kpiDocs[kpiID] = kept
	return nil
}

func (c *fakeCatalog) CreateCluster(_ context.Context, req *dbv1.CreateClusterRequest) (*commonv1.Cluster, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := "cl-" + req.GetLabel()
	if _, dup := c.clusters[id]; dup {
		id += "-" + req.GetParams()
	}
	cl := &commonv1.Cluster{Id: id, OwnerId: req.GetOwnerId(), Label: req.GetLabel(), Params: req.GetParams()}
	c.clusters[cl.GetId()] = cl
	return cl, nil
}

func (c *fakeCatalog) GetCluster(_ context.Context, id string) (*commonv1.Cluster, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.clusters[id]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return cl, nil
}

func (c *fakeCatalog) ListClusters(_ context.Context, ownerID string) ([]*commonv1.Cluster, error) {
	if c.listClErr != nil {
		return nil, c.listClErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*commonv1.Cluster, 0, len(c.clusters))
	for _, cl := range c.clusters {
		if cl.GetOwnerId() == ownerID {
			out = append(out, cl)
		}
	}
	return out, nil
}

func (c *fakeCatalog) UpdateCluster(_ context.Context, cluster *commonv1.Cluster) error {
	c.mu.Lock()
	c.clusters[cluster.GetId()] = cluster
	c.mu.Unlock()
	return nil
}

func (c *fakeCatalog) DeleteClusters(_ context.Context, ownerID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, cl := range c.clusters {
		if cl.GetOwnerId() == ownerID {
			delete(c.clusters, id)
		}
	}
	return nil
}

func (c *fakeCatalog) CreateHypothesis(
	_ context.Context, req *dbv1.CreateHypothesisRequest,
) (*commonv1.Hypothesis, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := req.GetHypothesis()
	if h.GetId() == "" {
		h.Id = "hyp-" + h.GetTitle()
	}
	c.hypotheses[h.GetId()] = h
	if len(h.GetEvidence()) > 0 {
		c.evidence[h.GetId()] = h.GetEvidence()
	}
	return h, nil
}

func (c *fakeCatalog) GetHypothesis(_ context.Context, id string) (*commonv1.Hypothesis, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.hypotheses[id]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return h, nil
}

func (c *fakeCatalog) ListHypotheses(
	_ context.Context, req *dbv1.ListHypothesesRequest,
) ([]*commonv1.Hypothesis, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*commonv1.Hypothesis, 0, len(c.hypotheses))
	for _, h := range c.hypotheses {
		if req.GetOwnerId() == "" || h.GetOwnerId() == req.GetOwnerId() {
			out = append(out, h)
		}
	}
	return out, nil
}

func (c *fakeCatalog) UpdateHypothesis(_ context.Context, req *dbv1.UpdateHypothesisRequest) error {
	c.mu.Lock()
	c.hypotheses[req.GetHypothesis().GetId()] = req.GetHypothesis()
	c.mu.Unlock()
	return nil
}

func (c *fakeCatalog) AddHypothesisRevision(
	_ context.Context, rev *commonv1.HypothesisRevision,
) (*commonv1.HypothesisRevision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rev.Id = "rev"
	c.revisions[rev.GetHypothesisId()] = append(c.revisions[rev.GetHypothesisId()], rev)
	return rev, nil
}

func (c *fakeCatalog) ListHypothesisRevisions(
	_ context.Context, hypothesisID string,
) ([]*commonv1.HypothesisRevision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revisions[hypothesisID], nil
}

func (c *fakeCatalog) ListHypothesisEvidence(
	_ context.Context, hypothesisID string,
) ([]*commonv1.HypothesisEvidence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evidence[hypothesisID], nil
}

// putHypothesis seeds a hypothesis directly.
func (c *fakeCatalog) putHypothesis(h *commonv1.Hypothesis) {
	c.mu.Lock()
	c.hypotheses[h.GetId()] = h
	c.mu.Unlock()
}

type fakeDocs struct {
	mu      sync.Mutex
	docs    map[string]*commonv1.Document
	listErr error
}

func newDocs() *fakeDocs { return &fakeDocs{docs: map[string]*commonv1.Document{}} }

func (f *fakeDocs) CreateDocument(_ context.Context, req *dbv1.CreateDocumentRequest) (*commonv1.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := &commonv1.Document{
		Id: "doc-" + req.GetFilename(), OwnerId: req.GetOwnerId(), Filename: req.GetFilename(),
		MimeType: req.GetMimeType(), Size: req.GetSize(), ObjectKey: req.GetObjectKey(),
	}
	f.docs[d.GetId()] = d
	return d, nil
}

func (f *fakeDocs) GetDocument(_ context.Context, id string) (*commonv1.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[id]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return d, nil
}

func (f *fakeDocs) FindDocumentByHash(_ context.Context, _, _ string) (*commonv1.Document, error) {
	return nil, dbclient.ErrNotFound
}

func (f *fakeDocs) ListDocuments(_ context.Context, ownerID string) ([]*commonv1.Document, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*commonv1.Document, 0, len(f.docs))
	for _, d := range f.docs {
		if ownerID == "" || d.GetOwnerId() == ownerID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDocs) UpdateDocumentStatus(_ context.Context, id, status, _ string, _ *int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.docs[id]; ok {
		d.Status = status
	}
	return nil
}

type fakeChats struct {
	mu       sync.Mutex
	chats    map[string]*commonv1.Chat
	messages map[string][]*commonv1.Message
	models   []*commonv1.Model
	listErr  error
}

func newChats() *fakeChats {
	return &fakeChats{chats: map[string]*commonv1.Chat{}, messages: map[string][]*commonv1.Message{}}
}

func (f *fakeChats) CreateChat(_ context.Context, ownerID, title, source string) (*commonv1.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &commonv1.Chat{Id: "chat-" + title, OwnerId: ownerID, Title: title, Source: source}
	f.chats[c.GetId()] = c
	return c, nil
}

func (f *fakeChats) GetChat(_ context.Context, id string) (*commonv1.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.chats[id]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return c, nil
}

func (f *fakeChats) ListChats(
	_ context.Context, ownerID string, _, _ int, allOwners bool,
) ([]*commonv1.Chat, int64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*commonv1.Chat, 0, len(f.chats))
	for _, c := range f.chats {
		if allOwners || c.GetOwnerId() == ownerID {
			out = append(out, c)
		}
	}
	return out, int64(len(out)), nil
}

func (f *fakeChats) AddMessage(
	_ context.Context, chatID, role, content string, sources []*commonv1.Source, meta string,
) (*commonv1.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &commonv1.Message{Id: "msg", ChatId: chatID, Role: role, Content: content, Sources: sources, Meta: meta}
	f.messages[chatID] = append(f.messages[chatID], m)
	return m, nil
}

func (f *fakeChats) ListMessages(_ context.Context, chatID string) ([]*commonv1.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[chatID], nil
}

func (f *fakeChats) ListModels(_ context.Context) ([]*commonv1.Model, error) {
	return f.models, nil
}
