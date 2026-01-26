// Package main implements the InfraLens eBPF agent.
// This agent traces TCP connect syscalls using eBPF to detect service-to-service traffic
// and collects network metrics (bytes sent/received) with throughput calculation.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/perf"
	"github.com/cilium/ebpf/rlimit"
	bpf "github.com/Herenn/Infralens/agent/ebpf"
	"github.com/Herenn/Infralens/agent/inspector"
	"github.com/Herenn/Infralens/agent/metrics"
	"github.com/Herenn/Infralens/agent/updater"
)

const (
	AF_INET  = 2
	AF_INET6 = 10

	// Direction constants (matching eBPF program)
	DIRECTION_OUTBOUND = 0  // Client initiated (connect)
	DIRECTION_INBOUND  = 1  // Server accepted (accept)
)

// EventT matches the C struct event_t in traffic.c
type EventT struct {
	Pid       uint32
	Af        uint32      // Address family (AF_INET or AF_INET6)
	SaddrV4   uint32      // Source IPv4
	DaddrV4   uint32      // Destination IPv4
	SaddrV6   [16]byte    // Source IPv6
	DaddrV6   [16]byte    // Destination IPv6
	Dport     uint16      // Destination port (network byte order)
	Direction uint8       // 0 = outbound (connect), 1 = inbound (accept)
	Pad       uint8
	Comm      [16]int8
}

// ConnKeyT matches the C struct conn_key_t in traffic.c
type ConnKeyT struct {
	SockPtr uint64
}

// ConnStatsT matches the C struct conn_stats_t in traffic.c
// Note: Must match C struct layout including padding for 8-byte alignment
type ConnStatsT struct {
	BytesSent   uint64
	BytesRecv   uint64
	PacketsSent uint64
	PacketsRecv uint64
	Pid         uint32
	Af          uint32
	SaddrV4     uint32
	DaddrV4     uint32
	SaddrV6     [16]byte
	DaddrV6     [16]byte
	Dport       uint16
	Sport       uint16
	Comm        [16]int8
	_           [4]byte  // Padding for 8-byte alignment of LastUpdate
	LastUpdate  uint64
}

// ConnectionStats represents stats for reporting
type ConnectionStats struct {
	BytesSent   uint64 `json:"bytes_sent"`
	BytesRecv   uint64 `json:"bytes_recv"`
	PacketsSent uint64 `json:"packets_sent"`
	PacketsRecv uint64 `json:"packets_recv"`
}

// EventPayload is the JSON structure sent to the backend
type EventPayload struct {
	PID       uint32           `json:"pid"`
	Comm      string           `json:"comm"`
	SrcAddr   string           `json:"src_addr"`
	DstAddr   string           `json:"dst_addr"`
	DstPort   uint16           `json:"dst_port"`
	Direction uint8            `json:"direction"` // 0 = outbound (connect), 1 = inbound (accept)
	Stats     *ConnectionStats `json:"stats,omitempty"`
}

// EventBatch represents a batch of events sent to the backend
type EventBatch struct {
	NodeName    string         `json:"node_name"`
	ClusterName string         `json:"cluster_name,omitempty"` // Groups nodes in UI
	Timestamp   time.Time      `json:"timestamp"`
	Events      []EventPayload `json:"events"`
}

// ThroughputReport represents throughput stats sent to the backend
type ThroughputReport struct {
	NodeName       string                     `json:"node_name"`
	ClusterName    string                     `json:"cluster_name,omitempty"` // Groups nodes in UI
	Timestamp      time.Time                  `json:"timestamp"`
	IntervalMs     int64                      `json:"interval_ms"`
	Connections    []ConnectionThroughput     `json:"connections"`
}

// ConnectionThroughput represents throughput for a single connection
type ConnectionThroughput struct {
	PID            uint32  `json:"pid"`
	Comm           string  `json:"comm"`
	SrcAddr        string  `json:"src_addr"`
	DstAddr        string  `json:"dst_addr"`
	SrcPort        uint16  `json:"src_port"`
	DstPort        uint16  `json:"dst_port"`
	BytesSent      uint64  `json:"bytes_sent"`       // Total bytes sent
	BytesRecv      uint64  `json:"bytes_recv"`       // Total bytes received
	BytesSentRate  float64 `json:"bytes_sent_rate"`  // Bytes/second sent
	BytesRecvRate  float64 `json:"bytes_recv_rate"`  // Bytes/second received
	PacketsSent    uint64  `json:"packets_sent"`
	PacketsRecv    uint64  `json:"packets_recv"`
}

