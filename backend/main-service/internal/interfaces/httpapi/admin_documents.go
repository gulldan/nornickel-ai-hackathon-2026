package httpapi

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	"github.com/example/main-service/internal/platform/httpx"
)

// Постраничная выдача GET /admin/documents: корпус растёт (десятки тысяч
// документов), полный список в один ответ больше не отдаём. Фильтры считаются
// в памяти по срезу ListAllDocuments — на этих объёмах это микросекунды.
const (
	adminDocsDefaultLimit = 100
	adminDocsMaxLimit     = 500
	statsDaysWindow       = 14
	statsTopDocs          = 10
)

// Категории типа файла — значения фильтра type= и ключи by_file_type
// (совпадают с фронтовым docAdapter.fileTypeOf).
const (
	typePDF   = "pdf"
	typeDocx  = "docx"
	typeEml   = "eml"
	typeXlsx  = "xlsx"
	typePptx  = "pptx"
	typeTxt   = "txt"
	typeImage = "image"
	typeDB    = "db"
	typeOther = "other"
)

// adminDocumentListView — страница GET /admin/documents.
type adminDocumentListView struct {
	Items  []adminDocumentView `json:"items"`
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

// dayCountView — загрузки за один календарный день (UTC, "YYYY-MM-DD").
type dayCountView struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

// topDocView — строка топа документов по числу фрагментов.
type topDocView struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	Title      string `json:"title,omitempty"`
	ChunkCount int32  `json:"chunk_count"`
}

// adminDocumentsStatsView — агрегаты GET /admin/documents/stats для «Обзора»:
// дашборд больше не тянет полный список ради шести плиток и трёх графиков.
type adminDocumentsStatsView struct {
	Total          int            `json:"total"`
	ByStatus       map[string]int `json:"by_status"`
	ByFileType     map[string]int `json:"by_file_type"`
	TotalSizeBytes int64          `json:"total_size_bytes"`
	TotalChunks    int64          `json:"total_chunks"`
	UploadsByDay   []dayCountView `json:"uploads_by_day"`
	TopByChunks    []topDocView   `json:"top_by_chunks"`
	IndexedPct     int            `json:"indexed_pct"`
}

// adminDocuments отдаёт страницу корпуса с фильтрами q= (подстрока в имени
// файла и заголовке, без учёта регистра), status= и type= (категории фронта).
func (a *API) adminDocuments(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin", "operator") {
		return
	}
	docs, err := a.ingestion.ListAllDocuments(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	docs = filterAdminDocuments(docs, r.URL.Query())
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].GetCreatedAt() != docs[j].GetCreatedAt() {
			return docs[i].GetCreatedAt() > docs[j].GetCreatedAt()
		}
		return docs[i].GetId() > docs[j].GetId()
	})

	limit := queryInt(r, "limit", adminDocsDefaultLimit)
	if limit <= 0 {
		limit = adminDocsDefaultLimit
	}
	if limit > adminDocsMaxLimit {
		limit = adminDocsMaxLimit
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	total := len(docs)
	from := min(offset, total)
	to := min(from+limit, total)
	httpx.JSON(w, http.StatusOK, adminDocumentListView{
		Items:  newAdminDocumentViews(docs[from:to]),
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// adminDocumentsStats считает агрегаты по всему корпусу за один проход.
func (a *API) adminDocumentsStats(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin", "operator") {
		return
	}
	docs, err := a.ingestion.ListAllDocuments(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}

	stats := adminDocumentsStatsView{
		Total:      len(docs),
		ByStatus:   map[string]int{},
		ByFileType: map[string]int{},
	}
	days := lastDays(time.Now().UTC(), statsDaysWindow)
	dayIndex := make(map[string]int, len(days))
	for i, d := range days {
		dayIndex[d.Day] = i
	}
	for _, d := range docs {
		stats.ByStatus[d.GetStatus()]++
		stats.ByFileType[docFileType(d.GetFilename(), d.GetMimeType())]++
		stats.TotalSizeBytes += d.GetSize()
		stats.TotalChunks += int64(d.GetChunkCount())
		if created, perr := time.Parse(time.RFC3339, d.GetCreatedAt()); perr == nil {
			if i, ok := dayIndex[created.UTC().Format(time.DateOnly)]; ok {
				days[i].Count++
			}
		}
	}
	stats.UploadsByDay = days
	stats.TopByChunks = topByChunks(docs, statsTopDocs)
	if stats.Total > 0 {
		indexed := stats.ByStatus["indexed"]
		stats.IndexedPct = (indexed*100 + stats.Total/2) / stats.Total
	}
	httpx.JSON(w, http.StatusOK, stats)
}

// filterAdminDocuments применяет q=/status=/type= к полному срезу корпуса.
func filterAdminDocuments(docs []*commonv1.Document, q url.Values) []*commonv1.Document {
	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))
	status := q.Get("status")
	ftype := q.Get("type")
	if needle == "" && status == "" && ftype == "" {
		return docs
	}
	out := make([]*commonv1.Document, 0, len(docs))
	for _, d := range docs {
		if needle != "" &&
			!strings.Contains(strings.ToLower(d.GetFilename()), needle) &&
			!strings.Contains(strings.ToLower(d.GetTitle()), needle) {
			continue
		}
		if status != "" && d.GetStatus() != status {
			continue
		}
		if ftype != "" && docFileType(d.GetFilename(), d.GetMimeType()) != ftype {
			continue
		}
		out = append(out, d)
	}
	return out
}

