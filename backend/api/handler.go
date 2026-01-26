// Package api provides HTTP handlers for the InfraLens backend.
package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/infralens/infralens/backend/graph"
	"github.com/infralens/infralens/backend/k8s"
	"github.com/infralens/infralens/backend/pkg/fingerprint"
	"github.com/infralens/infralens/backend/pkg/llm"
	log "github.com/sirupsen/logrus"
)

// Handler provides HTTP API endpoints.
type Handler struct {
	graph        *graph.ServiceGraph
	k8sWatcher   *k8s.Watcher
	llmManager   *llm.Manager
	docsGen      *llm.DocsGenerator
	upgrader     websocket.Upgrader
}

// EventBatch represents a batch of events from an agent.
type EventBatch struct {
	NodeName  string     `json:"node_name"`
	Timestamp time.Time  `json:"timestamp"`
	Events    []TCPEvent `json:"events"`
}

// Direction constants
const (
	DirectionOutbound = 0 // Client initiated (connect)
	DirectionInbound  = 1 // Server accepted (accept)
)

// TCPEvent matches the agent's event structure.
type TCPEvent struct {
	PID       uint32 `json:"pid"`
	Comm      string `json:"comm"`
	SrcAddr   string `json:"src_addr"`
	DstAddr   string `json:"dst_addr"`
	DstPort   uint16 `json:"dst_port"`
	Direction uint8  `json:"direction"` // 0 = outbound (connect), 1 = inbound (accept)
}

// ThroughputReport represents throughput stats from an agent.
type ThroughputReport struct {
	NodeName    string                   `json:"node_name"`
	Timestamp   time.Time                `json:"timestamp"`
	IntervalMs  int64                    `json:"interval_ms"`
	Connections []ConnectionStatsPayload `json:"connections"`
}

// ConnectionStatsPayload represents stats for a single connection.
type ConnectionStatsPayload struct {
	PID           uint32  `json:"pid"`
	Comm          string  `json:"comm"`
	SrcAddr       string  `json:"src_addr"`
	DstAddr       string  `json:"dst_addr"`
	SrcPort       uint16  `json:"src_port"`
	DstPort       uint16  `json:"dst_port"`
	BytesSent     uint64  `json:"bytes_sent"`
	BytesRecv     uint64  `json:"bytes_recv"`
	BytesSentRate float64 `json:"bytes_sent_rate"` // Bytes/second
	BytesRecvRate float64 `json:"bytes_recv_rate"` // Bytes/second
	PacketsSent   uint64  `json:"packets_sent"`
	PacketsRecv   uint64  `json:"packets_recv"`
}

// HostMetricsPayload represents CPU/RAM metrics from an agent.
type HostMetricsPayload struct {
	NodeName   string  `json:"node_name"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float64 `json:"mem_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
}

// InspectionReport represents deep inspection data from an agent.
type InspectionReport struct {
	NodeName   string              `json:"node_name"`
	Timestamp  time.Time           `json:"timestamp"`
	ServiceID  string              `json:"service_id"`
	Inspection *InspectionPayload  `json:"inspection"`
}

// InspectionPayload contains all inspection data fields.
type InspectionPayload struct {
	PID          int32                  `json:"pid,omitempty"`
	ProcessName  string                 `json:"process_name,omitempty"`
	CommandLine  []string               `json:"command_line,omitempty"`
	WorkingDir   string                 `json:"working_dir,omitempty"`
	EnvVarNames  []string               `json:"env_var_names,omitempty"`
	ListenPorts  []int                  `json:"listen_ports,omitempty"`
	ConfigFiles  []string               `json:"config_files,omitempty"`
	Dependencies []DependencyPayload    `json:"dependencies,omitempty"`
	HTTPInfo     *HTTPProbePayload      `json:"http_info,omitempty"`
	DBInfo       *DBProbePayload        `json:"db_info,omitempty"`
	K8sMetadata  *K8sMetadataPayload    `json:"k8s_metadata,omitempty"`
	CodeContext  *CodeContextPayload    `json:"code_context,omitempty"`
}

