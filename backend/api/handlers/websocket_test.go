package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// newWSServer starts an httptest server serving the WebSocket handler with
// aggressive keepalive timings so tests exercise the ping path quickly.
func newWSServer(t *testing.T, ping, resync time.Duration) (*testServer, *httptest.Server) {
	t.Helper()

	ts := newTestServer(t)
	wsHandler := handlers.NewWebSocketHandler(ts.topology, ts.eventBus)
	wsHandler.SetKeepalive(ping, resync)

	srv := httptest.NewServer(http.HandlerFunc(wsHandler.HandleWebSocket))
	t.Cleanup(srv.Close)

	return ts, srv
}

func dialWS(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestWebSocket_ConcurrentPingAndEventWrites is a regression test for a panic
// ("concurrent write to websocket connection") that killed the whole process:
// pings were written from a separate goroutine while the event loop wrote
// deltas to the same connection. gorilla/websocket allows only one writer.
//
// The handler runs inside httptest's server goroutine, so a panic here fails
// the test rather than being swallowed.
func TestWebSocket_ConcurrentPingAndEventWrites(t *testing.T) {
	ts, srv := newWSServer(t, time.Millisecond, 2*time.Millisecond)

	var pings int64
	for i := 0; i < 4; i++ {
		conn := dialWS(t, srv)
		conn.SetPingHandler(func(string) error {
			atomic.AddInt64(&pings, 1)
			return nil
		})

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		t.Cleanup(func() {
			conn.Close()
			wg.Wait()
		})
	}

	// Hammer the bus so delta writes interleave with ping and resync writes.
	deadline := time.After(750 * time.Millisecond)
	for done := false; !done; {
		select {
		case <-deadline:
			done = true
		default:
			ts.eventBus.PublishMetricsUpdated(service.MetricsEvent{
				NodeName:   "node-1",
				CPUPercent: 42,
				MemPercent: 17,
			})
			ts.eventBus.PublishServiceCreated(service.ServiceEvent{
				ServiceID: "10.0.0.1/nginx",
				Name:      "nginx",
				Healthy:   true,
			})
		}
	}

	if got := atomic.LoadInt64(&pings); got == 0 {
		t.Error("client received no pings; keepalive is not running")
	}
}

// TestWebSocket_ClientCloseIsDetected verifies the server tears the
// subscription down when the client goes away. Previously nothing read from
// the connection, so a departed client was only noticed on the next write.
func TestWebSocket_ClientCloseIsDetected(t *testing.T) {
	ts, srv := newWSServer(t, time.Second, time.Hour)

	conn := dialWS(t, srv)

	// Drain the initial snapshot so the handler is in its select loop.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}

	if got := ts.eventBus.SubscriberCount(); got != 1 {
		t.Fatalf("expected 1 subscriber, got %d", got)
	}

	conn.Close()

	// The read pump should observe the close and unwind the handler without
	// waiting for a write to fail.
	waitFor(t, 2*time.Second, func() bool {
		return ts.eventBus.SubscriberCount() == 0
	})
}

// TestWebSocket_SnapshotThenDeltas checks the wire protocol: an initial
// snapshot followed by per-entity delta messages.
func TestWebSocket_SnapshotThenDeltas(t *testing.T) {
	ts, srv := newWSServer(t, time.Second, time.Hour)

	conn := dialWS(t, srv)

	var first struct {
		Type string `json:"type"`
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}
	if first.Type != "snapshot" {
		t.Fatalf("expected first message type %q, got %q", "snapshot", first.Type)
	}

	// A service must exist in storage for the handler to emit a delta.
	if err := ts.topology.AddOrUpdateService(t.Context(), &storage.Service{
		ID:       "10.0.0.7/redis",
		Name:     "redis",
		LastSeen: time.Now(),
	}); err != nil {
		t.Fatalf("adding service: %v", err)
	}

	var msg struct {
		Type string `json:"type"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading delta: %v", err)
	}
	if msg.Type != "service" {
		t.Fatalf("expected message type %q, got %q", "service", msg.Type)
	}
	if msg.Data.ID != "10.0.0.7/redis" {
		t.Fatalf("expected service id %q, got %q", "10.0.0.7/redis", msg.Data.ID)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
