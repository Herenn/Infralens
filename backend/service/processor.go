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

// TCPEvent represents a network connection event from an agent (TCP or UDP).
type TCPEvent struct {
	PID       uint32 `json:"pid"`
	Comm      string `json:"comm"`
	SrcAddr   string `json:"src_addr"`
	DstAddr   string `json:"dst_addr"`
	DstPort   uint16 `json:"dst_port"`
	Direction uint8  `json:"direction"`
	Protocol  string `json:"protocol,omitempty"` // "tcp" (default) or "udp"
}

// ThroughputStats represents throughput statistics for a connection.
type ThroughputStats struct {
	PID           uint32  `json:"pid"`
	Comm          string  `json:"comm"`
	SrcAddr       string  `json:"src_addr"`
	DstAddr       string  `json:"dst_addr"`
	SrcPort       uint16  `json:"src_port"`
	DstPort       uint16  `json:"dst_port"`
	Protocol      string  `json:"protocol,omitempty"` // "tcp" (default) or "udp"
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

	// Build connection ID (UDP flows get a suffix so they never collide with
	// TCP connections between the same endpoints; TCP IDs stay unchanged)
	connID := makeConnectionID(srcID, dstID, event.DstPort, event.Protocol)

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
		Protocol: normalizeProtocol(event.Protocol),
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

// aggregatedStats accumulates several sockets' counters onto one topology edge.
type aggregatedStats struct {
	bytesSent     uint64
	bytesRecv     uint64
	bytesSentRate float64
	bytesRecvRate float64
	packetsSent   uint64
	packetsRecv   uint64
}

// connectionIDsForStats returns every connection ID a throughput sample could
// belong to.
//
// The agent reports one sample per socket, always from the local socket's point
// of view: src is the local end, dst is the peer, dst_port is the peer's port
// and src_port is the local port. For a connection this host opened that lines
// up with the outbound connection ID directly. For a connection this host
// *accepted* it does not: the accept event recorded client -> server on the
// listening port, while the sample says server -> client on the client's
// ephemeral port. Those never matched, so inbound edges silently never received
// any throughput at all.
//
// Both forms are returned. Only one of them corresponds to a row that exists,
// and updating a connection ID that isn't stored affects no rows, so trying
// both is harmless and avoids a lookup per sample.
func (p *EventProcessor) connectionIDsForStats(stats ThroughputStats) []string {
	resolve := func(addr string) string {
		if p.k8sWatcher != nil {
			return p.k8sWatcher.Resolve(addr)
		}
		return addr
	}

	localResolved := resolve(stats.SrcAddr)
	peerResolved := resolve(stats.DstAddr)

	// Identity of the local process, as both directions record it.
	localID := localResolved
	if !isK8sResource(localResolved) {
		localID = makeServiceIDWithProcess(stats.SrcAddr, stats.Comm)
	}

	// Outbound: local/comm -> peer:peerPort, keyed on the peer's port.
	peerAsDest := peerResolved
	if !isK8sResource(peerResolved) {
		peerAsDest = makeServiceIDWithPort(stats.DstAddr, stats.DstPort)
	}
	ids := []string{makeConnectionID(localID, peerAsDest, stats.DstPort, stats.Protocol)}

	// Inbound: peer -> local/comm, keyed on our listening port (src_port).
	// Only meaningful when we actually have a local port to key on.
	if stats.SrcPort != 0 {
		peerAsSource := peerResolved
		if !isK8sResource(peerResolved) {
			peerAsSource = makeServiceID(stats.DstAddr)
		}
		inbound := makeConnectionID(peerAsSource, localID, stats.SrcPort, stats.Protocol)
		if inbound != ids[0] {
			ids = append(ids, inbound)
		}
	}

	return ids
}

// ProcessThroughputStats processes throughput statistics for a single socket.
func (p *EventProcessor) ProcessThroughputStats(ctx context.Context, nodeName string, stats ThroughputStats) error {
	return p.ProcessThroughputBatch(ctx, nodeName, []ThroughputStats{stats})
}

// ProcessThroughputBatch processes a report's worth of throughput samples.
//
// Samples are summed per connection ID before being written. A topology edge
// can cover many sockets — every client connected to one listening port
// collapses onto a single inbound edge — and the stats update sets absolute
// values, so writing each sample individually meant the last one processed
// overwrote the rest instead of the edge showing their total.
func (p *EventProcessor) ProcessThroughputBatch(ctx context.Context, nodeName string, batch []ThroughputStats) error {
	if len(batch) == 0 {
		return nil
	}

	totals := make(map[string]*aggregatedStats, len(batch))
	for _, stats := range batch {
		for _, connID := range p.connectionIDsForStats(stats) {
			agg, ok := totals[connID]
			if !ok {
				agg = &aggregatedStats{}
				totals[connID] = agg
			}
			agg.bytesSent += stats.BytesSent
			agg.bytesRecv += stats.BytesRecv
			agg.bytesSentRate += stats.BytesSentRate
			agg.bytesRecvRate += stats.BytesRecvRate
			agg.packetsSent += stats.PacketsSent
			agg.packetsRecv += stats.PacketsRecv
		}
	}

	for connID, agg := range totals {
		err := p.topology.UpdateConnectionStats(ctx, connID,
			agg.bytesSent, agg.bytesRecv,
			agg.bytesSentRate, agg.bytesRecvRate,
			agg.packetsSent, agg.packetsRecv)
		if err != nil {
			// The connection may not exist: either it hasn't been seen yet, or
			// this is the candidate ID for the direction that doesn't apply.
			log.WithField("conn_id", connID).Debug("Connection not found for stats update")
		}
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

// normalizeProtocol maps agent-reported protocols to "tcp" or "udp".
// Older agents don't send a protocol at all, which means TCP.
func normalizeProtocol(protocol string) string {
	if protocol == "udp" {
		return "udp"
	}
	return "tcp"
}

// makeConnectionID builds a stable connection ID. UDP flows are suffixed so
// they never collide with TCP connections between the same endpoints; TCP
// IDs keep the historical format for backward compatibility.
func makeConnectionID(srcID, dstID string, port uint16, protocol string) string {
	id := fmt.Sprintf("%s->%s:%d", srcID, dstID, port)
	if normalizeProtocol(protocol) == "udp" {
		id += "/udp"
	}
	return id
}
