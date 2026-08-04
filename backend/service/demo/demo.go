// Package demo provides a built-in topology simulator so InfraLens can be
// tried without a Linux host, eBPF support, or a running agent.
//
// When enabled (DEMO_MODE=true), the simulator seeds a realistic multi-node
// topology through the regular TopologyService, then continuously emits
// throughput and host-metric updates. The frontend renders it exactly like
// real agent data, including live rate changes on edges.
package demo

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// tickInterval controls how often the simulator emits updates.
const tickInterval = 2 * time.Second

// serviceRefreshInterval controls how often services are re-upserted so
// their last_seen stays ahead of the pruner.
const serviceRefreshInterval = 1 * time.Minute

// demoConnection describes a simulated connection with a base throughput
// that the simulator randomly walks around.
type demoConnection struct {
	sourceID string
	targetID string
	port     uint16
	protocol string // "tcp" or "udp"

	baseSentRate float64 // bytes/sec
	baseRecvRate float64 // bytes/sec

	// mutable simulation state
	bytesSent   uint64
	bytesRecv   uint64
	packetsSent uint64
	packetsRecv uint64
}

func (c *demoConnection) id() string {
	id := fmt.Sprintf("%s->%s:%d", c.sourceID, c.targetID, c.port)
	if c.protocol == "udp" {
		id += "/udp"
	}
	return id
}

// demoNode describes a simulated host with CPU/RAM that drifts over time.
type demoNode struct {
	name     string
	memTotal uint64

	cpuPercent float64
	memPercent float64
}

// Simulator seeds and animates a demo topology.
type Simulator struct {
	topology *service.TopologyService
	rng      *rand.Rand

	services    []storage.Service
	connections []*demoConnection
	nodes       []*demoNode
}

// NewSimulator creates a demo simulator backed by the given topology service.
func NewSimulator(topology *service.TopologyService) *Simulator {
	s := &Simulator{
		topology: topology,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	s.buildScenario()
	return s
}

// Run seeds the demo topology and emits updates until ctx is cancelled.
func (s *Simulator) Run(ctx context.Context) {
	log.Info("Demo mode enabled - simulating topology (no agent required)")

	if err := s.seed(ctx); err != nil {
		log.WithError(err).Error("Demo simulator failed to seed topology")
		return
	}

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	refreshTicker := time.NewTicker(serviceRefreshInterval)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Demo simulator stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		case <-refreshTicker.C:
			s.refreshServices(ctx)
		}
	}
}

// buildScenario defines the static demo world: a small e-commerce stack
// spread across three servers plus external client traffic.
func (s *Simulator) buildScenario() {
	const (
		nodeEdge = "demo-edge-1"
		nodeApp  = "demo-app-1"
		nodeData = "demo-data-1"
	)

	svc := func(id, name, svcType, tech, icon, node, ip string) storage.Service {
		return storage.Service{
			ID:          id,
			Name:        name,
			DisplayName: name,
			Type:        svcType,
			Tech:        tech,
			Icon:        icon,
			Node:        node,
			PodIP:       ip,
			Healthy:     true,
		}
	}

	s.services = []storage.Service{
		// External clients (no node = rendered outside server groups)
		svc("203.0.113.10", "external", "unknown", "External", "globe", "", "203.0.113.10"),

		// Edge node
		svc("10.0.1.10/nginx", "nginx", "web_server", "Nginx", "nginx", nodeEdge, "10.0.1.10"),
		svc("10.0.1.11/node", "storefront", "application", "Node.js", "nodejs", nodeEdge, "10.0.1.11"),

		// Application node
		svc("10.0.2.10/api-gateway", "api-gateway", "application", "Go", "go", nodeApp, "10.0.2.10"),
		svc("10.0.2.11/auth-service", "auth-service", "application", "Go", "go", nodeApp, "10.0.2.11"),
		svc("10.0.2.12/orders-service", "orders-service", "application", "Python/Uvicorn", "python", nodeApp, "10.0.2.12"),
		svc("10.0.2.13/payment-worker", "payment-worker", "application", "Python", "python", nodeApp, "10.0.2.13"),

		// Data node
		svc("10.0.3.10:5432", "postgres", "database", "PostgreSQL 16.2", "postgresql", nodeData, "10.0.3.10"),
		svc("10.0.3.11:6379", "redis", "cache", "Redis 7.2", "redis", nodeData, "10.0.3.11"),
		svc("10.0.3.12:9092", "kafka", "message_queue", "Kafka", "kafka", nodeData, "10.0.3.12"),
		svc("10.0.3.13:9090", "prometheus", "monitoring", "Prometheus", "prometheus", nodeData, "10.0.3.13"),
		svc("10.0.3.14:53", "dns", "application", "DNS", "dns", nodeData, "10.0.3.14"),
	}

	conn := func(src, dst string, port uint16, sentKBps, recvKBps float64) *demoConnection {
		return &demoConnection{
			sourceID:     src,
			targetID:     dst,
			port:         port,
			protocol:     "tcp",
			baseSentRate: sentKBps * 1024,
			baseRecvRate: recvKBps * 1024,
		}
	}

	udpConn := func(src, dst string, port uint16, sentKBps, recvKBps float64) *demoConnection {
		c := conn(src, dst, port, sentKBps, recvKBps)
		c.protocol = "udp"
		return c
	}

	s.connections = []*demoConnection{
		conn("203.0.113.10", "10.0.1.10/nginx", 443, 40, 400),
		conn("10.0.1.10/nginx", "10.0.1.11/node", 3000, 30, 300),
		conn("10.0.1.10/nginx", "10.0.2.10/api-gateway", 8080, 60, 220),
		conn("10.0.1.11/node", "10.0.2.10/api-gateway", 8080, 25, 120),
		conn("10.0.2.10/api-gateway", "10.0.2.11/auth-service", 5000, 15, 20),
		conn("10.0.2.10/api-gateway", "10.0.2.12/orders-service", 8000, 35, 90),
		conn("10.0.2.10/api-gateway", "10.0.3.11:6379", 6379, 20, 60),
		conn("10.0.2.11/auth-service", "10.0.3.10:5432", 5432, 8, 25),
		conn("10.0.2.11/auth-service", "10.0.3.11:6379", 6379, 10, 15),
		conn("10.0.2.12/orders-service", "10.0.3.10:5432", 5432, 45, 130),
		conn("10.0.2.12/orders-service", "10.0.3.12:9092", 9092, 55, 5),
		conn("10.0.2.13/payment-worker", "10.0.3.12:9092", 9092, 5, 70),
		conn("10.0.2.13/payment-worker", "10.0.3.10:5432", 5432, 12, 30),
		conn("10.0.3.13:9090", "10.0.2.10/api-gateway", 8080, 3, 40),
		udpConn("10.0.2.10/api-gateway", "10.0.3.14:53", 53, 1, 2),
		udpConn("10.0.2.12/orders-service", "10.0.3.14:53", 53, 1, 2),
	}

	gib := func(n uint64) uint64 { return n * 1024 * 1024 * 1024 }
	s.nodes = []*demoNode{
		{name: "demo-edge-1", memTotal: gib(8), cpuPercent: 22, memPercent: 38},
		{name: "demo-app-1", memTotal: gib(16), cpuPercent: 45, memPercent: 55},
		{name: "demo-data-1", memTotal: gib(32), cpuPercent: 30, memPercent: 68},
	}
}