// CodeContextPayload contains source code context from the agent.
type CodeContextPayload struct {
	ProjectName    string `json:"project_name,omitempty"`
	Description    string `json:"description,omitempty"`
	README         string `json:"readme,omitempty"`
	EntryPointFile string `json:"entry_point_file,omitempty"`
	EntryPoint     string `json:"entry_point,omitempty"`
	Dockerfile     string `json:"dockerfile,omitempty"`
}

// DependencyPayload represents a package dependency.
type DependencyPayload struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Type    string `json:"type"`
}

// HTTPProbePayload contains HTTP probing results.
type HTTPProbePayload struct {
	ServerHeader    string            `json:"server_header,omitempty"`
	PoweredBy       string            `json:"x_powered_by,omitempty"`
	HealthEndpoint  string            `json:"health_endpoint,omitempty"`
	HealthStatus    int               `json:"health_status,omitempty"`
	Endpoints       []string          `json:"endpoints,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// DBProbePayload contains database probing results.
type DBProbePayload struct {
	Type       string   `json:"type"`
	Version    string   `json:"version,omitempty"`
	Databases  []string `json:"databases,omitempty"`
	TableCount int      `json:"table_count,omitempty"`
}

// K8sMetadataPayload contains Kubernetes metadata.
type K8sMetadataPayload struct {
	PodName        string            `json:"pod_name,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	ServiceAccount string            `json:"service_account,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
}

// NewHandler creates a new API handler.
func NewHandler(g *graph.ServiceGraph, watcher *k8s.Watcher) *Handler {
	return &Handler{
		graph:      g,
		k8sWatcher: watcher,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for development
			},
		},
	}
}

// SetLLMManager sets the LLM manager for AI documentation.
func (h *Handler) SetLLMManager(manager *llm.Manager) {
	h.llmManager = manager
	h.docsGen = llm.NewDocsGenerator(manager)
}

// RegisterRoutes registers all API routes.
func (h *Handler) RegisterRoutes(r *mux.Router) {
	api := r.PathPrefix("/api/v1").Subrouter()

	// Event ingestion from agents
	api.HandleFunc("/events", h.handleEvents).Methods("POST")

	// Connection stats ingestion from agents
	api.HandleFunc("/stats", h.handleIngestStats).Methods("POST")

	// Host metrics ingestion from agents (CPU/RAM)
	api.HandleFunc("/metrics", h.handleIngestMetrics).Methods("POST")

	// Deep inspection data from agents
	api.HandleFunc("/inspection", h.handleIngestInspection).Methods("POST")

	// Topology queries
	api.HandleFunc("/topology", h.handleGetTopology).Methods("GET")
	api.HandleFunc("/services", h.handleGetServices).Methods("GET")
	api.HandleFunc("/services/{id}", h.handleGetService).Methods("GET")

	// WebSocket for real-time updates
	api.HandleFunc("/ws", h.handleWebSocket)

	// Health check
	r.HandleFunc("/health", h.handleHealth).Methods("GET")
	r.HandleFunc("/ready", h.handleReady).Methods("GET")

	// Graph stats (GET)
	api.HandleFunc("/graph/stats", h.handleGraphStats).Methods("GET")

	// AI Documentation endpoints
	api.HandleFunc("/ai/status", h.handleAIStatus).Methods("GET")
	api.HandleFunc("/ai/config", h.handleAIConfig).Methods("GET", "POST")
	api.HandleFunc("/ai/docs", h.handleAIDocs).Methods("POST")  // serviceId in query param
	api.HandleFunc("/ai/ask", h.handleAIAsk).Methods("POST")    // serviceId in query param
	api.HandleFunc("/ai/providers", h.handleAIProviders).Methods("GET")

	// Kubernetes watcher status
	api.HandleFunc("/k8s/status", h.handleK8sStatus).Methods("GET")
}

