// Package httpapi is main-service's REST delivery layer. It binds the
// application services to HTTP, pulls the owner id from the verified JWT claims
// on the context, and translates errors into status codes. Routing uses the
// stdlib ServeMux method+wildcard patterns (Go 1.22+), matching db-service.
package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lukechampine.com/blake3"

	"github.com/gabriel-vasile/mimetype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/domain"
	"github.com/example/main-service/internal/platform/dbclient"
	"github.com/example/main-service/internal/platform/httpx"
	"github.com/example/main-service/internal/platform/jwt"
	"github.com/example/main-service/internal/platform/runtimecfg"
	"github.com/example/main-service/internal/platform/storage"
)

// metricsStore persists the latest RAG evaluation scorecard (opaque JSON) so
// the admin UI can display it. Satisfied by *valkey.Client.
type metricsStore interface {
	Set(ctx context.Context, key, val string, ttl time.Duration) error
	Get(ctx context.Context, key string) (string, bool, error)
}

// stageRecorder records the latency of one ingestion stage for the time-trace
// (satisfied by platform/observability.Metrics). Optional; nil disables timing.
type stageRecorder interface {
	RecordStage(stage string, seconds float64)
}

// evalScorecardKey holds the most recent evaluation scorecard in Valkey.
const evalScorecardKey = "rag:eval:scorecard"

// Trigger keys are watched by background workers. The API only requests a run;
// it never executes long model-dependent jobs in the request path.
const (
	evalTriggerKey              = "rag:eval:trigger"
	itcTriggerKey               = "rag:itc:trigger"
	clusterPublishVersionPrefix = "rag:clusters:publish_version:"
)

// API binds HTTP handlers to the application services.
type API struct {
	ingestion   *application.IngestionService
	chat        *application.ChatService
	admin       *application.AdminService
	uploads     *application.UploadSessionService
	hypotheses  *application.HypothesisService
	hypJobs     *application.HypothesisJobService
	metrics     metricsStore
	usage       *application.LLMUsageService
	stages      stageRecorder
	maxUploadMB int64
	// sharedCorpus mirrors llm-service's RAG_SHARED_CORPUS: when on, any
	// signed-in user may preview any indexed document (the knowledge base is
	// company-wide), so the per-owner check in documentChunks is skipped.
	sharedCorpus bool
	appSettings  *AppSettingsStore
	ovr          *runtimecfg.Overrides
}

// SetAppSettingsStore attaches the global runtime-settings store (admin panel).
func (a *API) SetAppSettingsStore(s *AppSettingsStore) { a.appSettings = s }

// SetRuntimeOverrides wires the runtime-overrides reader used by feature flags;
// a nil value keeps plain env reads.
func (a *API) SetRuntimeOverrides(ovr *runtimecfg.Overrides) { a.ovr = ovr }

// New builds the API. maxUploadMB caps the size of an uploaded file.
func New(
	ingestion *application.IngestionService,
	chat *application.ChatService,
	admin *application.AdminService,
	uploads *application.UploadSessionService,
	hypotheses *application.HypothesisService,
	hypJobs *application.HypothesisJobService,
	metrics metricsStore,
	usage *application.LLMUsageService,
	stages stageRecorder,
	maxUploadMB int64,
	sharedCorpus bool,
) *API {
	if maxUploadMB <= 0 {
		maxUploadMB = 50
	}
	return &API{
		ingestion:    ingestion,
		chat:         chat,
		admin:        admin,
		uploads:      uploads,
		hypotheses:   hypotheses,
		hypJobs:      hypJobs,
		metrics:      metrics,
		usage:        usage,
		stages:       stages,
		maxUploadMB:  maxUploadMB,
		sharedCorpus: sharedCorpus,
	}
}

