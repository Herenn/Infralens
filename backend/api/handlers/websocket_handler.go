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

	// Send initial topology snapshot
	ctx := context.Background()
	if !h.sendSnapshot(ctx, conn) {
		return
	}

	// Set up ping/pong for connection health
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Start a goroutine to handle pings
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-sub.Done:
				return
			}
		}
	}()

	// Periodic full snapshot as a re-sync safety net (also covers deletions
	// from the pruner, which bypass the event bus).
	resyncTicker := time.NewTicker(resyncInterval)
	defer resyncTicker.Stop()

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
			if err := conn.WriteJSON(msg); err != nil {
				log.WithError(err).Debug("WebSocket write failed")
				return
			}

		case <-resyncTicker.C:
			if !h.sendSnapshot(ctx, conn) {
				return
			}

		case <-sub.Done:
			log.WithField("subscriber", subID).Info("WebSocket subscription ended")
			return
		}
	}
}

// sendSnapshot sends the full topology; returns false if the write failed.
func (h *WebSocketHandler) sendSnapshot(ctx context.Context, conn *websocket.Conn) bool {
	topology, err := h.topology.GetTopology(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get topology for WebSocket snapshot")
		return true // storage error, not a connection error
	}
	msg := WSMessage{Type: "snapshot", Data: convertTopologyToResponse(topology)}
	if err := conn.WriteJSON(msg); err != nil {
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