// topByChunks — топ-N документов по числу фрагментов, без мутации исходного среза.
func topByChunks(docs []*commonv1.Document, n int) []topDocView {
	sorted := make([]*commonv1.Document, len(docs))
	copy(sorted, docs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].GetChunkCount() != sorted[j].GetChunkCount() {
			return sorted[i].GetChunkCount() > sorted[j].GetChunkCount()
		}
		return sorted[i].GetId() < sorted[j].GetId()
	})
	out := make([]topDocView, 0, min(n, len(sorted)))
	for _, d := range sorted[:min(n, len(sorted))] {
		out = append(out, topDocView{
			ID:         d.GetId(),
			Filename:   d.GetFilename(),
			Title:      d.GetTitle(),
			ChunkCount: d.GetChunkCount(),
		})
	}
	return out
}

// lastDays — n календарных дней UTC по сегодняшний включительно, счётчики по нулям.
func lastDays(now time.Time, n int) []dayCountView {
	out := make([]dayCountView, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, dayCountView{Day: now.AddDate(0, 0, -i).Format(time.DateOnly)})
	}
	return out
}

// docFileType зеркалит фронтовый docAdapter.fileTypeOf: категория бейджа/фильтра
// по расширению, затем по MIME.
func docFileType(filename, mime string) string {
	name := strings.ToLower(filename)
	ext := ""
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		ext = name[dot+1:]
	}
	switch ext {
	case "pdf":
		return typePDF
	case "doc", "docx":
		return typeDocx
	case "eml", "msg":
		return typeEml
	case "xls", "xlsx":
		return typeXlsx
	case "ppt", "pptx":
		return typePptx
	case "txt", "md":
		return typeTxt
	case "png", "jpg", "jpeg", "webp", "tiff":
		return typeImage
	case "db", "sqlite", "sqlite3":
		return typeDB
	}
	mt := strings.ToLower(mime)
	switch {
	case strings.Contains(mt, "pdf"):
		return typePDF
	case strings.HasPrefix(mt, "image/"):
		return typeImage
	case strings.Contains(mt, "wordprocessingml"), strings.Contains(mt, "msword"):
		return typeDocx
	case strings.Contains(mt, "spreadsheetml"), strings.Contains(mt, "excel"):
		return typeXlsx
	case strings.Contains(mt, "presentationml"), strings.Contains(mt, "powerpoint"):
		return typePptx
	case mt == "message/rfc822":
		return typeEml
	case strings.Contains(mt, "sqlite"):
		return typeDB
	case strings.HasPrefix(mt, "text/"):
		return typeTxt
	}
	return typeOther
}

func (a *API) requeueDocuments(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin", "operator") {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "queued"
	}
	olderMin := queryInt(r, "older_min", 10)
	limit := queryInt(r, "limit", 5000)
	n, err := a.ingestion.RequeueStuck(r.Context(), status, time.Duration(olderMin)*time.Minute, limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"requeued": n})
}