// Routes registers all business endpoints on mux. Health endpoints live on the
// ops port (observability.RunOps); the /ws upgrade is mounted separately in
// cmd/server with its own header/query auth.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /documents", a.uploadDocument)
	mux.HandleFunc("GET /documents", a.listDocuments)
	mux.HandleFunc("GET /documents/{id}", a.getDocument)

	mux.HandleFunc("POST /chats", a.createChat)
	mux.HandleFunc("GET /chats", a.listChats)
	mux.HandleFunc("GET /chats/{id}", a.getChat)
	mux.HandleFunc("GET /chats/{id}/messages", a.listMessages)
	mux.HandleFunc("POST /chats/{id}/messages", a.postMessage)

	mux.HandleFunc("GET /documents/{id}/chunks", a.documentChunks)
	mux.HandleFunc("GET /documents/{id}/content", a.documentContent)

	// Direct-to-S3 upload sessions (large files; parts bypass the edge).
	mux.HandleFunc("POST /uploads", a.beginUpload)
	mux.HandleFunc("GET /uploads/{id}/parts", a.uploadPartURLs)
	mux.HandleFunc("POST /uploads/{id}/complete", a.completeUpload)
	mux.HandleFunc("DELETE /uploads/{id}", a.abortUpload)

	mux.HandleFunc("GET /models", a.listModels)

	// Background-pipeline heartbeat for the activity panel (any signed-in role).
	mux.HandleFunc("GET /system/activity", a.systemActivity)

	// Privileged panels. Role checks live in the handlers (403 on mismatch).
	mux.HandleFunc("GET /admin/users", a.adminUsers)
	mux.HandleFunc("GET /admin/documents", a.adminDocuments)
	mux.HandleFunc("GET /admin/documents/stats", a.adminDocumentsStats)
	mux.HandleFunc("POST /admin/documents/requeue", a.requeueDocuments)

	// RAG quality: eval-service stores scorecards; admins can request an
	// asynchronous run from the "Metrics" panel, and deployments may enable a
	// positive EVAL_INTERVAL_SEC for scheduled runs.
	mux.HandleFunc("POST /admin/metrics", a.postMetrics)
	mux.HandleFunc("GET /admin/metrics", a.getMetrics)
	mux.HandleFunc("GET /admin/llm-usage", a.getLLMUsage)
	mux.HandleFunc("GET /admin/perf", a.getPerf)
	mux.HandleFunc("POST /admin/metrics/run", a.triggerMetricsRun)
	mux.HandleFunc("POST /admin/itc/run", a.triggerITCRun)

	// Global runtime settings (DB-backed env overrides; apply without redeploy).
	mux.HandleFunc("GET /admin/settings", a.getAppSettings)
	mux.HandleFunc("PUT /admin/settings", a.putAppSettings)

	// Hypothesis Factory (owner-scoped; any signed-in user). The board endpoint
	// returns paginated rows plus server-side queue/facet aggregates.
	mux.HandleFunc("GET /hypotheses/board", a.hypothesisBoard)
	mux.HandleFunc("GET /hypotheses", a.listHypotheses)
	mux.HandleFunc("POST /hypotheses", a.createHypothesis)
	mux.HandleFunc("POST /hypotheses/generate", a.generateHypotheses)
	mux.HandleFunc("GET /hypotheses/graph", a.hypothesisGraph)
	mux.HandleFunc("GET /hypotheses/{id}", a.getHypothesis)
	mux.HandleFunc("POST /hypotheses/{id}/verify", a.verifyHypothesis)
	mux.HandleFunc("POST /hypotheses/{id}/enrich", a.enrichHypothesis)
	mux.HandleFunc("POST /hypotheses/{id}/assess-trl", a.assessHypothesisTRL)
	mux.HandleFunc("POST /hypotheses/{id}/competitors", a.analyzeCompetitors)
	mux.HandleFunc("POST /hypotheses/{id}/refine", a.refineHypothesis)
	mux.HandleFunc("POST /hypotheses/{id}/experiment", a.planExperiment)
	mux.HandleFunc("POST /hypotheses/{id}/tag", a.tagHypothesis)
	mux.HandleFunc("POST /hypotheses/{id}/itc", a.storeHypothesisITC)
	mux.HandleFunc("GET /trl/rubric", a.trlRubric)
	mux.HandleFunc("GET /itc/rubric", a.itcRubric)

	// Owner-editable transparent-ranking weights (literal path wins over /{id}).
	mux.HandleFunc("GET /hypotheses/scoring-weights", a.getScoringWeights)
	mux.HandleFunc("PUT /hypotheses/scoring-weights", a.updateScoringWeights)
	mux.HandleFunc("GET /hypotheses/runtime-settings", a.getHypothesisRuntimeSettings)
	mux.HandleFunc("PUT /hypotheses/runtime-settings", a.updateHypothesisRuntimeSettings)
	mux.HandleFunc("PUT /hypotheses/{id}", a.updateHypothesis)
	mux.HandleFunc("GET /hypotheses/{id}/revisions", a.listHypothesisRevisions)
	mux.HandleFunc("POST /hypotheses/{id}/revisions", a.addHypothesisRevision)
	mux.HandleFunc("GET /hypotheses/{id}/evidence", a.listHypothesisEvidence)

	mux.HandleFunc("POST /hypothesis-jobs", a.createHypothesisJob)
	mux.HandleFunc("GET /hypothesis-jobs", a.listHypothesisJobs)
	mux.HandleFunc("GET /hypothesis-jobs/{id}", a.getHypothesisJob)

	mux.HandleFunc("GET /kpis", a.listKPIs)
	mux.HandleFunc("POST /kpis", a.createKPI)
	mux.HandleFunc("POST /kpis/suggest", a.suggestKPIs)
	mux.HandleFunc("POST /kpis/parse", a.parseKPIPrompt)
	mux.HandleFunc("POST /kpis/{id}/graph-hypotheses", a.graphHypotheses)
	mux.HandleFunc("GET /kpis/{id}", a.getKPI)
	mux.HandleFunc("PUT /kpis/{id}", a.updateKPI)
	mux.HandleFunc("DELETE /kpis/{id}", a.deleteKPI)
	mux.HandleFunc("POST /kpis/{id}/documents", a.attachKPIDocuments)
	mux.HandleFunc("GET /kpis/{id}/documents", a.listKPIDocuments)
	mux.HandleFunc("DELETE /kpis/{id}/documents/{docId}", a.detachKPIDocument)

	mux.HandleFunc("GET /clusters", a.listClusters)
	mux.HandleFunc("POST /clusters/replace", a.replaceClusters)
	mux.HandleFunc("POST /clusters", a.createCluster)
	mux.HandleFunc("DELETE /clusters", a.deleteClusters)
	mux.HandleFunc("GET /clusters/{id}", a.getCluster)
	mux.HandleFunc("PUT /clusters/{id}", a.updateCluster)
}