// seed creates the initial services, connections, and node metrics.
func (s *Simulator) seed(ctx context.Context) error {
	s.refreshServices(ctx)

	for _, c := range s.connections {
		if err := s.topology.AddConnection(ctx, &storage.Connection{
			ID:       c.id(),
			SourceID: c.sourceID,
			TargetID: c.targetID,
			Port:     c.port,
			Protocol: c.protocol,
			LastSeen: time.Now(),
		}); err != nil {
			return err
		}
	}

	s.tick(ctx)
	return nil
}

// refreshServices re-upserts all demo services to keep last_seen fresh.
func (s *Simulator) refreshServices(ctx context.Context) {
	for i := range s.services {
		svc := s.services[i] // copy: Upsert mutates LastSeen
		svc.LastSeen = time.Now()
		if err := s.topology.AddOrUpdateService(ctx, &svc); err != nil {
			log.WithError(err).WithField("service", svc.ID).Warn("Demo simulator failed to upsert service")
		}
	}
}

// tick advances the simulation by one step: throughput random walk and
// host metric drift.
func (s *Simulator) tick(ctx context.Context) {
	elapsed := tickInterval.Seconds()

	for _, c := range s.connections {
		sentRate := s.jitter(c.baseSentRate, 0.5)
		recvRate := s.jitter(c.baseRecvRate, 0.5)

		c.bytesSent += uint64(sentRate * elapsed)
		c.bytesRecv += uint64(recvRate * elapsed)
		// Assume ~1400 byte average packets
		c.packetsSent += uint64(sentRate*elapsed/1400) + 1
		c.packetsRecv += uint64(recvRate*elapsed/1400) + 1

		if err := s.topology.UpdateConnectionStats(ctx, c.id(),
			c.bytesSent, c.bytesRecv, sentRate, recvRate,
			c.packetsSent, c.packetsRecv); err != nil {
			log.WithError(err).WithField("connection", c.id()).Debug("Demo simulator failed to update stats")
		}
	}

	for _, n := range s.nodes {
		n.cpuPercent = s.drift(n.cpuPercent, 5, 3, 95)
		n.memPercent = s.drift(n.memPercent, 2, 10, 95)

		memUsed := uint64(float64(n.memTotal) * n.memPercent / 100)
		if err := s.topology.UpdateNodeMetrics(ctx, &storage.NodeMetrics{
			NodeName:   n.name,
			CPUPercent: round1(n.cpuPercent),
			MemPercent: round1(n.memPercent),
			MemUsed:    memUsed,
			MemTotal:   n.memTotal,
			LastSeen:   time.Now(),
		}); err != nil {
			log.WithError(err).WithField("node", n.name).Debug("Demo simulator failed to update metrics")
		}
	}
}

// jitter returns base scaled by a random factor in [1-amount, 1+amount].
func (s *Simulator) jitter(base, amount float64) float64 {
	factor := 1 + (s.rng.Float64()*2-1)*amount
	return base * factor
}

// drift moves value by a random step within [-step, +step], clamped to [min, max].
func (s *Simulator) drift(value, step, min, max float64) float64 {
	value += (s.rng.Float64()*2 - 1) * step
	return math.Max(min, math.Min(max, value))
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
