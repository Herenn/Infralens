package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/k8s"
	"github.com/Herenn/Infralens/backend/pkg/fingerprint"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// Direction constants for TCP events.
const (
	DirectionOutbound = 0 // Client initiated (connect)
	DirectionInbound  = 1 // Server accepted (accept)
)

// TCPEvent represents a TCP connection event from an agent.
type TCPEvent struct {
	PID       uint32 `json:"pid"`
	Comm      string `json:"comm"`
	SrcAddr   string `json:"src_addr"`
	DstAddr   string `json:"dst_addr"`
	DstPort   uint16 `json:"dst_port"`
	Direction uint8  `json:"direction"`
}

// ThroughputStats represents throughput statistics for a connection.
type ThroughputStats struct {
	PID           uint32  `json:"pid"`
	Comm          string  `json:"comm"`
	SrcAddr       string  `json:"src_addr"`
	DstAddr       string  `json:"dst_addr"`
	SrcPort       uint16  `json:"src_port"`
	DstPort       uint16  `json:"dst_port"`
	BytesSent     uint64  `json:"bytes_sent"`
	BytesRecv     uint64  `json:"bytes_recv"`
	BytesSentRate float64 `json:"bytes_sent_rate"`
	BytesRecvRate float64 `json:"bytes_recv_rate"`
	PacketsSent   uint64  `json:"packets_sent"`
	PacketsRecv   uint64  `json:"packets_recv"`
}

