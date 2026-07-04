package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/example/main-service/internal/platform/httpx"
	"github.com/example/main-service/internal/platform/jsonx"

	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

// hypothesisLifecycleOps are the operations whose usage is attributed to a
// single hypothesis for the "≈ per hypothesis" estimate.
func hypothesisLifecycleOps() []string { return []string{"generate", "verify", "enrich", "assess_trl"} }

const (
	// Дефолты фри-тарифа OpenRouter; для других провайдеров лимиты задаются
	// env LLM_DAILY_LIMIT / LLM_PER_MIN_LIMIT (0 = лимита нет, UI прячет шкалу).
	// У Yandex AI Studio суточных/минутных квот нет — только 10 одновременных
	// генераций, поэтому там оба лимита нулевые.
	freeDailyLimit  = 1000
	freePerMinLimit = 20
	// dayFmt is the wire date format for the usage ledger ("YYYY-MM-DD").
	dayFmt = "2006-01-02"
)

// quotaLimits reads the display limits: explicit env wins; the OpenRouter
// free-tier defaults apply only to an OpenRouter ":free" model — paid models
// have no request quotas, so the gauges are hidden.
func quotaLimits(vllmURL, model string) (daily, perMin int) {
	daily, _ = strconv.Atoi(os.Getenv("LLM_DAILY_LIMIT"))
	perMin, _ = strconv.Atoi(os.Getenv("LLM_PER_MIN_LIMIT"))
	if daily == 0 && perMin == 0 &&
		strings.Contains(vllmURL, "openrouter.ai") && strings.HasSuffix(model, ":free") {
		return freeDailyLimit, freePerMinLimit
	}
	return daily, perMin
}

type luRow struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type luDay struct {
	Day string `json:"day"`
	luRow
}
type luModel struct {
	Model string `json:"model"`
	luRow
}
type luOp struct {
	Operation string `json:"operation"`
	luRow
}
type luPerHyp struct {
	Hypotheses  int      `json:"hypotheses"`
	Requests    float64  `json:"requests"`
	TotalTokens float64  `json:"total_tokens"`
	CostUSD     float64  `json:"cost_usd"`
	CostRub     float64  `json:"cost_rub"`
	Operations  []string `json:"operations"`
}

// normalizeModel collapses OpenRouter's dated permaslug (…-20230311:free) back to
// the base slug (…:free) so one model is not split across near-identical rows.
func normalizeModel(m string) string {
	base, tier := m, ""
	if i := strings.LastIndex(m, ":"); i >= 0 {
		base, tier = m[:i], m[i:]
	}
	if i := strings.LastIndex(base, "-"); i >= 0 && isDate8(base[i+1:]) {
		base = base[:i]
	}
	return base + tier
}

// isDate8 reports whether s is exactly 8 digits (a YYYYMMDD permaslug stamp).
func isDate8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type luBudget struct {
	CreditsTotal     float64 `json:"credits_total"`
	CreditsUsed      float64 `json:"credits_used"`
	CreditsRemaining float64 `json:"credits_remaining"`
}

// modelPrice прайсит модели по подстроке имени: у каждой модели свой тариф
// (deepseek-v4-flash 300/500 ₽ за 1M ≠ qwen 500/500), а явный 0/0 помечает
// известно-бесплатные модели, чтобы фолбэк их не переоценивал.
type modelPrice struct {
	match           string
	promptPer1M     float64
	completionPer1M float64
}

// llmPricing is the read-side price list (per 1M tokens, provider currency)
// for backends that do not report usage.cost (e.g. Yandex AI Studio).
// models берётся из LLM_PRICES; пара promptPer1M/completionPer1M — фолбэк
// для моделей вне списка.
type llmPricing struct {
	promptPer1M     float64
	completionPer1M float64
	currency        string
	models          []modelPrice
}

