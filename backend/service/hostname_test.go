package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/agent/dnscache"
	"github.com/Herenn/Infralens/backend/storage"
)

const (
	checkoutIP = "10.244.1.7"
	stripeIP   = "52.1.2.3"
	stripeID   = "52.1.2.3:443"
)

func outbound(hostname string) TCPEvent {
	return TCPEvent{PID: 1, Comm: "checkout", SrcAddr: checkoutIP, DstAddr: stripeIP, DstPort: 443,
		Direction: DirectionOutbound, DstHostname: hostname}
}

func newHostnameProcessor(t *testing.T) (*EventProcessor, *TopologyService, storage.Store) {
	t.Helper()
	ts, store := newHistoryTestService(t, true)
	return NewEventProcessor(ts, nil), ts, store
}

func connHostnames(t *testing.T, store storage.Store, id string) []string {
	t.Helper()
	c, err := store.Connections().Get(context.Background(), id)
	if err != nil || c == nil {
		t.Fatalf("connection %s: %v %v", id, c, err)
	}
	return storage.HostnameNames(c.Hostnames)
}

// eqNames compares name lists, order included.
func eqNames(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func serviceByID(t *testing.T, topo *storage.Topology, id string) storage.Service {
	t.Helper()
	for _, s := range topo.Services {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("service %s not in topology", id)
	return storage.Service{}
}

func TestNormalizeHostname(t *testing.T) {
	good := map[string]string{
		"api.stripe.com":  "api.stripe.com",
		"API.Stripe.com.": "api.stripe.com",
		"_srv.example":    "_srv.example",
	}
	for in, want := range good {
		if got, ok := normalizeHostname(in); !ok || got != want {
			t.Errorf("normalizeHostname(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", ".", "a..b", "a b", "<script>.com", "x\",\"y", "a,b.com", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "a"} {
		if got, ok := normalizeHostname(bad); ok {
			t.Errorf("normalizeHostname(%q) accepted as %q", bad, got)
		}
	}
}

// The backend re-validates what agents send with its own copy of the rules;
// the two must agree exactly or names would be dropped (or accepted) on one
// side only.
func FuzzNormalizeHostnameMatchesAgent(f *testing.F) {
	for _, s := range []string{"api.stripe.com.", "A.B", "..", "a_b-c.d", "\xff", "a,b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, aok := normalizeHostname(s)
		b, bok := dnscache.NormalizeName(s)
		if a != b || aok != bok {
			t.Fatalf("backend %q,%v != agent %q,%v for %q", a, aok, b, bok, s)
		}
	})
}

// Old agents omit the field; new agents send it. Both must decode, and an old
// backend (encoding/json, no DisallowUnknownFields) ignores it - covered by
// the handler test suite decoding with the same decoder.
func TestTCPEventWireCompatibility(t *testing.T) {
	var old TCPEvent
	if err := json.Unmarshal([]byte(`{"pid":1,"comm":"x","src_addr":"10.0.0.1","dst_addr":"52.1.2.3","dst_port":443,"direction":0}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.DstHostname != "" {
		t.Errorf("old payload hostname = %q", old.DstHostname)
	}
	var ev TCPEvent
	if err := json.Unmarshal([]byte(`{"pid":1,"comm":"x","src_addr":"10.0.0.1","dst_addr":"52.1.2.3","dst_port":443,"direction":0,"dst_hostname":"api.stripe.com"}`), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.DstHostname != "api.stripe.com" {
		t.Errorf("hostname = %q", ev.DstHostname)
	}
	var st ThroughputStats
	if err := json.Unmarshal([]byte(`{"src_addr":"10.0.0.1","dst_addr":"52.1.2.3","dst_port":443,"dst_hostname":"api.stripe.com"}`), &st); err != nil || st.DstHostname != "api.stripe.com" {
		t.Errorf("throughput hostname = %q, %v", st.DstHostname, err)
	}
	// Omitted when empty, so a new backend's own JSON stays unchanged.
	b, _ := json.Marshal(TCPEvent{})
	if strings.Contains(string(b), "dst_hostname") {
		t.Errorf("empty hostname serialized: %s", b)
	}
}

func TestOutboundEventCarriesHostname(t *testing.T) {
	p, ts, store := newHostnameProcessor(t)
	ctx := context.Background()

	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("API.Stripe.com.")); err != nil {
		t.Fatal(err)
	}
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")
	if got := connHostnames(t, store, connID); !eqNames(got, []string{"api.stripe.com"}) {
		t.Fatalf("connection hostnames = %v", got)
	}

	topo, err := ts.GetTopology(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := serviceByID(t, topo, stripeID).Hostnames; !eqNames(got, []string{"api.stripe.com"}) {
		t.Errorf("service hostnames = %v", got)
	}
	// The source service is not address-keyed by destination; nothing to label.
	if got := serviceByID(t, topo, checkoutIP+"/checkout").Hostnames; len(got) != 0 {
		t.Errorf("source got hostnames %v", got)
	}

	// The single-service read (WebSocket deltas, GET /services/{id}) agrees.
	svc, err := ts.GetService(ctx, stripeID)
	if err != nil || svc == nil || !eqNames(svc.Hostnames, []string{"api.stripe.com"}) {
		t.Errorf("GetService = %+v, %v", svc, err)
	}
	list, err := ts.ListServices(ctx, storage.ServiceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == stripeID && !eqNames(s.Hostnames, []string{"api.stripe.com"}) {
			t.Errorf("ListServices hostnames = %v", s.Hostnames)
		}
	}

	// Service IDs are unchanged by the feature: still keyed by address.
	if svc.ID != stripeID {
		t.Errorf("service ID changed to %q", svc.ID)
	}
}

func TestHostnamesAccumulateAndArePreserved(t *testing.T) {
	p, _, store := newHostnameProcessor(t)
	ctx := context.Background()
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")

	for _, h := range []string{"api.stripe.com", "", "files.stripe.com", "api.stripe.com"} {
		if err := p.ProcessTCPEvent(ctx, "node-1", outbound(h)); err != nil {
			t.Fatal(err)
		}
	}
	// Both kept, once each; the nameless event (old agent / no DNS seen)
	// erased nothing. (Order among names re-observed within
	// storage.HostnameRefresh is by their stored times; see the recency test.)
	got := connHostnames(t, store, connID)
	if len(got) != 2 || !containsString(got, "api.stripe.com") || !containsString(got, "files.stripe.com") {
		t.Errorf("hostnames = %v", got)
	}
}

func TestHostnameRejectedAndInboundIgnored(t *testing.T) {
	p, _, store := newHostnameProcessor(t)
	ctx := context.Background()

	if err := p.ProcessTCPEvent(ctx, "node-1", outbound(`evil"]; click`)); err != nil {
		t.Fatal(err)
	}
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")
	if got := connHostnames(t, store, connID); len(got) != 0 {
		t.Errorf("invalid hostname stored: %v", got)
	}

	// On an accepted connection the destination is the local service; a name
	// there would mislabel it.
	in := TCPEvent{PID: 2, Comm: "nginx", SrcAddr: "198.51.100.9", DstAddr: "10.0.1.10", DstPort: 443,
		Direction: DirectionInbound, DstHostname: "www.example.com"}
	if err := p.ProcessTCPEvent(ctx, "node-1", in); err != nil {
		t.Fatal(err)
	}
	inID := makeConnectionID("198.51.100.9", "10.0.1.10/nginx", 443, "tcp")
	if got := connHostnames(t, store, inID); len(got) != 0 {
		t.Errorf("inbound edge got hostnames %v", got)
	}
}

// The connect event can win the race against the DNS answer being parsed, or
// be dropped. Throughput reports carry the name too and fill it in.
func TestThroughputFillsHostnameAndHistory(t *testing.T) {
	p, ts, store := newHostnameProcessor(t)
	ctx := context.Background()
	connID := makeConnectionID(checkoutIP+"/checkout", stripeID, 443, "tcp")

	if err := p.ProcessTCPEvent(ctx, "node-1", outbound("")); err != nil {
		t.Fatal(err)
	}
	if got := connHostnames(t, store, connID); len(got) != 0 {
		t.Fatalf("unexpected names %v", got)
	}

	sample := ThroughputStats{PID: 1, Comm: "checkout", SrcAddr: checkoutIP, DstAddr: stripeIP,
		SrcPort: 40000, DstPort: 443, BytesSent: 100, BytesRecv: 200, DstHostname: "api.stripe.com"}
	if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{sample}); err != nil {
		t.Fatal(err)
	}
	if got := connHostnames(t, store, connID); !eqNames(got, []string{"api.stripe.com"}) {
		t.Fatalf("after throughput hostnames = %v", got)
	}
	// Stats still applied.
	if c, _ := store.Connections().Get(ctx, connID); c.BytesSent != 100 {
		t.Errorf("bytes_sent = %d", c.BytesSent)
	}
	// The inbound candidate ID for the same sample did not get a row or name.
	inID := makeConnectionID(stripeIP, checkoutIP+"/checkout", 40000, "tcp")
	if c, _ := store.Connections().Get(ctx, inID); c != nil {
		t.Errorf("phantom inbound edge %+v", c)
	}

	// History-at-time reconstruction carries the name.
	topo, err := ts.GetTopologyAt(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range topo.Connections {
		if c.ID == connID {
			found = true
			if !eqNames(storage.HostnameNames(c.Hostnames), []string{"api.stripe.com"}) {
				t.Errorf("historical connection hostnames = %v", c.Hostnames)
			}
		}
	}
	if !found {
		t.Fatal("connection missing from historical topology")
	}
	if got := serviceByID(t, topo, stripeID).Hostnames; !eqNames(got, []string{"api.stripe.com"}) {
		t.Errorf("historical service hostnames = %v", got)
	}

	// Repeated reports with the same name are throttled - no rewrite, still correct.
	for i := 0; i < 5; i++ {
		if err := p.ProcessThroughputBatch(ctx, "node-1", []ThroughputStats{sample}); err != nil {
			t.Fatal(err)
		}
	}
	if got := connHostnames(t, store, connID); !eqNames(got, []string{"api.stripe.com"}) {
		t.Errorf("hostnames drifted: %v", got)
	}
}

func TestAddConnectionHostnamesMissingConnection(t *testing.T) {
	_, ts, _ := newHostnameProcessor(t)
	if err := ts.AddConnectionHostnames(context.Background(), "nope", []string{"a.example"}); err != nil {
		t.Errorf("missing connection must be a no-op, got %v", err)
	}
	if err := ts.AddConnectionHostnames(context.Background(), "nope", nil); err != nil {
		t.Errorf("no names must be a no-op, got %v", err)
	}
}

// One address, several callers, several names: the node is labelled with the
// name observed most recently, ties broken by how many callers used it, and
// every live name stays available; expired names are dropped.
func TestAnnotateHostnamesRanking(t *testing.T) {
	now := time.Now()
	obs := func(name string, ago time.Duration) storage.Hostname {
		return storage.Hostname{Name: name, LastSeen: now.Add(-ago)}
	}
	services := []storage.Service{
		{ID: "104.16.0.1:443"},
		{ID: "Deploy: default/payments"}, // Kubernetes-resolved: never relabelled
		{ID: "10.0.0.9/web"},
	}
	conns := []storage.Connection{
		{SourceID: "a", TargetID: "104.16.0.1:443", Hostnames: []storage.Hostname{obs("cdn.example.com", time.Hour)}},
		{SourceID: "b", TargetID: "104.16.0.1:443", Hostnames: []storage.Hostname{obs("cdn.example.com", time.Hour), obs("img.example.com", 2*time.Hour)}},
		{SourceID: "c", TargetID: "104.16.0.1:443", Hostnames: []storage.Hostname{obs("zz.example.org", time.Hour)}},
		{SourceID: "d", TargetID: "104.16.0.1:443", Hostnames: []storage.Hostname{obs("new.example.com", time.Minute)}},
		{SourceID: "e", TargetID: "104.16.0.1:443", Hostnames: []storage.Hostname{obs("stale.example.com", storage.HostnameMaxAge+time.Hour)}},
		{SourceID: "a", TargetID: "Deploy: default/payments", Hostnames: []storage.Hostname{obs("payments.default.svc.cluster.local", 0)}},
	}
	AnnotateHostnames(services, conns)

	want := []string{"new.example.com", "cdn.example.com", "zz.example.org", "img.example.com"}
	if !eqNames(services[0].Hostnames, want) {
		t.Errorf("ranking = %v, want %v", services[0].Hostnames, want)
	}
	if len(services[1].Hostnames) != 0 {
		t.Errorf("k8s service relabelled: %v", services[1].Hostnames)
	}
	if len(services[2].Hostnames) != 0 {
		t.Errorf("service without incoming names got %v", services[2].Hostnames)
	}
}

func TestDriftReportNamesExternalTarget(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	d := h.detector(DriftConfig{Interval: 5 * time.Minute, Warmup: time.Hour})

	h.advance(time.Minute)
	// First sighting without a name (the connect event beat the DNS answer)...
	conn := &storage.Connection{ID: "checkout->" + stripeID, SourceID: "checkout", TargetID: stripeID, Port: 443, Protocol: "tcp"}
	if err := h.store.History().RecordConnection(context.Background(), conn, h.clock, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	// ...then the name, a second later, before the drift pass runs.
	conn.Hostnames = storage.ObservedHostnames(h.clock.Add(time.Second), "api.stripe.com")
	if err := h.store.History().RecordConnection(context.Background(), conn, h.clock.Add(time.Second), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	h.advance(4 * time.Minute)

	r := h.check(d)
	assertEdges(t, "appeared", r.Appeared, conn.ID)
	if r.Appeared[0].TargetHostname != "api.stripe.com" || !r.Appeared[0].External {
		t.Fatalf("edge = %+v", r.Appeared[0])
	}

	text := driftSummaryText(r)
	if !strings.Contains(text, "checkout -> api.stripe.com [52.1.2.3:443] (port 443/tcp) [internet]") {
		t.Errorf("summary text does not name the destination:\n%s", text)
	}
	body, _ := json.Marshal(buildDriftWebhookPayload(r))
	if !strings.Contains(string(body), `"target_hostname":"api.stripe.com"`) {
		t.Errorf("webhook payload lacks target_hostname: %s", body)
	}
}

// Edges without a name render exactly as before.
func TestDriftTextUnchangedWithoutHostname(t *testing.T) {
	r := &DriftReport{Appeared: []DriftEdge{{ConnectionID: "x", SourceID: "a", TargetID: "52.1.2.3:443", Port: 443, External: true}}}
	if text := driftSummaryText(r); !strings.Contains(text, "  - a -> 52.1.2.3:443 (port 443/tcp) [internet]") {
		t.Errorf("text changed for nameless edges:\n%s", text)
	}
	body, _ := json.Marshal(buildDriftWebhookPayload(r))
	if strings.Contains(string(body), "target_hostname") {
		t.Errorf("empty target_hostname serialized: %s", body)
	}
}
