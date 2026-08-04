// Package main implements the InfraLens eBPF agent.
// This agent traces TCP connections using eBPF to detect service-to-service traffic
// and collects network metrics (bytes sent/received) with throughput calculation.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Herenn/Infralens/agent/collector"
	"github.com/Herenn/Infralens/agent/inspector"
	"github.com/Herenn/Infralens/agent/metrics"
	"github.com/Herenn/Infralens/agent/updater"
)

// Command-line flags
var (
	backendAddr         = flag.String("backend", "", "Backend address or URL (e.g., localhost:8080, https://infralens.example.com)")
	apiKey              = flag.String("api-key", os.Getenv("INFRALENS_API_KEY"), "API key for backend authentication (or INFRALENS_API_KEY env)")
	nodeName            = flag.String("node", "", "Node name (defaults to hostname)")
	clusterName         = flag.String("cluster", "", "Cluster name (groups all nodes under this name in UI)")
	batchSize           = flag.Int("batch-size", 10, "Number of events to batch before sending")
	batchTimeout        = flag.Duration("batch-timeout", 1*time.Second, "Max time to wait before sending batch")
	statsInterval       = flag.Duration("stats-interval", 1*time.Second, "Interval to poll and report throughput stats")
	metricsInterval     = flag.Duration("metrics-interval", 5*time.Second, "Interval to collect and report host metrics")
	enableInspection    = flag.Bool("inspect", true, "Enable deep process inspection")
	inspectionCooldown  = flag.Duration("inspect-cooldown", 30*time.Second, "Minimum time between re-inspecting same PID")
	autoUpdate          = flag.Bool("auto-update", true, "Enable automatic self-updates")
	updateCheckInterval = flag.Duration("update-interval", 1*time.Hour, "Interval to check for updates")
	showVersion         = flag.Bool("version", false, "Show version and exit")
)

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

	// Initialize the BPF collector
	coll, err := collector.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize collector: %v\n", err)
		os.Exit(1)
	}
	defer coll.Close()

	// Setup HTTP client for backend reporting
	backendURL := normalizeBackendURL(*backendAddr)
	var httpClient *http.Client
	if backendURL != "" {
		httpClient = &http.Client{Timeout: 10 * time.Second}
		fmt.Printf("✓ Backend reporting enabled: %s\n", backendURL)
		if *apiKey != "" {
			fmt.Println("✓ API key authentication enabled")
		}
	} else {
		fmt.Println("ℹ Backend reporting disabled (use --backend=host:port to enable)")
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("\nListening for TCP connections... (Press Ctrl+C to stop)")
	fmt.Println("─────────────────────────────────────────────────────────")

	// Event batching
	eventBatch := make([]EventPayload, 0, *batchSize)
	batchTimer := time.NewTicker(*batchTimeout)
	defer batchTimer.Stop()

	// Throughput polling ticker
	statsTicker := time.NewTicker(*statsInterval)
	defer statsTicker.Stop()

	// Host metrics ticker
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
	if *autoUpdate && backendURL != "" {
		agentUpdater := updater.NewUpdater(backendURL, *updateCheckInterval)
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

	// Channel for events from collector
	eventCh := make(chan *collector.Event, 100)
	doneCh := make(chan struct{})

	// Start collector goroutine
	go func() {
		defer close(doneCh)
		if err := coll.Start(ctx, eventCh); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Collector error: %v\n", err)
		}
	}()

	// Main event loop
	for {
		select {
		case <-sig:
			fmt.Println("\n\nShutting down...")
			cancel()
			close(stopUpdateCh)
			<-doneCh
			// Send any remaining events
			if httpClient != nil && len(eventBatch) > 0 {
				sendBatch(ctx, httpClient, backendURL, *nodeName, eventBatch)
			}
			fmt.Println("Agent stopped.")
			return

		case event := <-eventCh:
			payload := eventToPayload(event)
			printEvent(payload)

			eventBatch = append(eventBatch, payload)
			if len(eventBatch) >= *batchSize {
				if httpClient != nil {
					go sendBatch(ctx, httpClient, backendURL, *nodeName, eventBatch)
				}
				eventBatch = make([]EventPayload, 0, *batchSize)
			}

			// Trigger deep inspection for new PIDs
			if processInspector != nil && httpClient != nil && shouldInspect(payload.PID) {
				go inspectAndReport(ctx, httpClient, backendURL, processInspector, payload)
			}

		case <-batchTimer.C:
			if httpClient != nil && len(eventBatch) > 0 {
				go sendBatch(ctx, httpClient, backendURL, *nodeName, eventBatch)
				eventBatch = make([]EventPayload, 0, *batchSize)
			}

		case <-statsTicker.C:
			throughput := coll.PollThroughput()
			if len(throughput) > 0 {
				printThroughput(throughput)
				if httpClient != nil {
					go sendThroughput(ctx, httpClient, backendURL, *nodeName, throughput)
				}
			}

		case <-metricsTicker.C:
			hostMetrics, err := metricsCollector.Collect()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to collect host metrics: %v\n", err)
			} else {
				printHostMetrics(hostMetrics)
				if httpClient != nil {
					go sendHostMetrics(ctx, httpClient, backendURL, hostMetrics)
				}
			}
		}
	}
}

