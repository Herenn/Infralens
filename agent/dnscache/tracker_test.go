package dnscache

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A node as the tracker sees it: Calico-style /32 routes for its pods, a
// bridge subnet, an overlay route to another node, and the uplink.
const (
	hostIP     = "192.168.1.20" // the node's primary address
	bridgeIP   = "10.244.3.1"   // cni0's address
	coreDNS    = "10.96.0.10"   // kube-dns ClusterIP
	coreDNSPod = "10.244.1.50"  // a CoreDNS replica on this node
	podIP      = "10.244.1.7"
	otherPod   = "10.244.1.8"
	bridgePod  = "10.244.3.9" // behind cni0
	remotePod  = "10.244.7.9" // on another node, via the overlay
	podIP6     = "fd00:10:244:1::7"
	stripeIP   = "52.1.2.3"
	stripeIP6  = "2600:1f18::1"
	attacker   = "203.0.113.66"

	ifLo, ifEth0, ifCoreDNS, ifPod, ifOtherPod, ifCni0, ifVethBr, ifFlannel = 1, 2, 5, 7, 8, 20, 21, 30
)

func testRoutes() *RouteTable {
	p := netip.MustParsePrefix
	return NewRouteTable([]Route{
		{Prefix: p("0.0.0.0/0"), Iface: "eth0", Gateway: true},
		{Prefix: p("192.168.1.0/24"), Iface: "eth0"},
		{Prefix: p(podIP + "/32"), Iface: "cali7"},
		{Prefix: p(otherPod + "/32"), Iface: "cali8"},
		{Prefix: p(coreDNSPod + "/32"), Iface: "cali5"},
		{Prefix: p("10.244.3.0/24"), Iface: "cni0"},
		{Prefix: p("10.244.7.0/24"), Iface: "flannel.1", Gateway: true},
		{Prefix: p(podIP6 + "/128"), Iface: "cali7"},
		{Prefix: p("::/0"), Iface: "eth0", Gateway: true},
	}, map[string]int{"lo": ifLo, "eth0": ifEth0, "cali5": ifCoreDNS, "cali7": ifPod, "cali8": ifOtherPod, "cni0": ifCni0, "veth-br": ifVethBr, "flannel.1": ifFlannel})
}

func newTestTracker(t testing.TB, now *time.Time) *Tracker {
	t.Helper()
	return NewTracker(Config{
		MaxEntries: 1024,
		Now:        func() time.Time { return *now },
		LocalAddrs: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(hostIP), netip.MustParseAddr(bridgeIP), netip.MustParseAddr("::1")}, nil
		},
		Routes: func() (*RouteTable, error) { return testRoutes(), nil },
	})
}

// Legs: where the client-side copy of each direction is seen.
var (
	podQueryLeg  = PacketMeta{Ifindex: ifPod, PktType: PacketHost}
	podRespLeg   = PacketMeta{Ifindex: ifPod, PktType: PacketOutgoing}
	hostQueryLeg = PacketMeta{Ifindex: ifEth0, PktType: PacketOutgoing}
	hostRespLeg  = PacketMeta{Ifindex: ifEth0, PktType: PacketHost}
	loQueryLeg   = PacketMeta{Ifindex: ifLo, PktType: PacketOutgoing}
	loRespLeg    = PacketMeta{Ifindex: ifLo, PktType: PacketHost}
)

func stripeAnswer(t testing.TB) []byte {
	return stripeAnswerID(t, testID)
}

func stripeAnswerID(t testing.TB, id uint16) []byte {
	return buildResponseID(t, id, "api.stripe.com.", dnsmessage.TypeA,
		rr{"api.stripe.com.", dnsmessage.TypeCNAME, 60, "api.stripe.com.cdn.example.net."},
		rr{"api.stripe.com.cdn.example.net.", dnsmessage.TypeA, 60, stripeIP})
}

func stripeQuery(t testing.TB) []byte {
	return buildQuery(t, testID, "api.stripe.com.", dnsmessage.TypeA)
}

// resolve plays a real lookup: the query on the client's leg, then the answer.
func resolve(tr *Tracker, qLeg, rLeg PacketMeta, client string, port uint16, server string, query, answer []byte) {
	tr.HandlePacket(qLeg, udpPacket(client, server, port, 53, query))
	tr.HandlePacket(rLeg, udpPacket(server, client, 53, port, answer))
}

func udpPacket(src, dst string, sport, dport uint16, payload []byte) []byte {
	if netip.MustParseAddr(src).Is4() {
		return ipv4UDP(src, dst, sport, dport, payload)
	}
	return ipv6UDP(src, dst, sport, dport, payload)
}