// pricingFrom reads LLM_PRICES ("substr=in/out;substr=in/out", за 1M), фолбэк
// LLM_PRICE_*_PER_1M и LLM_COST_CURRENCY (default "₽") через get — оверрайд из
// админ-настроек → env → пусто, поэтому прайс меняется без редеплоя.
func pricingFrom(get func(string) string) llmPricing {
	p := llmPricing{currency: "₽"}
	if c := strings.TrimSpace(get("LLM_COST_CURRENCY")); c != "" {
		p.currency = c
	}
	p.promptPer1M, _ = strconv.ParseFloat(get("LLM_PRICE_PROMPT_PER_1M"), 64)
	p.completionPer1M, _ = strconv.ParseFloat(get("LLM_PRICE_COMPLETION_PER_1M"), 64)
	for _, entry := range strings.Split(get("LLM_PRICES"), ";") {
		name, rates, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok {
			continue
		}
		in, out, ok := strings.Cut(rates, "/")
		if !ok {
			continue
		}
		mp := modelPrice{match: strings.ToLower(strings.TrimSpace(name))}
		mp.promptPer1M, _ = strconv.ParseFloat(strings.TrimSpace(in), 64)
		mp.completionPer1M, _ = strconv.ParseFloat(strings.TrimSpace(out), 64)
		if mp.match != "" {
			p.models = append(p.models, mp)
		}
	}
	return p
}

func (p llmPricing) enabled() bool {
	return p.promptPer1M > 0 || p.completionPer1M > 0 || len(p.models) > 0
}

// rates resolves the per-1M prices for a model: первый матч по подстроке из
// LLM_PRICES (включая явный 0/0 для бесплатных), иначе фолбэк-пара.
func (p llmPricing) rates(model string) (in, out float64) {
	m := strings.ToLower(model)
	for _, mp := range p.models {
		if strings.Contains(m, mp.match) {
			return mp.promptPer1M, mp.completionPer1M
		}
	}
	return p.promptPer1M, p.completionPer1M
}

// estimateCostNano returns the ledger cost as-is when the provider reported it
// (or no price list is set); otherwise prices the tokens by the row's model and
// reports the value as an estimate. Nano-units of the provider currency.
func estimateCostNano(costNano, promptTokens, completionTokens int64, model string, p llmPricing) (int64, bool) {
	if costNano != 0 || !p.enabled() {
		return costNano, false
	}
	in, out := p.rates(model)
	est := int64((float64(promptTokens)*in + float64(completionTokens)*out) / 1e6 * 1e9)
	return est, est > 0
}

// currencyFor picks the dashboard currency: OpenRouter reports usage.cost in
// USD, other backends are estimated with the env price list currency.
func currencyFor(base, envCurrency string) string {
	if strings.Contains(base, "openrouter.ai") {
		return "$"
	}
	return envCurrency
}

// providerName maps VLLM_URL to a human label for the dashboard.
func providerName(base string) string {
	switch {
	case base == "":
		return ""
	case strings.Contains(base, "llm.api.cloud.yandex.net"):
		return "Yandex AI Studio"
	case strings.Contains(base, "openrouter.ai"):
		return "OpenRouter"
	case strings.Contains(base, "llama-server"):
		return "локальный LLM"
	}
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// kind of a provider's spend: real money (OpenRouter USD), notional (Yandex on
// the organizer key — estimated rubles, nothing actually paid) or free (local).
const (
	kindReal     = "real"
	kindNotional = "notional"
	kindFree     = "free"
)

type providerInfo struct {
	key      string
	label    string
	currency string
	kind     string
}

type providerRule struct {
	match string
	providerInfo
}

// providersFrom reads LLM_PROVIDERS ("substr=key:label:currency:kind;…") via get
// — подстрочная классификация строки model в провайдера для разбивки расхода.
func providersFrom(get func(string) string) []providerRule {
	var rules []providerRule
	for _, entry := range strings.Split(get("LLM_PROVIDERS"), ";") {
		name, spec, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok {
			continue
		}
		p := strings.Split(spec, ":")
		if len(p) < 4 {
			continue
		}
		r := providerRule{match: strings.ToLower(strings.TrimSpace(name))}
		r.key, r.label = strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		r.currency, r.kind = strings.TrimSpace(p[2]), strings.TrimSpace(p[3])
		if r.match != "" && r.kind != "" {
			rules = append(rules, r)
		}
	}
	return rules
}

// classifyProvider maps a ledger row (by model string + reported cost) to its
// provider bucket. LLM_PROVIDERS rules win; иначе — дефолты стенда: Yandex по
// имени модели (условный, ключ организаторов), реальный usage.cost → OpenRouter,
// hosted-слаг без цены → OpenRouter free, прочее → локальная модель (бесплатно).
func classifyProvider(model string, costNano int64, rules []providerRule, envCurrency string) providerInfo {
	m := strings.ToLower(model)
	for _, r := range rules {
		if strings.Contains(m, r.match) {
			return r.providerInfo
		}
	}
	switch {
	case strings.Contains(m, "yandexgpt") || strings.Contains(m, "gpt://"):
		return providerInfo{key: "yandex", label: "Yandex AI Studio", currency: envCurrency, kind: kindNotional}
	case costNano > 0 || strings.Contains(m, "/"):
		// OpenRouter is a real-money provider; a hosted slug that reported no
		// cost (a :free model or a call OpenRouter did not price) is real $0, not
		// "free" — so its rows never split the bucket into a bogus free kind.
		return providerInfo{key: "openrouter", label: "OpenRouter", currency: "$", kind: kindReal}
	default:
		return providerInfo{key: "local", label: "локальный LLM", currency: "", kind: kindFree}
	}
}

// kindRank orders the provider cards: real spend first, notional next, free last.
func kindRank(kind string) int {
	switch kind {
	case kindReal:
		return 0
	case kindNotional:
		return 1
	default:
		return 2
	}
}

// rubPerUsdFrom reads LLM_RUB_PER_USD (USD→RUB rate for the unified ruble total
// on the dashboard) via get; defaults to 90 when unset/invalid.
func rubPerUsdFrom(get func(string) string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(get("LLM_RUB_PER_USD")), 64); err == nil && v > 0 {
		return v
	}
	return 90
}

