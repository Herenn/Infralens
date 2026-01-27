package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Herenn/Infralens/backend/pkg/metrics"
	"github.com/Herenn/Infralens/backend/service"
	log "github.com/sirupsen/logrus"
)

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
				return true // Allow all origins for development
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}
}

// HandleWebSocket handles WebSocket connections.
// It sends real-time topology updates to connected clients.
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
	if topology, err := h.topology.GetTopology(ctx); err == nil {
		response := convertTopologyToResponse(topology)
		if err := conn.WriteJSON(response); err != nil {
			log.WithError(err).Debug("Failed to send initial topology")
			return
		}
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

	// Periodic full topology broadcast (fallback for missed events)
	// Send full topology every 5 seconds for now (can be optimized to use events)
	snapshotTicker := time.NewTicker(2 * time.Second)
	defer snapshotTicker.Stop()

	for {
		select {
		case event, ok := <-sub.Events:
			if !ok {
				return
			}

			// For topology snapshots, send the full topology
			if event.Type == service.EventTopologySnapshot {
				if err := conn.WriteJSON(event.Data); err != nil {
					log.WithError(err).Debug("WebSocket write failed")
					return
				}
			} else {
				// For delta events, wrap in event envelope
				// Note: Frontend currently expects full topology, so we still send full topology
				// This can be optimized to send just deltas once frontend supports it
				if topology, err := h.topology.GetTopology(ctx); err == nil {
					response := convertTopologyToResponse(topology)
					if err := conn.WriteJSON(response); err != nil {
						log.WithError(err).Debug("WebSocket write failed")
						return
					}
				}
			}

		case <-snapshotTicker.C:
			// Periodic full topology broadcast
			if topology, err := h.topology.GetTopology(ctx); err == nil {
				response := convertTopologyToResponse(topology)
				if err := conn.WriteJSON(response); err != nil {
					log.WithError(err).Debug("WebSocket write failed, client disconnected")
					return
				}
			}

		case <-sub.Done:
			log.WithField("subscriber", subID).Info("WebSocket subscription ended")
			return
		}
	}
}