// handleEvents processes incoming events from agents.
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	var batch EventBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	log.WithFields(log.Fields{
		"node":   batch.NodeName,
		"events": len(batch.Events),
	}).Debug("Received event batch")

	for _, event := range batch.Events {
		h.processEvent(batch.NodeName, event)
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// processEvent converts a TCP event into graph updates.
// It uses the Kubernetes watcher to resolve IPs to Pod/Service names
// and the fingerprinter to identify service types.
//
// Deduplication Logic for Inbound Events:
// - Outbound: A -> B means A initiated connection to B (we create A -> B edge)
// - Inbound: A -> B (reported by B) means external A connected to our service B
//   We check if edge A -> B already exists (from A's outbound event) to avoid duplicates
func (h *Handler) processEvent(nodeName string, event TCPEvent) {
	now := time.Now()

	// Resolve source IP to Kubernetes resource name
	srcResolved := h.k8sWatcher.Resolve(event.SrcAddr)
	
	// Resolve destination IP to Kubernetes resource name
	dstResolved := h.k8sWatcher.Resolve(event.DstAddr)

	// Create service IDs - include process name to differentiate services on same host
	var srcID, dstID string
	if event.Direction == DirectionOutbound {
		// Outbound: source is local process, dest is remote
		srcID = makeServiceIDWithProcess(event.SrcAddr, event.Comm)
		dstID = makeServiceIDWithPort(event.DstAddr, event.DstPort)
	} else {
		// Inbound: source is remote (external), dest is local process
		srcID = makeServiceID(event.SrcAddr) // External - no process info
		dstID = makeServiceIDWithProcess(event.DstAddr, event.Comm)
	}

	// Build connection ID (same format regardless of direction)
	connID := fmt.Sprintf("%s->%s:%d", srcID, dstID, event.DstPort)

	// For INBOUND events, check if we already have this connection (from an outbound event)
	if event.Direction == DirectionInbound {
		if _, exists := h.graph.GetConnection(connID); exists {
			// Connection already exists from the outbound side, skip to avoid duplicates
			log.WithFields(log.Fields{
				"conn_id":   connID,
				"direction": "inbound",
			}).Debug("Skipping duplicate inbound connection")
			return
		}
	}

	// Determine service names based on direction
	var srcName, dstName string
	var srcInfo, dstInfo fingerprint.ServiceInfo

	if event.Direction == DirectionOutbound {
		// Outbound: source is the process making the connection
		srcName = h.getServiceName(srcResolved, event.Comm)
		srcInfo = fingerprint.Identify(event.Comm, 0)
		dstName = h.getServiceName(dstResolved, fmt.Sprintf(":%d", event.DstPort))
		dstInfo = fingerprint.IdentifyByPort(event.DstPort)
	} else {
		// Inbound: source is external (remote peer), dest is our local service
		srcName = h.getServiceName(srcResolved, "external")
		srcInfo = fingerprint.ServiceInfo{Type: fingerprint.TypeUnknown, Tech: "External", Icon: "globe"}
		dstName = h.getServiceName(dstResolved, event.Comm)
		dstInfo = fingerprint.Identify(event.Comm, event.DstPort)
	}

	// Create/update source service
	srcService := graph.Service{
		ID:           srcID,
		Name:         srcName,
		DisplayName:  srcResolved,
		Type:         string(srcInfo.Type),
		Tech:         srcInfo.Tech,
		Icon:         srcInfo.Icon,
		PodIP:        event.SrcAddr,
		ResolvedName: srcResolved,
		LastSeen:     now,
	}
	// Only set Node for outbound (local source) or inbound destination
	if event.Direction == DirectionOutbound {
		srcService.Node = nodeName
	}
	h.graph.AddOrUpdateService(srcService)

	// Create/update destination service
	dstService := graph.Service{
		ID:           dstID,
		Name:         dstName,
		DisplayName:  dstResolved,
		Type:         string(dstInfo.Type),
		Tech:         dstInfo.Tech,
		Icon:         dstInfo.Icon,
		PodIP:        event.DstAddr,
		ResolvedName: dstResolved,
		LastSeen:     now,
	}
	// For inbound, the destination is our local service
	if event.Direction == DirectionInbound {
		dstService.Node = nodeName
	}
	h.graph.AddOrUpdateService(dstService)

	// Create/update connection
	h.graph.AddConnection(graph.Connection{
		ID:       connID,
		SourceID: srcID,
		TargetID: dstID,
		Port:     event.DstPort,
		LastSeen: now,
	})

	log.WithFields(log.Fields{
		"conn_id":   connID,
		"direction": map[uint8]string{0: "outbound", 1: "inbound"}[event.Direction],
		"src":       srcID,
		"dst":       dstID,
		"port":      event.DstPort,
	}).Debug("Processed connection event")
}

// getServiceName returns a human-readable service name.
// If the resolved name is a K8s resource, use it; otherwise use the fallback.
func (h *Handler) getServiceName(resolved, fallback string) string {
	// If it's resolved to a K8s resource, use that
	if strings.HasPrefix(resolved, "Pod:") || strings.HasPrefix(resolved, "Svc:") {
		// Extract just the name part for display
		parts := strings.Split(resolved, "/")
		if len(parts) > 1 {
			return parts[len(parts)-1] // Return just the resource name
		}
		return resolved
	}
	return fallback
}

// makeServiceID creates a service ID from an IP address.
func makeServiceID(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}
	return ip.String()
}