// connKey is used to track connections across polls
type connKey struct {
	srcAddr string
	dstAddr string
	dstPort uint16
}

// prevStats stores previous poll values for delta calculation
type prevStats struct {
	bytesSent   uint64
	bytesRecv   uint64
	packetsSent uint64
	packetsRecv uint64
}

var (
	backendAddr        = flag.String("backend", "", "Backend address (e.g., localhost:8080)")
	nodeName           = flag.String("node", "", "Node name (defaults to hostname)")
	clusterName        = flag.String("cluster", "", "Cluster name (groups all nodes under this name in UI)")
	batchSize          = flag.Int("batch-size", 10, "Number of events to batch before sending")
	batchTimeout       = flag.Duration("batch-timeout", 1*time.Second, "Max time to wait before sending batch")
	statsInterval      = flag.Duration("stats-interval", 1*time.Second, "Interval to poll and report throughput stats")
	metricsInterval    = flag.Duration("metrics-interval", 5*time.Second, "Interval to collect and report host metrics")
	enableInspection   = flag.Bool("inspect", true, "Enable deep process inspection")
	inspectionCooldown = flag.Duration("inspect-cooldown", 30*time.Second, "Minimum time between re-inspecting same PID")
	autoUpdate         = flag.Bool("auto-update", true, "Enable automatic self-updates")
	updateCheckInterval = flag.Duration("update-interval", 1*time.Hour, "Interval to check for updates")
	showVersion        = flag.Bool("version", false, "Show version and exit")
)

// Global map to store previous stats for delta calculation
var previousStats = make(map[connKey]prevStats)
var lastPollTime = time.Now()

// Track inspected PIDs to avoid re-inspecting too frequently
var (
	inspectedPIDs   = make(map[uint32]time.Time)
	inspectedPIDsMu sync.Mutex
)

