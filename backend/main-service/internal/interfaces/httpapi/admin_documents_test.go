package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// seedCorpus наполняет фейковый корпус документами с разными статусами,
// типами и русско-китайскими именами. Даты в далёком прошлом, чтобы не
// попадать в скользящее 14-дневное окно uploads_by_day.
func seedCorpus(h *harness) {
	docs := []*commonv1.Document{
		{Id: "d1", Filename: "Отчёт.pdf", Status: "indexed", ChunkCount: 40, Size: 100, CreatedAt: "2020-01-01T10:00:00Z"},
		{
			Id: "d2", Filename: "论文.pdf", Title: "Китайская статья",
			Status: "indexed", ChunkCount: 30, Size: 200, CreatedAt: "2020-01-02T10:00:00Z",
		},
		{Id: "d3", Filename: "notes.txt", Status: "parsing", ChunkCount: 0, Size: 300, CreatedAt: "2020-01-03T10:00:00Z"},
		{Id: "d4", Filename: "scan.jpg", Status: "failed", ChunkCount: 0, Size: 400, CreatedAt: "2020-01-03T11:00:00Z"},
		{Id: "d5", Filename: "таблица.xlsx", Status: "indexed", ChunkCount: 10, Size: 500, CreatedAt: "2020-01-04T10:00:00Z"},
	}
	for _, d := range docs {
		h.docs.docs[d.GetId()] = d
	}
}

