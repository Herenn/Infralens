package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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

// HandleGetTopologyDiff returns what appeared and disappeared between two
// instants (?from=&to=, both required RFC 3339 timestamps) - the on-demand
// sibling of continuous change detection.
func (h *TopologyHandler) HandleGetTopologyDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	fromParam := r.URL.Query().Get("from")
	toParam := r.URL.Query().Get("to")
	if fromParam == "" || toParam == "" {
		http.Error(w, "Both 'from' and 'to' query parameters are required (RFC 3339 timestamps)", http.StatusBadRequest)
		return
	}

	from, err := time.Parse(time.RFC3339, fromParam)
	if err != nil {
		http.Error(w, "Invalid 'from' parameter: expected RFC 3339 timestamp", http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toParam)
	if err != nil {
		http.Error(w, "Invalid 'to' parameter: expected RFC 3339 timestamp", http.StatusBadRequest)
		return
	}

	diff, err := h.topology.GetTopologyDiff(ctx, from, to)
	if errors.Is(err, service.ErrHistoryDisabled) {
		http.Error(w, "Topology history is not enabled", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to get topology diff", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(convertTopologyDiffToResponse(diff))
}

// HandleGetStaleServices returns decommission candidates: services whose
// most recent observation is older than ?olderThan=<Go duration> (default
// and unit: the storage history retention window, e.g. "720h").
func (h *TopologyHandler) HandleGetStaleServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var olderThan time.Duration
	if raw := r.URL.Query().Get("olderThan"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			http.Error(w, "Invalid 'olderThan' parameter: expected a positive Go duration (e.g. '720h')", http.StatusBadRequest)
			return
		}
		olderThan = parsed
	}

	stale, err := h.topology.GetStaleServices(ctx, olderThan)
	if errors.Is(err, service.ErrHistoryDisabled) {
		http.Error(w, "Topology history is not enabled", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to get stale services", http.StatusInternalServerError)
		return
	}

	response := make([]StaleServiceResponse, 0, len(stale))
	for _, svc := range stale {
		response = append(response, StaleServiceResponse{
			ID:        svc.ServiceID,
			Name:      svc.Name,
			Type:      svc.Type,
			Tech:      svc.Tech,
			Namespace: svc.Namespace,
			Node:      svc.Node,
			LastSeen:  svc.LastSeen.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetImpact returns the subgraph reachable from a service, answering
// "what breaks if I take this down" (?direction=upstream, the default) or
// "what does this depend on" (?direction=downstream). ?depth=<n> caps how
// many hops out the traversal goes (default 5, capped at 20).
func (h *TopologyHandler) HandleGetImpact(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	ctx := r.Context()

	direction := service.ImpactUpstream
	if d := r.URL.Query().Get("direction"); d != "" {
		switch service.ImpactDirection(d) {
		case service.ImpactUpstream, service.ImpactDownstream:
			direction = service.ImpactDirection(d)
		default:
			http.Error(w, "Invalid 'direction' parameter: expected 'upstream' or 'downstream'", http.StatusBadRequest)
			return
		}
	}

	depth := 0 // GetImpact applies its own default
	if d := r.URL.Query().Get("depth"); d != "" {
		parsed, err := strconv.Atoi(d)
		if err != nil || parsed < 1 {
			http.Error(w, "Invalid 'depth' parameter: expected a positive integer", http.StatusBadRequest)
			return
		}
		depth = parsed
	}

	topology, err := h.topology.GetImpact(ctx, id, direction, depth)
	if err != nil {
		http.Error(w, "Failed to get impact", http.StatusInternalServerError)
		return
	}
	if topology == nil {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(convertTopologyToResponse(topology))
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

// HandleGetCriticality ranks services by upstream blast radius - the
// riskiest single points of failure in one list, instead of clicking
// through the impact view service by service. ?limit=<n> caps the results
// (default 20).
func (h *TopologyHandler) HandleGetCriticality(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			http.Error(w, "Invalid 'limit' parameter: expected a positive integer", http.StatusBadRequest)
			return
		}
		limit = parsed
	}

	ranked, err := h.topology.GetCriticality(ctx, limit)
	if err != nil {
		http.Error(w, "Failed to get criticality ranking", http.StatusInternalServerError)
		return
	}

	response := make([]CriticalityResponse, 0, len(ranked))
	for _, sc := range ranked {
		response = append(response, CriticalityResponse{
			ID:          sc.Service.ID,
			Name:        sc.Service.Name,
			Type:        sc.Service.Type,
			Node:        sc.Service.Node,
			BlastRadius: sc.BlastRadius,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetOrphans returns services with no connections at all - neither
// caller nor callee - a fact about the live graph, not a history query.
func (h *TopologyHandler) HandleGetOrphans(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orphans, err := h.topology.GetOrphanServices(ctx)
	if err != nil {
		http.Error(w, "Failed to get orphan services", http.StatusInternalServerError)
		return
	}

	response := make([]ServiceResponse, 0, len(orphans))
	for _, svc := range orphans {
		response = append(response, convertServiceToResponse(svc))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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

// StaleServiceResponse is a decommission candidate: a service's most recent
// observation, for services not seen since before the requested cutoff.
type StaleServiceResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"`
	Tech      string `json:"tech,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Node      string `json:"node,omitempty"`
	LastSeen  string `json:"last_seen"`
}

// CriticalityResponse is a service's rank in the blast-radius-size ranking.
type CriticalityResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Node        string `json:"node,omitempty"`
	BlastRadius int    `json:"blast_radius"`
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

// TopologyDiffResponse is what appeared and disappeared between two instants.
type TopologyDiffResponse struct {
	From                string                `json:"from"`
	To                  string                `json:"to"`
	AddedServices       []ServiceResponse     `json:"added_services"`
	RemovedServices     []ServiceResponse     `json:"removed_services"`
	AddedConnections    []ConnectionResponse  `json:"added_connections"`
	RemovedConnections  []ConnectionResponse  `json:"removed_connections"`
}

func convertTopologyDiffToResponse(d *service.TopologyDiff) TopologyDiffResponse {
	addedSvc := make([]ServiceResponse, 0, len(d.AddedServices))
	for _, svc := range d.AddedServices {
		addedSvc = append(addedSvc, convertServiceToResponse(svc))
	}
	removedSvc := make([]ServiceResponse, 0, len(d.RemovedServices))
	for _, svc := range d.RemovedServices {
		removedSvc = append(removedSvc, convertServiceToResponse(svc))
	}
	addedConn := make([]ConnectionResponse, 0, len(d.AddedConnections))
	for _, conn := range d.AddedConnections {
		addedConn = append(addedConn, convertConnectionToResponse(conn))
	}
	removedConn := make([]ConnectionResponse, 0, len(d.RemovedConnections))
	for _, conn := range d.RemovedConnections {
		removedConn = append(removedConn, convertConnectionToResponse(conn))
	}
	return TopologyDiffResponse{
		From:               d.From.Format(time.RFC3339),
		To:                 d.To.Format(time.RFC3339),
		AddedServices:      addedSvc,
		RemovedServices:    removedSvc,
		AddedConnections:   addedConn,
		RemovedConnections: removedConn,
	}
}
