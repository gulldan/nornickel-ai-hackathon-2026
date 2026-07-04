package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/example/main-service/internal/platform/dbclient"
	"github.com/example/main-service/internal/platform/storage"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	llmv1 "github.com/example/main-service/internal/platform/genproto/llm/v1"
)

// answer is one canned LLM reply for the scripted answerer.
type answer struct {
	resp *commonv1.RagResponse
	err  error
}

// scriptedAnswerer returns queued replies in order, recording every request, so a
// multi-pass flow (verify → counter, generate → judge → stance, …) can be driven
// deterministically. Once the script is exhausted it repeats the last reply.
type scriptedAnswerer struct {
	mu       sync.Mutex
	replies  []answer
	calls    int
	requests []*commonv1.RagRequest
}

func newAnswerer(replies ...answer) *scriptedAnswerer {
	return &scriptedAnswerer{replies: replies}
}

// reply builds a successful answer from a model answer string and sources.
func reply(text string, sources ...*commonv1.Source) answer {
	return answer{resp: &commonv1.RagResponse{Answer: text, Sources: sources, Model: "fake-model"}, err: nil}
}

// failReply builds an error answer.
func failReply(err error) answer { return answer{resp: nil, err: err} }

func (s *scriptedAnswerer) Answer(_ context.Context, req *commonv1.RagRequest) (*commonv1.RagResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	idx := s.calls
	s.calls++
	if len(s.replies) == 0 {
		return &commonv1.RagResponse{Answer: "", Model: "fake-model"}, nil
	}
	if idx >= len(s.replies) {
		idx = len(s.replies) - 1
	}
	a := s.replies[idx]
	return a.resp, a.err
}