// makeServiceIDWithProcess creates a unique service ID from IP and process name.
// This allows differentiating multiple services running on the same host.
func makeServiceIDWithProcess(ipStr, processName string) string {
	ip := net.ParseIP(ipStr)
	ipPart := ipStr
	if ip != nil {
		ipPart = ip.String()
	}
	
	// Clean process name (remove path, keep binary name)
	proc := processName
	if idx := strings.LastIndex(proc, "/"); idx >= 0 {
		proc = proc[idx+1:]
	}
	
	// For common system processes, use more generic ID to reduce noise
	systemProcs := map[string]bool{"sshd": true, "systemd": true, "init": true}
	if systemProcs[proc] {
		return ipPart // Don't differentiate system processes
	}
	
	return fmt.Sprintf("%s/%s", ipPart, proc)
}

// makeServiceIDWithPort creates a service ID for remote services (identified by port).
func makeServiceIDWithPort(ipStr string, port uint16) string {
	ip := net.ParseIP(ipStr)
	ipPart := ipStr
	if ip != nil {
		ipPart = ip.String()
	}
	return fmt.Sprintf("%s:%d", ipPart, port)
}

// handleGetTopology returns the complete topology.
func (h *Handler) handleGetTopology(w http.ResponseWriter, r *http.Request) {
	topology := h.graph.GetTopology()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(topology)
}

// handleGetServices returns all services.
func (h *Handler) handleGetServices(w http.ResponseWriter, r *http.Request) {
	topology := h.graph.GetTopology()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(topology.Services)
}

// handleGetService returns a single service by ID.
func (h *Handler) handleGetService(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	svc, ok := h.graph.GetService(id)
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(svc)
}

// handleWebSocket provides real-time topology updates.
func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("WebSocket upgrade failed")
		return
	}
	defer conn.Close()

	log.Info("WebSocket client connected")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			topology := h.graph.GetTopology()
			if err := conn.WriteJSON(topology); err != nil {
				log.WithError(err).Debug("WebSocket write failed, client disconnected")
				return
			}
		}
	}
}

// handleHealth returns the health status.
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// handleReady returns the readiness status.
func (h *Handler) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

