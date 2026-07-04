// KPI suggestion endpoint: proposes candidate R&D goals (KPIs) mined from a
// representative sample of the corpus so the /kpi page can offer "accept in one
// click". It is advisory and best-effort — gated by the KPI_SUGGEST flag and
// degrading to an empty list (never a 500) when disabled or when the LLM is
// unavailable. Kept in its own file so the route wiring in api.go stays a single
// line and does not collide with parallel work there.

package httpapi

import (
	"net/http"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/platform/httpx"
)

// suggestKPIs returns candidate goals extracted from the corpus. The heavy LLM
// work lives in application.SuggestKPIs; this handler only enforces auth and the
// feature flag and shapes the {suggestions:[...]} envelope the frontend expects.
func (a *API) suggestKPIs(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := ownerFromContext(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing claims")
		return
	}
	if !a.ovr.GetBool(r.Context(), "KPI_SUGGEST", true) {
		httpx.JSON(w, http.StatusOK, map[string]any{"suggestions": []application.KPISuggestion{}})
		return
	}
	suggestions, err := a.hypotheses.SuggestKPIs(r.Context(), ownerID)
	if err != nil || suggestions == nil {
		// Best-effort: an unavailable LLM must not 500 the page.
		suggestions = []application.KPISuggestion{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"suggestions": suggestions})
}