func TestTrackerPodResolution(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, stripeQuery(t), stripeAnswer(t))

	if got := tr.Hostname(podIP, stripeIP); got != "api.stripe.com" {
		t.Errorf("Hostname(pod) = %q", got)
	}
	// Another pod on the node never asked: it must not inherit the name.
	if got := tr.Hostname(otherPod, stripeIP); got != "" {
		t.Errorf("Hostname(other pod) = %q, want none", got)
	}
	if got := tr.Hostname(hostIP, stripeIP); got != "" {
		t.Errorf("Hostname(host) = %q, want none", got)
	}
	if s := tr.Stats(); s.Packets != 2 || s.Queries != 1 || s.Learned != 1 || s.Entries != 1 || s.Pending != 0 {
		t.Errorf("stats = %+v", s)
	}
}

// Ported from the review PoC: a response nobody asked for - from any source,
// to any port, with any ID - must teach nothing, and must not relabel an
// address the pod really resolved.
func TestTrackerRejectsUnsolicitedResponses(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, stripeQuery(t), stripeAnswer(t))

	forged := buildResponseID(t, 0xdead, "evil-exfil.example.", dnsmessage.TypeA, rr{"evil-exfil.example.", dnsmessage.TypeA, 0, stripeIP})
	tr.HandlePacket(podRespLeg, ipv4UDP(attacker, podIP, 53, 9, forged))
	tr.HandlePacket(podRespLeg, ipv4UDP(coreDNS, podIP, 53, 40000, forged)) // even "from" the resolver
	if got := tr.Hostname(podIP, stripeIP); got != "api.stripe.com" {
		t.Errorf("forged answer relabelled the pod's destination: %q", got)
	}

	// To the node address: would label the attacker's IP for every host process.
	tr.HandlePacket(hostRespLeg, ipv4UDP("198.51.100.1", hostIP, 53, 1,
		buildResponseID(t, 1, "api.stripe.com.", dnsmessage.TypeA, rr{"api.stripe.com.", dnsmessage.TypeA, 0, "6.6.6.6"})))
	for _, src := range []string{hostIP, "127.0.0.1"} {
		if got := tr.Hostname(src, "6.6.6.6"); got != "" {
			t.Errorf("unsolicited answer to the node learned: %s -> %q", src, got)
		}
	}
	if s := tr.Stats(); s.Unmatched != 3 || s.Learned != 1 {
		t.Errorf("stats = %+v", s)
	}
}

// Everything a response must echo back is checked.
func TestTrackerMatchRequiresEveryField(t *testing.T) {
	q := func(t *testing.T) []byte { return stripeQuery(t) }
	cases := map[string]struct {
		server string
		port   uint16
		answer func(t *testing.T) []byte
	}{
		"wrong id":     {coreDNS, 40000, func(t *testing.T) []byte { return stripeAnswerID(t, testID+1) }},
		"wrong port":   {coreDNS, 40001, stripeAnswerT},
		"wrong server": {"10.96.0.11", 40000, stripeAnswerT},
		"wrong name": {coreDNS, 40000, func(t *testing.T) []byte {
			return buildResponse(t, "evil.example.", dnsmessage.TypeA, rr{"evil.example.", dnsmessage.TypeA, 60, stripeIP})
		}},
		"wrong type": {coreDNS, 40000, func(t *testing.T) []byte {
			return buildResponse(t, "api.stripe.com.", dnsmessage.TypeAAAA, rr{"api.stripe.com.", dnsmessage.TypeAAAA, 60, stripeIP6})
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			now := t0
			tr := newTestTracker(t, &now)
			tr.HandlePacket(podQueryLeg, ipv4UDP(podIP, coreDNS, 40000, 53, q(t)))
			tr.HandlePacket(podRespLeg, ipv4UDP(c.server, podIP, 53, c.port, c.answer(t)))
			if s := tr.Stats(); s.Learned != 0 || s.Entries != 0 {
				t.Fatalf("learned from a mismatched response: %+v", s)
			}
			// The mismatch did not consume the query: the real answer still counts.
			tr.HandlePacket(podRespLeg, ipv4UDP(coreDNS, podIP, 53, 40000, stripeAnswer(t)))
			if got := tr.Hostname(podIP, stripeIP); got != "api.stripe.com" {
				t.Errorf("real answer after a mismatch = %q", got)
			}
		})
	}
}