// handleIngestStats processes incoming throughput stats from agents.
func (h *Handler) handleIngestStats(w http.ResponseWriter, r *http.Request) {
	var report ThroughputReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	log.WithFields(log.Fields{
		"node":        report.NodeName,
		"connections": len(report.Connections),
		"interval_ms": report.IntervalMs,
	}).Debug("Received throughput report")

	for _, connStats := range report.Connections {
		h.processConnectionStats(report.NodeName, connStats)
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// processConnectionStats updates connection stats in the graph.
func (h *Handler) processConnectionStats(nodeName string, stats ConnectionStatsPayload) {
	// Update the connection stats in the graph
	h.graph.UpdateConnectionStats(
		stats.SrcAddr,
		stats.DstAddr,
		stats.DstPort,
		stats.BytesSent,
		stats.BytesRecv,
		stats.BytesSentRate,
		stats.BytesRecvRate,
		stats.PacketsSent,
		stats.PacketsRecv,
	)

	log.WithFields(log.Fields{
		"src":       stats.SrcAddr,
		"dst":       fmt.Sprintf("%s:%d", stats.DstAddr, stats.DstPort),
		"sent_rate": fmt.Sprintf("%.1f B/s", stats.BytesSentRate),
		"recv_rate": fmt.Sprintf("%.1f B/s", stats.BytesRecvRate),
	}).Debug("Updated connection throughput")
}

// handleGraphStats returns graph statistics.
func (h *Handler) handleGraphStats(w http.ResponseWriter, r *http.Request) {
	stats := h.graph.Stats()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleK8sStatus returns Kubernetes watcher status.
func (h *Handler) handleK8sStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"enabled": h.k8sWatcher.IsEnabled(),
		"cache":   h.k8sWatcher.Stats(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleIngestInspection processes deep inspection data from agents.
func (h *Handler) handleIngestInspection(w http.ResponseWriter, r *http.Request) {
	var report InspectionReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	log.WithFields(log.Fields{
		"node":       report.NodeName,
		"service_id": report.ServiceID,
		"pid":        report.Inspection.PID,
		"process":    report.Inspection.ProcessName,
	}).Debug("Received inspection data")

	// Convert payload to graph inspection type
	inspection := h.convertInspection(report.Inspection)
	
	// Update the service with inspection data
	h.graph.UpdateServiceInspection(report.ServiceID, inspection)

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// convertInspection converts the API payload to the graph type.
func (h *Handler) convertInspection(p *InspectionPayload) *graph.ServiceInspection {
	if p == nil {
		return nil
	}

	inspection := &graph.ServiceInspection{
		PID:          p.PID,
		ProcessName:  p.ProcessName,
		CommandLine:  p.CommandLine,
		WorkingDir:   p.WorkingDir,
		EnvVarNames:  p.EnvVarNames,
		ListenPorts:  p.ListenPorts,
		ConfigFiles:  p.ConfigFiles,
		InspectedAt:  time.Now(),
	}

	// Convert dependencies
	if len(p.Dependencies) > 0 {
		inspection.Dependencies = make([]graph.Dependency, len(p.Dependencies))
		for i, d := range p.Dependencies {
			inspection.Dependencies[i] = graph.Dependency{
				Name:    d.Name,
				Version: d.Version,
				Type:    d.Type,
			}
		}
	}

	// Convert HTTP info
	if p.HTTPInfo != nil {
		inspection.HTTPInfo = &graph.HTTPProbeInfo{
			ServerHeader:    p.HTTPInfo.ServerHeader,
			PoweredBy:       p.HTTPInfo.PoweredBy,
			HealthEndpoint:  p.HTTPInfo.HealthEndpoint,
			HealthStatus:    p.HTTPInfo.HealthStatus,
			Endpoints:       p.HTTPInfo.Endpoints,
			ResponseHeaders: p.HTTPInfo.ResponseHeaders,
		}
	}

	// Convert DB info
	if p.DBInfo != nil {
		inspection.DBInfo = &graph.DBProbeInfo{
			Type:       p.DBInfo.Type,
			Version:    p.DBInfo.Version,
			Databases:  p.DBInfo.Databases,
			TableCount: p.DBInfo.TableCount,
		}
	}

	// Convert K8s metadata
	if p.K8sMetadata != nil {
		inspection.K8sMetadata = &graph.K8sMetadataInfo{
			PodName:        p.K8sMetadata.PodName,
			Namespace:      p.K8sMetadata.Namespace,
			ServiceAccount: p.K8sMetadata.ServiceAccount,
			Labels:         p.K8sMetadata.Labels,
			Annotations:    p.K8sMetadata.Annotations,
		}
	}

	// Convert code context (for AI analysis)
	if p.CodeContext != nil {
		inspection.CodeContext = &graph.CodeContext{
			ProjectName:    p.CodeContext.ProjectName,
			Description:    p.CodeContext.Description,
			README:         p.CodeContext.README,
			EntryPointFile: p.CodeContext.EntryPointFile,
			EntryPoint:     p.CodeContext.EntryPoint,
			Dockerfile:     p.CodeContext.Dockerfile,
		}
	}

	return inspection
}

// handleIngestMetrics processes incoming host metrics (CPU/RAM) from agents.
func (h *Handler) handleIngestMetrics(w http.ResponseWriter, r *http.Request) {
	var metrics HostMetricsPayload
	if err := json.NewDecoder(r.Body).Decode(&metrics); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	log.WithFields(log.Fields{
		"node": metrics.NodeName,
		"cpu":  fmt.Sprintf("%.1f%%", metrics.CPUPercent),
		"mem":  fmt.Sprintf("%.1f%%", metrics.MemPercent),
	}).Debug("Received host metrics")

	// Update the graph with node metrics
	h.graph.UpdateNodeMetrics(
		metrics.NodeName,
		metrics.CPUPercent,
		metrics.MemPercent,
		metrics.MemUsed,
		metrics.MemTotal,
	)

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// ============================================================================
// AI Documentation Handlers
// ============================================================================

// AIConfigRequest represents a request to update AI configuration.
type AIConfigRequest struct {
	OpenAIAPIKey    string `json:"openai_api_key,omitempty"`
	OpenAIModel     string `json:"openai_model,omitempty"`
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
	AnthropicModel  string `json:"anthropic_model,omitempty"`
	GeminiAPIKey    string `json:"gemini_api_key,omitempty"`
	GeminiModel     string `json:"gemini_model,omitempty"`
	OllamaURL       string `json:"ollama_url,omitempty"`
	OllamaModel     string `json:"ollama_model,omitempty"`
	LMStudioURL     string `json:"lmstudio_url,omitempty"`
	LMStudioModel   string `json:"lmstudio_model,omitempty"`
	DefaultProvider string `json:"default_provider,omitempty"`
}

// AIDocsRequest represents a request for AI documentation.
type AIDocsRequest struct {
	Provider string `json:"provider,omitempty"` // Optional: override default provider
}

// AIAskRequest represents a question about a service.
type AIAskRequest struct {
	Question string `json:"question"`
	Provider string `json:"provider,omitempty"` // Optional: override default provider
}

// handleAIStatus returns the status of AI providers.
func (h *Handler) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.llmManager == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":   false,
			"providers": map[string]bool{},
			"message":   "AI documentation not configured",
		})
		return
	}

	status := h.llmManager.Status()
	hasConfigured := false
	for _, configured := range status {
		if configured {
			hasConfigured = true
			break
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":   hasConfigured,
		"providers": status,
	})
}

// handleAIConfig gets or sets AI configuration.
func (h *Handler) handleAIConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "GET" {
		// Return current config (without sensitive keys)
		if h.llmManager == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"configured": false,
			})
			return
		}

		status := h.llmManager.Status()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": true,
			"providers":  status,
		})
		return
	}

	// POST: Update configuration
	var req AIConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Build new config
	config := &llm.Config{
		OpenAIAPIKey:    req.OpenAIAPIKey,
		OpenAIModel:     req.OpenAIModel,
		AnthropicAPIKey: req.AnthropicAPIKey,
		AnthropicModel:  req.AnthropicModel,
		GeminiAPIKey:    req.GeminiAPIKey,
		GeminiModel:     req.GeminiModel,
		OllamaURL:       req.OllamaURL,
		OllamaModel:     req.OllamaModel,
		LMStudioURL:     req.LMStudioURL,
		LMStudioModel:   req.LMStudioModel,
		DefaultProvider: llm.Provider(req.DefaultProvider),
	}

	// Update or create manager
	if h.llmManager == nil {
		h.llmManager = llm.NewManager(config)
		h.docsGen = llm.NewDocsGenerator(h.llmManager)
	} else {
		h.llmManager.UpdateConfig(config)
	}

	log.Info("AI configuration updated")

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "updated",
		"providers": h.llmManager.Status(),
	})
}

