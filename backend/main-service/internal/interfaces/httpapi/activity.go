package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/main-service/internal/platform/httpx"
)

// workerActivityView is one worker's heartbeat. All fields except Name are
// pointers so a worker that has never reported serialises as nulls rather
// than misleading zeros.
type workerActivityView struct {
	Name          string  `json:"name"`
	State         *string `json:"state"`
	Epoch         *int64  `json:"epoch"`
	ProgressDone  *int64  `json:"progress_done"`
	ProgressTotal *int64  `json:"progress_total"`
	UpdatedAt     *string `json:"updated_at"`
	LastError     *string `json:"last_error"`
	// LastRunSeconds is the wall time of the worker's most recent completed
	// run (running -> idle), as published in its heartbeat.
	LastRunSeconds *float64 `json:"last_run_seconds"`
	// LastSuccessAt is when the worker last finished a run cleanly; it tells the
	// UI how fresh the board is, distinct from UpdatedAt (last heartbeat).
	LastSuccessAt *string `json:"last_success_at"`
}

// systemActivityView is the /system/activity payload: the shared corpus epoch,
// per-worker heartbeats and the last corpus epoch each pipeline processed.
type systemActivityView struct {
	CorpusEpoch *int64               `json:"corpus_epoch"`
	Workers     []workerActivityView `json:"workers"`
	LastEpochs  map[string]*int64    `json:"last_epochs"`
}

// systemActivity reports the background-pipeline heartbeat for the activity
// panel. Available to every signed-in role. Reads are best-effort: a missing
// key (worker never ran) or a Valkey blip yields nulls, never an error.
func (a *API) systemActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := ownerFromContext(r); !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	// The surfaced workers, in display order. Each may publish a JSON heartbeat
	// under rag:worker:<name>:status; itc and eval additionally keep a
	// plain-text rag:<name>:last_status that serves as the state fallback.
	workers := []string{"clusters", "discovery", "raptor", keyITC, "eval"}
	ctx := r.Context()
	view := systemActivityView{
		CorpusEpoch: a.activityInt(ctx, "rag:corpus_epoch:shared"),
		Workers:     make([]workerActivityView, 0, len(workers)),
		LastEpochs: map[string]*int64{
			"clusters":  a.activityInt(ctx, "rag:clusters:last_epoch"),
			"discovery": a.activityInt(ctx, "rag:discovery:last_epoch"),
			"raptor":    a.activityInt(ctx, "rag:raptor:last_epoch"),
		},
	}
	for _, name := range workers {
		ws := workerActivityView{Name: name}
		if raw, found, err := a.metrics.Get(ctx, "rag:worker:"+name+":status"); err == nil && found {
			_ = json.Unmarshal([]byte(raw), &ws)
			ws.Name = name
		}
		// itc/eval publish a one-line last_status; use it when no heartbeat exists.
		if ws.State == nil && (name == keyITC || name == "eval") {
			if raw, found, err := a.metrics.Get(ctx, "rag:"+name+":last_status"); err == nil && found {
				s := strings.TrimSpace(raw)
				ws.State = &s
			}
		}
		view.Workers = append(view.Workers, ws)
	}
	httpx.JSON(w, http.StatusOK, view)
}

// activityInt reads an integer Valkey key, returning nil when the key is
// absent, unreadable or not a number.
func (a *API) activityInt(ctx context.Context, key string) *int64 {
	raw, found, err := a.metrics.Get(ctx, key)
	if err != nil || !found {
		return nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}