func stripeAnswerT(t *testing.T) []byte { return stripeAnswer(t) }

func TestTrackerQueryIsSingleUseAndExpires(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, stripeQuery(t), stripeAnswer(t))
	// A second answer to the same query (replay, or a forger racing the real
	// one and losing) is not believed.
	tr.HandlePacket(podRespLeg, ipv4UDP(coreDNS, podIP, 53, 40000,
		buildResponse(t, "api.stripe.com.", dnsmessage.TypeA, rr{"api.stripe.com.", dnsmessage.TypeA, 60, "6.6.6.6"})))
	if got := tr.Hostname(podIP, "6.6.6.6"); got != "" {
		t.Errorf("second answer to one query learned: %q", got)
	}

	// An answer arriving after QueryWindow is not believed.
	tr.HandlePacket(podQueryLeg, ipv4UDP(podIP, coreDNS, 40002, 53, buildQuery(t, 7, "late.example.", dnsmessage.TypeA)))
	now = now.Add(QueryWindow)
	tr.HandlePacket(podRespLeg, ipv4UDP(coreDNS, podIP, 53, 40002,
		buildResponseID(t, 7, "late.example.", dnsmessage.TypeA, rr{"late.example.", dnsmessage.TypeA, 60, "198.51.100.7"})))
	if got := tr.Hostname(podIP, "198.51.100.7"); got != "" {
		t.Errorf("late answer learned: %q", got)
	}
	if tr.Stats().Pending != 0 {
		t.Errorf("expired query still pending")
	}
}

// A query is only recorded from the client's own leg. A packet claiming a
// pod's address but arriving on another interface (another pod's veth, the
// uplink) is spoofed, and recording it would let the spoofer then answer it.
func TestTrackerIgnoresSpoofedQueries(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	evil := buildResponse(t, "evil-exfil.example.", dnsmessage.TypeA, rr{"evil-exfil.example.", dnsmessage.TypeA, 60, stripeIP})
	evilQ := buildQuery(t, testID, "evil-exfil.example.", dnsmessage.TypeA)
	for _, leg := range []PacketMeta{
		{Ifindex: ifOtherPod, PktType: PacketHost}, // from another pod's interface
		{Ifindex: ifEth0, PktType: PacketHost},     // from off the node
		{Ifindex: ifPod, PktType: PacketOutgoing},  // transmitted *to* the pod
	} {
		resolve(tr, leg, podRespLeg, podIP, 40000, coreDNS, evilQ, evil)
	}
	// A spoofed host-address query arriving from outside is not a host query.
	resolve(tr, PacketMeta{Ifindex: ifEth0, PktType: PacketHost}, hostRespLeg, hostIP, 40000, "198.51.100.53", evilQ, evil)
	if s := tr.Stats(); s.Queries != 0 || s.Entries != 0 {
		t.Errorf("spoofed queries recorded: %+v", s)
	}
}

// Ported from the review PoC: a CoreDNS replica on this node answering a pod
// on another node must not fill the cache with entries for a client this
// agent will never see connect.
func TestTrackerIgnoresRemoteClients(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	// As seen on the CoreDNS replica's interface and the overlay.
	resolve(tr, PacketMeta{Ifindex: ifFlannel, PktType: PacketHost}, PacketMeta{Ifindex: ifFlannel, PktType: PacketOutgoing},
		remotePod, 40000, coreDNSPod, stripeQuery(t), stripeAnswer(t))
	resolve(tr, PacketMeta{Ifindex: ifCoreDNS, PktType: PacketOutgoing}, PacketMeta{Ifindex: ifCoreDNS, PktType: PacketHost},
		remotePod, 40000, coreDNSPod, stripeQuery(t), stripeAnswer(t))
	if s := tr.Stats(); s.Entries != 0 || s.Queries != 0 || s.NotLocal != 4 {
		t.Errorf("remote client cached: %+v", s)
	}
}