// HostMetrics represents CPU/RAM metrics from an agent.
type HostMetrics struct {
	NodeName    string  `json:"node_name"`
	ClusterName string  `json:"cluster_name,omitempty"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemPercent  float64 `json:"mem_percent"`
	MemUsed     uint64  `json:"mem_used"`
	MemTotal    uint64  `json:"mem_total"`
}

// EventProcessor handles incoming events from agents.
type EventProcessor struct {
	topology   *TopologyService
	k8sWatcher *k8s.Watcher
}

// NewEventProcessor creates a new event processor.
func NewEventProcessor(topology *TopologyService, k8sWatcher *k8s.Watcher) *EventProcessor {
	return &EventProcessor{
		topology:   topology,
		k8sWatcher: k8sWatcher,
	}
}

// ProcessTCPEvent processes a single TCP event.
func (p *EventProcessor) ProcessTCPEvent(ctx context.Context, nodeName string, event TCPEvent) error {
	now := time.Now()

	// Resolve source IP to Kubernetes resource name
	var srcResolved, dstResolved string
	if p.k8sWatcher != nil {
		srcResolved = p.k8sWatcher.Resolve(event.SrcAddr)
		dstResolved = p.k8sWatcher.Resolve(event.DstAddr)
	} else {
		// No K8s watcher, use raw IP addresses
		srcResolved = event.SrcAddr
		dstResolved = event.DstAddr
	}

	// Check if IPs resolved to K8s resources
	srcIsK8s := isK8sResource(srcResolved)
	dstIsK8s := isK8sResource(dstResolved)

	// Create service IDs
	var srcID, dstID string
	if event.Direction == DirectionOutbound {
		if srcIsK8s {
			srcID = srcResolved
		} else {
			srcID = makeServiceIDWithProcess(event.SrcAddr, event.Comm)
		}
		if dstIsK8s {
			dstID = dstResolved
		} else {
			dstID = makeServiceIDWithPort(event.DstAddr, event.DstPort)
		}
	} else {
		if srcIsK8s {
			srcID = srcResolved
		} else {
			srcID = makeServiceID(event.SrcAddr)
		}
		if dstIsK8s {
			dstID = dstResolved
		} else {
			dstID = makeServiceIDWithProcess(event.DstAddr, event.Comm)
		}
	}

	// Build connection ID
	connID := fmt.Sprintf("%s->%s:%d", srcID, dstID, event.DstPort)

	// For INBOUND events, check if connection already exists to avoid duplicates
	if event.Direction == DirectionInbound {
		exists, err := p.topology.ConnectionExists(ctx, connID)
		if err != nil {
			log.WithError(err).Warn("Error checking connection existence")
		}
		if exists {
			log.WithField("conn_id", connID).Debug("Skipping duplicate inbound connection")
			return nil
		}
	}

	// Determine service names and fingerprints
	var srcName, dstName string
	var srcInfo, dstInfo fingerprint.ServiceInfo

	if event.Direction == DirectionOutbound {
		srcName = getServiceName(srcResolved, event.Comm)
		srcInfo = fingerprint.Identify(event.Comm, 0)
		dstName = getServiceName(dstResolved, fmt.Sprintf(":%d", event.DstPort))
		dstInfo = fingerprint.IdentifyByPort(event.DstPort)
	} else {
		srcName = getServiceName(srcResolved, "external")
		srcInfo = fingerprint.ServiceInfo{Type: fingerprint.TypeUnknown, Tech: "External", Icon: "globe"}
		dstName = getServiceName(dstResolved, event.Comm)
		dstInfo = fingerprint.Identify(event.Comm, event.DstPort)
	}

	// Create/update source service
	srcService := &storage.Service{
		ID:           srcID,
		Name:         srcName,
		DisplayName:  srcResolved,
		Type:         string(srcInfo.Type),
		Tech:         srcInfo.Tech,
		Icon:         srcInfo.Icon,
		PodIP:        event.SrcAddr,
		ResolvedName: srcResolved,
		LastSeen:     now,
		Healthy:      true,
	}
	if event.Direction == DirectionOutbound {
		srcService.Node = nodeName
	}
	if err := p.topology.AddOrUpdateService(ctx, srcService); err != nil {
		log.WithError(err).WithField("service_id", srcID).Error("Failed to add source service")
	}

	// Create/update destination service
	dstService := &storage.Service{
		ID:           dstID,
		Name:         dstName,
		DisplayName:  dstResolved,
		Type:         string(dstInfo.Type),
		Tech:         dstInfo.Tech,
		Icon:         dstInfo.Icon,
		PodIP:        event.DstAddr,
		ResolvedName: dstResolved,
		LastSeen:     now,
		Healthy:      true,
	}
	if event.Direction == DirectionInbound {
		dstService.Node = nodeName
	}
	if err := p.topology.AddOrUpdateService(ctx, dstService); err != nil {
		log.WithError(err).WithField("service_id", dstID).Error("Failed to add destination service")
	}

	// Create/update connection
	conn := &storage.Connection{
		ID:       connID,
		SourceID: srcID,
		TargetID: dstID,
		Port:     event.DstPort,
		LastSeen: now,
	}
	if err := p.topology.AddConnection(ctx, conn); err != nil {
		log.WithError(err).WithField("conn_id", connID).Error("Failed to add connection")
	}

	log.WithFields(log.Fields{
		"conn_id":   connID,
		"direction": map[uint8]string{0: "outbound", 1: "inbound"}[event.Direction],
		"src":       srcID,
		"dst":       dstID,
		"port":      event.DstPort,
	}).Debug("Processed connection event")

	return nil
}

// ProcessThroughputStats processes throughput statistics.
func (p *EventProcessor) ProcessThroughputStats(ctx context.Context, nodeName string, stats ThroughputStats) error {
	// Build connection ID
	var srcResolved, dstResolved string
	if p.k8sWatcher != nil {
		srcResolved = p.k8sWatcher.Resolve(stats.SrcAddr)
		dstResolved = p.k8sWatcher.Resolve(stats.DstAddr)
	} else {
		srcResolved = stats.SrcAddr
		dstResolved = stats.DstAddr
	}

	srcIsK8s := isK8sResource(srcResolved)
	dstIsK8s := isK8sResource(dstResolved)

	var srcID, dstID string
	if srcIsK8s {
		srcID = srcResolved
	} else {
		srcID = makeServiceIDWithProcess(stats.SrcAddr, stats.Comm)
	}
	if dstIsK8s {
		dstID = dstResolved
	} else {
		dstID = makeServiceIDWithPort(stats.DstAddr, stats.DstPort)
	}

	connID := fmt.Sprintf("%s->%s:%d", srcID, dstID, stats.DstPort)

	// Update connection stats
	err := p.topology.UpdateConnectionStats(ctx, connID,
		stats.BytesSent, stats.BytesRecv,
		stats.BytesSentRate, stats.BytesRecvRate,
		stats.PacketsSent, stats.PacketsRecv)
	if err != nil {
		// Connection might not exist yet - this is okay
		log.WithField("conn_id", connID).Debug("Connection not found for stats update")
	}

	return nil
}

// ProcessHostMetrics processes host metrics.
func (p *EventProcessor) ProcessHostMetrics(ctx context.Context, metrics HostMetrics) error {
	// Use cluster name for grouping if provided
	nodeName := metrics.NodeName
	if metrics.ClusterName != "" {
		nodeName = metrics.ClusterName
	}

	m := &storage.NodeMetrics{
		NodeName:   nodeName,
		CPUPercent: metrics.CPUPercent,
		MemPercent: metrics.MemPercent,
		MemUsed:    metrics.MemUsed,
		MemTotal:   metrics.MemTotal,
		LastSeen:   time.Now(),
	}

	return p.topology.UpdateNodeMetrics(ctx, m)
}

// ProcessInspection processes service inspection data.
func (p *EventProcessor) ProcessInspection(ctx context.Context, serviceID string, insp *ServiceInspection) error {
	return p.topology.UpdateServiceInspection(ctx, serviceID, insp)
}

// Helper functions

func isK8sResource(resolved string) bool {
	return strings.HasPrefix(resolved, "Deploy:") ||
		strings.HasPrefix(resolved, "STS:") ||
		strings.HasPrefix(resolved, "DS:") ||
		strings.HasPrefix(resolved, "Job:") ||
		strings.HasPrefix(resolved, "App:") ||
		strings.HasPrefix(resolved, "Pod:") ||
		strings.HasPrefix(resolved, "Svc:") ||
		strings.HasPrefix(resolved, "RS:")
}

func getServiceName(resolved, fallback string) string {
	if isK8sResource(resolved) {
		parts := strings.Split(resolved, "/")
		if len(parts) > 1 {
			return parts[len(parts)-1]
		}
		if idx := strings.Index(resolved, ": "); idx > 0 {
			return resolved[idx+2:]
		}
		return resolved
	}
	return fallback
}

func makeServiceID(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}
	return ip.String()
}

func makeServiceIDWithProcess(ipStr, processName string) string {
	ip := net.ParseIP(ipStr)
	ipPart := ipStr
	if ip != nil {
		ipPart = ip.String()
	}

	proc := processName
	if idx := strings.LastIndex(proc, "/"); idx >= 0 {
		proc = proc[idx+1:]
	}

	systemProcs := map[string]bool{"sshd": true, "systemd": true, "init": true}
	if systemProcs[proc] {
		return ipPart
	}

	return fmt.Sprintf("%s/%s", ipPart, proc)
}

func makeServiceIDWithPort(ipStr string, port uint16) string {
	ip := net.ParseIP(ipStr)
	ipPart := ipStr
	if ip != nil {
		ipPart = ip.String()
	}
	return fmt.Sprintf("%s:%d", ipPart, port)
}
