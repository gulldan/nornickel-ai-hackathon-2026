// Package ws is main-service's WebSocket delivery layer. It upgrades GET /ws to
// a WebSocket, authenticating the caller from either the Authorization bearer
// header (native clients) or a ?token= query parameter (browsers, which cannot
// set headers on the WebSocket handshake). On success it registers the socket
// with the fan-out hub keyed by the owner id from the JWT claims.
package ws

import (
	"net/http"
	"strings"

	"github.com/gorilla/websocket"

	wsinfra "github.com/example/main-service/internal/infrastructure/ws"
	"github.com/example/main-service/internal/platform/jwt"
	"github.com/example/main-service/internal/platform/logger"
)

// Handler upgrades connections and binds them to the hub.
type Handler struct {
	hub      *wsinfra.Hub
	jwt      *jwt.Manager
	upgrader websocket.Upgrader
}

// New builds the WebSocket handler. The upgrader accepts any origin because the
// edge sits behind nginx, which is responsible for origin/host policy.
func New(hub *wsinfra.Hub, mgr *jwt.Manager) *Handler {
	return &Handler{
		hub: hub,
		jwt: mgr,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(_ *http.Request) bool { return true },
		},
	}
}

// ServeHTTP authenticates, upgrades, registers and then services the socket.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := logger.From(r.Context())

	claims, err := h.authenticate(r)
	if err != nil {
		// The handshake has not happened yet, so a plain HTTP 401 is correct.
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response on failure.
		log.Warn().Err(err).Msg("websocket upgrade failed")
		return
	}

	client := h.hub.Register(claims.UserID, conn)
	log.Info().Str("owner_id", claims.UserID).Msg("websocket connected")

	// Block in the read loop until the client disconnects or the server shuts
	// down (ctx cancellation closes the socket).
	client.ReadLoop(r.Context())
	log.Info().Str("owner_id", claims.UserID).Msg("websocket disconnected")
}

// authenticate extracts and validates the JWT from the Authorization header or
// the ?token= query parameter.
func (h *Handler) authenticate(r *http.Request) (*jwt.Claims, error) {
	raw := bearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		raw = r.URL.Query().Get("token")
	}
	if raw == "" {
		return nil, errMissingToken
	}
	return h.jwt.Parse(raw)
}

// bearerToken returns the token from an "Authorization: Bearer <token>" header,
// or "" when the header is absent or malformed.
func bearerToken(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

var errMissingToken = &authError{"missing token"}

// authError is a small typed error for the auth step.
type authError struct{ msg string }

func (e *authError) Error() string { return e.msg }

// compile-time assertion that Handler is an http.Handler.
var _ http.Handler = (*Handler)(nil)