// A forwarded lookup is seen on several legs, before and after NAT. Exactly
// the client-side copies are used: once, with consistent pre-NAT addresses.
func TestTrackerUsesOnlyClientLegs(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	q, a := stripeQuery(t), stripeAnswer(t)
	// Query: received from the pod (to the ClusterIP), then after DNAT
	// transmitted to the local CoreDNS replica.
	tr.HandlePacket(podQueryLeg, ipv4UDP(podIP, coreDNS, 40000, 53, q))
	tr.HandlePacket(PacketMeta{Ifindex: ifCoreDNS, PktType: PacketOutgoing}, ipv4UDP(podIP, coreDNSPod, 40000, 53, q))
	// Answer: received from the replica (pre-unNAT), then transmitted to the pod.
	tr.HandlePacket(PacketMeta{Ifindex: ifCoreDNS, PktType: PacketHost}, ipv4UDP(coreDNSPod, podIP, 53, 40000, a))
	tr.HandlePacket(podRespLeg, ipv4UDP(coreDNS, podIP, 53, 40000, a))

	s := tr.Stats()
	if s.Queries != 1 || s.Learned != 1 || s.OtherLeg != 2 || s.Unmatched != 0 {
		t.Errorf("stats = %+v", s)
	}
	if tr.Hostname(podIP, stripeIP) != "api.stripe.com" {
		t.Error("name not learned from the client leg")
	}
}

// Bridge CNIs: the pod's packets are seen on its veth and on the bridge; the
// bridge (the routed interface) is the client leg.
func TestTrackerBridgeLegs(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	q, a := stripeQuery(t), stripeAnswer(t)
	tr.HandlePacket(PacketMeta{Ifindex: ifVethBr, PktType: PacketOtherHost}, ipv4UDP(bridgePod, coreDNS, 40000, 53, q))
	tr.HandlePacket(PacketMeta{Ifindex: ifCni0, PktType: PacketHost}, ipv4UDP(bridgePod, coreDNS, 40000, 53, q))
	tr.HandlePacket(PacketMeta{Ifindex: ifCni0, PktType: PacketOutgoing}, ipv4UDP(coreDNS, bridgePod, 53, 40000, a))
	tr.HandlePacket(PacketMeta{Ifindex: ifVethBr, PktType: PacketOutgoing}, ipv4UDP(coreDNS, bridgePod, 53, 40000, a))
	if s := tr.Stats(); s.Queries != 1 || s.Learned != 1 || s.OtherLeg != 2 {
		t.Errorf("stats = %+v", s)
	}
	if tr.Hostname(bridgePod, stripeIP) != "api.stripe.com" {
		t.Error("bridged pod did not learn the name")
	}
}

// NodeLocal DNSCache / a host resolver answering a pod: the answer is
// generated on the host and only ever transmitted to the pod.
func TestTrackerNodeLocalResolver(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, "169.254.20.10", stripeQuery(t), stripeAnswer(t))
	if tr.Hostname(podIP, stripeIP) != "api.stripe.com" {
		t.Error("answer from a node-local resolver not learned")
	}
}

// A host process resolving through a loopback stub resolver (systemd-resolved
// at 127.0.0.53) and connecting from the node's primary address. Loopback
// packets are seen twice (transmit and receive); one of each is used.
func TestTrackerHostIdentityViaLoopback(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	q, a := stripeQuery(t), stripeAnswer(t)
	tr.HandlePacket(loQueryLeg, ipv4UDP("127.0.0.1", "127.0.0.53", 40000, 53, q))
	tr.HandlePacket(loRespLeg, ipv4UDP("127.0.0.1", "127.0.0.53", 40000, 53, q))
	tr.HandlePacket(loQueryLeg, ipv4UDP("127.0.0.53", "127.0.0.1", 53, 40000, a))
	tr.HandlePacket(loRespLeg, ipv4UDP("127.0.0.53", "127.0.0.1", 53, 40000, a))
	if s := tr.Stats(); s.Queries != 1 || s.Learned != 1 || s.OtherLeg != 2 {
		t.Errorf("stats = %+v", s)
	}
	for _, src := range []string{hostIP, bridgeIP, "127.0.0.1", "::ffff:" + hostIP} {
		if got := tr.Hostname(src, stripeIP); got != "api.stripe.com" {
			t.Errorf("Hostname(%s) = %q", src, got)
		}
	}
	if got := tr.Hostname(podIP, stripeIP); got != "" {
		t.Errorf("pods must not see host resolutions, got %q", got)
	}
}

func TestTrackerHostViaRemoteResolver(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	resolve(tr, hostQueryLeg, hostRespLeg, hostIP, 40000, "192.168.1.2", stripeQuery(t), stripeAnswer(t))
	if tr.Hostname(hostIP, stripeIP) != "api.stripe.com" {
		t.Error("host process name not learned")
	}
}