func (s *scriptedAnswerer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fakeCatalog is an in-memory HypothesisCatalog. Each entity is keyed by id; the
// optional *Err fields force a specific method to fail so error branches are
// reachable. Owner scoping is the application layer's job, not the store's, so
// the fake returns rows regardless of owner.
type fakeCatalog struct {
	mu          sync.Mutex
	kpis        map[string]*commonv1.KPI
	clusters    map[string]*commonv1.Cluster
	hypotheses  map[string]*commonv1.Hypothesis
	revisions   map[string][]*commonv1.HypothesisRevision
	evidence    map[string][]*commonv1.HypothesisEvidence
	list        []*commonv1.Hypothesis
	updateCalls int
	kpiDocs     map[string][]*dbv1.KpiDocumentLink
	docs        []*commonv1.Document

	createKPIErr        error
	getKPIErr           error
	listKPIErr          error
	updateKPIErr        error
	createClusterErr    error
	getClusterErr       error
	listClusterErr      error
	updateClusterErr    error
	deleteClustersErr   error
	createHypErr        error
	getHypErr           error
	listHypErr          error
	updateHypErr        error
	addRevErr           error
	listRevErr          error
	listEvidenceErr     error
	deleteClustersCount int
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
	if c.createKPIErr != nil {
		return nil, c.createKPIErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := &commonv1.KPI{
		Id: "kpi-" + req.GetTitle(), OwnerId: req.GetOwnerId(), Title: req.GetTitle(),
		Metric: req.GetMetric(), Description: req.GetDescription(), FunctionArea: req.GetFunctionArea(),
	}
	c.kpis[k.GetId()] = k
	return k, nil
}

func (c *fakeCatalog) GetKPI(_ context.Context, id string) (*commonv1.KPI, error) {
	if c.getKPIErr != nil {
		return nil, c.getKPIErr
	}
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
	if c.updateKPIErr != nil {
		return c.updateKPIErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kpis[kpi.GetId()] = kpi
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
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.docs, nil
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
	if c.createClusterErr != nil {
		return nil, c.createClusterErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cl := &commonv1.Cluster{
		Id: "cl-" + req.GetLabel(), OwnerId: req.GetOwnerId(), Label: req.GetLabel(),
		Summary: req.GetSummary(), Params: req.GetParams(),
	}
	c.clusters[cl.GetId()] = cl
	return cl, nil
}

func (c *fakeCatalog) GetCluster(_ context.Context, id string) (*commonv1.Cluster, error) {
	if c.getClusterErr != nil {
		return nil, c.getClusterErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.clusters[id]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return cl, nil
}

func (c *fakeCatalog) ListClusters(_ context.Context, ownerID string) ([]*commonv1.Cluster, error) {
	if c.listClusterErr != nil {
		return nil, c.listClusterErr
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
	if c.updateClusterErr != nil {
		return c.updateClusterErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusters[cluster.GetId()] = cluster
	return nil
}

func (c *fakeCatalog) DeleteClusters(_ context.Context, ownerID string) error {
	c.mu.Lock()
	c.deleteClustersCount++
	c.mu.Unlock()
	if c.deleteClustersErr != nil {
		return c.deleteClustersErr
	}
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
	if c.createHypErr != nil {
		return nil, c.createHypErr
	}
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
	if c.getHypErr != nil {
		return nil, c.getHypErr
	}
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
	if c.listHypErr != nil {
		return nil, c.listHypErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list != nil {
		return c.list, nil
	}
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
	c.updateCalls++
	c.mu.Unlock()
	if c.updateHypErr != nil {
		return c.updateHypErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := req.GetHypothesis()
	c.hypotheses[h.GetId()] = h
	return nil
}

func (c *fakeCatalog) AddHypothesisRevision(
	_ context.Context, rev *commonv1.HypothesisRevision,
) (*commonv1.HypothesisRevision, error) {
	if c.addRevErr != nil {
		return nil, c.addRevErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rev.Id = "rev"
	c.revisions[rev.GetHypothesisId()] = append(c.revisions[rev.GetHypothesisId()], rev)
	return rev, nil
}

func (c *fakeCatalog) ListHypothesisRevisions(
	_ context.Context, hypothesisID string,
) ([]*commonv1.HypothesisRevision, error) {
	if c.listRevErr != nil {
		return nil, c.listRevErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revisions[hypothesisID], nil
}

func (c *fakeCatalog) ListHypothesisEvidence(
	_ context.Context, hypothesisID string,
) ([]*commonv1.HypothesisEvidence, error) {
	if c.listEvidenceErr != nil {
		return nil, c.listEvidenceErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evidence[hypothesisID], nil
}

// putHypothesis seeds a hypothesis directly (bypassing CreateHypothesis ranking).
func (c *fakeCatalog) putHypothesis(h *commonv1.Hypothesis) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hypotheses[h.GetId()] = h
}

// fakeWeights is an in-memory ScoringWeightsStore.
type fakeWeights struct {
	w      map[string]*ScoringWeights
	getErr error
	setErr error
}

func newWeights() *fakeWeights { return &fakeWeights{w: map[string]*ScoringWeights{}} }

func (f *fakeWeights) Get(_ context.Context, ownerID string) (*ScoringWeights, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.w[ownerID], nil
}

func (f *fakeWeights) Set(_ context.Context, ownerID string, w ScoringWeights) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.w[ownerID] = &w
	return nil
}

// fakeRuntimeSettings is an in-memory RuntimeSettingsStore.
type fakeRuntimeSettings struct {
	settings map[string]*HypothesisRuntimeSettings
	getErr   error
	setErr   error
}

func newRuntimeSettings() *fakeRuntimeSettings {
	return &fakeRuntimeSettings{settings: map[string]*HypothesisRuntimeSettings{}}
}

func (f *fakeRuntimeSettings) Get(_ context.Context, ownerID string) (*HypothesisRuntimeSettings, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.settings[ownerID], nil
}

func (f *fakeRuntimeSettings) Set(_ context.Context, ownerID string, settings HypothesisRuntimeSettings) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.settings[ownerID] = &settings
	return nil
}

// fakeGraph is an in-memory GraphStore.
type fakeGraph struct {
	edges    map[string][]KGEdge
	addErr   error
	edgesErr error
}

func newGraph() *fakeGraph { return &fakeGraph{edges: map[string][]KGEdge{}} }

func (f *fakeGraph) AddEdges(_ context.Context, ownerID string, edges []KGEdge) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.edges[ownerID] = append(f.edges[ownerID], edges...)
	return nil
}

func (f *fakeGraph) Edges(_ context.Context, ownerID string) ([]KGEdge, error) {
	if f.edgesErr != nil {
		return nil, f.edgesErr
	}
	return f.edges[ownerID], nil
}

// ---- IngestionService fakes ----

// fakeObjectStore is an in-memory ObjectStore. Stored bytes are kept so OpenDocument
// can stream them back.
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	putErr  error
	getErr  error
}

func newObjectStore() *fakeObjectStore { return &fakeObjectStore{objects: map[string][]byte{}} }

func (f *fakeObjectStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	if f.putErr != nil {
		return f.putErr
	}
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
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	data, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return nil, errStoreMiss
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

var errStoreMiss = errors.New("object not found")

// fakeDocs is an in-memory DocumentCatalog.
type fakeDocs struct {
	mu            sync.Mutex
	docs          map[string]*commonv1.Document
	byHash        map[string]*commonv1.Document
	createErr     error
	getErr        error
	findErr       error
	listErr       error
	statusUpdates int
}

func newDocs() *fakeDocs {
	return &fakeDocs{docs: map[string]*commonv1.Document{}, byHash: map[string]*commonv1.Document{}}
}

func (f *fakeDocs) CreateDocument(
	_ context.Context, req *dbv1.CreateDocumentRequest,
) (*commonv1.Document, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := &commonv1.Document{
		Id: "doc-" + req.GetFilename(), OwnerId: req.GetOwnerId(), Filename: req.GetFilename(),
		MimeType: req.GetMimeType(), Size: req.GetSize(), ObjectKey: req.GetObjectKey(),
	}
	f.docs[d.GetId()] = d
	if req.GetContentHash() != "" {
		f.byHash[req.GetOwnerId()+"|"+req.GetContentHash()] = d
	}
	return d, nil
}

func (f *fakeDocs) GetDocument(_ context.Context, id string) (*commonv1.Document, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[id]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return d, nil
}

func (f *fakeDocs) FindDocumentByHash(_ context.Context, ownerID, hash string) (*commonv1.Document, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.byHash[ownerID+"|"+hash]
	if !ok {
		return nil, dbclient.ErrNotFound
	}
	return d, nil
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
	f.statusUpdates++
	if d, ok := f.docs[id]; ok {
		d.Status = status
	}
	return nil
}

// fakePublisher records published proto messages.
type fakePublisher struct {
	mu     sync.Mutex
	routes []string
	err    error
}

func (f *fakePublisher) PublishProto(_ context.Context, _, routingKey string, _ proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.routes = append(f.routes, routingKey)
	return nil
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.routes)
}

// fakeChunks is an in-memory ChunkReader.
type fakeChunks struct {
	chunks map[string][]*llmv1.DocumentChunk
	err    error
}

func (f *fakeChunks) DocumentChunks(
	_ context.Context, _, documentID string,
) ([]*llmv1.DocumentChunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.chunks[documentID], nil
}

// ---- ChatService fakes ----

// fakeChats is an in-memory ChatCatalog.
type fakeChats struct {
	mu          sync.Mutex
	chats       map[string]*commonv1.Chat
	messages    map[string][]*commonv1.Message
	models      []*commonv1.Model
	createErr   error
	getErr      error
	listErr     error
	addErr      error
	listMsgErr  error
	listModErr  error
	addMsgCalls int
}

func newChats() *fakeChats {
	return &fakeChats{chats: map[string]*commonv1.Chat{}, messages: map[string][]*commonv1.Message{}}
}

func (f *fakeChats) CreateChat(_ context.Context, ownerID, title, source string) (*commonv1.Chat, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &commonv1.Chat{Id: "chat-" + title, OwnerId: ownerID, Title: title, Source: source}
	f.chats[c.GetId()] = c
	return c, nil
}

func (f *fakeChats) GetChat(_ context.Context, id string) (*commonv1.Chat, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
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
	f.addMsgCalls++
	f.mu.Unlock()
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &commonv1.Message{Id: "msg", ChatId: chatID, Role: role, Content: content, Sources: sources, Meta: meta}
	f.messages[chatID] = append(f.messages[chatID], m)
	return m, nil
}

func (f *fakeChats) ListMessages(_ context.Context, chatID string) ([]*commonv1.Message, error) {
	if f.listMsgErr != nil {
		return nil, f.listMsgErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[chatID], nil
}

func (f *fakeChats) ListModels(_ context.Context) ([]*commonv1.Model, error) {
	if f.listModErr != nil {
		return nil, f.listModErr
	}
	return f.models, nil
}

// fakeUsers is an in-memory UserCatalog.
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

// ---- UploadSession / job store fakes ----

// fakeMultipart is an in-memory MultipartStore.
type fakeMultipart struct {
	mu            sync.Mutex
	createErr     error
	presignErr    error
	completeErr   error
	abortErr      error
	completeCalls int
	abortCalls    int
}

func (f *fakeMultipart) CreateMultipart(_ context.Context, _, _ string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	return "s3-upload-id", nil
}

func (f *fakeMultipart) PresignUploadPart(
	_ context.Context, _, _ string, _ int32, _ time.Duration,
) (string, error) {
	if f.presignErr != nil {
		return "", f.presignErr
	}
	return "https://s3/part", nil
}

func (f *fakeMultipart) CompleteMultipart(
	_ context.Context, _, _ string, _ []storage.MultipartPart,
) error {
	f.mu.Lock()
	f.completeCalls++
	f.mu.Unlock()
	return f.completeErr
}

func (f *fakeMultipart) AbortMultipart(_ context.Context, _, _ string) error {
	f.mu.Lock()
	f.abortCalls++
	f.mu.Unlock()
	return f.abortErr
}

// fakeSessions is an in-memory SessionStore.
type fakeSessions struct {
	mu      sync.Mutex
	data    map[string][]byte
	setErr  error
	getErr  error
	delCnt  int
	setJSON func()
}

func newSessions() *fakeSessions { return &fakeSessions{data: map[string][]byte{}} }

func (f *fakeSessions) SetJSON(_ context.Context, key string, v any, _ time.Duration) error {
	if f.setJSON != nil {
		f.setJSON()
	}
	if f.setErr != nil {
		return f.setErr
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.data[key] = b
	f.mu.Unlock()
	return nil
}

func (f *fakeSessions) GetJSON(_ context.Context, key string, dest any) (bool, error) {
	if f.getErr != nil {
		return false, f.getErr
	}
	f.mu.Lock()
	b, ok := f.data[key]
	f.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(b, dest); err != nil {
		return false, err
	}
	return true, nil
}

func (f *fakeSessions) Del(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delCnt += len(keys)
	for _, k := range keys {
		delete(f.data, k)
	}
	return nil
}

// fakeJobStore is an in-memory HypothesisJobStore (Valkey surface).
type fakeJobStore struct {
	mu      sync.Mutex
	json    map[string][]byte
	lists   map[string][]string
	nx      map[string]bool
	setErr  error
	getErr  error
	nxErr   error
	pushErr error
	rngErr  error
	claimNo bool
	// claimGate, when non-nil, parks the SetNX claim until closed, so a test can
	// hold the detached runner while it inspects the returned job object.
	claimGate chan struct{}
}

func newJobStore() *fakeJobStore {
	return &fakeJobStore{json: map[string][]byte{}, lists: map[string][]string{}, nx: map[string]bool{}}
}

func (f *fakeJobStore) SetJSON(_ context.Context, key string, v any, _ time.Duration) error {
	if f.setErr != nil {
		return f.setErr
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.json[key] = b
	f.mu.Unlock()
	return nil
}

func (f *fakeJobStore) GetJSON(_ context.Context, key string, dest any) (bool, error) {
	if f.getErr != nil {
		return false, f.getErr
	}
	f.mu.Lock()
	b, ok := f.json[key]
	f.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, dest)
}

func (f *fakeJobStore) SetNX(_ context.Context, key, _ string, _ time.Duration) (bool, error) {
	if f.nxErr != nil {
		return false, f.nxErr
	}
	if f.claimNo {
		return false, nil
	}
	f.mu.Lock()
	gate := f.claimGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
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
	if f.pushErr != nil {
		return f.pushErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists[key] = append(append([]string{}, values...), f.lists[key]...)
	return nil
}

func (f *fakeJobStore) LTrim(_ context.Context, _ string, _, _ int64) error { return nil }

func (f *fakeJobStore) LRange(_ context.Context, key string, _, _ int64) ([]string, error) {
	if f.rngErr != nil {
		return nil, f.rngErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.lists[key]...), nil
}