func main() {
	flag.Parse()

	// Show version and exit
	if *showVersion {
		fmt.Printf("InfraLens Agent %s\n", updater.GetVersion())
		os.Exit(0)
	}

	// Get node name
	if *nodeName == "" {
		hostname, _ := os.Hostname()
		*nodeName = hostname
	}
	
	fmt.Printf("InfraLens Agent %s starting...\n", updater.GetVersion())

	// Remove memory lock limit for eBPF (required on older kernels)
	if err := rlimit.RemoveMemlock(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove memlock limit: %v\n", err)
	}

	// 1. Load the compiled BPF objects
	objs, err := bpf.LoadObjects()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load BPF objects: %v\n", err)
		os.Exit(1)
	}
	defer objs.Close()
	fmt.Println("✓ BPF objects loaded successfully")

	// 2. Attach IPv4 probes (tcp_v4_connect)
	kpV4, err := link.Kprobe("tcp_v4_connect", objs.KprobeTcpV4Connect, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to attach kprobe to tcp_v4_connect: %v\n", err)
		os.Exit(1)
	}
	defer kpV4.Close()
	fmt.Println("✓ Kprobe attached to tcp_v4_connect")

	krpV4, err := link.Kretprobe("tcp_v4_connect", objs.KretprobeTcpV4Connect, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to attach kretprobe to tcp_v4_connect: %v\n", err)
		os.Exit(1)
	}
	defer krpV4.Close()
	fmt.Println("✓ Kretprobe attached to tcp_v4_connect")

	// 3. Attach IPv6 probes (tcp_v6_connect)
	kpV6, err := link.Kprobe("tcp_v6_connect", objs.KprobeTcpV6Connect, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to attach kprobe to tcp_v6_connect: %v\n", err)
		fmt.Println("  (IPv6 tracing disabled)")
	} else {
		defer kpV6.Close()
		fmt.Println("✓ Kprobe attached to tcp_v6_connect")

		krpV6, err := link.Kretprobe("tcp_v6_connect", objs.KretprobeTcpV6Connect, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to attach kretprobe to tcp_v6_connect: %v\n", err)
		} else {
			defer krpV6.Close()
			fmt.Println("✓ Kretprobe attached to tcp_v6_connect")
		}
	}

	// 4. Attach tcp_sendmsg probe for bytes sent tracking
	kpSendmsg, err := link.Kprobe("tcp_sendmsg", objs.KprobeTcpSendmsg, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to attach kprobe to tcp_sendmsg: %v\n", err)
		fmt.Println("  (Bytes sent tracking disabled)")
	} else {
		defer kpSendmsg.Close()
		fmt.Println("✓ Kprobe attached to tcp_sendmsg (throughput tracking enabled)")
	}

	// 5. Attach tcp_recvmsg probes for bytes received tracking
	kpRecvmsg, err := link.Kprobe("tcp_recvmsg", objs.KprobeTcpRecvmsg, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to attach kprobe to tcp_recvmsg: %v\n", err)
		fmt.Println("  (Bytes recv tracking disabled)")
	} else {
		defer kpRecvmsg.Close()
		fmt.Println("✓ Kprobe attached to tcp_recvmsg")

		krpRecvmsg, err := link.Kretprobe("tcp_recvmsg", objs.KretprobeTcpRecvmsg, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to attach kretprobe to tcp_recvmsg: %v\n", err)
		} else {
			defer krpRecvmsg.Close()
			fmt.Println("✓ Kretprobe attached to tcp_recvmsg")
		}
	}

	// 6. Attach tcp_close probe for cleanup
	kpClose, err := link.Kprobe("tcp_close", objs.KprobeTcpClose, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to attach kprobe to tcp_close: %v\n", err)
	} else {
		defer kpClose.Close()
		fmt.Println("✓ Kprobe attached to tcp_close")
	}

	// 7. Attach inet_csk_accept kretprobe for incoming connections (accept)
	krpAccept, err := link.Kretprobe("inet_csk_accept", objs.KretprobeInetCskAccept, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to attach kretprobe to inet_csk_accept: %v\n", err)
		fmt.Println("  (Incoming connection tracing disabled)")
	} else {
		defer krpAccept.Close()
		fmt.Println("✓ Kretprobe attached to inet_csk_accept (ingress tracking enabled)")
	}

	// 8. Initialize perf event reader on the events map
	reader, err := perf.NewReader(objs.Events, os.Getpagesize()*64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create perf reader: %v\n", err)
		os.Exit(1)
	}
	defer reader.Close()
	fmt.Println("✓ Perf reader initialized")

	// 9. Setup HTTP client for backend reporting
	var httpClient *http.Client
	if *backendAddr != "" {
		httpClient = &http.Client{Timeout: 10 * time.Second}
		fmt.Printf("✓ Backend reporting enabled: %s\n", *backendAddr)
	} else {
		fmt.Println("ℹ Backend reporting disabled (use --backend=host:port to enable)")
	}

	// Setup graceful shutdown on SIGINT/SIGTERM
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("\nListening for TCP connections... (Press Ctrl+C to stop)")
	fmt.Println("─────────────────────────────────────────────────────────")

	// Event batching
	eventBatch := make([]EventPayload, 0, *batchSize)
	batchTimer := time.NewTicker(*batchTimeout)
	defer batchTimer.Stop()

	// Throughput polling ticker (1 second for accurate rates)
	statsTicker := time.NewTicker(*statsInterval)
	defer statsTicker.Stop()

	// Host metrics ticker (5 seconds - less frequent than throughput)
	metricsTicker := time.NewTicker(*metricsInterval)
	defer metricsTicker.Stop()
	metricsCollector := metrics.NewCollectorWithCluster(*nodeName, *clusterName)
	fmt.Printf("✓ Host metrics collection enabled (every %s)\n", *metricsInterval)

	// Deep process inspector
	var processInspector *inspector.Inspector
	if *enableInspection {
		processInspector = inspector.New()
		fmt.Println("✓ Deep process inspection enabled")
	}

	// Auto-update checker
	stopUpdateCh := make(chan struct{})
	if *autoUpdate && *backendAddr != "" {
		agentUpdater := updater.NewUpdater(*backendAddr, *updateCheckInterval)
		agentUpdater.SetUpdateCallback(func() {
			fmt.Println("📦 New version available! Attempting self-update...")
			if err := agentUpdater.SelfUpdate(); err != nil {
				fmt.Printf("⚠️ Self-update failed: %v\n", err)
				fmt.Println("   Please run the install script again to update manually.")
			} else {
				fmt.Println("✓ Update installed! Restarting...")
				if err := updater.RestartSelf(); err != nil {
					fmt.Printf("⚠️ Restart failed: %v\n", err)
					fmt.Println("   Please restart the agent manually: systemctl restart infralens-agent")
				}
			}
		})
		go agentUpdater.StartPeriodicCheck(stopUpdateCh)
		fmt.Printf("✓ Auto-update enabled (checking every %s)\n", *updateCheckInterval)
	}

	// Channels
	eventCh := make(chan EventPayload, 100)
	doneCh := make(chan struct{})

	// Event reader goroutine
	go func() {
		defer close(doneCh)
		for {
			record, err := reader.Read()
			if err != nil {
				if errors.Is(err, perf.ErrClosed) {
					return
				}
				fmt.Fprintf(os.Stderr, "Error reading perf event: %v\n", err)
				continue
			}

			if record.LostSamples > 0 {
				fmt.Fprintf(os.Stderr, "Warning: lost %d samples\n", record.LostSamples)
				continue
			}

			var event EventT
			if err := binary.Read(bytes.NewReader(record.RawSample), binary.LittleEndian, &event); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to parse event: %v\n", err)
				continue
			}

			payload := eventToPayload(&event)
			printEvent(payload)

			select {
			case eventCh <- payload:
			default:
				// Channel full, drop event
			}
		}
	}()

	// Main loop
	for {
		select {
		case <-sig:
			fmt.Println("\n\nShutting down...")
			cancel()
			close(stopUpdateCh) // Stop auto-update checker
			reader.Close()
			<-doneCh
			// Send any remaining events
			if httpClient != nil && len(eventBatch) > 0 {
				sendBatch(ctx, httpClient, *backendAddr, *nodeName, eventBatch)
			}
			fmt.Println("Agent stopped.")
			return

		case payload := <-eventCh:
			eventBatch = append(eventBatch, payload)
			if len(eventBatch) >= *batchSize {
				if httpClient != nil {
					go sendBatch(ctx, httpClient, *backendAddr, *nodeName, eventBatch)
				}
				eventBatch = make([]EventPayload, 0, *batchSize)
			}

			// Trigger deep inspection for new PIDs (async, don't block event processing)
			if processInspector != nil && httpClient != nil && shouldInspect(payload.PID) {
				go inspectAndReport(ctx, httpClient, processInspector, payload)
			}

		case <-batchTimer.C:
			if httpClient != nil && len(eventBatch) > 0 {
				go sendBatch(ctx, httpClient, *backendAddr, *nodeName, eventBatch)
				eventBatch = make([]EventPayload, 0, *batchSize)
			}

		case <-statsTicker.C:
			// Poll connection stats and calculate throughput
			throughput := pollThroughput(objs.ConnStats)
			if len(throughput) > 0 {
				printThroughput(throughput)
				if httpClient != nil {
					go sendThroughput(ctx, httpClient, *backendAddr, *nodeName, throughput)
				}
			}

		case <-metricsTicker.C:
			// Collect and send host metrics (CPU, RAM)
			hostMetrics, err := metricsCollector.Collect()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to collect host metrics: %v\n", err)
			} else {
				printHostMetrics(hostMetrics)
				if httpClient != nil {
					go sendHostMetrics(ctx, httpClient, *backendAddr, hostMetrics)
				}
			}
		}
	}
}

