package httpapi

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/platform/httpx"
	"github.com/example/main-service/internal/platform/jsonx"
)

// stageLatency is a per-stage p50/p95 duration in seconds.
type stageLatency struct {
	P50 *float64 `json:"p50,omitempty"`
	P95 *float64 `json:"p95,omitempty"`
}

// perfResponse is the live pipeline-performance snapshot for the Metrics panel.
// Both maps come straight from Prometheus histograms over real traffic — the
// query path (rag_stage_duration_seconds by stage) and the document-ingestion
// path (rag_processing_duration_seconds by queue). No evaluation dataset is
// involved, so these are real latencies rather than scored quality.
// HypothesisJobs adds full durable-job durations (start→finish) per kind over
// the last day — the metric behind «первичный набор гипотез за минуты».
type perfResponse struct {
	Pipeline       map[string]*stageLatency        `json:"pipeline"`
	Ingestion      map[string]*stageLatency        `json:"ingestion"`
	HypothesisJobs []application.HypothesisJobStat `json:"hypothesis_jobs"`
}

const perfJobsWindow = 24 * time.Hour

// getPerf serves live per-stage latency from Prometheus. Best-effort: if
// Prometheus is unreachable or has no samples the maps are simply empty, so the
// panel degrades to "no data yet" rather than erroring.
func (a *API) getPerf(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin", "operator") {
		return
	}
	resp := perfResponse{
		Pipeline:  promStages(r.Context(), "rag_stage_duration_seconds", "stage"),
		Ingestion: promStages(r.Context(), "rag_processing_duration_seconds", "queue"),
	}
	jobs := a.hypJobs.ListRecentAll(r.Context(), 0)
	resp.HypothesisJobs = application.SummarizeHypothesisJobs(jobs, time.Now(), perfJobsWindow)
	httpx.JSON(w, http.StatusOK, resp)
}

// promURL is the Prometheus base URL; defaults to the compose service name.
func promURL() string {
	if v := os.Getenv("PROM_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://prometheus:9090"
}

// promStages returns p50/p95 for a histogram metric grouped by the given label.
// It mirrors eval-service's Prometheus reader so latency no longer depends on an
// evaluation run to be refreshed.
func promStages(ctx context.Context, metric, group string) map[string]*stageLatency {
	out := map[string]*stageLatency{}
	for _, q := range []struct {
		quant float64
		set   func(*stageLatency, float64)
	}{
		{0.5, func(s *stageLatency, v float64) { s.P50 = &v }},
		{0.95, func(s *stageLatency, v float64) { s.P95 = &v }},
	} {
		promql := "histogram_quantile(" + strconv.FormatFloat(q.quant, 'f', -1, 64) +
			", sum by (" + group + ", le) (" + metric + "_bucket))"
		for label, v := range promQuery(ctx, promql, group) {
			s := out[label]
			if s == nil {
				s = &stageLatency{}
				out[label] = s
			}
			q.set(s, v)
		}
	}
	return out
}

// promQuery runs an instant PromQL query and returns {labelValue: sampleValue},
// skipping NaN samples. Any transport/parse error yields an empty map.
func promQuery(ctx context.Context, promql, group string) map[string]float64 {
	res := map[string]float64{}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	u := promURL() + "/api/v1/query?query=" + url.QueryEscape(promql)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		return res
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return res
	}
	var body struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if derr := jsonx.NewDecoder(resp.Body).Decode(&body); derr != nil {
		return res
	}
	for _, r := range body.Data.Result {
		if len(r.Value) != 2 {
			continue
		}
		s, ok := r.Value[1].(string)
		if !ok {
			continue
		}
		v, perr := strconv.ParseFloat(s, 64)
		if perr != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		key := r.Metric[group]
		if key == "" {
			key = "?"
		}
		res[key] = math.Round(v*1000) / 1000
	}
	return res
}
