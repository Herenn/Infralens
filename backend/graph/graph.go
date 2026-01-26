// Package graph provides the service topology graph data structure.
package graph

import (
	"fmt"
	"sync"
	"time"
)

// Service represents a service (process) in the topology.
type Service struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`  // K8s resolved name or IP
	ResolvedName string            `json:"resolved_name,omitempty"` // Full K8s path (e.g., "Pod: namespace/name")
	Type         string            `json:"type,omitempty"`          // Service type (database, cache, web_server, etc.)
	Tech         string            `json:"tech,omitempty"`          // Specific technology (PostgreSQL, Redis, Nginx, etc.)
	Icon         string            `json:"icon,omitempty"`          // Icon identifier for frontend
	Namespace    string            `json:"namespace,omitempty"`
	Node         string            `json:"node,omitempty"`
	PodIP        string            `json:"pod_ip,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	LastSeen     time.Time         `json:"last_seen"`
	Healthy      bool              `json:"healthy"`
	
	// Deep inspection data (from protocol probing)
	Inspection   *ServiceInspection `json:"inspection,omitempty"`
}

// ServiceInspection contains deep inspection data for a service.
type ServiceInspection struct {
	PID          int32             `json:"pid,omitempty"`
	ProcessName  string            `json:"process_name,omitempty"`
	CommandLine  []string          `json:"command_line,omitempty"`
	WorkingDir   string            `json:"working_dir,omitempty"`
	EnvVarNames  []string          `json:"env_var_names,omitempty"`  // Names only (security)
	ListenPorts  []int             `json:"listen_ports,omitempty"`
	ConfigFiles  []string          `json:"config_files,omitempty"`   // File names only
	Dependencies []Dependency      `json:"dependencies,omitempty"`
	HTTPInfo     *HTTPProbeInfo    `json:"http_info,omitempty"`
	DBInfo       *DBProbeInfo      `json:"db_info,omitempty"`
	K8sMetadata  *K8sMetadataInfo  `json:"k8s_metadata,omitempty"`
	InspectedAt  time.Time         `json:"inspected_at"`
	
	// Source code context for AI analysis
	CodeContext  *CodeContext      `json:"code_context,omitempty"`
}

// CodeContext contains source code information for AI analysis.
type CodeContext struct {
	ProjectName    string            `json:"project_name,omitempty"`
	Description    string            `json:"description,omitempty"`
	README         string            `json:"readme,omitempty"`
	EntryPointFile string            `json:"entry_point_file,omitempty"`
	EntryPoint     string            `json:"entry_point,omitempty"`
	Dockerfile     string            `json:"dockerfile,omitempty"`
}

// Dependency represents a package dependency.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Type    string `json:"type"` // npm, pip, go, maven
}

// HTTPProbeInfo contains HTTP probing results.
type HTTPProbeInfo struct {
	ServerHeader    string            `json:"server_header,omitempty"`
	PoweredBy       string            `json:"x_powered_by,omitempty"`
	HealthEndpoint  string            `json:"health_endpoint,omitempty"`
	HealthStatus    int               `json:"health_status,omitempty"`
	Endpoints       []string          `json:"endpoints,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// DBProbeInfo contains database probing results.
type DBProbeInfo struct {
	Type       string   `json:"type"`                  // postgres, mysql, redis, mongodb
	Version    string   `json:"version,omitempty"`
	Databases  []string `json:"databases,omitempty"`
	TableCount int      `json:"table_count,omitempty"`
}

// K8sMetadataInfo contains Kubernetes metadata.
type K8sMetadataInfo struct {
	PodName        string            `json:"pod_name,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	ServiceAccount string            `json:"service_account,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
}

// Connection represents a connection between two services.
type Connection struct {
	ID            string    `json:"id"`
	SourceID      string    `json:"source_id"`
	TargetID      string    `json:"target_id"`
	Port          uint16    `json:"port"`
	Count         int64     `json:"count"`
	BytesSent     uint64    `json:"bytes_sent,omitempty"`
	BytesRecv     uint64    `json:"bytes_recv,omitempty"`
	BytesSentRate float64   `json:"bytes_sent_rate,omitempty"` // Bytes/second
	BytesRecvRate float64   `json:"bytes_recv_rate,omitempty"` // Bytes/second
	PacketsSent   uint64    `json:"packets_sent,omitempty"`
	PacketsRecv   uint64    `json:"packets_recv,omitempty"`
	LastSeen      time.Time `json:"last_seen"`
	Latency       float64   `json:"latency_ms,omitempty"`
}

