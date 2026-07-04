package httpapi

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/example/main-service/internal/application"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

// Browser-facing JSON for the Hypothesis Factory. Like the other views, these
// map the genproto DTOs to stable snake_case JSON. The JSONB columns travel as
// raw-JSON strings in the proto messages and are re-emitted as real JSON objects
// (json.RawMessage) so the frontend gets structured data, not a quoted string.
// Nullable scores stay as pointers so an unscored hypothesis serialises null.

// rawJSON re-embeds a raw-JSON string as a JSON value (null if empty/invalid).
func rawJSON(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage("null")
	}
	return json.RawMessage(s)
}

// jsonStr flattens a request's JSON value back to the raw string the proto carries.
func jsonStr(m json.RawMessage) string {
	if len(m) == 0 {
		return ""
	}
	return string(m)
}

// pruneJSONBranches удаляет из JSON-объекта перечисленные ветки (путь — цепочка
// ключей). Так списочные ручки срезают объёмные под-деревья, не трогая остальную
// структуру; полный объект остаётся за ручками деталей. Пустой/невалидный JSON
// или не-объект отдаётся как раньше (через rawJSON).
func pruneJSONBranches(s string, paths ...[]string) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return rawJSON(s)
	}
	for _, path := range paths {
		node, ok := m, true
		for _, key := range path[:len(path)-1] {
			node, ok = node[key].(map[string]any)
			if !ok {
				break
			}
		}
		if ok {
			delete(node, path[len(path)-1])
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return rawJSON(s)
	}
	return out
}

// keepJSONKeys оставляет в JSON-объекте только перечисленные верхние ключи —
// для generation в списках нужны лишь скаляры, по которым фронт делит
// гипотезы/направления/черновики. Невалидный JSON отдаётся как раньше.
func keepJSONKeys(s string, keys ...string) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return rawJSON(s)
	}
	kept := make(map[string]any, len(keys))
	for _, key := range keys {
		if v, ok := m[key]; ok {
			kept[key] = v
		}
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return rawJSON(s)
	}
	return out
}

// generationRef — generation, усечённый до скаляров, нужных спискам (вкладки
// гипотезы/направления/черновики, роутинг карточек, бейдж происхождения).
func generationRef(s string) json.RawMessage {
	return keepJSONKeys(s, "kind", "semantic_kind", "model", "fallback_reason")
}

// orEmptyStrings keeps a string slice non-nil so it serialises as [] not null.
func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// nilIfEmpty maps a blank optional string to nil so an empty kpi_id / cluster_id
// / document_id becomes SQL NULL rather than "" (which would break the FK).
func nilIfEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// ---- KPI ----

// ---- Hypothesis jobs ----

type hypothesisJobInput struct {
	Kind           string   `json:"kind"`
	HypothesisID   string   `json:"hypothesis_id"`
	KPIID          string   `json:"kpi_id"`
	KPITitle       string   `json:"kpi_title"`
	KPIMetric      string   `json:"kpi_metric"`
	KPIDescription string   `json:"kpi_description"`
	Constraints    string   `json:"constraints"`
	Count          int      `json:"count"`
	DocumentIDs    []string `json:"document_ids"`
}

func (in hypothesisJobInput) toApplicationInput() application.HypothesisJobInput {
	return application.HypothesisJobInput{
		HypothesisID:   in.HypothesisID,
		KPIID:          in.KPIID,
		KPITitle:       in.KPITitle,
		KPIMetric:      in.KPIMetric,
		KPIDescription: in.KPIDescription,
		Constraints:    in.Constraints,
		Count:          in.Count,
		DocumentIDs:    in.DocumentIDs,
	}
}

