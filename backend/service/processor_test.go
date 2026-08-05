package service

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

func newTestProcessor(t *testing.T) (*EventProcessor, storage.Store) {
	t.Helper()

	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bus := NewEventBus(256)
	t.Cleanup(bus.Close)

	return NewEventProcessor(NewTopologyService(store, bus), nil), store
}

// TestInboundThroughputReachesTheConnection is a regression test.
//
// The agent reports throughput from the local socket's point of view. For an
// accepted connection that is the reverse of how the accept event recorded it,
// and keyed on the client's ephemeral port rather than the listening port, so
// the stats update matched no row and inbound edges never showed throughput.
func TestInboundThroughputReachesTheConnection(t *testing.T) {
	p, store := newTestProcessor(t)
	ctx := context.Background()

	const (
		clientIP    = "10.0.0.9"
		serverIP    = "10.0.0.1"
		listenPort  = 8080
		clientPort  = 54321
		serverComm  = "nginx"
		wantSent    = uint64(4096)
		wantRecvked = uint64(2048)
	)

	// An accepted connection: client -> server on the listening port.
	if err := p.ProcessTCPEvent(ctx, "node-1", TCPEvent{
		PID:       1,
		Comm:      serverComm,
		SrcAddr:   clientIP,
		DstAddr:   serverIP,
		DstPort:   listenPort,
		Direction: DirectionInbound,
	}); err != nil {
		t.Fatalf("processing inbound event: %v", err)
	}

	conns, err := store.Connections().List(ctx, storage.ConnectionFilter{})
	if err != nil {
		t.Fatalf("listing connections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected exactly 1 connection, got %d", len(conns))
	}
	connID := conns[0].ID

	// The matching throughput sample, as the agent actually reports it:
	// local (server) as src, peer (client) as dst, peer's ephemeral port as
	// dst_port, and our listening port as src_port.
	if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{{
		PID:       1,
		Comm:      serverComm,
		SrcAddr:   serverIP,
		DstAddr:   clientIP,
		SrcPort:   listenPort,
		DstPort:   clientPort,
		BytesSent: wantSent,
		BytesRecv: wantRecvked,
	}}); err != nil {
		t.Fatalf("processing throughput: %v", err)
	}

	got, err := store.Connections().Get(ctx, connID)
	if err != nil {
		t.Fatalf("getting connection: %v", err)
	}
	if got == nil {
		t.Fatalf("connection %q vanished", connID)
	}
	if got.BytesSent != wantSent || got.BytesRecv != wantRecvked {
		t.Errorf("inbound throughput not applied to %q: bytes_sent=%d (want %d), bytes_recv=%d (want %d)",
			connID, got.BytesSent, wantSent, got.BytesRecv, wantRecvked)
	}
}

// TestOutboundThroughputStillWorks guards against the inbound fix regressing
// the direction that already worked.
func TestOutboundThroughputStillWorks(t *testing.T) {
	p, store := newTestProcessor(t)
	ctx := context.Background()

	if err := p.ProcessTCPEvent(ctx, "node-1", TCPEvent{
		PID:       2,
		Comm:      "curl",
		SrcAddr:   "10.0.0.5",
		DstAddr:   "10.0.0.6",
		DstPort:   443,
		Direction: DirectionOutbound,
	}); err != nil {
		t.Fatalf("processing outbound event: %v", err)
	}

	conns, _ := store.Connections().List(ctx, storage.ConnectionFilter{})
	if len(conns) != 1 {
		t.Fatalf("expected exactly 1 connection, got %d", len(conns))
	}

	if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{{
		PID:       2,
		Comm:      "curl",
		SrcAddr:   "10.0.0.5",
		DstAddr:   "10.0.0.6",
		SrcPort:   40000,
		DstPort:   443,
		BytesSent: 100,
		BytesRecv: 200,
	}}); err != nil {
		t.Fatalf("processing throughput: %v", err)
	}

	got, _ := store.Connections().Get(ctx, conns[0].ID)
	if got == nil || got.BytesSent != 100 || got.BytesRecv != 200 {
		t.Errorf("outbound throughput not applied: %+v", got)
	}
}

