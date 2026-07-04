// Package ws holds the WebSocket fan-out hub. The hub maps an owner id to that
// owner's live browser sockets and pushes ingestion-progress events to them.
// It is concurrency-safe: HTTP upgrade goroutines register/unregister clients
// while the events-consumer goroutine broadcasts, so all access to the
// registry is guarded by a mutex.
package ws

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"github.com/example/main-service/internal/platform/contracts"
	"github.com/example/main-service/internal/platform/jsonx"
	"github.com/example/main-service/internal/platform/logger"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// writeWait bounds how long a single frame write may block before the socket is
// considered dead.
const writeWait = 10 * time.Second

// Client is one connected WebSocket. Outbound frames are funnelled through send
// so exactly one goroutine ever writes to the underlying connection (gorilla
// connections are not safe for concurrent writers).
type Client struct {
	hub     *Hub
	ownerID string
	conn    *websocket.Conn
	send    chan []byte
}

// Hub tracks connected clients grouped by owner id.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*Client]struct{} // ownerID -> set of clients
	log     zerolog.Logger
	// onIndexed, if set, is called when an "indexed" event is observed (to bump
	// the answer-cache corpus epoch). Set once before the consumer starts.
	onIndexed func(context.Context, string)
}

// NewHub builds an empty hub.
func NewHub(log zerolog.Logger) *Hub {
	return &Hub{
		mu:      sync.RWMutex{},
		clients: make(map[string]map[*Client]struct{}),
		log:     log,
	}
}

// SetOnIndexed registers a callback fired when an "indexed" ingestion event is
// observed. Call it once before the events consumer starts.
func (h *Hub) SetOnIndexed(fn func(context.Context, string)) { h.onIndexed = fn }

// Register creates a Client for an upgraded connection, adds it to ownerID's
// set, and starts its writer pump. The caller should then run ReadLoop to keep
// the connection alive and detect disconnects.
func (h *Hub) Register(ownerID string, conn *websocket.Conn) *Client {
	c := &Client{
		hub:     h,
		ownerID: ownerID,
		conn:    conn,
		send:    make(chan []byte, 64),
	}
	h.mu.Lock()
	set, ok := h.clients[ownerID]
	if !ok {
		set = make(map[*Client]struct{})
		h.clients[ownerID] = set
	}
	set[c] = struct{}{}
	h.mu.Unlock()

	go c.writePump()
	return c
}

// unregister removes c from the hub and closes its send channel. Idempotent.
func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.clients[c.ownerID]
	if !ok {
		return
	}
	if _, ok := set[c]; ok {
		delete(set, c)
		close(c.send)
		if len(set) == 0 {
			delete(h.clients, c.ownerID)
		}
	}
}

// Broadcast delivers the raw payload to every socket owned by ownerID. A client
// whose buffer is full is dropped (slow consumer) rather than blocking the
// broadcaster.
func (h *Hub) Broadcast(ownerID string, payload []byte) {
	h.mu.RLock()
	set := h.clients[ownerID]
	targets := make([]*Client, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.send <- payload:
		default:
			// Buffer full: the consumer cannot keep up. Drop it so progress
			// updates for healthy clients are not held back.
			h.log.Warn().Str("owner_id", ownerID).Msg("dropping slow websocket client")
			c.close()
		}
	}
}

// ingestionEventView is the browser-facing JSON shape of an IngestionEvent. The
// WS wire format predates the protobuf migration and must not change, so the
// binary event is re-encoded through this view before fan-out. The stage
// progress pair is additive and appears only when the stage reports a total.
type ingestionEventView struct {
	DocumentID   string `json:"document_id"`
	OwnerID      string `json:"owner_id"`
	Status       string `json:"status"`
	Message      string `json:"message,omitempty"`
	Timestamp    string `json:"timestamp"`
	StageCurrent int32  `json:"stage_current,omitempty"`
	StageTotal   int32  `json:"stage_total,omitempty"`
}

// HandleEvent is the messaging.ConsumeEvents handler: it decodes a protobuf
// IngestionEvent and pushes its JSON view to that owner's sockets. It is
// best-effort, so a decode error is logged and swallowed (returning nil keeps
// the auto-acked events stream flowing).
func (h *Hub) HandleEvent(ctx context.Context, body []byte) error {
	var evt commonv1.IngestionEvent
	if err := proto.Unmarshal(body, &evt); err != nil {
		logger.From(ctx).Warn().Err(err).Msg("ignoring malformed ingestion event")
		return nil
	}
	if evt.GetOwnerId() == "" {
		return nil
	}
	if h.onIndexed != nil && evt.GetStatus() == contracts.StatusIndexed {
		h.onIndexed(ctx, evt.GetOwnerId())
	}
	view := ingestionEventView{
		DocumentID: evt.GetDocumentId(),
		OwnerID:    evt.GetOwnerId(),
		Status:     evt.GetStatus(),
		Message:    evt.GetMessage(),
		Timestamp:  evt.GetTimestamp(),
	}
	if evt.GetStageTotal() > 0 {
		view.StageCurrent = evt.GetStageCurrent()
		view.StageTotal = evt.GetStageTotal()
	}
	payload, err := jsonx.Marshal(view)
	if err != nil {
		logger.From(ctx).Warn().Err(err).Msg("encode ingestion event view")
		return nil
	}
	h.Broadcast(evt.GetOwnerId(), payload)
	return nil
}

// close tears down a client connection and removes it from the hub.
func (c *Client) close() {
	c.hub.unregister(c)
	_ = c.conn.Close()
}

// ReadLoop drains inbound frames (clients are not expected to send anything
// meaningful) so control frames are processed and disconnects are detected. It
// blocks until the connection closes or ctx is cancelled, then cleans up.
func (c *Client) ReadLoop(ctx context.Context) {
	defer c.close()

	// A cancelled context (server shutdown) closes the connection, unblocking
	// the read below.
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	defer stop()

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump serialises all writes to the connection and exits when the send
// channel is closed by unregister.
func (c *Client) writePump() {
	for payload := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			// Drop the client; ReadLoop will observe the closed connection.
			c.hub.unregister(c)
			_ = c.conn.Close()
			return
		}
	}
	// send was closed: send a close frame so the browser tears down cleanly.
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}
