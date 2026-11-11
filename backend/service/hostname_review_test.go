package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Regression tests for the review of the DNS-names branch.

func throughputSample(hostname string) ThroughputStats {
	return ThroughputStats{Comm: "checkout", SrcAddr: checkoutIP, DstAddr: stripeIP, DstPort: 443, SrcPort: 40000,
		BytesSent: 1, DstHostname: hostname}
}

// Ported from the review PoC: concurrent reports for one edge with
// different names (replicas on several nodes) must not lose any. It lost a
// name in 95 of 100 rounds before connLocks.
func TestConcurrentHostnameMergesLoseNothing(t *testing.T) {
	p, _, store := newHostnameProcessor(t)
	ctx := context.Background()
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")
	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("")); err != nil {
		t.Fatal(err)
	}
	const rounds, perRound = 100, 6
	for r := 0; r < rounds; r++ {
		if err := store.Connections().SetHostnames(ctx, connID, nil); err != nil {
			t.Fatal(err)
		}
		names := make([]string, perRound)
		var wg sync.WaitGroup
		for i := range names {
			names[i] = fmt.Sprintf("n%d-%d.example", r, i)
			wg.Add(1)
			go func(h string, i int) {
				defer wg.Done()
				if i%2 == 0 {
					_ = p.ProcessTCPEvent(ctx, "node-1", outbound(h))
				} else {
					_ = p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{throughputSample(h)})
				}
			}(names[i], i)
		}
		wg.Wait()
		got := connHostnames(t, store, connID)
		for _, n := range names {
			if !containsString(got, n) {
				t.Fatalf("round %d: lost %q (have %v)", r, n, got)
			}
		}
	}
}

// drain collects the events published so far.
func drain(sub *Subscriber) []Event {
	var out []Event
	for {
		select {
		case e := <-sub.Events:
			out = append(out, e)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

func serviceUpdatedFor(events []Event, id string) int {
	last := -1
	for i, e := range events {
		if se, ok := e.Data.(ServiceEvent); ok && e.Type == EventServiceUpdated && se.ServiceID == id {
			last = i
		}
	}
	return last
}

// The live UI derives a node's label from its incoming connections, so when
// a connection's names change the target service must be re-published.
// Before the fix, a name that arrived on a throughput report (after the
// connect event) never reached open browsers.
func TestHostnameChangePublishesTargetService(t *testing.T) {
	p, ts, _ := newHostnameProcessor(t)
	ctx := context.Background()
	sub := ts.eventBus.Subscribe("test")
	defer ts.eventBus.Unsubscribe("test")

	// Connect event without a name; then the name arrives with throughput.
	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("")); err != nil {
		t.Fatal(err)
	}
	drain(sub)
	if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{throughputSample("api.stripe.com")}); err != nil {
		t.Fatal(err)
	}
	events := drain(sub)
	if serviceUpdatedFor(events, stripeID) < 0 {
		t.Fatalf("no service update for %s after its name arrived; events: %+v", stripeID, events)
	}
	// What a WebSocket client fetches on that event now carries the name.
	if svc, _ := ts.GetService(ctx, stripeID); svc == nil || !eqNames(svc.Hostnames, []string{"api.stripe.com"}) {
		t.Errorf("service after update = %+v", svc)
	}

	// Event path: the processor stores the service before the connection,
	// so the update carrying the name must come after the connection's.
	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("files.stripe.com")); err != nil {
		t.Fatal(err)
	}
	events = drain(sub)
	lastConn := -1
	for i, e := range events {
		if e.Type == EventConnectionUpdated || e.Type == EventConnectionCreated {
			lastConn = i
		}
	}
	if su := serviceUpdatedFor(events, stripeID); su < lastConn || lastConn < 0 {
		t.Errorf("service update (%d) not after the connection carrying the name (%d): %+v", su, lastConn, events)
	}

	// No change, no extra service event.
	if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{throughputSample("api.stripe.com")}); err != nil {
		t.Fatal(err)
	}
	if serviceUpdatedFor(drain(sub), stripeID) >= 0 {
		t.Error("service re-published although its names did not change")
	}
}

// An IP literal posing as a name would label an IP node with a different IP.
func TestNumericHostnameRejected(t *testing.T) {
	p, _, store := newHostnameProcessor(t)
	ctx := context.Background()
	for _, h := range []string{"52.1.2.4", "52.1.2.4.", "host.123"} {
		if err := p.ProcessTCPEvent(ctx, "node-1", outbound(h)); err != nil {
			t.Fatal(err)
		}
	}
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")
	if got := connHostnames(t, store, connID); len(got) != 0 {
		t.Errorf("numeric names stored: %v", got)
	}
}

// A wrong (stale, or forged) name must not stay the label: the name observed
// most recently wins, and names not observed for HostnameMaxAge disappear.
func TestStaleHostnameLosesLabelAndExpires(t *testing.T) {
	p, ts, store := newHostnameProcessor(t)
	ctx := context.Background()
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")
	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// A wrong name, observed by several callers an hour ago.
	if err := store.Connections().SetHostnames(ctx, connID, []storage.Hostname{{Name: "evil-exfil.example", LastSeen: now.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	// The right name is observed now.
	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("api.stripe.com")); err != nil {
		t.Fatal(err)
	}
	svc, _ := ts.GetService(ctx, stripeID)
	if svc == nil || len(svc.Hostnames) == 0 || svc.Hostnames[0] != "api.stripe.com" {
		t.Fatalf("most recent name not primary: %+v", svc)
	}

	// Names unseen for longer than HostnameMaxAge are no longer shown.
	if err := store.Connections().SetHostnames(ctx, connID, []storage.Hostname{
		{Name: "api.stripe.com", LastSeen: now.Add(-time.Hour)},
		{Name: "old.example", LastSeen: now.Add(-storage.HostnameMaxAge - time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	topo, _ := ts.GetTopology(ctx)
	if got := serviceByID(t, topo, stripeID).Hostnames; !eqNames(got, []string{"api.stripe.com"}) {
		t.Errorf("expired name still shown: %v", got)
	}
	// ...and the next write drops it from storage.
	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("api.stripe.com")); err != nil {
		t.Fatal(err)
	}
	if got := connHostnames(t, store, connID); !eqNames(got, []string{"api.stripe.com"}) {
		t.Errorf("expired name kept on write: %v", got)
	}
}

// Steady throughput with the same name does not rewrite the row: last-seen
// only moves in HostnameRefresh steps.
func TestRepeatedHostnameDoesNotRewrite(t *testing.T) {
	p, _, store := newHostnameProcessor(t)
	ctx := context.Background()
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")
	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("api.stripe.com")); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Connections().Get(ctx, connID)
	for i := 0; i < 3; i++ {
		_ = p.ProcessTCPEvent(ctx, "node-1", outbound("api.stripe.com"))
	}
	after, _ := store.Connections().Get(ctx, connID)
	if !storage.EqualHostnames(before.Hostnames, after.Hostnames) {
		t.Errorf("names rewritten within the refresh window: %v -> %v", before.Hostnames, after.Hostnames)
	}
}