func TestTrackerIPv6AndMapped(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	q := buildQuery(t, testID, "api.stripe.com.", dnsmessage.TypeAAAA)
	a := buildResponse(t, "api.stripe.com.", dnsmessage.TypeAAAA, rr{"api.stripe.com.", dnsmessage.TypeAAAA, 60, stripeIP6})
	resolve(tr, podQueryLeg, podRespLeg, podIP6, 40000, "fd00:10:96::a", q, a)
	if got := tr.Hostname(podIP6, stripeIP6); got != "api.stripe.com" {
		t.Errorf("Hostname(v6) = %q", got)
	}

	// A dual-stack socket reports IPv4 peers as ::ffff:a.b.c.d.
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, stripeQuery(t), stripeAnswer(t))
	if got := tr.Hostname("::ffff:"+podIP, "::ffff:"+stripeIP); got != "api.stripe.com" {
		t.Errorf("Hostname(mapped) = %q", got)
	}
}

// Known limitation, kept visible as a test (from the review PoC): a
// dual-stack pod that resolves AAAA over IPv4 and connects from its IPv6
// address gets no name, because the answer is filed under its IPv4 address
// and nothing tells the agent the two addresses are one pod.
func TestTrackerDualStackLimitation(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	q := buildQuery(t, testID, "api.stripe.com.", dnsmessage.TypeAAAA)
	a := buildResponse(t, "api.stripe.com.", dnsmessage.TypeAAAA, rr{"api.stripe.com.", dnsmessage.TypeAAAA, 60, stripeIP6})
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, q, a)
	if got := tr.Hostname(podIP, stripeIP6); got != "api.stripe.com" {
		t.Errorf("filed under the v4 address: %q", got)
	}
	if got := tr.Hostname(podIP6, stripeIP6); got != "" {
		t.Errorf("limitation changed (update the docs): v6 source now gets %q", got)
	}
}

func TestTrackerIgnoresJunk(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	sinkholeQ := buildQuery(t, testID, "ads.example.", dnsmessage.TypeA)
	sinkhole := buildResponse(t, "ads.example.", dnsmessage.TypeA, rr{"ads.example.", dnsmessage.TypeA, 60, "127.0.0.1"})
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, sinkholeQ, sinkhole)

	for _, in := range [][]byte{
		nil,
		{0x45},
		ipv4UDP(coreDNS, podIP, 5353, 40000, stripeAnswer(t)),
		ipv4UDP(podIP, coreDNS, 40000, 53, stripeAnswer(t)), // response sent to 53
		ipv4UDP(coreDNS, podIP, 53, 40000, stripeQuery(t)),  // query sent from 53
		ipv4UDP(coreDNS, podIP, 53, 40000, []byte("not dns, but twelve bytes+")),
		ipv4UDP(coreDNS, podIP, 53, 40000, make([]byte, maxDNSMessage+1)),
	} {
		tr.HandlePacket(podRespLeg, in)
	}
	tr.HandlePacket(podRespLeg, ipv4UDP(coreDNS, "0.0.0.0", 53, 40000, stripeAnswer(t)))
	tr.HandlePacket(podRespLeg, ipv4UDP(coreDNS, "224.0.0.251", 53, 40000, stripeAnswer(t)))
	if s := tr.Stats(); s.Learned != 0 || s.Entries != 0 || s.Malformed != 7 || s.NotLocal != 2 {
		t.Errorf("stats = %+v", s)
	}
	if got := tr.Hostname(podIP, "127.0.0.1"); got != "" {
		t.Errorf("sinkholed name leaked: %q", got)
	}
}

func TestTrackerPendingLimits(t *testing.T) {
	now := t0
	tr := newTestTracker(t, &now)
	for i := 0; i < maxPendingPerClient+10; i++ {
		tr.HandlePacket(podQueryLeg, ipv4UDP(podIP, coreDNS, uint16(10000+i), 53, stripeQuery(t)))
	}
	// One busy client does not use up another's room.
	tr.HandlePacket(PacketMeta{Ifindex: ifOtherPod, PktType: PacketHost}, ipv4UDP(otherPod, coreDNS, 40000, 53, stripeQuery(t)))
	s := tr.Stats()
	if s.Queries != maxPendingPerClient+1 || s.QueriesFull != 10 || s.Pending != maxPendingPerClient+1 {
		t.Errorf("stats = %+v", s)
	}
	// Retransmission of the same query does not take a second slot.
	tr.HandlePacket(PacketMeta{Ifindex: ifOtherPod, PktType: PacketHost}, ipv4UDP(otherPod, coreDNS, 40000, 53, stripeQuery(t)))
	if tr.Stats().Pending != maxPendingPerClient+1 {
		t.Error("retransmission took a second slot")
	}
}

