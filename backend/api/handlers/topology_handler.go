package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/gorilla/mux"
)

// TopologyHandler handles topology query endpoints.
type TopologyHandler struct {
	topology *service.TopologyService
}

// NewTopologyHandler creates a new topology handler.
func NewTopologyHandler(topology *service.TopologyService) *TopologyHandler {
	return &TopologyHandler{
		topology: topology,
	}
}

// HandleGetTopology returns the complete topology. With an `at` query
// parameter (RFC 3339), it instead returns the topology reconstructed from
// history at that instant - the endpoint a timeline scrubber drives.
func (h *TopologyHandler) HandleGetTopology(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	atParam := r.URL.Query().Get("at")
	if atParam == "" {
		topology, err := h.topology.GetTopology(ctx)
		if err != nil {
			http.Error(w, "Failed to get topology", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(convertTopologyToResponse(topology))
		return
	}

	at, err := time.Parse(time.RFC3339, atParam)
	if err != nil {
		http.Error(w, "Invalid 'at' parameter: expected RFC 3339 timestamp", http.StatusBadRequest)
		return
	}

	topology, err := h.topology.GetTopologyAt(ctx, at)
	if errors.Is(err, service.ErrHistoryDisabled) {
		http.Error(w, "Topology history is not enabled", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to get topology", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(convertTopologyToResponse(topology))
}

// HandleGetHistoryRange returns the earliest and latest instants covered by
// recorded topology history, so a UI timeline control knows what range it
// can scrub across. Fields are null when history is enabled but nothing has
// been recorded yet.
func (h *TopologyHandler) HandleGetHistoryRange(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	bounds, ok, err := h.topology.GetHistoryBounds(ctx)
	if errors.Is(err, service.ErrHistoryDisabled) {
		http.Error(w, "Topology history is not enabled", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to get history range", http.StatusInternalServerError)
		return
	}

	response := HistoryRangeResponse{}
	if ok {
		earliest := bounds.Earliest.Format(time.RFC3339)
		latest := bounds.Latest.Format(time.RFC3339)
		response.Earliest = &earliest
		response.Latest = &latest
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetServices returns all services.
func (h *TopologyHandler) HandleGetServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	services, err := h.topology.ListServices(ctx, storage.ServiceFilter{})
	if err != nil {
		http.Error(w, "Failed to get services", http.StatusInternalServerError)
		return
	}

	// Convert to response format
	response := make([]ServiceResponse, 0, len(services))
	for _, svc := range services {
		response = append(response, convertServiceToResponse(svc))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetService returns a single service by ID.
func (h *TopologyHandler) HandleGetService(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	ctx := r.Context()
	svc, err := h.topology.GetService(ctx, id)
	if err != nil {
		http.Error(w, "Failed to get service", http.StatusInternalServerError)
		return
	}
	if svc == nil {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	response := convertServiceToResponse(*svc)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetStats returns graph statistics.
func (h *TopologyHandler) HandleGetStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats, err := h.topology.GetStats(ctx)
	if err != nil {
		http.Error(w, "Failed to get stats", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// Response types to maintain frontend compatibility

// TopologyResponse is the topology format expected by the frontend.
type TopologyResponse struct {
	Services    []ServiceResponse          `json:"services"`
	Connections []ConnectionResponse       `json:"connections"`
	NodeMetrics map[string]MetricsResponse `json:"node_metrics,omitempty"`
	UpdatedAt   string                     `json:"updated_at"`
}

// ServiceResponse is the service format expected by the frontend.
type ServiceResponse struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`
	ResolvedName string            `json:"resolved_name,omitempty"`
	Type         string            `json:"type,omitempty"`
	Tech         string            `json:"tech,omitempty"`
	Icon         string            `json:"icon,omitempty"`
	Namespace    string            `json:"namespace,omitempty"`
	Node         string            `json:"node,omitempty"`
	PodIP        string            `json:"pod_ip,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	LastSeen     string            `json:"last_seen"`
	Healthy      bool              `json:"healthy"`
}

// ConnectionResponse is the connection format expected by the frontend.
type ConnectionResponse struct {
	ID            string  `json:"id"`
	SourceID      string  `json:"source_id"`
	TargetID      string  `json:"target_id"`
	Port          uint16  `json:"port"`
	Protocol      string  `json:"protocol,omitempty"`
	Count         int64   `json:"count"`
	BytesSent     uint64  `json:"bytes_sent,omitempty"`
	BytesRecv     uint64  `json:"bytes_recv,omitempty"`
	BytesSentRate float64 `json:"bytes_sent_rate,omitempty"`
	BytesRecvRate float64 `json:"bytes_recv_rate,omitempty"`
	PacketsSent   uint64  `json:"packets_sent,omitempty"`
	PacketsRecv   uint64  `json:"packets_recv,omitempty"`
	LastSeen      string  `json:"last_seen"`
	LatencyMs     float64 `json:"latency_ms,omitempty"`
}

// HistoryRangeResponse is the earliest/latest recorded history instants,
// RFC 3339-formatted. Both fields are null when nothing has been recorded.
type HistoryRangeResponse struct {
	Earliest *string `json:"earliest"`
	Latest   *string `json:"latest"`
}

// MetricsResponse is the metrics format expected by the frontend.
type MetricsResponse struct {
	NodeName   string  `json:"node_name"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float64 `json:"mem_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	LastSeen   string  `json:"last_seen"`
}

// Conversion functions

func convertTopologyToResponse(t *storage.Topology) TopologyResponse {
	services := make([]ServiceResponse, 0, len(t.Services))
	for _, svc := range t.Services {
		services = append(services, convertServiceToResponse(svc))
	}

	connections := make([]ConnectionResponse, 0, len(t.Connections))
	for _, conn := range t.Connections {
		connections = append(connections, convertConnectionToResponse(conn))
	}

	metrics := make(map[string]MetricsResponse)
	for name, m := range t.NodeMetrics {
		metrics[name] = convertMetricsToResponse(m)
	}

	return TopologyResponse{
		Services:    services,
		Connections: connections,
		NodeMetrics: metrics,
		UpdatedAt:   t.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func convertServiceToResponse(svc storage.Service) ServiceResponse {
	return ServiceResponse{
		ID:           svc.ID,
		Name:         svc.Name,
		DisplayName:  svc.DisplayName,
		ResolvedName: svc.ResolvedName,
		Type:         svc.Type,
		Tech:         svc.Tech,
		Icon:         svc.Icon,
		Namespace:    svc.Namespace,
		Node:         svc.Node,
		PodIP:        svc.PodIP,
		Labels:       svc.Labels,
		LastSeen:     svc.LastSeen.Format("2006-01-02T15:04:05Z07:00"),
		Healthy:      svc.Healthy,
	}
}

func convertConnectionToResponse(conn storage.Connection) ConnectionResponse {
	return ConnectionResponse{
		ID:            conn.ID,
		SourceID:      conn.SourceID,
		TargetID:      conn.TargetID,
		Port:          conn.Port,
		Protocol:      conn.Protocol,
		Count:         conn.Count,
		BytesSent:     conn.BytesSent,
		BytesRecv:     conn.BytesRecv,
		BytesSentRate: conn.BytesSentRate,
		BytesRecvRate: conn.BytesRecvRate,
		PacketsSent:   conn.PacketsSent,
		PacketsRecv:   conn.PacketsRecv,
		LastSeen:      conn.LastSeen.Format("2006-01-02T15:04:05Z07:00"),
		LatencyMs:     conn.Latency,
	}
}

func convertMetricsToResponse(m storage.NodeMetrics) MetricsResponse {
	return MetricsResponse{
		NodeName:   m.NodeName,
		CPUPercent: m.CPUPercent,
		MemPercent: m.MemPercent,
		MemUsed:    m.MemUsed,
		MemTotal:   m.MemTotal,
		LastSeen:   m.LastSeen.Format("2006-01-02T15:04:05Z07:00"),
	}
}