// handleAIDocs generates AI documentation for a service.
func (h *Handler) handleAIDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.docsGen == nil {
		http.Error(w, "AI documentation not configured. Please set API keys first.", http.StatusServiceUnavailable)
		return
	}

	// Get service ID from query parameter (handles IDs with / like "127.0.0.1/gunicorn")
	serviceID := r.URL.Query().Get("serviceId")
	if serviceID == "" {
		http.Error(w, "serviceId query parameter is required", http.StatusBadRequest)
		return
	}

	var req AIDocsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength > 0 {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Get service from graph
	service, ok := h.graph.GetService(serviceID)
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// Build service context
	ctx := h.buildServiceContext(service)

	// Generate documentation
	docReq := llm.DocumentationRequest{
		Context: ctx,
		Format:  "markdown",
	}

	var resp *llm.DocumentationResponse
	var err error

	if req.Provider != "" {
		resp, err = h.docsGen.GenerateWithProvider(r.Context(), llm.Provider(req.Provider), docReq)
	} else {
		resp, err = h.docsGen.GenerateDocumentation(r.Context(), docReq)
	}

	if err != nil {
		log.WithError(err).Error("Failed to generate AI documentation")
		http.Error(w, fmt.Sprintf("AI generation failed: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(resp)
}

// handleAIAsk answers a specific question about a service.
func (h *Handler) handleAIAsk(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.docsGen == nil {
		http.Error(w, "AI documentation not configured. Please set API keys first.", http.StatusServiceUnavailable)
		return
	}

	// Get service ID from query parameter (handles IDs with / like "127.0.0.1/gunicorn")
	serviceID := r.URL.Query().Get("serviceId")
	if serviceID == "" {
		http.Error(w, "serviceId query parameter is required", http.StatusBadRequest)
		return
	}

	var req AIAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.Question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}

	// Get service from graph
	service, ok := h.graph.GetService(serviceID)
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// Build service context
	ctx := h.buildServiceContext(service)

	// Ask question
	docReq := llm.DocumentationRequest{
		Context:  ctx,
		Question: req.Question,
	}

	resp, err := h.docsGen.AskQuestion(r.Context(), docReq)
	if err != nil {
		log.WithError(err).Error("Failed to answer question")
		http.Error(w, fmt.Sprintf("AI query failed: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(resp)
}

// handleAIProviders returns available AI providers and their models.
func (h *Handler) handleAIProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	providers := []map[string]interface{}{
		{
			"id":          "openai",
			"name":        "OpenAI",
			"description": "GPT-4 and GPT-3.5 models",
			"models":      []string{"gpt-4-turbo-preview", "gpt-4", "gpt-3.5-turbo"},
			"requires":    "api_key",
		},
		{
			"id":          "anthropic",
			"name":        "Anthropic",
			"description": "Claude 3 models",
			"models":      []string{"claude-3-opus-20240229", "claude-3-sonnet-20240229", "claude-3-haiku-20240307"},
			"requires":    "api_key",
		},
		{
			"id":          "gemini",
			"name":        "Google Gemini",
			"description": "Gemini Pro models",
			"models":      []string{"gemini-pro", "gemini-1.5-pro", "gemini-1.5-flash"},
			"requires":    "api_key",
		},
		{
			"id":          "ollama",
			"name":        "Ollama (Local)",
			"description": "Local LLM via Ollama",
			"models":      []string{"llama2", "mistral", "codellama"},
			"requires":    "local_server",
		},
		{
			"id":          "lmstudio",
			"name":        "LM Studio (Local)",
			"description": "Local LLM via LM Studio",
			"models":      []string{},
			"requires":    "local_server",
		},
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": providers,
	})
}

// buildServiceContext converts a graph Service to an LLM ServiceContext.
func (h *Handler) buildServiceContext(service *graph.Service) llm.ServiceContext {
	ctx := llm.ServiceContext{
		ServiceID:   service.ID,
		ServiceName: service.Name,
		ServiceType: service.Type,
		Technology:  service.Tech,
		NodeName:    service.Node,
		IPAddress:   service.PodIP,
	}

	// Get connections for this service
	topology := h.graph.GetTopology()

	for _, conn := range topology.Connections {
		connInfo := llm.ConnectionInfo{
			Port:        int(conn.Port),
			BytesPerSec: conn.BytesSentRate + conn.BytesRecvRate,
		}

		if conn.SourceID == service.ID {
			// Outgoing connection
			if targetSvc, ok := h.graph.GetService(conn.TargetID); ok {
				connInfo.RemoteIP = targetSvc.PodIP
				connInfo.RemoteName = targetSvc.Name
			}
			ctx.OutgoingConnections = append(ctx.OutgoingConnections, connInfo)
		} else if conn.TargetID == service.ID {
			// Incoming connection
			if sourceSvc, ok := h.graph.GetService(conn.SourceID); ok {
				connInfo.RemoteIP = sourceSvc.PodIP
				connInfo.RemoteName = sourceSvc.Name
			}
			ctx.IncomingConnections = append(ctx.IncomingConnections, connInfo)
		}
	}

	// Add inspection data if available
	if service.Inspection != nil {
		insp := service.Inspection
		ctx.ProcessName = insp.ProcessName
		if len(insp.CommandLine) > 0 {
			ctx.CommandLine = strings.Join(insp.CommandLine, " ")
		}
		ctx.ListenPorts = insp.ListenPorts
		ctx.EnvVarNames = insp.EnvVarNames
		ctx.ConfigFiles = insp.ConfigFiles

		// Dependencies
		for _, dep := range insp.Dependencies {
			ctx.Dependencies = append(ctx.Dependencies, fmt.Sprintf("%s@%s (%s)", dep.Name, dep.Version, dep.Type))
		}

		// HTTP info
		if insp.HTTPInfo != nil {
			ctx.ServerHeader = insp.HTTPInfo.ServerHeader
			ctx.HTTPEndpoints = insp.HTTPInfo.Endpoints
			ctx.HealthStatus = insp.HTTPInfo.HealthStatus
		}

		// DB info
		if insp.DBInfo != nil {
			ctx.DBType = insp.DBInfo.Type
			ctx.DBVersion = insp.DBInfo.Version
			ctx.DBDatabases = insp.DBInfo.Databases
		}

		// Source code context (for AI)
		if insp.CodeContext != nil {
			ctx.ProjectName = insp.CodeContext.ProjectName
			ctx.ProjectDesc = insp.CodeContext.Description
			ctx.README = insp.CodeContext.README
			ctx.EntryPointFile = insp.CodeContext.EntryPointFile
			ctx.EntryPointCode = insp.CodeContext.EntryPoint
			ctx.Dockerfile = insp.CodeContext.Dockerfile
		}
	}

	return ctx
}