// Ported from the review PoC (flood eviction): a pod that resolves huge
// numbers of names - the only way left to insert entries - cannot evict
// another pod's names.
func TestTrackerOneClientCannotEvictAnother(t *testing.T) {
	now := t0
	tr := NewTracker(Config{
		Now:        func() time.Time { return now },
		LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Routes:     func() (*RouteTable, error) { return testRoutes(), nil },
	})
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, stripeQuery(t), stripeAnswer(t))
	other := PacketMeta{Ifindex: ifOtherPod, PktType: PacketHost}
	otherResp := PacketMeta{Ifindex: ifOtherPod, PktType: PacketOutgoing}
	for i := 0; i < 600; i++ {
		id := uint16(i)
		answers := make([]rr, 64)
		for j := range answers {
			answers[j] = rr{"x.example.", dnsmessage.TypeA, 3600, addrN(i*64 + j).String()}
		}
		resolve(tr, other, otherResp, otherPod, 50000, coreDNS,
			buildQuery(t, id, "x.example.", dnsmessage.TypeA), buildResponseID(t, id, "x.example.", dnsmessage.TypeA, answers...))
	}
	if got := tr.Hostname(podIP, stripeIP); got != "api.stripe.com" {
		t.Errorf("victim entry evicted by another client's lookups (entries=%d)", tr.Stats().Entries)
	}
}

func TestTrackerNewPodRouteRefreshedOnMiss(t *testing.T) {
	now := t0
	var loads atomic.Int32
	var withNewPod atomic.Bool
	tr := NewTracker(Config{
		Now:        func() time.Time { return now },
		LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Routes: func() (*RouteTable, error) {
			loads.Add(1)
			rt := testRoutes()
			if withNewPod.Load() {
				rt.routes = append(rt.routes, Route{Prefix: netip.MustParsePrefix("10.244.1.99/32"), Iface: "cali99"})
				rt.ifindex["cali99"] = 99
			}
			return rt, nil
		},
	})
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, stripeQuery(t), stripeAnswer(t)) // first load

	withNewPod.Store(true) // the pod is created...
	leg := PacketMeta{Ifindex: 99, PktType: PacketHost}
	tr.HandlePacket(leg, ipv4UDP("10.244.1.99", coreDNS, 40000, 53, stripeQuery(t)))
	if tr.Stats().Queries != 1 {
		t.Fatal("refresh on miss must be rate-limited (no time has passed)")
	}
	now = now.Add(routeMissRefresh) // ...and resolves a second later
	tr.HandlePacket(leg, ipv4UDP("10.244.1.99", coreDNS, 40000, 53, stripeQuery(t)))
	tr.HandlePacket(PacketMeta{Ifindex: 99, PktType: PacketOutgoing}, ipv4UDP(coreDNS, "10.244.1.99", 53, 40000, stripeAnswer(t)))
	if tr.Hostname("10.244.1.99", stripeIP) != "api.stripe.com" {
		t.Errorf("new pod's route not picked up on miss (loads=%d)", loads.Load())
	}
}

func TestTrackerNilAndBadInput(t *testing.T) {
	var tr *Tracker
	if tr.Hostname(podIP, stripeIP) != "" {
		t.Error("nil tracker must know no names")
	}
	if tr.Stats() != (Stats{}) {
		t.Error("nil tracker stats must be zero")
	}
	if err := tr.Start(context.Background(), nil); err == nil {
		t.Error("Start on nil tracker must fail")
	}
	now := t0
	tr = newTestTracker(t, &now)
	if tr.Hostname("not-an-ip", stripeIP) != "" || tr.Hostname(podIP, "") != "" {
		t.Error("unparseable addresses must miss")
	}
}

func TestTrackerLocalAddrsRefresh(t *testing.T) {
	now := t0
	var calls atomic.Int32
	var mu sync.Mutex
	addrs := []netip.Addr{netip.MustParseAddr(hostIP)}
	tr := NewTracker(Config{
		Now:    func() time.Time { return now },
		Routes: func() (*RouteTable, error) { return testRoutes(), nil },
		LocalAddrs: func() ([]netip.Addr, error) {
			if calls.Add(1) == 2 {
				return nil, errors.New("netlink hiccup")
			}
			mu.Lock()
			defer mu.Unlock()
			return append([]netip.Addr(nil), addrs...), nil
		},
	})
	resolve(tr, loQueryLeg, loRespLeg, "127.0.0.1", 40000, "127.0.0.53", stripeQuery(t), stripeAnswer(t))
	if tr.Hostname(hostIP, stripeIP) == "" {
		t.Fatal("host identity not applied")
	}
	now = now.Add(localRefresh) // this refresh fails
	if tr.Hostname(hostIP, stripeIP) == "" {
		t.Error("a failed refresh must not forget the host's addresses")
	}
	mu.Lock()
	addrs = append(addrs, netip.MustParseAddr("192.168.1.99"))
	mu.Unlock()
	now = now.Add(localRefresh)
	if tr.Hostname("192.168.1.99", stripeIP) == "" {
		t.Error("new local address not picked up")
	}
}

