// KPI prompt parsing endpoint: the unified create-goal dialog sends one
// free-text prompt; the response pre-fills the structured goal. Best-effort by
// design — the application layer falls back to a deterministic split, so this
// never returns a 500 for LLM trouble.

package httpapi

import (
	"net/http"
	"strings"

	"github.com/example/main-service/internal/platform/httpx"
)

type kpiParseRequest struct {
	Prompt string `json:"prompt"`
}

func (a *API) parseKPIPrompt(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	var req kpiParseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		httpx.Error(w, http.StatusBadRequest, "prompt is required")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"kpi": a.hypotheses.ParseKPIPrompt(r.Context(), ownerID, req.Prompt),
	})
}
