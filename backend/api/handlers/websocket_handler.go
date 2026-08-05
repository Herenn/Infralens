package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Herenn/Infralens/backend/pkg/metrics"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// resyncInterval controls how often a full topology snapshot is sent as a
// safety net (covers pruned entities and any missed delta events).
const resyncInterval = 30 * time.Second

const (
	// pingPeriod is how often a ping control frame is sent to the client.
	// It must be shorter than pongWait so a healthy client always answers in time.
	pingPeriod = 30 * time.Second

	// pongWait is how long we wait for a pong (or any frame) before declaring
	// the connection dead.
	pongWait = 60 * time.Second

	// writeWait bounds a single write so one stalled client can't block the
	// writer goroutine forever.
	writeWait = 10 * time.Second

	// maxClientMessageSize caps inbound frames. Clients are not expected to
	// send anything, so this only needs to be large enough for control frames.
	maxClientMessageSize = 4096
)

// WSMessage is the envelope for all WebSocket messages sent to clients.
//
// Message types:
//   - "snapshot"           data is a full TopologyResponse
//   - "service"            data is a ServiceResponse (created or updated)
//   - "service.deleted"    data is {"service_id": "..."}
//   - "connection"         data is a ConnectionResponse (created or updated)
//   - "connection.deleted" data is {"connection_id": "..."}
//   - "metrics"            data is a MetricsResponse
type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// WebSocketHandler handles WebSocket connections for real-time updates.
type WebSocketHandler struct {
	topology *service.TopologyService
	eventBus *service.EventBus
	upgrader websocket.Upgrader

	pingPeriod time.Duration
	resync     time.Duration
}

// NewWebSocketHandler creates a new WebSocket handler.
func NewWebSocketHandler(topology *service.TopologyService, eventBus *service.EventBus) *WebSocketHandler {
	return &WebSocketHandler{
		topology: topology,
		eventBus: eventBus,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // CORS is enforced at the middleware level
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		pingPeriod: pingPeriod,
		resync:     resyncInterval,
	}
}

// SetKeepalive overrides the ping and full-resync intervals. Non-positive
// values leave the corresponding default in place.
func (h *WebSocketHandler) SetKeepalive(ping, resync time.Duration) {
	if ping > 0 {
		h.pingPeriod = ping
	}
	if resync > 0 {
		h.resync = resync
	}
}

// HandleWebSocket handles WebSocket connections.
// Clients receive an initial full snapshot, then incremental delta messages
// per changed entity, with a periodic full snapshot as a re-sync safety net.
func (h *WebSocketHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("WebSocket upgrade failed")
		return
	}
	defer conn.Close()

	// Track WebSocket connection metrics
	metrics.RecordWebSocketConnect()
	defer metrics.RecordWebSocketDisconnect()

	// Generate unique subscriber ID
	subID := uuid.New().String()
	log.WithField("subscriber", subID).Info("WebSocket client connected")

	// Subscribe to events
	sub := h.eventBus.Subscribe(subID)
	defer h.eventBus.Unsubscribe(subID)

	// Start the read pump. A gorilla/websocket connection supports one
	// concurrent reader and one concurrent writer, so this goroutine only ever
	// reads: it drives the pong handler (keeping the read deadline fresh) and
	// detects client-side closes. Without it, pongs are never processed and a
	// dead peer is only noticed on the next failed write.
	readerDone := make(chan struct{})
	go h.readPump(conn, readerDone)

	// Send initial topology snapshot
	ctx := context.Background()
	if !h.sendSnapshot(ctx, conn) {
		return
	}

	// Periodic full snapshot as a re-sync safety net (also covers deletions
	// from the pruner, which bypass the event bus).
	resyncTicker := time.NewTicker(h.resync)
	defer resyncTicker.Stop()

	// Ping from this loop rather than a separate goroutine: every write to the
	// connection must come from a single goroutine, otherwise gorilla panics
	// with "concurrent write to websocket connection".
	pingTicker := time.NewTicker(h.pingPeriod)
	defer pingTicker.Stop()

	for {
		select {
		case event, ok := <-sub.Events:
			if !ok {
				return
			}

			msg, ok := h.eventToMessage(ctx, event)
			if !ok {
				continue
			}
			if err := writeJSON(conn, msg); err != nil {
				log.WithError(err).Debug("WebSocket write failed")
				return
			}

		case <-resyncTicker.C:
			if !h.sendSnapshot(ctx, conn) {
				return
			}

		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.WithError(err).Debug("WebSocket ping failed")
				return
			}

		case <-readerDone:
			log.WithField("subscriber", subID).Info("WebSocket client disconnected")
			return

		case <-sub.Done:
			log.WithField("subscriber", subID).Info("WebSocket subscription ended")
			return
		}
	}
}