// fakeSource replays scripted reads.
type fakeSource struct {
	mu     sync.Mutex
	reads  []func([]byte) (int, PacketMeta, error)
	closed bool
	done   chan struct{}
	drops  uint64
}

func newFakeSource(reads ...func([]byte) (int, PacketMeta, error)) *fakeSource {
	return &fakeSource{reads: reads, done: make(chan struct{})}
}

func (f *fakeSource) ReadPacket(buf []byte) (int, PacketMeta, error) {
	f.mu.Lock()
	if len(f.reads) == 0 {
		f.mu.Unlock()
		time.Sleep(time.Millisecond)
		return 0, PacketMeta{}, errReadTimeout
	}
	r := f.reads[0]
	f.reads = f.reads[1:]
	f.mu.Unlock()
	return r(buf)
}

func (f *fakeSource) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.done)
	}
	return nil
}

func (f *fakeSource) KernelDrops() (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.drops
	f.drops = 0
	return d, nil
}

func (f *fakeSource) RecvBuffer() int { return 2 << 20 }

func packetRead(meta PacketMeta, p []byte) func([]byte) (int, PacketMeta, error) {
	return func(buf []byte) (int, PacketMeta, error) { return copy(buf, p), meta, nil }
}

func failRead(err error) func([]byte) (int, PacketMeta, error) {
	return func([]byte) (int, PacketMeta, error) { return 0, PacketMeta{}, err }
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTrackerRunLoop(t *testing.T) {
	now := t0
	var mu sync.Mutex
	tr := NewTracker(Config{
		Now:        func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Routes:     func() (*RouteTable, error) { return testRoutes(), nil },
	})
	src := newFakeSource(
		failRead(errReadTimeout),
		failRead(errors.New("transient")),
		packetRead(podQueryLeg, ipv4UDP(podIP, coreDNS, 40000, 53, stripeQuery(t))),
		packetRead(podRespLeg, ipv4UDP(coreDNS, podIP, 53, 40000, stripeAnswer(t))),
	)
	src.drops = 3
	ctx, cancel := context.WithCancel(context.Background())
	go tr.run(ctx, src, func(f string, a ...any) { t.Errorf("unexpected log: "+f, a...) })

	waitFor(t, "packet processed", func() bool { return tr.Hostname(podIP, stripeIP) != "" })
	if s := tr.Stats(); s.KernelDrops != 3 || s.RecvBuffer != 2<<20 {
		t.Errorf("kernel stats not read: %+v", s)
	}
	cancel()
	select {
	case <-src.done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop and close the source on cancel")
	}
}

// The packet budget: past MaxPPS the loop stops reading (so the kernel, not
// the agent, absorbs a flood) and counts it.
func TestTrackerRunLoopBudget(t *testing.T) {
	var mu sync.Mutex
	now := t0
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var slept, sleptAt50 atomic.Int64
	reads := make([]func([]byte) (int, PacketMeta, error), 50)
	for i := range reads {
		reads[i] = packetRead(podRespLeg, ipv4UDP(attacker, podIP, 53, 9, stripeAnswer(t)))
	}
	last := reads[49]
	reads[49] = func(b []byte) (int, PacketMeta, error) {
		sleptAt50.Store(slept.Load()) // measure exactly at the 50th packet
		return last(b)
	}
	src := newFakeSource(reads...)
	tr := NewTracker(Config{
		MaxPPS:     10,
		Now:        clock,
		LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Routes:     func() (*RouteTable, error) { return testRoutes(), nil },
		sleep: func(d time.Duration) {
			slept.Add(int64(d))
			mu.Lock()
			now = now.Add(d) // simulated time passes only while waiting
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.run(ctx, src, func(string, ...any) {})
	waitFor(t, "all packets read", func() bool { return tr.Stats().Packets == 50 })
	s := tr.Stats()
	if s.BudgetWaits == 0 {
		t.Fatalf("no budget waits at 50 packets with a 10 pps budget: %+v", s)
	}
	// 50 packets at 10/s with a burst of 10 needs ~4s of waiting.
	if got := time.Duration(sleptAt50.Load()); got < 3500*time.Millisecond || got > 4500*time.Millisecond {
		t.Errorf("waited %s for 50 packets at 10 pps", got)
	}
}

func TestTrackerRunLoopReopensWithBackoff(t *testing.T) {
	var mu sync.Mutex
	now := t0
	var slept []time.Duration
	var reads []func([]byte) (int, PacketMeta, error)
	for i := 0; i < maxConsecutiveReadErrors; i++ {
		reads = append(reads, failRead(errors.New("socket gone")))
	}
	first := newFakeSource(reads...)
	second := newFakeSource(
		packetRead(podQueryLeg, ipv4UDP(podIP, coreDNS, 40000, 53, stripeQuery(t))),
		packetRead(podRespLeg, ipv4UDP(coreDNS, podIP, 53, 40000, stripeAnswer(t))),
	)
	var opens atomic.Int32
	tr := NewTracker(Config{
		Now:        func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Routes:     func() (*RouteTable, error) { return testRoutes(), nil },
		open: func() (packetSource, error) {
			if opens.Add(1) < 3 {
				return nil, errors.New("still broken")
			}
			return second, nil
		},
		sleep: func(d time.Duration) {
			mu.Lock()
			slept = append(slept, d)
			now = now.Add(d)
			mu.Unlock()
		},
	})
	var logs atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.run(ctx, first, func(string, ...any) { logs.Add(1) })

	waitFor(t, "name learned after reopen", func() bool { return tr.Hostname(podIP, stripeIP) != "" })
	if !first.closed {
		t.Error("failed source not closed")
	}
	s := tr.Stats()
	if s.Reopens != 1 || !s.CaptureActive {
		t.Errorf("stats = %+v", s)
	}
	// One log for giving up on the socket, one per failed reopen, one on success.
	if logs.Load() != 4 {
		t.Errorf("logged %d times", logs.Load())
	}
	mu.Lock()
	total := time.Duration(0)
	for _, d := range slept {
		total += d
	}
	mu.Unlock()
	// Backoff 1s, 2s, 4s before the successful reopen.
	if total != 7*time.Second {
		t.Errorf("total backoff %s, want 7s", total)
	}
}

func TestTrackerStartUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("capture is supported on Linux; see the integration test")
	}
	now := t0
	tr := newTestTracker(t, &now)
	if err := tr.Start(context.Background(), nil); !errors.Is(err, ErrCaptureUnsupported) {
		t.Errorf("Start = %v, want ErrCaptureUnsupported", err)
	}
	if tr.Hostname(podIP, stripeIP) != "" || tr.Stats().CaptureActive {
		t.Error("fail-open tracker must stay usable and inactive")
	}
}

func TestStatsString(t *testing.T) {
	s := Stats{Packets: 1, Learned: 2, KernelDrops: 3}.String()
	for _, want := range []string{"packets=1", "learned=2", "kernel_drops=3"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}
}

func TestOutboundFlows(t *testing.T) {
	f := NewOutboundFlows(2)
	f.Add(podIP, stripeIP, 443, false, t0)
	if !f.Contains(podIP, stripeIP, 443, false, t0.Add(time.Minute)) {
		t.Error("known outbound flow missed")
	}
	if f.Contains(stripeIP, podIP, 40000, false, t0) || f.Contains(podIP, stripeIP, 443, true, t0) {
		t.Error("other direction / protocol matched")
	}
	if !f.Contains("::ffff:"+podIP, stripeIP, 443, false, t0) {
		t.Error("mapped addresses not normalized")
	}
	// Bounded: a full set of live flows ignores new ones; idle ones expire.
	f.Add(podIP, "198.51.100.1", 443, false, t0)
	f.Add(podIP, "198.51.100.2", 443, false, t0)
	if f.Len() != 2 {
		t.Errorf("len = %d", f.Len())
	}
	later := t0.Add(flowIdle + time.Minute)
	if f.Contains(podIP, stripeIP, 443, false, later) {
		t.Error("idle flow not expired")
	}
	f.Add(podIP, "198.51.100.3", 443, false, later)
	if !f.Contains(podIP, "198.51.100.3", 443, false, later) {
		t.Error("new flow not added after expiry made room")
	}
	var nilSet *OutboundFlows
	nilSet.Add(podIP, stripeIP, 443, false, t0)
	if nilSet.Contains(podIP, stripeIP, 443, false, t0) {
		t.Error("nil set must contain nothing")
	}
}