// pollThroughput reads stats from BPF map and calculates throughput (bytes/sec)
func pollThroughput(m *ebpf.Map) []ConnectionThroughput {
	now := time.Now()
	intervalSec := now.Sub(lastPollTime).Seconds()
	lastPollTime = now

	var throughputs []ConnectionThroughput
	var key ConnKeyT
	var value ConnStatsT
	currentStats := make(map[connKey]ConnStatsT)

	// Read all current stats
	iter := m.Iterate()
	for iter.Next(&key, &value) {
		var srcAddr, dstAddr string
		switch value.Af {
		case AF_INET:
			srcAddr = intToIPv4(value.SaddrV4)
			dstAddr = intToIPv4(value.DaddrV4)
		case AF_INET6:
			srcAddr = net.IP(value.SaddrV6[:]).String()
			dstAddr = net.IP(value.DaddrV6[:]).String()
		default:
			continue
		}

		ck := connKey{
			srcAddr: srcAddr,
			dstAddr: dstAddr,
			dstPort: ntohs(value.Dport),
		}
		currentStats[ck] = value
	}

	if err := iter.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error iterating conn_stats map: %v\n", err)
	}

	// Calculate deltas and throughput
	for ck, current := range currentStats {
		prev, hasPrev := previousStats[ck]
		
		var deltaSent, deltaRecv, deltaPktSent, deltaPktRecv uint64
		if hasPrev {
			// Calculate delta (handle counter wrapping)
			if current.BytesSent >= prev.bytesSent {
				deltaSent = current.BytesSent - prev.bytesSent
			}
			if current.BytesRecv >= prev.bytesRecv {
				deltaRecv = current.BytesRecv - prev.bytesRecv
			}
			if current.PacketsSent >= prev.packetsSent {
				deltaPktSent = current.PacketsSent - prev.packetsSent
			}
			if current.PacketsRecv >= prev.packetsRecv {
				deltaPktRecv = current.PacketsRecv - prev.packetsRecv
			}
		} else {
			// First time seeing this connection, use current values
			deltaSent = current.BytesSent
			deltaRecv = current.BytesRecv
			deltaPktSent = current.PacketsSent
			deltaPktRecv = current.PacketsRecv
		}

		// Only report if there's actual traffic
		if deltaSent > 0 || deltaRecv > 0 {
			// Calculate rate (bytes per second)
			var sentRate, recvRate float64
			if intervalSec > 0 {
				sentRate = float64(deltaSent) / intervalSec
				recvRate = float64(deltaRecv) / intervalSec
			}

			throughputs = append(throughputs, ConnectionThroughput{
				PID:           current.Pid,
				Comm:          commToString(current.Comm),
				SrcAddr:       ck.srcAddr,
				DstAddr:       ck.dstAddr,
				SrcPort:       current.Sport,
				DstPort:       ck.dstPort,
				BytesSent:     current.BytesSent,
				BytesRecv:     current.BytesRecv,
				BytesSentRate: sentRate,
				BytesRecvRate: recvRate,
				PacketsSent:   deltaPktSent,
				PacketsRecv:   deltaPktRecv,
			})
		}

		// Update previous stats
		previousStats[ck] = prevStats{
			bytesSent:   current.BytesSent,
			bytesRecv:   current.BytesRecv,
			packetsSent: current.PacketsSent,
			packetsRecv: current.PacketsRecv,
		}
	}

	// Clean up stale entries from previousStats
	for ck := range previousStats {
		if _, exists := currentStats[ck]; !exists {
			delete(previousStats, ck)
		}
	}

	return throughputs
}

