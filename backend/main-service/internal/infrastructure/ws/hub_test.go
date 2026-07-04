package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"github.com/example/main-service/internal/platform/contracts"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// hasClients reports whether the hub holds at least one socket for ownerID (a
// white-box helper so tests can synchronise on registration without a production
// accessor).
func hasClients(h *Hub, ownerID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[ownerID]) > 0
}

// dialHub stands up an httptest server that upgrades GET requests, registers the
// socket with the hub under ownerID, and returns the client connection.
func dialHub(t *testing.T, hub *Hub, ownerID string) *websocket.Conn {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := hub.Register(ownerID, conn)
		client.ReadLoop(r.Context())
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Broadcast delivers a payload to a registered client.
func TestHub_BroadcastReachesClient(t *testing.T) {
	hub := NewHub(zerolog.Nop())
	conn := dialHub(t, hub, "alice")

	// Give Register time to add the client before broadcasting.
	waitForClient(t, hub, "alice")
	hub.Broadcast("alice", []byte("hello"))

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "hello" {
		t.Fatalf("payload = %q, want hello", msg)
	}
}

// A broadcast to an owner with no sockets is a harmless no-op.
func TestHub_BroadcastNoClients(_ *testing.T) {
	hub := NewHub(zerolog.Nop())
	hub.Broadcast("nobody", []byte("x")) // must not panic
}

// HandleEvent decodes an IngestionEvent and fans its JSON view out to the owner.
func TestHub_HandleEvent(t *testing.T) {
	hub := NewHub(zerolog.Nop())
	conn := dialHub(t, hub, "alice")
	waitForClient(t, hub, "alice")

	evt := &commonv1.IngestionEvent{
		DocumentId: "d1", OwnerId: "alice", Status: "queued", Message: "ok", Timestamp: "2026-01-01T00:00:00Z",
	}
	body, err := proto.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if herr := hub.HandleEvent(context.Background(), body); herr != nil {
		t.Fatalf("HandleEvent: %v", herr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, rerr := conn.ReadMessage()
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if !strings.Contains(string(msg), `"document_id":"d1"`) || !strings.Contains(string(msg), `"status":"queued"`) {
		t.Fatalf("unexpected event view: %s", msg)
	}
	if strings.Contains(string(msg), "stage_") {
		t.Fatalf("stage fields must be omitted without a total: %s", msg)
	}

	// A stage total exposes the progress pair on the wire.
	evt.Status, evt.StageCurrent, evt.StageTotal = contracts.StatusOCR, 3, 7
	body, _ = proto.Marshal(evt)
	if herr := hub.HandleEvent(context.Background(), body); herr != nil {
		t.Fatalf("HandleEvent: %v", herr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, rerr = conn.ReadMessage()
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if !strings.Contains(string(msg), `"stage_current":3`) || !strings.Contains(string(msg), `"stage_total":7`) {
		t.Fatalf("stage progress not forwarded: %s", msg)
	}
}

// HandleEvent fires the onIndexed hook for an "indexed" event.
func TestHub_HandleEvent_OnIndexed(t *testing.T) {
	hub := NewHub(zerolog.Nop())
	got := make(chan string, 1)
	hub.SetOnIndexed(func(_ context.Context, ownerID string) { got <- ownerID })

	evt := &commonv1.IngestionEvent{DocumentId: "d1", OwnerId: "alice", Status: contracts.StatusIndexed}
	body, _ := proto.Marshal(evt)
	if err := hub.HandleEvent(context.Background(), body); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	select {
	case owner := <-got:
		if owner != "alice" {
			t.Fatalf("onIndexed owner = %q", owner)
		}
	case <-time.After(time.Second):
		t.Fatal("onIndexed was not called for an indexed event")
	}
}

// HandleEvent ignores a malformed body and an event without an owner.
func TestHub_HandleEvent_Ignored(t *testing.T) {
	hub := NewHub(zerolog.Nop())
	if err := hub.HandleEvent(context.Background(), []byte("not-proto")); err != nil {
		t.Fatalf("malformed body must be swallowed, got %v", err)
	}
	noOwner, _ := proto.Marshal(&commonv1.IngestionEvent{DocumentId: "d1", Status: "queued"})
	if err := hub.HandleEvent(context.Background(), noOwner); err != nil {
		t.Fatalf("owner-less event must be swallowed, got %v", err)
	}
}

// Closing the client connection unregisters it from the hub.
func TestHub_UnregisterOnClose(t *testing.T) {
	hub := NewHub(zerolog.Nop())
	conn := dialHub(t, hub, "alice")
	waitForClient(t, hub, "alice")
	_ = conn.Close()
	// After the client disconnects, the owner's bucket should drain.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !hasClients(hub, "alice") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("client was not unregistered after close")
}

// waitForClient blocks until the hub has at least one socket for ownerID.
func waitForClient(t *testing.T, hub *Hub, ownerID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasClients(hub, ownerID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no client registered for %q in time", ownerID)
}