// NodeMetrics represents resource usage for a physical/virtual node.
type NodeMetrics struct {
	NodeName   string    `json:"node_name"`
	CPUPercent float64   `json:"cpu_percent"`
	MemPercent float64   `json:"mem_percent"`
	MemUsed    uint64    `json:"mem_used"`
	MemTotal   uint64    `json:"mem_total"`
	LastSeen   time.Time `json:"last_seen"`
}

// Topology represents the complete service topology graph.
type Topology struct {
	Services    []Service               `json:"services"`
	Connections []Connection            `json:"connections"`
	NodeMetrics map[string]*NodeMetrics `json:"node_metrics,omitempty"` // Keyed by node name
	UpdatedAt   time.Time               `json:"updated_at"`
}

// ServiceGraph manages the in-memory service topology.
type ServiceGraph struct {
	mu          sync.RWMutex
	services    map[string]*Service
	connections map[string]*Connection
	nodeMetrics map[string]*NodeMetrics // CPU/RAM per node
}

// NewServiceGraph creates a new empty service graph.
func NewServiceGraph() *ServiceGraph {
	return &ServiceGraph{
		services:    make(map[string]*Service),
		connections: make(map[string]*Connection),
		nodeMetrics: make(map[string]*NodeMetrics),
	}
}

// AddOrUpdateService adds or updates a service in the graph.
func (g *ServiceGraph) AddOrUpdateService(svc Service) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Mark as healthy by default
	svc.Healthy = true

	existing, ok := g.services[svc.ID]
	if ok {
		// Update existing service
		existing.LastSeen = svc.LastSeen
		existing.Healthy = true
		if svc.Name != "" {
			existing.Name = svc.Name
		}
		if svc.DisplayName != "" {
			existing.DisplayName = svc.DisplayName
		}
		if svc.ResolvedName != "" {
			existing.ResolvedName = svc.ResolvedName
		}
		if svc.Type != "" {
			existing.Type = svc.Type
		}
		if svc.Tech != "" {
			existing.Tech = svc.Tech
		}
		if svc.Icon != "" {
			existing.Icon = svc.Icon
		}
		if svc.Node != "" {
			existing.Node = svc.Node
		}
		if svc.PodIP != "" {
			existing.PodIP = svc.PodIP
		}
		if svc.Namespace != "" {
			existing.Namespace = svc.Namespace
		}
	} else {
		// Add new service
		g.services[svc.ID] = &svc
	}
}

// UpdateServiceInspection updates the inspection data for a service.
func (g *ServiceGraph) UpdateServiceInspection(serviceID string, inspection *ServiceInspection) {
	g.mu.Lock()
	defer g.mu.Unlock()

	existing, ok := g.services[serviceID]
	if ok {
		existing.Inspection = inspection
		existing.LastSeen = time.Now()
		
		// Also update tech/type if we learned more from inspection
		if inspection.HTTPInfo != nil {
			if existing.Tech == "" && inspection.HTTPInfo.ServerHeader != "" {
				existing.Tech = inspection.HTTPInfo.ServerHeader
			}
		}
		if inspection.DBInfo != nil {
			if existing.Tech == "" && inspection.DBInfo.Type != "" {
				existing.Tech = inspection.DBInfo.Type
			}
			if inspection.DBInfo.Version != "" {
				existing.Tech = fmt.Sprintf("%s %s", inspection.DBInfo.Type, inspection.DBInfo.Version)
			}
		}
	}
}

// AddConnection adds or updates a connection in the graph.
func (g *ServiceGraph) AddConnection(conn Connection) {
	g.mu.Lock()
	defer g.mu.Unlock()

	existing, ok := g.connections[conn.ID]
	if ok {
		// Update existing connection
		existing.Count++
		existing.LastSeen = conn.LastSeen
		if conn.BytesSent > 0 {
			existing.BytesSent = conn.BytesSent // Replace with latest stats
		}
		if conn.BytesRecv > 0 {
			existing.BytesRecv = conn.BytesRecv
		}
		if conn.PacketsSent > 0 {
			existing.PacketsSent = conn.PacketsSent
		}
		if conn.PacketsRecv > 0 {
			existing.PacketsRecv = conn.PacketsRecv
		}
	} else {
		// Add new connection
		conn.Count = 1
		g.connections[conn.ID] = &conn
	}
}

