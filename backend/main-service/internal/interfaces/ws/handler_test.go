package ws_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	wshub "github.com/example/main-service/internal/infrastructure/ws"
	wsapi "github.com/example/main-service/internal/interfaces/ws"
	"github.com/example/main-service/internal/platform/jwt"
)

const wsSecret = "ws-test-secret-32-bytes-padding!!"

// wsServer wires the handler over a fresh hub + JWT manager and returns the server.
func wsServer(t *testing.T) (*httptest.Server, *jwt.Manager) {
	t.Helper()
	mgr := jwt.NewManager(wsSecret, "rag-platform", time.Hour)
	hub := wshub.NewHub(zerolog.Nop())
	srv := httptest.NewServer(wsapi.New(hub, mgr))
	t.Cleanup(srv.Close)
	return srv, mgr
}

func wsURL(srv *httptest.Server, query string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + query
}

// closeResp closes a handshake response body when present.
func closeResp(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// A handshake without any token is rejected with 401 before the upgrade.
func TestWSHandler_NoTokenRejected(t *testing.T) {
	srv, _ := wsServer(t)
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(srv, ""), nil)
	defer closeResp(resp)
	if err == nil {
		t.Fatal("dial without token should fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token = %v, want 401", resp)
	}
}

// An invalid token is rejected.
func TestWSHandler_InvalidTokenRejected(t *testing.T) {
	srv, _ := wsServer(t)
	hdr := http.Header{"Authorization": {"Bearer not-a-real-token"}}
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(srv, ""), hdr)
	defer closeResp(resp)
	if err == nil {
		t.Fatal("dial with invalid token should fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid token = %v, want 401", resp)
	}
}

// A valid bearer header upgrades the connection.
func TestWSHandler_HeaderTokenUpgrades(t *testing.T) {
	srv, mgr := wsServer(t)
	tok, _, err := mgr.Issue("alice", "alice", []string{"user"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	hdr := http.Header{"Authorization": {"Bearer " + tok}}
	conn, resp, derr := websocket.DefaultDialer.Dial(wsURL(srv, ""), hdr)
	defer closeResp(resp)
	if derr != nil {
		t.Fatalf("dial with header token failed: %v (resp %v)", derr, resp)
	}
	defer func() { _ = conn.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
}

// A valid ?token= query parameter upgrades the connection (browsers cannot set
// headers on the WebSocket handshake).
func TestWSHandler_QueryTokenUpgrades(t *testing.T) {
	srv, mgr := wsServer(t)
	tok, _, err := mgr.Issue("bob", "bob", nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	conn, resp, derr := websocket.DefaultDialer.Dial(wsURL(srv, "?token="+tok), nil)
	defer closeResp(resp)
	if derr != nil {
		t.Fatalf("dial with query token failed: %v (resp %v)", derr, resp)
	}
	defer func() { _ = conn.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
}