// =============================================================================
// Payload Types
// =============================================================================

// EventPayload is the JSON structure sent to the backend
type EventPayload struct {
	PID       uint32 `json:"pid"`
	Comm      string `json:"comm"`
	SrcAddr   string `json:"src_addr"`
	DstAddr   string `json:"dst_addr"`
	DstPort   uint16 `json:"dst_port"`
	Direction uint8  `json:"direction"`
}

// EventBatch represents a batch of events sent to the backend
type EventBatch struct {
	NodeName    string         `json:"node_name"`
	ClusterName string         `json:"cluster_name,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
	Events      []EventPayload `json:"events"`
}

// ThroughputReport represents throughput stats sent to the backend
type ThroughputReport struct {
	NodeName    string                 `json:"node_name"`
	ClusterName string                 `json:"cluster_name,omitempty"`
	Timestamp   time.Time              `json:"timestamp"`
	IntervalMs  int64                  `json:"interval_ms"`
	Connections []ConnectionThroughput `json:"connections"`
}

// ConnectionThroughput represents throughput for a single connection
type ConnectionThroughput struct {
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

// InspectionReport is the JSON structure sent to the backend
type InspectionReport struct {
	NodeName    string                      `json:"node_name"`
	ClusterName string                      `json:"cluster_name,omitempty"`
	Timestamp   time.Time                   `json:"timestamp"`
	ServiceID   string                      `json:"service_id"`
	Inspection  *inspector.InspectionResult `json:"inspection"`
}

// =============================================================================
// Conversion Functions
// =============================================================================

func eventToPayload(event *collector.Event) EventPayload {
	return EventPayload{
		PID:       event.Pid,
		Comm:      event.Comm,
		SrcAddr:   event.SrcAddr.String(),
		DstAddr:   event.DstAddr.String(),
		DstPort:   event.DstPort,
		Direction: event.Direction,
	}
}

func throughputToPayload(t collector.Throughput) ConnectionThroughput {
	return ConnectionThroughput{
		PID:           t.Pid,
		Comm:          t.Comm,
		SrcAddr:       t.SrcAddr,
		DstAddr:       t.DstAddr,
		SrcPort:       t.SrcPort,
		DstPort:       t.DstPort,
		BytesSent:     t.BytesSent,
		BytesRecv:     t.BytesRecv,
		BytesSentRate: t.BytesSentRate,
		BytesRecvRate: t.BytesRecvRate,
		PacketsSent:   t.PacketsSent,
		PacketsRecv:   t.PacketsRecv,
	}
}

// =============================================================================
// Output Functions
// =============================================================================

func printEvent(payload EventPayload) {
	dirStr := "→"
	dirLabel := "OUT"
	if payload.Direction == collector.DirectionInbound {
		dirStr = "←"
		dirLabel = "IN"
	}
	fmt.Printf("[%s] [%d] %s %s %s %s:%d\n",
		dirLabel, payload.PID, payload.Comm, payload.SrcAddr, dirStr, payload.DstAddr, payload.DstPort)
}

func printThroughput(stats []collector.Throughput) {
	fmt.Printf("\n📊 Throughput (%d active):\n", len(stats))
	for _, s := range stats {
		fmt.Printf("   [%d] %s | %s:%d → %s:%d | ↑ %s/s ↓ %s/s\n",
			s.Pid, s.Comm, s.SrcAddr, s.SrcPort, s.DstAddr, s.DstPort,
			formatBytes(uint64(s.BytesSentRate)), formatBytes(uint64(s.BytesRecvRate)))
	}
}

func printHostMetrics(m *metrics.HostMetrics) {
	fmt.Printf("💻 Host Metrics: CPU %.1f%% | MEM %.1f%% (%s / %s)\n",
		m.CPUPercent, m.MemPercent, formatBytes(m.MemUsed), formatBytes(m.MemTotal))
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

// =============================================================================
// HTTP Reporting Functions
// =============================================================================

// normalizeBackendURL converts a backend address into a full base URL.
// Accepts bare host:port ("localhost:8080") as well as full URLs
// ("https://infralens.example.com"). Returns "" for empty input.
func normalizeBackendURL(addr string) string {
	addr = strings.TrimRight(strings.TrimSpace(addr), "/")
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

// postJSON marshals payload and POSTs it to url, attaching the configured
// API key (if any). Errors are returned so callers can add context.
func postJSON(ctx context.Context, client *http.Client, url string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if *apiKey != "" {
		req.Header.Set("X-API-Key", *apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("backend rejected request (status %d): check --api-key matches the backend API_KEY", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("backend returned status %d", resp.StatusCode)
	}
	return nil
}

func sendBatch(ctx context.Context, client *http.Client, backendURL, nodeName string, events []EventPayload) {
	batch := EventBatch{
		NodeName:    nodeName,
		ClusterName: *clusterName,
		Timestamp:   time.Now(),
		Events:      events,
	}

	if err := postJSON(ctx, client, backendURL+"/api/v1/events", batch); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "Failed to send events: %v\n", err)
	}
}

func sendThroughput(ctx context.Context, client *http.Client, backendURL, nodeName string, stats []collector.Throughput) {
	connections := make([]ConnectionThroughput, len(stats))
	for i, t := range stats {
		connections[i] = throughputToPayload(t)
	}

	report := ThroughputReport{
		NodeName:    nodeName,
		ClusterName: *clusterName,
		Timestamp:   time.Now(),
		IntervalMs:  statsInterval.Milliseconds(),
		Connections: connections,
	}

	if err := postJSON(ctx, client, backendURL+"/api/v1/stats", report); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "Failed to send throughput: %v\n", err)
	}
}

func sendHostMetrics(ctx context.Context, client *http.Client, backendURL string, m *metrics.HostMetrics) {
	if err := postJSON(ctx, client, backendURL+"/api/v1/metrics", m); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "Failed to send host metrics: %v\n", err)
	}
}

// =============================================================================
// Process Inspection
// =============================================================================

func shouldInspect(pid uint32) bool {
	inspectedPIDsMu.Lock()
	defer inspectedPIDsMu.Unlock()

	lastInspected, seen := inspectedPIDs[pid]
	if seen && time.Since(lastInspected) < *inspectionCooldown {
		return false
	}

	inspectedPIDs[pid] = time.Now()
	return true
}

func inspectAndReport(ctx context.Context, client *http.Client, backendURL string, insp *inspector.Inspector, event EventPayload) {
	result, err := insp.InspectProcess(int32(event.PID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Inspection failed for PID %d: %v\n", event.PID, err)
		return
	}

	// Build service ID (must match backend's ID format)
	var serviceID string
	if event.Direction == collector.DirectionOutbound {
		serviceID = fmt.Sprintf("%s/%s", event.SrcAddr, event.Comm)
	} else {
		serviceID = fmt.Sprintf("%s/%s", event.DstAddr, event.Comm)
	}

	report := InspectionReport{
		NodeName:    *nodeName,
		ClusterName: *clusterName,
		Timestamp:   time.Now(),
		ServiceID:   serviceID,
		Inspection:  result,
	}

	if err := postJSON(ctx, client, backendURL+"/api/v1/inspection", report); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Failed to send inspection: %v\n", err)
		}
		return
	}

	fmt.Printf("🔍 Inspected [%d] %s: %d ports, %d deps, %d configs\n",
		event.PID, event.Comm, len(result.ListenPorts), len(result.Dependencies), len(result.ConfigFiles))
}