// fail maps domain/client errors to HTTP status codes. ErrForbidden and
// ErrNotFound both become 404 so we never reveal others' resources; a
// ResourceExhausted from llm-service's admission queue becomes 503 so clients
// know to retry rather than treat the burst as a hard failure.
func (a *API) fail(w http.ResponseWriter, err error) {
	if s, ok := status.FromError(err); ok && s.Code() == codes.ResourceExhausted {
		httpx.Error(w, http.StatusServiceUnavailable, "the system is busy answering questions; please retry shortly")
		return
	}
	switch {
	case errors.Is(err, application.ErrSessionNotFound):
		httpx.Error(w, http.StatusNotFound, "сеанс загрузки не найден или истёк")
	case errors.Is(err, application.ErrInvalidArgument):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, application.ErrForbidden), errors.Is(err, application.ErrNotFound), errors.Is(err, dbclient.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	default:
		httpx.Error(w, http.StatusInternalServerError, err.Error())
	}
}

// ---- Documents ----

// spoolUploadPart streams the multipart "file" part into a temp file and
// returns it with the client-sent filename and the byte count. The request
// body is already capped by MaxBytesReader upstream, so the copy is bounded.
// The caller owns the temp file (close + remove).
func spoolUploadPart(r *http.Request) (*os.File, string, int64, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", 0, fmt.Errorf("parse multipart form: %w", err)
	}
	for {
		part, perr := mr.NextPart()
		if perr != nil {
			if errors.Is(perr, io.EOF) {
				return nil, "", 0, errors.New("missing file field")
			}
			return nil, "", 0, fmt.Errorf("parse multipart form: %w", perr)
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		tmp, terr := os.CreateTemp("", "rag-upload-*")
		if terr != nil {
			_ = part.Close()
			return nil, "", 0, fmt.Errorf("spool upload: %w", terr)
		}
		size, cerr := io.Copy(tmp, part)
		_ = part.Close()
		if cerr != nil {
			_ = tmp.Close()
			_ = os.Remove(filepath.Clean(tmp.Name()))
			return nil, "", 0, fmt.Errorf("read file: %w", cerr)
		}
		return tmp, part.FileName(), size, nil
	}
}

