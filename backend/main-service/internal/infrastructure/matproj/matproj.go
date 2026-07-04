// Package matproj adapts the Materials Project REST API to the
// application.MaterialsRef port, so novelty scoring can sanity-check whether a
// proposed material formula is already a known, stable entry. It lives outside
// the application package to keep that package free of any concrete HTTP client
// (clean architecture). When no API key is configured the constructor returns a
// safe stub that always reports "unknown" and never errors, so the system runs
// without a key.
package matproj

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/example/main-service/internal/platform/jsonx"
)

// summaryEndpoint is the Materials Project summary search endpoint.
const summaryEndpoint = "https://api.materialsproject.org/materials/summary/"

// httpTimeout bounds a single MP request; novelty is best-effort, so a slow or
// unreachable MP must never stall generation.
const httpTimeout = 6 * time.Second

// MaterialSummary is the distilled result of a formula lookup: whether MP knows
// the formula and a few headline properties of the best match.
type MaterialSummary struct {
	Known      bool    `json:"known"`
	MaterialID string  `json:"material_id"`
	Formula    string  `json:"formula"`
	IsStable   bool    `json:"is_stable"`
	BandGap    float64 `json:"band_gap"`
	Density    float64 `json:"density"`
}

// Client queries the Materials Project API. A zero-key Client is a stub: Summary
// reports "unknown" and Known returns false, both without error.
type Client struct {
	apiKey string
	http   *http.Client
}

// New builds a Materials Project client. An empty apiKey yields a safe stub that
// performs no network calls, so callers degrade gracefully without a key.
func New(apiKey string) *Client {
	return &Client{
		apiKey: strings.TrimSpace(apiKey),
		http:   &http.Client{Timeout: httpTimeout},
	}
}

// mpDoc mirrors one entry in the MP summary response; only the requested fields
// are decoded.
type mpDoc struct {
	MaterialID  string   `json:"material_id"`
	FormulaPret string   `json:"formula_pretty"`
	IsStable    bool     `json:"is_stable"`
	BandGap     *float64 `json:"band_gap"`
	Density     *float64 `json:"density"`
}

// mpResponse is the MP summary envelope ({"data": [...]}).
type mpResponse struct {
	Data []mpDoc `json:"data"`
}

// Summary looks up a chemical formula in the Materials Project. A blank formula
// or a stubbed (keyless) client yields a not-known summary with no error. MP
// 404s, transport errors and malformed bodies all degrade to "unknown" rather
// than failing the caller, since novelty is advisory.
func (c *Client) Summary(ctx context.Context, formula string) (*MaterialSummary, error) {
	formula = strings.TrimSpace(formula)
	if c.apiKey == "" || formula == "" {
		return &MaterialSummary{
			Known: false, MaterialID: "", Formula: formula,
			IsStable: false, BandGap: 0, Density: 0,
		}, nil
	}

	endpoint := summaryEndpoint + "?" + url.Values{
		"formula": {formula},
		"_fields": {"material_id,formula_pretty,is_stable,band_gap,density"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, errors.New("matproj: build request failed")
	}
	// MP authenticates on the X-API-KEY header; HTTP header names are case-
	// insensitive (RFC 7230), so the canonical form is wire-equivalent.
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Network/transport failure is tolerated: treat the material as unknown.
		return &MaterialSummary{
			Known: false, MaterialID: "", Formula: formula,
			IsStable: false, BandGap: 0, Density: 0,
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// 404 (unknown formula) or any non-200: report unknown, never hard-error.
		return &MaterialSummary{
			Known: false, MaterialID: "", Formula: formula,
			IsStable: false, BandGap: 0, Density: 0,
		}, nil
	}

	var body mpResponse
	if derr := jsonx.NewDecoder(resp.Body).Decode(&body); derr != nil || len(body.Data) == 0 {
		return &MaterialSummary{
			Known: false, MaterialID: "", Formula: formula,
			IsStable: false, BandGap: 0, Density: 0,
		}, nil
	}

	doc := body.Data[0]
	out := &MaterialSummary{
		Known: true, MaterialID: doc.MaterialID, Formula: doc.FormulaPret,
		IsStable: doc.IsStable, BandGap: derefFloat(doc.BandGap), Density: derefFloat(doc.Density),
	}
	if out.Formula == "" {
		out.Formula = formula
	}
	return out, nil
}

// Known reports whether the formula is a recognised Materials Project entry. It
// never hard-errors: an unreachable MP or an unknown formula both yield false.
func (c *Client) Known(ctx context.Context, formula string) (bool, error) {
	s, err := c.Summary(ctx, formula)
	if err != nil {
		return false, err
	}
	return s.Known, nil
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