// TestThroughputSamplesAreSummedPerEdge covers many client sockets collapsing
// onto one inbound edge. The stats update writes absolute values, so without
// aggregation the last sample processed would replace the others rather than
// the edge reporting their total.
func TestThroughputSamplesAreSummedPerEdge(t *testing.T) {
	p, store := newTestProcessor(t)
	ctx := context.Background()

	const serverIP, listenPort, comm = "10.0.0.1", uint16(9090), "api"

	if err := p.ProcessTCPEvent(ctx, "node-1", TCPEvent{
		PID: 3, Comm: comm,
		SrcAddr: "10.0.0.50", DstAddr: serverIP, DstPort: listenPort,
		Direction: DirectionInbound,
	}); err != nil {
		t.Fatalf("processing inbound event: %v", err)
	}

	conns, _ := store.Connections().List(ctx, storage.ConnectionFilter{})
	if len(conns) != 1 {
		t.Fatalf("expected exactly 1 connection, got %d", len(conns))
	}
	connID := conns[0].ID

	// Three different clients, three sockets, one topology edge.
	batch := []ThroughputStats{
		{PID: 3, Comm: comm, SrcAddr: serverIP, DstAddr: "10.0.0.50", SrcPort: listenPort, DstPort: 1111, BytesSent: 10, BytesRecv: 1},
		{PID: 3, Comm: comm, SrcAddr: serverIP, DstAddr: "10.0.0.50", SrcPort: listenPort, DstPort: 2222, BytesSent: 20, BytesRecv: 2},
		{PID: 3, Comm: comm, SrcAddr: serverIP, DstAddr: "10.0.0.50", SrcPort: listenPort, DstPort: 3333, BytesSent: 30, BytesRecv: 3},
	}
	if err := p.ProcessThroughputBatch(ctx, "node-1", batch); err != nil {
		t.Fatalf("processing throughput: %v", err)
	}

	got, _ := store.Connections().Get(ctx, connID)
	if got == nil {
		t.Fatal("connection vanished")
	}
	if got.BytesSent != 60 || got.BytesRecv != 6 {
		t.Errorf("samples not summed: bytes_sent=%d (want 60), bytes_recv=%d (want 6)",
			got.BytesSent, got.BytesRecv)
	}
}

// TestThroughputForUnknownConnectionIsIgnored confirms a sample for a
// connection we've never seen is a no-op rather than an error or a new row.
func TestThroughputForUnknownConnectionIsIgnored(t *testing.T) {
	p, store := newTestProcessor(t)
	ctx := context.Background()

	if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{{
		PID: 9, Comm: "ghost",
		SrcAddr: "10.1.1.1", DstAddr: "10.1.1.2",
		SrcPort: 1234, DstPort: 80,
		BytesSent: 1, BytesRecv: 1,
	}}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	count, err := store.Connections().Count(ctx)
	if err != nil {
		t.Fatalf("counting connections: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no connections to be created, got %d", count)
	}
}

// TestStatsUpdateRefreshesLastSeen matters because the pruner deletes rows by
// last_seen: a busy connection that only ever reports throughput (no new
// connect events) must not be pruned out from under the topology.
func TestStatsUpdateRefreshesLastSeen(t *testing.T) {
	p, store := newTestProcessor(t)
	ctx := context.Background()

	if err := p.ProcessTCPEvent(ctx, "node-1", TCPEvent{
		PID: 1, Comm: "svc", SrcAddr: "10.2.0.1", DstAddr: "10.2.0.2",
		DstPort: 80, Direction: DirectionOutbound,
	}); err != nil {
		t.Fatalf("processing event: %v", err)
	}

	conns, _ := store.Connections().List(ctx, storage.ConnectionFilter{})
	before := conns[0].LastSeen

	time.Sleep(10 * time.Millisecond)
	if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{{
		PID: 1, Comm: "svc", SrcAddr: "10.2.0.1", DstAddr: "10.2.0.2",
		SrcPort: 5000, DstPort: 80, BytesSent: 5,
	}}); err != nil {
		t.Fatalf("processing throughput: %v", err)
	}

	got, _ := store.Connections().Get(ctx, conns[0].ID)
	if !got.LastSeen.After(before) {
		t.Errorf("last_seen not refreshed by a stats update: before=%v after=%v", before, got.LastSeen)
	}
}