func printThroughput(stats []ConnectionThroughput) {
	fmt.Printf("\n📊 Throughput (%d active):\n", len(stats))
	for _, s := range stats {
		fmt.Printf("   [%d] %s | %s:%d → %s:%d | ↑ %s/s ↓ %s/s\n",
			s.PID,
			s.Comm,
			s.SrcAddr,
			s.SrcPort,
			s.DstAddr,
			s.DstPort,
			formatBytes(uint64(s.BytesSentRate)),
			formatBytes(uint64(s.BytesRecvRate)),
		)
	}
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func sendThroughput(ctx context.Context, client *http.Client, backendAddr, nodeName string, stats []ConnectionThroughput) {
	report := ThroughputReport{
		NodeName:    nodeName,
		ClusterName: *clusterName,
		Timestamp:   time.Now(),
		IntervalMs:  statsInterval.Milliseconds(),
		Connections: stats,
	}

	data, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal throughput: %v\n", err)
		return
	}

	url := fmt.Sprintf("http://%s/api/v1/stats", backendAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create stats request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Failed to send throughput: %v\n", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "Backend returned status %d for stats\n", resp.StatusCode)
	}
}

func eventToPayload(event *EventT) EventPayload {
	var srcAddr, dstAddr string
	port := ntohs(event.Dport)

	switch event.Af {
	case AF_INET:
		srcAddr = intToIPv4(event.SaddrV4)
		dstAddr = intToIPv4(event.DaddrV4)
	case AF_INET6:
		srcAddr = net.IP(event.SaddrV6[:]).String()
		dstAddr = net.IP(event.DaddrV6[:]).String()
	}

	return EventPayload{
		PID:       event.Pid,
		Comm:      commToString(event.Comm),
		SrcAddr:   srcAddr,
		DstAddr:   dstAddr,
		DstPort:   port,
		Direction: event.Direction,
	}
}

func printEvent(payload EventPayload) {
	dirStr := "→"      // Outbound
	dirLabel := "OUT"
	if payload.Direction == DIRECTION_INBOUND {
		dirStr = "←"   // Inbound
		dirLabel = "IN"
	}
	fmt.Printf("[%s] [%d] %s %s %s %s:%d\n",
		dirLabel,
		payload.PID,
		payload.Comm,
		payload.SrcAddr,
		dirStr,
		payload.DstAddr,
		payload.DstPort,
	)
}