// UpdateConnectionStats updates stats for a connection identified by source/target IPs and port.
func (g *ServiceGraph) UpdateConnectionStats(srcIP, dstIP string, dstPort uint16, bytesSent, bytesRecv uint64, bytesSentRate, bytesRecvRate float64, packetsSent, packetsRecv uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Build connection ID
	connID := fmt.Sprintf("%s->%s:%d", srcIP, dstIP, dstPort)

	existing, ok := g.connections[connID]
	if ok {
		existing.BytesSent = bytesSent
		existing.BytesRecv = bytesRecv
		existing.BytesSentRate = bytesSentRate
		existing.BytesRecvRate = bytesRecvRate
		existing.PacketsSent = packetsSent
		existing.PacketsRecv = packetsRecv
		existing.LastSeen = time.Now()
	}
	// If connection doesn't exist, we don't create it here
	// (it should be created by the events handler first)
}

// UpdateNodeMetrics updates CPU/RAM metrics for a node.
func (g *ServiceGraph) UpdateNodeMetrics(nodeName string, cpuPercent, memPercent float64, memUsed, memTotal uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	existing, ok := g.nodeMetrics[nodeName]
	if ok {
		existing.CPUPercent = cpuPercent
		existing.MemPercent = memPercent
		existing.MemUsed = memUsed
		existing.MemTotal = memTotal
		existing.LastSeen = time.Now()
	} else {
		g.nodeMetrics[nodeName] = &NodeMetrics{
			NodeName:   nodeName,
			CPUPercent: cpuPercent,
			MemPercent: memPercent,
			MemUsed:    memUsed,
			MemTotal:   memTotal,
			LastSeen:   time.Now(),
		}
	}
}

// GetNodeMetrics returns metrics for a specific node.
func (g *ServiceGraph) GetNodeMetrics(nodeName string) (*NodeMetrics, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	m, ok := g.nodeMetrics[nodeName]
	if !ok {
		return nil, false
	}
	// Return a copy
	copy := *m
	return &copy, true
}

// GetTopology returns a snapshot of the current topology.
func (g *ServiceGraph) GetTopology() Topology {
	g.mu.RLock()
	defer g.mu.RUnlock()

	services := make([]Service, 0, len(g.services))
	for _, svc := range g.services {
		services = append(services, *svc)
	}

	connections := make([]Connection, 0, len(g.connections))
	for _, conn := range g.connections {
		connections = append(connections, *conn)
	}

	// Include node metrics
	nodeMetrics := make(map[string]*NodeMetrics)
	for name, metrics := range g.nodeMetrics {
		copy := *metrics
		nodeMetrics[name] = &copy
	}

	return Topology{
		Services:    services,
		Connections: connections,
		NodeMetrics: nodeMetrics,
		UpdatedAt:   time.Now(),
	}
}

// GetService retrieves a service by ID.
func (g *ServiceGraph) GetService(id string) (*Service, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	svc, ok := g.services[id]
	if !ok {
		return nil, false
	}
	// Return a copy
	copy := *svc
	return &copy, true
}

// GetConnection retrieves a connection by ID.
func (g *ServiceGraph) GetConnection(id string) (*Connection, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	conn, ok := g.connections[id]
	if !ok {
		return nil, false
	}
	// Return a copy
	copy := *conn
	return &copy, true
}

// Prune removes services and connections that haven't been seen recently.
func (g *ServiceGraph) Prune(maxAge time.Duration) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	pruned := 0

	// Prune old services
	for id, svc := range g.services {
		if svc.LastSeen.Before(cutoff) {
			delete(g.services, id)
			pruned++
		}
	}

	// Prune old connections
	for id, conn := range g.connections {
		if conn.LastSeen.Before(cutoff) {
			delete(g.connections, id)
			pruned++
		}
	}

	return pruned
}

// Stats returns statistics about the graph.
func (g *ServiceGraph) Stats() map[string]int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return map[string]int{
		"services":    len(g.services),
		"connections": len(g.connections),
	}
}