func (a *API) uploadDocument(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}

	limit := a.maxUploadMB << 20
	// Cap the whole request body so a client cannot exhaust memory; allow a
	// little slack above the file limit for multipart framing overhead.
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	// Stream the "file" part straight to a temp file: upload memory stays O(64
	// KiB) per request regardless of file size, and the handler gets a seekable
	// body for the sniff/hash/S3 passes below.
	file, filename, size, err := spoolUploadPart(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(filepath.Clean(file.Name()))
	}()
	if size == 0 {
		httpx.Error(w, http.StatusBadRequest, "uploaded file is empty")
		return
	}
	if _, serr := file.Seek(0, io.SeekStart); serr != nil {
		httpx.Error(w, http.StatusInternalServerError, "rewind upload")
		return
	}

	// Sniff the MIME type from the leading bytes only, then rewind so the
	// object store streams the file from the start.
	head := make([]byte, 3072)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("read file: %v", err))
		return
	}
	mimeType := mimetype.Detect(head[:n]).String()

	// Content BLAKE3 in a separate pass over the seekable file (multipart spills
	// large parts to disk): the S3 SDK needs a seekable body, so we can't hash
	// on the fly via a TeeReader.
	if _, serr := file.Seek(0, io.SeekStart); serr != nil {
		httpx.Error(w, http.StatusInternalServerError, "rewind upload")
		return
	}
	hasher := blake3.New(32, nil)
	if _, herr := io.Copy(hasher, file); herr != nil {
		httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("read file: %v", herr))
		return
	}
	contentHash := hex.EncodeToString(hasher.Sum(nil))

	cmd := domain.UploadCommand{
		OwnerID:  ownerID,
		Filename: filename,
		Size:     size,
		Body:     file,
		Text:     "",
		Hash:     contentHash,
		Reuse:    r.URL.Query().Get("reuse") == "1",
	}

	// text/plain skips the parser tier: its content travels inline to
	// chunk-splitter, so only this branch reads the whole body into memory.
	if strings.HasPrefix(strings.ToLower(mimeType), "text/plain") {
		if _, serr := file.Seek(0, io.SeekStart); serr != nil {
			httpx.Error(w, http.StatusInternalServerError, "rewind upload")
			return
		}
		data, rerr := io.ReadAll(file)
		if rerr != nil {
			httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("read file: %v", rerr))
			return
		}
		cmd.Text = string(data)
	}
	if _, serr := file.Seek(0, io.SeekStart); serr != nil {
		httpx.Error(w, http.StatusInternalServerError, "rewind upload")
		return
	}

	doc, existed, err := a.ingestion.Upload(r.Context(), cmd, mimeType)
	if err != nil {
		a.fail(w, err)
		return
	}
	// "upload" stage of the document time-trace: server-side receive + content
	// hash + S3 PUT + record + publish. Recorded on success only.
	if a.stages != nil {
		a.stages.RecordStage("upload", time.Since(t0).Seconds())
	}
	uploadView := newDocumentView(doc)
	uploadView.Duplicate = existed
	httpx.JSON(w, http.StatusCreated, uploadView)
}