// rubNanoOf converts a nano-cost in the provider currency to nano-rubles: USD is
// scaled by the FX rate, rubles (and free/zero) pass through unchanged.
func rubNanoOf(costNano int64, currency string, rubPerUsd float64) int64 {
	if currency == "$" {
		return int64(float64(costNano) * rubPerUsd)
	}
	return costNano
}

type luQuota struct {
	TodayRequests int64 `json:"today_requests"`
	DailyLimit    int   `json:"daily_limit"`
	PerMinUsed    int64 `json:"per_min_used"`
	PerMinLimit   int   `json:"per_min_limit"`
}

// luProvider is one provider's slice of the spend, priced in its own currency:
// OpenRouter reports real USD, Yandex is a notional ruble estimate (organizer
// key — no money spent), a local engine is free. Costs are never summed across
// providers.
type luProvider struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	Currency      string  `json:"currency"`
	Kind          string  `json:"kind"`
	CostEstimated bool    `json:"cost_estimated"`
	CostRub       float64 `json:"cost_rub"`
	luRow
}

type llmUsageResponse struct {
	RangeDays     int          `json:"range_days"`
	From          string       `json:"from"`
	To            string       `json:"to"`
	Provider      string       `json:"provider"`
	Currency      string       `json:"currency"`
	CostEstimated bool         `json:"cost_estimated"`
	TotalCostRub  float64      `json:"total_cost_rub"`
	RubPerUsd     float64      `json:"rub_per_usd"`
	Totals        luRow        `json:"totals"`
	Providers     []luProvider `json:"providers"`
	ByDay         []luDay      `json:"by_day"`
	ByModel       []luModel    `json:"by_model"`
	ByOperation   []luOp       `json:"by_operation"`
	PerHypothesis luPerHyp     `json:"per_hypothesis"`
	Budget        luBudget     `json:"budget"`
	Quota         luQuota      `json:"quota"`
}

type usageAcc struct{ req, pt, ct, cost int64 }

func (a usageAcc) row() luRow {
	return luRow{
		Requests:         a.req,
		PromptTokens:     a.pt,
		CompletionTokens: a.ct,
		TotalTokens:      a.pt + a.ct,
		CostUSD:          float64(a.cost) / 1e9,
	}
}

// usageAggregate is the bucketed ledger for one response. Displayed money
// figures (totals, per-day/model/op, per-hypothesis, budget) carry real spend
// only; byProvider holds each provider's cost in its own currency.
type usageAggregate struct {
	totals, perHyp       usageAcc
	byDay, byModel, byOp map[string]*usageAcc
	byProvider           map[string]*usageAcc
	provMeta             map[string]providerInfo
	provEstimated        map[string]bool
	perHypByOp           map[string]int64
	allTimeRealCostNano  int64
	totalRubNano         int64
	perHypRubNano        int64
	estimated            bool
}