type hypothesisJobView struct {
	ID          string                         `json:"id"`
	OwnerID     string                         `json:"owner_id"`
	Kind        string                         `json:"kind"`
	Status      string                         `json:"status"`
	Input       application.HypothesisJobInput `json:"input"`
	ResultIDs   []string                       `json:"result_ids"`
	Error       string                         `json:"error,omitempty"`
	CreatedAt   string                         `json:"created_at"`
	StartedAt   string                         `json:"started_at,omitempty"`
	HeartbeatAt string                         `json:"heartbeat_at,omitempty"`
	FinishedAt  string                         `json:"finished_at,omitempty"`
}

func newHypothesisJobView(j *application.HypothesisJob) hypothesisJobView {
	resultIDs := j.ResultIDs
	if resultIDs == nil {
		resultIDs = []string{}
	}
	return hypothesisJobView{
		ID: j.ID, OwnerID: j.OwnerID, Kind: j.Kind, Status: j.Status, Input: j.Input,
		ResultIDs: resultIDs, Error: j.Error, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt,
		HeartbeatAt: j.HeartbeatAt, FinishedAt: j.FinishedAt,
	}
}

func newHypothesisJobViews(jobs []*application.HypothesisJob) []hypothesisJobView {
	out := make([]hypothesisJobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, newHypothesisJobView(j))
	}
	return out
}

type hypothesisBoardView struct {
	Items       []hypothesisView    `json:"items"`
	Total       int                 `json:"total"`
	Limit       int                 `json:"limit"`
	Offset      int                 `json:"offset"`
	QueueCounts map[string]int      `json:"queue_counts"`
	Facets      map[string][]string `json:"facets"`
}

func newHypothesisBoardView(
	page []*commonv1.Hypothesis, filtered []*commonv1.Hypothesis, aggregateBase []*commonv1.Hypothesis,
	limit, offset, total int,
) hypothesisBoardView {
	return hypothesisBoardView{
		Items:       newHypothesisBoardItemViews(page),
		Total:       total,
		Limit:       limit,
		Offset:      offset,
		QueueCounts: boardQueueCounts(aggregateBase),
		Facets:      boardFacets(filtered),
	}
}

func boardQueueCounts(hs []*commonv1.Hypothesis) map[string]int {
	queues := []string{queueNeedsVerify, queueNeedsTRL, queueReady, queueRisk, queueInsufficient}
	counts := map[string]int{queueAll: len(hs)}
	for _, q := range queues {
		counts[q] = 0
	}
	for _, h := range hs {
		for _, q := range queues {
			if boardQueueMatch(h, q) {
				counts[q]++
			}
		}
	}
	return counts
}

func boardFacets(hs []*commonv1.Hypothesis) map[string][]string {
	values := map[string]map[string]bool{
		"company":  {},
		"func":     {},
		"source":   {},
		keyTRL:     {},
		"research": {},
	}
	for _, h := range hs {
		addFacet(values["company"], h.GetOrganization())
		addFacet(values["func"], h.GetFunctionArea())
		addFacet(values["source"], h.GetSourceType())
		if h.Trl != nil {
			addFacet(values[keyTRL], strconv.Itoa(int(h.GetTrl())))
		}
		for _, tag := range h.GetTags() {
			v := strings.ToLower(strings.TrimSpace(tag))
			if v == "теоретическое исследование" || v == "практическое" {
				addFacet(values["research"], v)
			}
		}
	}
	out := make(map[string][]string, len(values))
	for key, set := range values {
		out[key] = sortedSet(set)
	}
	return out
}