func (a *API) listDocuments(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	docs, err := a.ingestion.ListDocuments(r.Context(), ownerID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		filtered := docs[:0]
		for _, d := range docs {
			if strings.Contains(strings.ToLower(d.GetFilename()), q) ||
				strings.Contains(strings.ToLower(d.GetTitle()), q) {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(docs)))
	if offset := queryInt(r, "offset", 0); offset > 0 {
		if offset >= len(docs) {
			docs = nil
		} else {
			docs = docs[offset:]
		}
	}
	if limit := queryInt(r, "limit", 0); limit > 0 && limit < len(docs) {
		docs = docs[:limit]
	}
	httpx.JSON(w, http.StatusOK, newDocumentViews(docs))
}

func (a *API) getDocument(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	doc, err := a.ingestion.GetDocument(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newDocumentView(doc))
}

// ---- Chats ----

func (a *API) createChat(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var req struct {
		Title  string `json:"title"`
		Source string `json:"source"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	chat, err := a.chat.CreateChat(r.Context(), ownerID, req.Title, req.Source)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, newChatView(chat))
}

func (a *API) listChats(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	limit := queryInt(r, "limit", 0)
	offset := queryInt(r, "offset", 0)
	// «Вся история» (все пользователи) доступна только администратору.
	all := false
	if r.URL.Query().Get("all") == "1" {
		if claims, okClaims := jwt.ClaimsFromContext(r.Context()); okClaims {
			all = hasAnyRole(claims, "admin")
		}
	}
	chats, total, err := a.chat.ListChats(r.Context(), ownerID, limit, offset, all)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, chatListView{Items: newChatViews(chats), Total: total})
}

func (a *API) getChat(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	chat, err := a.chat.GetChat(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newChatView(chat))
}

func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	msgs, err := a.chat.ListMessages(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newMessageViews(msgs))
}

func (a *API) postMessage(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Content == "" {
		httpx.Error(w, http.StatusBadRequest, "content is required")
		return
	}
	msg, err := a.chat.Ask(r.Context(), domain.AskCommand{
		OwnerID: ownerID,
		ChatID:  r.PathValue("id"),
		Content: req.Content,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, newMessageView(msg))
}

// documentChunks returns the indexed chunks of one document for the preview
// pane. Operators and admins may read any document, users only their own.
func (a *API) documentChunks(w http.ResponseWriter, r *http.Request) {
	claims, ok := jwt.ClaimsFromContext(r.Context())
	if !ok || claims.UserID == "" {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	privileged := a.sharedCorpus || hasAnyRole(claims, "operator", "admin")
	doc, chunks, err := a.ingestion.DocumentChunks(r.Context(), claims.UserID, privileged, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newDocumentChunksView(doc, chunks))
}

// documentContent streams the original stored file (PDF, image, …) so the UI can
// show a true preview of the document in its original form, rendered natively by
// the browser from a blob URL. Access mirrors documentChunks (owner or shared).
func (a *API) documentContent(w http.ResponseWriter, r *http.Request) {
	claims, ok := jwt.ClaimsFromContext(r.Context())
	if !ok || claims.UserID == "" {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	privileged := a.sharedCorpus || hasAnyRole(claims, "operator", "admin")
	rc, doc, err := a.ingestion.OpenDocument(r.Context(), claims.UserID, privileged, r.PathValue("id"))
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotFound):
			// The catalogue row exists but the object is gone from the store.
			httpx.Error(w, http.StatusNotFound, "original file is not available")
		case errors.Is(err, application.ErrForbidden), errors.Is(err, application.ErrNotFound), errors.Is(err, dbclient.ErrNotFound):
			httpx.Error(w, http.StatusNotFound, "not found")
		default:
			// Storage/db internals must not leak to the browser.
			httpx.Error(w, http.StatusInternalServerError, "failed to load document content")
		}
		return
	}
	defer func() { _ = rc.Close() }()
	ct := doc.GetMimeType()
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", doc.GetFilename()))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, rc)
}

// ---- Upload sessions (browser-direct multipart) ----

type beginUploadRequest struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MIMEType string `json:"mime_type"`
	Reuse    bool   `json:"reuse"`
}

type uploadSessionView struct {
	UploadID  string `json:"upload_id"`
	PartSize  int64  `json:"part_size"`
	PartCount int32  `json:"part_count"`
}

func (a *API) beginUpload(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var req beginUploadRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Filename == "" || req.Size <= 0 {
		httpx.Error(w, http.StatusBadRequest, "filename and positive size are required")
		return
	}
	sess, err := a.uploads.Begin(r.Context(), ownerID, req.Filename, req.MIMEType, req.Size, req.Reuse)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, uploadSessionView{
		UploadID: sess.ID, PartSize: sess.PartSize, PartCount: sess.PartCount,
	})
}

type partURLView struct {
	PartNumber int32  `json:"part_number"`
	URL        string `json:"url"`
}

func (a *API) uploadPartURLs(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	from := int32(queryInt(r, "from", 1))
	count := int32(queryInt(r, "count", 100))
	urls, err := a.uploads.PartURLs(r.Context(), ownerID, r.PathValue("id"), from, count)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]partURLView, 0, len(urls))
	for _, u := range urls {
		out = append(out, partURLView{PartNumber: u.PartNumber, URL: u.URL})
	}
	httpx.JSON(w, http.StatusOK, map[string][]partURLView{"urls": out})
}

type completeUploadRequest struct {
	Parts []struct {
		PartNumber int32  `json:"part_number"`
		ETag       string `json:"etag"`
	} `json:"parts"`
}

func (a *API) completeUpload(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var req completeUploadRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	parts := make([]storage.MultipartPart, 0, len(req.Parts))
	for _, p := range req.Parts {
		parts = append(parts, storage.MultipartPart{PartNumber: p.PartNumber, ETag: p.ETag})
	}
	doc, existed, err := a.uploads.Complete(r.Context(), ownerID, r.PathValue("id"), parts)
	if err != nil {
		a.fail(w, err)
		return
	}
	view := newDocumentView(doc)
	view.Duplicate = existed
	httpx.JSON(w, http.StatusCreated, view)
}

func (a *API) abortUpload(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if err := a.uploads.Abort(r.Context(), ownerID, r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// splitIDList parses a comma-separated id list, dropping blanks.
func splitIDList(raw string) []string {
	var out []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// queryInt reads an integer query parameter with a default.
func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ---- Privileged panels ----

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin") {
		return
	}
	users, err := a.admin.ListUsers(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newUserViews(users))
}

// postMetrics stores an evaluation scorecard (opaque JSON) published by the
// eval-service harness. Admin only; wrapped with a server-side store timestamp.
func (a *API) postMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin") {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "read body")
		return
	}
	if !json.Valid(body) {
		httpx.Error(w, http.StatusBadRequest, "scorecard must be valid JSON")
		return
	}
	wrapped := fmt.Sprintf(`{"stored_at":%q,"scorecard":%s}`, time.Now().UTC().Format(time.RFC3339), body)
	if serr := a.metrics.Set(r.Context(), evalScorecardKey, wrapped, 0); serr != nil {
		a.fail(w, serr)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "stored"})
}

// getMetrics returns the latest evaluation scorecard for the admin/operator UI,
// or {"scorecard":null} when none has been published yet.
func (a *API) getMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin", "operator") {
		return
	}
	raw, found, err := a.metrics.Get(r.Context(), evalScorecardKey)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !found {
		_, _ = w.Write([]byte(`{"scorecard":null}`))
		return
	}
	_, _ = w.Write([]byte(raw))
}

func (a *API) triggerMetricsRun(w http.ResponseWriter, r *http.Request) {
	a.triggerWorker(w, r, evalTriggerKey)
}

func (a *API) triggerITCRun(w http.ResponseWriter, r *http.Request) {
	a.triggerWorker(w, r, itcTriggerKey)
}

func (a *API) triggerWorker(w http.ResponseWriter, r *http.Request, key string) {
	if !a.requireRole(w, r, "admin") {
		return
	}
	triggeredAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.metrics.Set(r.Context(), key, triggeredAt, 0); err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{
		"status":       "queued",
		"triggered_at": triggeredAt,
	})
}

// requireRole answers 403 (and returns false) unless the caller holds one of
// the given roles.
func (a *API) requireRole(w http.ResponseWriter, r *http.Request, roles ...string) bool {
	claims, ok := jwt.ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return false
	}
	if !hasAnyRole(claims, roles...) {
		httpx.Error(w, http.StatusForbidden, "insufficient role")
		return false
	}
	return true
}

// hypothesesPrivileged reports whether the caller may read every owner's
// hypotheses (admin oversight); writes stay strictly owner-scoped.
func (a *API) hypothesesPrivileged(r *http.Request) bool {
	claims, ok := jwt.ClaimsFromContext(r.Context())
	return ok && hasAnyRole(claims, "admin")
}

// hasAnyRole reports whether the verified claims carry one of the roles.
func hasAnyRole(claims *jwt.Claims, roles ...string) bool {
	for _, have := range claims.Roles {
		for _, want := range roles {
			if have == want {
				return true
			}
		}
	}
	return false
}

// ---- Models ----

func (a *API) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := a.chat.ListModels(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, newModelViews(models))
}

// ownerFromContext returns the authenticated user id placed by jwt.Middleware.
func ownerFromContext(r *http.Request) (string, bool) {
	claims, ok := jwt.ClaimsFromContext(r.Context())
	if !ok || claims.UserID == "" {
		return "", false
	}
	return claims.UserID, true
}