// aggregateUsage classifies each row by provider and prices it per kind — real
// USD as reported, notional rubles estimated from the price list, free = 0 — so
// currencies are never mixed. It also rolls every provider up into a unified
// ruble total (USD scaled by rubPerUsd). Rows before fromStr feed only the
// budget sum.
func aggregateUsage(rows []*dbv1.LLMUsageDailyRow, pricing llmPricing, rules []providerRule, rubPerUsd float64, fromStr string) usageAggregate {
	agg := usageAggregate{
		byDay:         map[string]*usageAcc{},
		byModel:       map[string]*usageAcc{},
		byOp:          map[string]*usageAcc{},
		byProvider:    map[string]*usageAcc{},
		provMeta:      map[string]providerInfo{},
		provEstimated: map[string]bool{},
		perHypByOp:    map[string]int64{},
	}
	lifecycle := map[string]bool{}
	for _, op := range hypothesisLifecycleOps() {
		lifecycle[op] = true
	}
	add := func(m map[string]*usageAcc, k string, row *dbv1.LLMUsageDailyRow, costNano int64) {
		a := m[k]
		if a == nil {
			a = &usageAcc{}
			m[k] = a
		}
		a.req += row.GetRequests()
		a.pt += row.GetPromptTokens()
		a.ct += row.GetCompletionTokens()
		a.cost += costNano
	}
	for _, row := range rows {
		info := classifyProvider(row.GetModel(), row.GetCostNanoUsd(), rules, pricing.currency)
		var realCost int64
		if info.kind == kindReal {
			realCost = row.GetCostNanoUsd()
		}
		agg.allTimeRealCostNano += realCost
		if row.GetDay() < fromStr { // budget-only tail before the visible range
			continue
		}
		provCost := realCost
		if info.kind == kindNotional {
			est, estimated := estimateCostNano(
				row.GetCostNanoUsd(), row.GetPromptTokens(), row.GetCompletionTokens(), row.GetModel(), pricing)
			provCost = est
			if estimated {
				agg.estimated = true
				agg.provEstimated[info.label] = true
			}
		}
		rubNano := rubNanoOf(provCost, info.currency, rubPerUsd)
		agg.totalRubNano += rubNano
		if _, ok := agg.provMeta[info.label]; !ok {
			agg.provMeta[info.label] = info
		}
		add(agg.byProvider, info.label, row, provCost)
		agg.totals.req += row.GetRequests()
		agg.totals.pt += row.GetPromptTokens()
		agg.totals.ct += row.GetCompletionTokens()
		agg.totals.cost += realCost
		add(agg.byDay, row.GetDay(), row, realCost)
		add(agg.byModel, normalizeModel(row.GetModel()), row, realCost)
		add(agg.byOp, row.GetOperation(), row, realCost)
		if lifecycle[row.GetOperation()] {
			agg.perHyp.req += row.GetRequests()
			agg.perHyp.pt += row.GetPromptTokens()
			agg.perHyp.ct += row.GetCompletionTokens()
			agg.perHyp.cost += realCost
			agg.perHypRubNano += rubNano
			agg.perHypByOp[row.GetOperation()] += row.GetRequests()
		}
	}
	return agg
}

