// Package httpapi is auth-service's delivery layer: it exposes the authentication
// use cases over REST and translates between wire DTOs and the domain. Routing
// uses the stdlib ServeMux method+path patterns (Go 1.22+), so no third-party
// router is needed. Endpoints live at the root: /login, /refresh, /logout,
// /register.
package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/auth/internal/application"
	"github.com/example/auth/internal/domain"
	"github.com/example/auth/internal/platform/httpx"
)

// API binds HTTP handlers to the application service.
type API struct {
	svc *application.AuthService
}

// New builds the API.
func New(svc *application.AuthService) *API { return &API{svc: svc} }

// Routes registers all endpoints on mux.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /refresh", a.refresh)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("POST /register", a.register)
}

// ---- request/response DTOs ----

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type registerRequest struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Roles    []string `json:"roles,omitempty"`
}

// userView is the public user block echoed to clients (never the password hash).
type userView struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
}

// tokenResponse is the body returned by /login, /refresh and /register.
type tokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	User         userView  `json:"user"`
}

// ---- handlers ----

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Username == "" || req.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "username and password are required")
		return
	}
	tokens, err := a.svc.Login(r.Context(), req.Username, req.Password, clientIP(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tokenView(tokens))
}

func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RefreshToken == "" {
		httpx.Error(w, http.StatusBadRequest, "refresh_token is required")
		return
	}
	tokens, err := a.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tokenView(tokens))
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.svc.Logout(r.Context(), req.RefreshToken); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Username == "" || req.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "username and password are required")
		return
	}
	tokens, err := a.svc.Register(r.Context(), req.Username, req.Password, req.Roles)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, tokenView(tokens))
}

// fail maps domain errors to HTTP status codes. Unrecognised errors collapse to
// a generic 500 so internal details (e.g. raw database or gRPC messages) never
// reach the client.
func (a *API) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, domain.ErrSessionNotFound):
		httpx.Error(w, http.StatusUnauthorized, "invalid or expired refresh token")
	case errors.Is(err, domain.ErrUserExists):
		httpx.Error(w, http.StatusConflict, "username already taken")
	case errors.Is(err, domain.ErrWeakPassword):
		// The wrapped reason ("must be at least 8 characters") is safe to show.
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, domain.ErrTooManyAttempts):
		var tma *domain.TooManyAttemptsError
		if errors.As(err, &tma) && tma.RetryAfter > 0 {
			secs := int(tma.RetryAfter.Seconds())
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		httpx.Error(w, http.StatusTooManyRequests, "too many failed login attempts; try again later")
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// clientIP best-effort resolves the caller's source address for rate-limiting.
// It prefers the proxy-set X-Real-IP / first X-Forwarded-For hop (this service
// sits behind the platform gateway) and falls back to the connection's remote
// host. The value only refines the per-username throttle key, so a spoofed
// header weakens it at worst — it never bypasses the username-scoped limit.
func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if first, _, found := strings.Cut(v, ","); found {
			v = first
		}
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// tokenView assembles the wire response from a successful authentication.
func tokenView(t *application.Tokens) tokenResponse {
	return tokenResponse{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    t.ExpiresAt,
		User: userView{
			ID:       t.Identity.UserID,
			Username: t.Identity.Username,
			Roles:    t.Identity.Roles,
		},
	}
}
