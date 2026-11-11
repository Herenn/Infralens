package handlers_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/storage"
)

// TestWebSocket_DriftMessage: newly recorded drift events reach open clients
// as a "drift" message; the delta protocol is otherwise unchanged.
func TestWebSocket_DriftMessage(t *testing.T) {
	ts, srv := newWSServer(t, time.Second, time.Hour)
	conn := dialWS(t, srv)

	var first struct {
		Type string `json:"type"`
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&first); err != nil || first.Type != "snapshot" {
		t.Fatalf("expected snapshot first, got %q (%v)", first.Type, err)
	}

	now := time.Now()
	ts.eventBus.PublishDriftDetected([]storage.DriftEvent{{
		ID: 7, DetectedAt: now, Kind: storage.DriftAppeared, ConnectionID: "api->1.1.1.1:443",
		SourceID: "api", SourceName: "api-gateway", TargetID: "1.1.1.1:443", Port: 443,
		External: true, FirstSeen: now, LastSeen: now,
	}})

	var msg struct {
		Type string                      `json:"type"`
		Data handlers.DriftEventsMessage `json:"data"`
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading drift message: %v", err)
	}
	if msg.Type != "drift" || len(msg.Data.Events) != 1 {
		t.Fatalf("unexpected message: %+v", msg)
	}
	ev := msg.Data.Events[0]
	if ev.ID != 7 || ev.Kind != "appeared" || !ev.External || ev.SourceName != "api-gateway" {
		t.Errorf("unexpected event: %+v", ev)
	}
	if msg.Data.Total != 1 || msg.Data.Truncated {
		t.Errorf("total=%d truncated=%v, want 1/false", msg.Data.Total, msg.Data.Truncated)
	}
}

// A 5000-edge rollout must not become one multi-megabyte message rendered in
// full: the message is capped and says so.
func TestWebSocket_DriftMessageIsCapped(t *testing.T) {
	ts, srv := newWSServer(t, time.Second, time.Hour)
	conn := dialWS(t, srv)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first struct{ Type string }
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	events := make([]storage.DriftEvent, 5000)
	for i := range events {
		events[i] = storage.DriftEvent{ID: int64(i + 1), DetectedAt: now, Kind: storage.DriftAppeared,
			ConnectionID: fmt.Sprintf("w%d->db", i), SourceID: fmt.Sprintf("w%d", i), TargetID: "db", FirstSeen: now, LastSeen: now}
	}
	ts.eventBus.PublishDriftDetected(events)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Type string                      `json:"type"`
		Data handlers.DriftEventsMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Data.Events) != handlers.MaxWSDriftEvents || msg.Data.Total != 5000 || !msg.Data.Truncated {
		t.Errorf("got %d events, total %d, truncated %v; want %d/5000/true",
			len(msg.Data.Events), msg.Data.Total, msg.Data.Truncated, handlers.MaxWSDriftEvents)
	}
	t.Logf("capped drift message: %d bytes", len(raw))
}