// adminDocsPage декодирует страницу GET /admin/documents.
type adminDocsPage struct {
	Items []struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
	} `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func getDocsPage(t *testing.T, h *harness, query string) adminDocsPage {
	t.Helper()
	r := h.do(t, http.MethodGet, "/admin/documents"+query, h.token(t, "op", "operator"), "")
	if r.code != http.StatusOK {
		t.Fatalf("GET /admin/documents%s = %d: %s", query, r.code, r.body)
	}
	var page adminDocsPage
	if err := json.Unmarshal([]byte(r.body), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return page
}

// Пагинация: сортировка created_at desc, окно limit/offset, total по фильтру.
func TestAdminDocuments_Pagination(t *testing.T) {
	h := newHarness(t)
	seedCorpus(h)

	page := getDocsPage(t, h, "?limit=2&offset=1")
	if page.Total != 5 || page.Limit != 2 || page.Offset != 1 {
		t.Fatalf("meta = %+v, want total 5 limit 2 offset 1", page)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "d4" || page.Items[1].ID != "d3" {
		t.Fatalf("window = %+v, want [d4 d3]", page.Items)
	}
	// Смещение за концом списка — пустая страница, не ошибка.
	if p := getDocsPage(t, h, "?offset=99"); len(p.Items) != 0 || p.Total != 5 {
		t.Fatalf("past-end = %+v, want empty items", p)
	}
	// Неположительный и сверхбольшой limit нормализуются к дефолту и капу.
	if p := getDocsPage(t, h, "?limit=-1"); p.Limit != 100 {
		t.Fatalf("limit -1 → %d, want 100", p.Limit)
	}
	if p := getDocsPage(t, h, "?limit=9999"); p.Limit != 500 {
		t.Fatalf("limit 9999 → %d, want 500", p.Limit)
	}
}

// Фильтры: подстрока без регистра (кириллица/китайский, имя и заголовок),
// статус и фронтовая категория типа.
func TestAdminDocuments_Filters(t *testing.T) {
	h := newHarness(t)
	seedCorpus(h)

	if p := getDocsPage(t, h, "?q=%D0%BE%D1%82%D1%87%D1%91%D1%82"); p.Total != 1 || p.Items[0].ID != "d1" { // «отчёт»
		t.Fatalf("q=отчёт → %+v, want d1", p)
	}
	if p := getDocsPage(t, h, "?q=%E8%AE%BA%E6%96%87"); p.Total != 1 || p.Items[0].ID != "d2" { // «论文»
		t.Fatalf("q=论文 → %+v, want d2", p)
	}
	if p := getDocsPage(t, h, "?q=%D0%BA%D0%B8%D1%82%D0%B0%D0%B9"); p.Total != 1 || p.Items[0].ID != "d2" { // «китай» — по заголовку
		t.Fatalf("q=китай → %+v, want d2", p)
	}
	if p := getDocsPage(t, h, "?status=failed"); p.Total != 1 || p.Items[0].ID != "d4" {
		t.Fatalf("status=failed → %+v, want d4", p)
	}
	if p := getDocsPage(t, h, "?type=pdf"); p.Total != 2 {
		t.Fatalf("type=pdf → total %d, want 2", p.Total)
	}
	if p := getDocsPage(t, h, "?type=image&status=failed&q=scan"); p.Total != 1 || p.Items[0].ID != "d4" {
		t.Fatalf("combined filters → %+v, want d4", p)
	}
	if p := getDocsPage(t, h, "?q=%D0%BD%D0%B5%D1%82%D1%83"); p.Total != 0 || len(p.Items) != 0 { // «нету»
		t.Fatalf("no matches → %+v, want empty", p)
	}
}

// Агрегаты «Обзора»: карты статусов/типов, объём, топ и загрузки по дням.
func TestAdminDocumentsStats(t *testing.T) {
	h := newHarness(t)
	seedCorpus(h)
	// Сегодняшняя загрузка попадает в 14-дневное окно uploads_by_day.
	today := time.Now().UTC().Format("2006-01-02")
	h.docs.docs["d6"] = &commonv1.Document{
		Id: "d6", Filename: "fresh.docx", Status: "indexed", ChunkCount: 5, Size: 50,
		CreatedAt: today + "T09:00:00Z",
	}

	if r := h.do(t, http.MethodGet, "/admin/documents/stats", h.token(t, "alice"), ""); r.code != http.StatusForbidden {
		t.Fatalf("non-privileged stats = %d, want 403", r.code)
	}
	r := h.do(t, http.MethodGet, "/admin/documents/stats", h.token(t, "root", "admin"), "")
	if r.code != http.StatusOK {
		t.Fatalf("stats = %d: %s", r.code, r.body)
	}
	var s struct {
		Total          int            `json:"total"`
		ByStatus       map[string]int `json:"by_status"`
		ByFileType     map[string]int `json:"by_file_type"`
		TotalSizeBytes int64          `json:"total_size_bytes"`
		TotalChunks    int64          `json:"total_chunks"`
		UploadsByDay   []struct {
			Day   string `json:"day"`
			Count int    `json:"count"`
		} `json:"uploads_by_day"`
		TopByChunks []struct {
			ID         string `json:"id"`
			ChunkCount int32  `json:"chunk_count"`
		} `json:"top_by_chunks"`
		IndexedPct int `json:"indexed_pct"`
	}
	if err := json.Unmarshal([]byte(r.body), &s); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if s.Total != 6 || s.ByStatus["indexed"] != 4 || s.ByStatus["failed"] != 1 || s.ByStatus["parsing"] != 1 {
		t.Fatalf("statuses = %+v (total %d)", s.ByStatus, s.Total)
	}
	if s.ByFileType["pdf"] != 2 || s.ByFileType["image"] != 1 || s.ByFileType["docx"] != 1 {
		t.Fatalf("types = %+v", s.ByFileType)
	}
	if s.TotalSizeBytes != 1550 || s.TotalChunks != 85 || s.IndexedPct != 67 {
		t.Fatalf("size %d chunks %d pct %d, want 1550/85/67", s.TotalSizeBytes, s.TotalChunks, s.IndexedPct)
	}
	if len(s.UploadsByDay) != 14 || s.UploadsByDay[13].Day != today || s.UploadsByDay[13].Count != 1 {
		t.Fatalf("uploads_by_day = %+v", s.UploadsByDay)
	}
	if len(s.TopByChunks) != 6 || s.TopByChunks[0].ID != "d1" || s.TopByChunks[1].ID != "d2" {
		t.Fatalf("top_by_chunks = %+v", s.TopByChunks)
	}
}

// Ошибка стора отдаётся как 500 обеими ручками.
func TestAdminDocuments_StoreError(t *testing.T) {
	h := newHarness(t)
	h.docs.listErr = errors.New("db unavailable")
	admin := h.token(t, "root", "admin")
	if r := h.do(t, http.MethodGet, "/admin/documents", admin, ""); r.code != http.StatusInternalServerError {
		t.Fatalf("list on error = %d, want 500", r.code)
	}
	if r := h.do(t, http.MethodGet, "/admin/documents/stats", admin, ""); r.code != http.StatusInternalServerError {
		t.Fatalf("stats on error = %d, want 500", r.code)
	}
}