// getLLMUsage serves the LLM usage & budget for the Metrics dashboard. It
// flushes the hot Valkey window into Postgres, aggregates the durable ledger
// for the range (pricing zero-cost rows by the env price list on read), and
// overlays live quota + the provider budget (env total or OpenRouter credits).
func (a *API) getLLMUsage(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin", "operator") {
		return
	}
	days := 7
	if r.URL.Query().Get("days") == "30" {
		days = 30
	}
	get := func(k string) string { return a.ovr.Get(r.Context(), k, "") }
	pricing := pricingFrom(get)
	rules := providersFrom(get)
	rubPerUsd := rubPerUsdFrom(get)
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -(days - 1))
	llmBase := a.ovr.Get(r.Context(), "VLLM_URL", "")
	dailyLimit, perMinLimit := quotaLimits(llmBase, a.ovr.Get(r.Context(), "VLLM_MODEL", ""))
	resp := llmUsageResponse{
		RangeDays:     days,
		From:          from.Format(dayFmt),
		To:            now.Format(dayFmt),
		Provider:      providerName(llmBase),
		Currency:      currencyFor(llmBase, pricing.currency),
		Providers:     []luProvider{},
		ByDay:         []luDay{},
		ByModel:       []luModel{},
		ByOperation:   []luOp{},
		PerHypothesis: luPerHyp{Operations: hypothesisLifecycleOps()},
		Quota:         luQuota{DailyLimit: dailyLimit, PerMinLimit: perMinLimit},
	}
	if a.usage == nil {
		httpx.JSON(w, http.StatusOK, resp)
		return
	}

	// With an env budget the whole ledger is listed (budget "used" counts from
	// day one); rows before the visible range only feed the budget sum.
	budgetTotal, _ := strconv.ParseFloat(get("LLM_BUDGET_TOTAL"), 64)
	listFrom := from
	if budgetTotal > 0 {
		listFrom = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	rows, err := a.usage.List(r.Context(), listFrom, now)
	if err != nil {
		a.fail(w, err)
		return
	}

	agg := aggregateUsage(rows, pricing, rules, rubPerUsd, from.Format(dayFmt))
	resp.CostEstimated = agg.estimated
	resp.Totals = agg.totals.row()
	resp.TotalCostRub = float64(agg.totalRubNano) / 1e9
	resp.RubPerUsd = rubPerUsd

	for label, acc := range agg.byProvider {
		info := agg.provMeta[label]
		resp.Providers = append(resp.Providers, luProvider{
			Key: info.key, Label: info.label, Currency: info.currency, Kind: info.kind,
			CostEstimated: agg.provEstimated[label],
			CostRub:       float64(rubNanoOf(acc.cost, info.currency, rubPerUsd)) / 1e9,
			luRow:         acc.row(),
		})
	}
	sort.Slice(resp.Providers, func(i, j int) bool {
		if ri, rj := kindRank(resp.Providers[i].Kind), kindRank(resp.Providers[j].Kind); ri != rj {
			return ri < rj
		}
		return resp.Providers[i].Requests > resp.Providers[j].Requests
	})

	for day, acc := range agg.byDay {
		resp.ByDay = append(resp.ByDay, luDay{Day: day, luRow: acc.row()})
	}
	sort.Slice(resp.ByDay, func(i, j int) bool { return resp.ByDay[i].Day < resp.ByDay[j].Day })
	for model, acc := range agg.byModel {
		resp.ByModel = append(resp.ByModel, luModel{Model: model, luRow: acc.row()})
	}
	sort.Slice(resp.ByModel, func(i, j int) bool { return resp.ByModel[i].Requests > resp.ByModel[j].Requests })
	for op, acc := range agg.byOp {
		resp.ByOperation = append(resp.ByOperation, luOp{Operation: op, luRow: acc.row()})
	}
	sort.Slice(resp.ByOperation, func(i, j int) bool { return resp.ByOperation[i].Requests > resp.ByOperation[j].Requests })

	// per-hypothesis: lifecycle spend attributed across the hypotheses that
	// actually moved through the pipeline this period. Dividing by the full
	// corpus (mostly older hypotheses with no recent activity) understates the
	// unit cost toward zero, so the denominator is the busiest lifecycle stage's
	// request count — a proxy for the distinct hypotheses touched in the window.
	var processed int64 = 1
	for _, c := range agg.perHypByOp {
		if c > processed {
			processed = c
		}
	}
	resp.PerHypothesis.Hypotheses = int(processed)
	resp.PerHypothesis.Requests = float64(agg.perHyp.req) / float64(processed)
	resp.PerHypothesis.TotalTokens = float64(agg.perHyp.pt+agg.perHyp.ct) / float64(processed)
	resp.PerHypothesis.CostUSD = float64(agg.perHyp.cost) / 1e9 / float64(processed)
	resp.PerHypothesis.CostRub = float64(agg.perHypRubNano) / 1e9 / float64(processed)

	resp.Quota.TodayRequests = a.usage.TodayRequests(r.Context())
	resp.Quota.PerMinUsed = a.usage.ReqPerMinute(r.Context())
	if budgetTotal > 0 {
		used := float64(agg.allTimeRealCostNano) / 1e9
		resp.Budget = luBudget{CreditsTotal: budgetTotal, CreditsUsed: used, CreditsRemaining: budgetTotal - used}
	} else {
		resp.Budget = openRouterCredits(r.Context(), llmBase, a.ovr.Get(r.Context(), "VLLM_API_KEY", ""))
	}

	httpx.JSON(w, http.StatusOK, resp)
}

// openRouterCredits reads the account credit balance. Called only for an
// openrouter.ai backend; best-effort — any error yields zeros so the endpoint
// never fails.
func openRouterCredits(ctx context.Context, base, key string) luBudget {
	var b luBudget
	if key == "" || !strings.Contains(base, "openrouter.ai") {
		return b
	}
	url := strings.TrimRight(base, "/") + "/v1/credits"
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return b
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return b
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return b
	}
	var body struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if derr := jsonx.NewDecoder(resp.Body).Decode(&body); derr != nil {
		return b
	}
	b.CreditsTotal = body.Data.TotalCredits
	b.CreditsUsed = body.Data.TotalUsage
	b.CreditsRemaining = body.Data.TotalCredits - body.Data.TotalUsage
	return b
}