func addFacet(set map[string]bool, value string) {
	v := strings.TrimSpace(value)
	low := strings.ToLower(v)
	if v == "" || v == "-" || v == "—" || low == "n/a" {
		return
	}
	set[v] = true
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

type kpiView struct {
	ID           string          `json:"id"`
	OwnerID      string          `json:"owner_id"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Metric       string          `json:"metric"`
	Unit         string          `json:"unit"`
	Direction    string          `json:"direction"`
	Baseline     *float64        `json:"baseline"`
	Target       *float64        `json:"target"`
	FunctionArea string          `json:"function_area"`
	Status       string          `json:"status"`
	Detail       json.RawMessage `json:"detail"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

func newKPIView(k *commonv1.KPI) kpiView {
	return kpiView{
		ID: k.GetId(), OwnerID: k.GetOwnerId(), Title: k.GetTitle(), Description: k.GetDescription(),
		Metric: k.GetMetric(), Unit: k.GetUnit(), Direction: k.GetDirection(), Baseline: k.Baseline,
		Target: k.Target, FunctionArea: k.GetFunctionArea(), Status: k.GetStatus(),
		Detail: rawJSON(k.GetDetail()), CreatedAt: k.GetCreatedAt(), UpdatedAt: k.GetUpdatedAt(),
	}
}

func newKPIViews(ks []*commonv1.KPI) []kpiView {
	out := make([]kpiView, 0, len(ks))
	for _, k := range ks {
		out = append(out, newKPIView(k))
	}
	return out
}

type kpiInput struct {
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Metric       string          `json:"metric"`
	Unit         string          `json:"unit"`
	Direction    string          `json:"direction"`
	Baseline     *float64        `json:"baseline"`
	Target       *float64        `json:"target"`
	FunctionArea string          `json:"function_area"`
	Status       string          `json:"status"`
	Detail       json.RawMessage `json:"detail"`
}

func (in kpiInput) toCreateRequest() *dbv1.CreateKPIRequest {
	return &dbv1.CreateKPIRequest{
		Title: in.Title, Description: in.Description, Metric: in.Metric, Unit: in.Unit,
		Direction: in.Direction, Baseline: in.Baseline, Target: in.Target,
		FunctionArea: in.FunctionArea, Detail: jsonStr(in.Detail),
	}
}

func (in kpiInput) toProto(id string) *commonv1.KPI {
	return &commonv1.KPI{
		Id: id, Title: in.Title, Description: in.Description, Metric: in.Metric, Unit: in.Unit,
		Direction: in.Direction, Baseline: in.Baseline, Target: in.Target,
		FunctionArea: in.FunctionArea, Status: in.Status, Detail: jsonStr(in.Detail),
	}
}

type kpiDocumentView struct {
	documentView
	Role       string `json:"role"`
	AttachedAt string `json:"attached_at"`
}

func newKPIDocumentView(l *dbv1.KpiDocumentLink) kpiDocumentView {
	return kpiDocumentView{
		documentView: newDocumentView(l.GetDocument()),
		Role:         l.GetRole(),
		AttachedAt:   l.GetAttachedAt(),
	}
}

// ---- Cluster ----

type clusterView struct {
	ID              string          `json:"id"`
	OwnerID         string          `json:"owner_id"`
	Label           string          `json:"label"`
	Summary         string          `json:"summary"`
	Keywords        []string        `json:"keywords"`
	Method          string          `json:"method"`
	ChunkCount      int32           `json:"chunk_count"`
	DocumentCount   int32           `json:"document_count"`
	Representatives json.RawMessage `json:"representatives"`
	Params          json.RawMessage `json:"params"`
	Status          string          `json:"status"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

func newClusterView(c *commonv1.Cluster) clusterView {
	return clusterView{
		ID: c.GetId(), OwnerID: c.GetOwnerId(), Label: c.GetLabel(), Summary: c.GetSummary(),
		Keywords: orEmptyStrings(c.GetKeywords()), Method: c.GetMethod(), ChunkCount: c.GetChunkCount(),
		DocumentCount: c.GetDocumentCount(), Representatives: rawJSON(c.GetRepresentatives()),
		Params: rawJSON(c.GetParams()), Status: c.GetStatus(),
		CreatedAt: c.GetCreatedAt(), UpdatedAt: c.GetUpdatedAt(),
	}
}

func newClusterViews(cs []*commonv1.Cluster) []clusterView {
	out := make([]clusterView, 0, len(cs))
	for _, c := range cs {
		out = append(out, newClusterView(c))
	}
	return out
}

// newClusterListViews — элементы списка GET /clusters: representatives (сниппеты)
// и тяжёлые ветки params (members, itc.components, itc.signals) отдаёт только
// GET /clusters/{id}; скаляры itc (score/band/techscore/axes) остаются.
func newClusterListViews(cs []*commonv1.Cluster) []clusterView {
	out := make([]clusterView, 0, len(cs))
	for _, c := range cs {
		v := newClusterView(c)
		v.Representatives = json.RawMessage("[]")
		v.Params = pruneJSONBranches(c.GetParams(),
			[]string{"members"}, []string{keyITC, "components"}, []string{keyITC, "signals"})
		out = append(out, v)
	}
	return out
}

type clusterInput struct {
	Label           string          `json:"label"`
	Summary         string          `json:"summary"`
	Keywords        []string        `json:"keywords"`
	Method          string          `json:"method"`
	ChunkCount      int32           `json:"chunk_count"`
	DocumentCount   int32           `json:"document_count"`
	Representatives json.RawMessage `json:"representatives"`
	Params          json.RawMessage `json:"params"`
	Status          string          `json:"status"`
}

func (in clusterInput) toCreateRequest() *dbv1.CreateClusterRequest {
	return &dbv1.CreateClusterRequest{
		Label: in.Label, Summary: in.Summary, Keywords: in.Keywords, Method: in.Method,
		ChunkCount: in.ChunkCount, DocumentCount: in.DocumentCount,
		Representatives: jsonStr(in.Representatives), Params: jsonStr(in.Params),
	}
}

func (in clusterInput) toProto(id string) *commonv1.Cluster {
	return &commonv1.Cluster{
		Id: id, Label: in.Label, Summary: in.Summary, Keywords: in.Keywords, Method: in.Method,
		ChunkCount: in.ChunkCount, DocumentCount: in.DocumentCount,
		Representatives: jsonStr(in.Representatives), Params: jsonStr(in.Params), Status: in.Status,
	}
}

// ---- Evidence & revisions ----

type evidenceView struct {
	ID             string   `json:"id"`
	DocumentID     string   `json:"document_id,omitempty"`
	ChunkID        string   `json:"chunk_id"`
	Filename       string   `json:"filename"`
	Snippet        string   `json:"snippet"`
	Stance         string   `json:"stance"`
	Score          *float64 `json:"score"`
	Ord            int32    `json:"ord"`
	Relation       string   `json:"relation,omitempty"`
	NumericSignals []string `json:"numeric_signals,omitempty"`
	PageStart      int32    `json:"page_start,omitempty"`
	PageEnd        int32    `json:"page_end,omitempty"`
	SectionHeading string   `json:"section_heading,omitempty"`
	Origin         string   `json:"origin,omitempty"`
}

// newEvidenceView prefers the model's per-fragment relation (a real "under
// conditions X — effect Y, with numbers" note); when a fragment was never
// classified it falls back to a deterministic stance+signals template, so a
// relation comment is always present.
func newEvidenceView(e *commonv1.HypothesisEvidence) evidenceView {
	signals := evidenceNumericSignals(e.GetSnippet())
	relation := strings.TrimSpace(e.GetRelation())
	if relation == "" {
		relation = evidenceRelation(e.GetStance(), signals)
	}
	return evidenceView{
		ID: e.GetId(), DocumentID: e.GetDocumentId(), ChunkID: e.GetChunkId(), Filename: e.GetFilename(),
		Snippet: e.GetSnippet(), Stance: e.GetStance(), Score: e.Score, Ord: e.GetOrd(),
		Relation: relation, NumericSignals: signals,
		PageStart: e.GetPageStart(), PageEnd: e.GetPageEnd(), SectionHeading: e.GetSectionHeading(),
		Origin: e.GetOrigin(),
	}
}

var evidenceNumberRE = regexp.MustCompile(`(?i)[+-]?[0-9]+(?:[.,][0-9]+)?\s?` +
	`(?:%|°C|K|МПа|ГПа|MPa|GPa|mAh/g|мА·ч/г|мм/год|mm/year|мкм|μm|nm|нм|h|ч|мин|циклов?)`)

func evidenceNumericSignals(snippet string) []string {
	matches := evidenceNumberRE.FindAllString(snippet, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(matches), 3))
	seen := map[string]struct{}{}
	for _, raw := range matches {
		v := strings.TrimSpace(raw)
		key := strings.ToLower(v)
		if v == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
		if len(out) == 3 {
			break
		}
	}
	return out
}

func evidenceRelation(stance string, signals []string) string {
	var base string
	switch stance {
	case "supports":
		base = "Фрагмент содержит результат или условие, которое работает в пользу заявленного эффекта."
	case "contradicts":
		base = "Фрагмент указывает на ограничение, отрицательный результат или условие, которое спорит с заявленным эффектом."
	default:
		base = "Фрагмент даёт условия, методику или материал для проверки, но сам по себе не доказывает эффект."
	}
	if len(signals) == 0 {
		return base
	}
	return base + " Численные сигналы: " + strings.Join(signals, ", ") + "."
}

func newEvidenceViews(es []*commonv1.HypothesisEvidence) []evidenceView {
	out := make([]evidenceView, 0, len(es))
	seen := map[string]struct{}{}
	for _, e := range es {
		key := strings.Join(strings.Fields(strings.ToLower(e.GetSnippet())), " ")
		if key != "" {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, newEvidenceView(e))
	}
	return out
}

type evidenceInput struct {
	DocumentID *string  `json:"document_id"`
	ChunkID    string   `json:"chunk_id"`
	Filename   string   `json:"filename"`
	Snippet    string   `json:"snippet"`
	Stance     string   `json:"stance"`
	Score      *float64 `json:"score"`
	Ord        int32    `json:"ord"`
}

type revisionView struct {
	ID           string          `json:"id"`
	HypothesisID string          `json:"hypothesis_id"`
	RevisionNo   int32           `json:"revision_no"`
	EditorID     string          `json:"editor_id"`
	Action       string          `json:"action"`
	Summary      string          `json:"summary"`
	Patch        json.RawMessage `json:"patch"`
	CreatedAt    string          `json:"created_at"`
}

func newRevisionView(r *commonv1.HypothesisRevision) revisionView {
	return revisionView{
		ID: r.GetId(), HypothesisID: r.GetHypothesisId(), RevisionNo: r.GetRevisionNo(),
		EditorID: r.GetEditorId(), Action: r.GetAction(), Summary: r.GetSummary(),
		Patch: rawJSON(r.GetPatch()), CreatedAt: r.GetCreatedAt(),
	}
}

func newRevisionViews(rs []*commonv1.HypothesisRevision) []revisionView {
	out := make([]revisionView, 0, len(rs))
	for _, r := range rs {
		out = append(out, newRevisionView(r))
	}
	return out
}

type revisionInput struct {
	Action   string          `json:"action"`
	Summary  string          `json:"summary"`
	EditorID string          `json:"editor_id"`
	Patch    json.RawMessage `json:"patch"`
}

func (in *revisionInput) toProto(hypothesisID string) *commonv1.HypothesisRevision {
	if in == nil {
		return nil
	}
	return &commonv1.HypothesisRevision{
		HypothesisId: hypothesisID, Action: in.Action, Summary: in.Summary,
		EditorId: in.EditorID, Patch: jsonStr(in.Patch),
	}
}

// ---- Hypothesis ----

type hypothesisView struct {
	ID               string          `json:"id"`
	OwnerID          string          `json:"owner_id"`
	RunID            string          `json:"run_id"`
	Title            string          `json:"title"`
	Statement        string          `json:"statement"`
	Rationale        string          `json:"rationale"`
	Method           string          `json:"method"`
	Status           string          `json:"status"`
	KPIID            string          `json:"kpi_id,omitempty"`
	PrimaryClusterID string          `json:"primary_cluster_id,omitempty"`
	TRL              *int32          `json:"trl"`
	NoveltyScore     *float64        `json:"novelty_score"`
	RiskScore        *float64        `json:"risk_score"`
	ValueScore       *float64        `json:"value_score"`
	ConfidenceScore  *float64        `json:"confidence_score"`
	CompositeScore   *float64        `json:"composite_score"`
	Measurable       bool            `json:"measurable"`
	Organization     string          `json:"organization"`
	FunctionArea     string          `json:"function_area"`
	SourceType       string          `json:"source_type"`
	Location         string          `json:"location"`
	Tags             []string        `json:"tags"`
	Assessment       json.RawMessage `json:"assessment"`
	Detail           json.RawMessage `json:"detail"`
	Generation       json.RawMessage `json:"generation"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	EvidenceCount    int             `json:"evidence_count"`
	EvidenceDocs     int             `json:"evidence_document_count"`
	EvidenceSupports int             `json:"evidence_support_count"`
	EvidenceAgainst  int             `json:"evidence_contradict_count"`
	Evidence         []evidenceView  `json:"evidence"`
}

func newHypothesisView(h *commonv1.Hypothesis) hypothesisView {
	return newHypothesisViewWithEvidence(h, true)
}

// boardAssessmentPaths — БЕЛЫЙ список веток assessment для карточки борда:
// ровно то, что читают сортировки/очереди/бейджи списка. Полный объект
// (факторы ранга, уровни TRL, компоненты ITC, тексты проверок) отдаёт только
// GET /hypotheses/{id}. Allowlist не распухает от новых веток оценки.
const (
	keyITC = "itc"
	keyTRL = "trl"
)

func boardAssessmentPaths() [][]string {
	return [][]string{
		{"ranking", "score"},
		{"learning_priority"},
		{keyITC, "score"}, {keyITC, "techscore"}, {keyITC, "band"}, {keyITC, "axes"},
		{"check", "verdict"},
		{"scores"},
		{"schema"},
		{keyTRL, "level"}, {keyTRL, "name"},
	}
}

// projectJSONBranches собирает новый JSON-объект только из перечисленных
// веток (путь — цепочка ключей); отсутствующие пути пропускаются, пустой или
// невалидный вход отдаётся как раньше (через rawJSON).
func projectJSONBranches(s string, paths ...[]string) json.RawMessage {
	var src map[string]any
	if err := json.Unmarshal([]byte(s), &src); err != nil || src == nil {
		return rawJSON(s)
	}
	dst := map[string]any{}
	for _, path := range paths {
		node, ok := any(src), true
		for _, key := range path {
			m, isMap := node.(map[string]any)
			if !isMap || m[key] == nil {
				ok = false
				break
			}
			node = m[key]
		}
		if !ok {
			continue
		}
		cur := dst
		for _, key := range path[:len(path)-1] {
			next, isMap := cur[key].(map[string]any)
			if !isMap {
				next = map[string]any{}
				cur[key] = next
			}
			cur = next
		}
		cur[path[len(path)-1]] = node
	}
	out, err := json.Marshal(dst)
	if err != nil {
		return rawJSON(s)
	}
	return out
}

// newHypothesisBoardItemView — карточка борда: detail не отдаётся, generation
// усечён до скаляров, assessment спроецирован белым списком (см.
// boardAssessmentPaths); полные объекты — только GET /hypotheses/{id}.
func newHypothesisBoardItemView(h *commonv1.Hypothesis) hypothesisView {
	v := newHypothesisViewWithEvidence(h, false)
	v.Detail = rawJSON("")
	v.Generation = generationRef(h.GetGeneration())
	v.Assessment = projectJSONBranches(h.GetAssessment(), boardAssessmentPaths()...)
	return v
}

func newHypothesisViewWithEvidence(h *commonv1.Hypothesis, includeEvidence bool) hypothesisView {
	evidence := h.GetEvidence()
	evidenceViews := []evidenceView{}
	if includeEvidence {
		evidenceViews = newEvidenceViews(evidence)
	}
	total, docs, supports, contradicts := hypothesisEvidenceStats(evidence)
	return hypothesisView{
		ID: h.GetId(), OwnerID: h.GetOwnerId(), RunID: h.GetRunId(), Title: h.GetTitle(),
		Statement: h.GetStatement(), Rationale: h.GetRationale(), Method: h.GetMethod(), Status: h.GetStatus(),
		KPIID: h.GetKpiId(), PrimaryClusterID: h.GetPrimaryClusterId(), TRL: h.Trl,
		NoveltyScore: h.NoveltyScore, RiskScore: h.RiskScore, ValueScore: h.ValueScore,
		ConfidenceScore: h.ConfidenceScore, CompositeScore: h.CompositeScore, Measurable: h.GetMeasurable(),
		Organization: h.GetOrganization(), FunctionArea: h.GetFunctionArea(), SourceType: h.GetSourceType(),
		Location: h.GetLocation(), Tags: orEmptyStrings(h.GetTags()), Assessment: rawJSON(h.GetAssessment()),
		Detail: rawJSON(h.GetDetail()), Generation: rawJSON(h.GetGeneration()),
		CreatedAt: h.GetCreatedAt(), UpdatedAt: h.GetUpdatedAt(),
		EvidenceCount: total, EvidenceDocs: docs, EvidenceSupports: supports, EvidenceAgainst: contradicts,
		Evidence: evidenceViews,
	}
}

func newHypothesisViews(hs []*commonv1.Hypothesis) []hypothesisView {
	out := make([]hypothesisView, 0, len(hs))
	for _, h := range hs {
		out = append(out, newHypothesisView(h))
	}
	return out
}

func newHypothesisBoardItemViews(hs []*commonv1.Hypothesis) []hypothesisView {
	out := make([]hypothesisView, 0, len(hs))
	for _, h := range hs {
		out = append(out, newHypothesisBoardItemView(h))
	}
	return out
}

// hypothesisRefView — лёгкий элемент GET /hypotheses?view=ref: страницам,
// считающим гипотезы (цели), полные объекты не нужны. generation усечён до
// скаляров, по которым фронт отделяет направления и черновики.
type hypothesisRefView struct {
	ID         string          `json:"id"`
	KPIID      string          `json:"kpi_id,omitempty"`
	Status     string          `json:"status"`
	Title      string          `json:"title"`
	Generation json.RawMessage `json:"generation"`
}

func newHypothesisRefViews(hs []*commonv1.Hypothesis) []hypothesisRefView {
	out := make([]hypothesisRefView, 0, len(hs))
	for _, h := range hs {
		out = append(out, hypothesisRefView{
			ID: h.GetId(), KPIID: h.GetKpiId(), Status: h.GetStatus(), Title: h.GetTitle(),
			Generation: generationRef(h.GetGeneration()),
		})
	}
	return out
}

func hypothesisEvidenceStats(evidence []*commonv1.HypothesisEvidence) (total, docs, supports, contradicts int) {
	docSet := map[string]bool{}
	for _, e := range evidence {
		if e == nil {
			continue
		}
		total++
		switch strings.ToLower(strings.TrimSpace(e.GetStance())) {
		case "supports":
			supports++
		case "contradicts":
			contradicts++
		}
		docKey := strings.TrimSpace(e.GetDocumentId())
		if docKey == "" {
			docKey = strings.TrimSpace(e.GetFilename())
		}
		if docKey != "" {
			docSet[docKey] = true
		}
	}
	return total, len(docSet), supports, contradicts
}

type hypothesisInput struct {
	RunID            string          `json:"run_id"`
	Title            string          `json:"title"`
	Statement        string          `json:"statement"`
	Rationale        string          `json:"rationale"`
	Method           string          `json:"method"`
	Status           string          `json:"status"`
	KPIID            *string         `json:"kpi_id"`
	PrimaryClusterID *string         `json:"primary_cluster_id"`
	TRL              *int32          `json:"trl"`
	NoveltyScore     *float64        `json:"novelty_score"`
	RiskScore        *float64        `json:"risk_score"`
	ValueScore       *float64        `json:"value_score"`
	ConfidenceScore  *float64        `json:"confidence_score"`
	CompositeScore   *float64        `json:"composite_score"`
	Measurable       bool            `json:"measurable"`
	Organization     string          `json:"organization"`
	FunctionArea     string          `json:"function_area"`
	SourceType       string          `json:"source_type"`
	Location         string          `json:"location"`
	Tags             []string        `json:"tags"`
	Assessment       json.RawMessage `json:"assessment"`
	Detail           json.RawMessage `json:"detail"`
	Generation       json.RawMessage `json:"generation"`
	Evidence         []evidenceInput `json:"evidence"`
	Revision         *revisionInput  `json:"revision"`
}

func (in hypothesisInput) toProto(id string) *commonv1.Hypothesis {
	h := &commonv1.Hypothesis{
		Id: id, RunId: in.RunID, Title: in.Title, Statement: in.Statement, Rationale: in.Rationale,
		Method: in.Method, Status: in.Status, KpiId: nilIfEmpty(in.KPIID), PrimaryClusterId: nilIfEmpty(in.PrimaryClusterID),
		Trl: in.TRL, NoveltyScore: in.NoveltyScore, RiskScore: in.RiskScore, ValueScore: in.ValueScore,
		ConfidenceScore: in.ConfidenceScore, CompositeScore: in.CompositeScore, Measurable: in.Measurable,
		Organization: in.Organization, FunctionArea: in.FunctionArea, SourceType: in.SourceType,
		Location: in.Location, Tags: in.Tags, Assessment: jsonStr(in.Assessment),
		Detail: jsonStr(in.Detail), Generation: jsonStr(in.Generation),
	}
	for _, e := range in.Evidence {
		h.Evidence = append(h.Evidence, &commonv1.HypothesisEvidence{
			DocumentId: nilIfEmpty(e.DocumentID), ChunkId: e.ChunkID, Filename: e.Filename, Snippet: e.Snippet,
			Stance: e.Stance, Score: e.Score, Ord: e.Ord,
		})
	}
	return h
}

// ---- Citation graph ----

type graphNodeView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Status  string `json:"status,omitempty"`
	TRL     *int32 `json:"trl,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Degree  int    `json:"degree,omitempty"`
	KPIID   string `json:"kpi_id,omitempty"`
}

type graphEdgeView struct {
	Source  string   `json:"source"`
	Target  string   `json:"target"`
	Class   string   `json:"class"`
	Score   *float64 `json:"score,omitempty"`
	ChunkID string   `json:"chunk_id,omitempty"`
}

type graphView struct {
	Nodes []graphNodeView `json:"nodes"`
	Edges []graphEdgeView `json:"edges"`
}

func newGraphView(g *application.HypothesisGraph) graphView {
	v := graphView{
		Nodes: make([]graphNodeView, 0, len(g.Nodes)),
		Edges: make([]graphEdgeView, 0, len(g.Edges)),
	}
	for _, n := range g.Nodes {
		v.Nodes = append(v.Nodes, graphNodeView{
			ID: n.ID, Kind: n.Kind, Label: n.Label, Status: n.Status,
			TRL: n.TRL, Verdict: n.Verdict, Degree: n.Degree, KPIID: n.KPIID,
		})
	}
	for _, e := range g.Edges {
		v.Edges = append(v.Edges, graphEdgeView{
			Source: e.Source, Target: e.Target, Class: e.Class, Score: e.Score, ChunkID: e.ChunkID,
		})
	}
	return v
}