// readPump consumes inbound frames so the pong handler runs and client closes
// are detected. It closes done when the connection is no longer readable.
// This is the only goroutine that reads from conn.
func (h *WebSocketHandler) readPump(conn *websocket.Conn, done chan<- struct{}) {
	defer close(done)

	conn.SetReadLimit(maxClientMessageSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		// Clients aren't expected to send data; ignore anything they do send
		// but treat it as liveness.
		conn.SetReadDeadline(time.Now().Add(pongWait))
	}
}

// writeJSON writes a message with a bounded deadline. Callers must ensure only
// one goroutine writes to conn at a time.
func writeJSON(conn *websocket.Conn, msg WSMessage) error {
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

// sendSnapshot sends the full topology; returns false if the write failed.
func (h *WebSocketHandler) sendSnapshot(ctx context.Context, conn *websocket.Conn) bool {
	topology, err := h.topology.GetTopology(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get topology for WebSocket snapshot")
		return true // storage error, not a connection error
	}
	if err := writeJSON(conn, WSMessage{Type: "snapshot", Data: convertTopologyToResponse(topology)}); err != nil {
		log.WithError(err).Debug("Failed to send topology snapshot")
		return false
	}
	return true
}

// eventToMessage converts a bus event into a WebSocket delta message.
// It fetches the full entity from storage so clients receive complete
// objects they can upsert directly. Returns ok=false when the event should
// be skipped (e.g. entity already pruned).
func (h *WebSocketHandler) eventToMessage(ctx context.Context, event service.Event) (WSMessage, bool) {
	switch event.Type {
	case service.EventServiceCreated, service.EventServiceUpdated:
		data, ok := event.Data.(service.ServiceEvent)
		if !ok {
			return WSMessage{}, false
		}
		svc, err := h.topology.GetService(ctx, data.ServiceID)
		if err != nil || svc == nil {
			return WSMessage{}, false
		}
		return WSMessage{Type: "service", Data: convertServiceToResponse(*svc)}, true

	case service.EventServiceDeleted:
		return WSMessage{Type: "service.deleted", Data: event.Data}, true

	case service.EventConnectionCreated, service.EventConnectionUpdated:
		data, ok := event.Data.(service.ConnectionEvent)
		if !ok {
			return WSMessage{}, false
		}
		conn, err := h.topology.GetConnection(ctx, data.ConnectionID)
		if err != nil || conn == nil {
			return WSMessage{}, false
		}
		return WSMessage{Type: "connection", Data: convertConnectionToResponse(*conn)}, true

	case service.EventConnectionDeleted:
		return WSMessage{Type: "connection.deleted", Data: event.Data}, true

	case service.EventMetricsUpdated:
		data, ok := event.Data.(service.MetricsEvent)
		if !ok {
			return WSMessage{}, false
		}
		m, err := h.topology.GetNodeMetrics(ctx, data.NodeName)
		if err != nil || m == nil {
			return WSMessage{}, false
		}
		return WSMessage{Type: "metrics", Data: convertMetricsToResponse(*m)}, true

	case service.EventTopologySnapshot:
		if t, ok := event.Data.(*storage.Topology); ok {
			return WSMessage{Type: "snapshot", Data: convertTopologyToResponse(t)}, true
		}
		return WSMessage{Type: "snapshot", Data: event.Data}, true

	default:
		return WSMessage{}, false
	}
}