func sendBatch(ctx context.Context, client *http.Client, backendAddr, nodeName string, events []EventPayload) {
	batch := EventBatch{
		NodeName:    nodeName,
		ClusterName: *clusterName,
		Timestamp:   time.Now(),
		Events:      events,
	}

	data, err := json.Marshal(batch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal batch: %v\n", err)
		return
	}

	url := fmt.Sprintf("http://%s/api/v1/events", backendAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Failed to send events: %v\n", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "Backend returned status %d\n", resp.StatusCode)
	}
}

// intToIPv4 converts a uint32 IP address to dotted decimal notation
func intToIPv4(ip uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d",
		byte(ip),
		byte(ip>>8),
		byte(ip>>16),
		byte(ip>>24))
}

// commToString converts the comm array to a Go string
func commToString(comm [16]int8) string {
	var buf bytes.Buffer
	for _, c := range comm {
		if c == 0 {
			break
		}
		buf.WriteByte(byte(c))
	}
	return buf.String()
}

// ntohs converts a uint16 from network byte order to host byte order
func ntohs(n uint16) uint16 {
	return (n>>8)&0xff | (n&0xff)<<8
}

// printHostMetrics prints host metrics to stdout
func printHostMetrics(m *metrics.HostMetrics) {
	fmt.Printf("💻 Host Metrics: CPU %.1f%% | MEM %.1f%% (%s / %s)\n",
		m.CPUPercent,
		m.MemPercent,
		formatBytes(m.MemUsed),
		formatBytes(m.MemTotal),
	)
}

// sendHostMetrics sends host metrics to the backend
func sendHostMetrics(ctx context.Context, client *http.Client, backendAddr string, m *metrics.HostMetrics) {
	data, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal host metrics: %v\n", err)
		return
	}

	url := fmt.Sprintf("http://%s/api/v1/metrics", backendAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create metrics request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Failed to send host metrics: %v\n", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "Backend returned status %d for metrics\n", resp.StatusCode)
	}
}

// shouldInspect checks if we should inspect this PID (rate limiting)
func shouldInspect(pid uint32) bool {
	inspectedPIDsMu.Lock()
	defer inspectedPIDsMu.Unlock()

	lastInspected, seen := inspectedPIDs[pid]
	if seen && time.Since(lastInspected) < *inspectionCooldown {
		return false // Recently inspected, skip
	}

	// Mark as inspected now
	inspectedPIDs[pid] = time.Now()
	return true
}

// InspectionReport is the JSON structure sent to the backend
type InspectionReport struct {
	NodeName    string                      `json:"node_name"`
	ClusterName string                      `json:"cluster_name,omitempty"` // Groups nodes in UI
	Timestamp   time.Time                   `json:"timestamp"`
	ServiceID   string                      `json:"service_id"`
	Inspection  *inspector.InspectionResult `json:"inspection"`
}

// inspectAndReport performs deep inspection on a process and sends results to backend
func inspectAndReport(ctx context.Context, client *http.Client, insp *inspector.Inspector, event EventPayload) {
	// Inspect the process
	result, err := insp.InspectProcess(int32(event.PID))
	if err != nil {
		// Non-critical error, just log and continue
		fmt.Fprintf(os.Stderr, "Inspection failed for PID %d: %v\n", event.PID, err)
		return
	}

	// Build report with service ID (must match backend's ID format: IP/process)
	var serviceID string
	if event.Direction == DIRECTION_OUTBOUND {
		// Outbound: local process making connection
		serviceID = fmt.Sprintf("%s/%s", event.SrcAddr, event.Comm)
	} else {
		// Inbound: local process receiving connection
		serviceID = fmt.Sprintf("%s/%s", event.DstAddr, event.Comm)
	}

	report := InspectionReport{
		NodeName:    *nodeName,
		ClusterName: *clusterName,
		Timestamp:   time.Now(),
		ServiceID:   serviceID,
		Inspection:  result,
	}

	data, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal inspection: %v\n", err)
		return
	}

	url := fmt.Sprintf("http://%s/api/v1/inspection", *backendAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create inspection request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Failed to send inspection: %v\n", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		fmt.Printf("🔍 Inspected [%d] %s: %d ports, %d deps, %d configs\n",
			event.PID,
			event.Comm,
			len(result.ListenPorts),
			len(result.Dependencies),
			len(result.ConfigFiles),
		)
	}
}
