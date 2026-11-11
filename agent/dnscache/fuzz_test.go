package dnscache

import (
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/bpf"
	"golang.org/x/net/dns/dnsmessage"
)

func seedMessages(f *testing.F) [][]byte {
	f.Helper()
	return [][]byte{
		stripeAnswer(f),
		stripeQuery(f),
		buildResponse(f, "v6.example.", dnsmessage.TypeAAAA, rr{"v6.example.", dnsmessage.TypeAAAA, 60, "2001:db8::1"}),
		buildResponse(f, "a.example.", dnsmessage.TypeA,
			rr{"a.example.", dnsmessage.TypeCNAME, 60, "b.example."},
			rr{"b.example.", dnsmessage.TypeCNAME, 60, "a.example."}),
		append(append(rawHeader(1), 1, 'a', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 0, 1, 0, 1), 0xc0, 27, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4),
		hostileResponse(f, testID, 40),
		{},
	}
}

// FuzzParseResponse: never panics, and anything it returns satisfies the
// invariants downstream code relies on.
func FuzzParseResponse(f *testing.F) {
	for _, m := range seedMessages(f) {
		f.Add(m)
	}
	f.Fuzz(func(t *testing.T, msg []byte) {
		resp, err := ParseResponse(msg)
		if err != nil && len(resp.Answers) != 0 {
			t.Fatalf("answers returned with error %v", err)
		}
		if err != nil {
			return
		}
		if n, ok := NormalizeName(resp.Name); !ok || n != resp.Name {
			t.Fatalf("returned non-normalized name %q", resp.Name)
		}
		if len(resp.Answers) == 0 || len(resp.Answers) > maxAddrsPerAnswer {
			t.Fatalf("answer count %d out of range", len(resp.Answers))
		}
		seen := map[netip.Addr]bool{}
		for _, a := range resp.Answers {
			if !a.Addr.IsValid() || a.Addr.IsUnspecified() || a.Addr.Is4In6() || seen[a.Addr] {
				t.Fatalf("bad answer address %v", a.Addr)
			}
			seen[a.Addr] = true
		}
	})
}

func FuzzParseQuery(f *testing.F) {
	for _, m := range seedMessages(f) {
		f.Add(m)
	}
	f.Fuzz(func(t *testing.T, msg []byte) {
		_, name, qtype, err := ParseQuery(msg)
		if err != nil {
			return
		}
		if n, ok := NormalizeName(name); !ok || n != name {
			t.Fatalf("non-normalized query name %q", name)
		}
		if qtype != dnsmessage.TypeA && qtype != dnsmessage.TypeAAAA {
			t.Fatalf("query type %v accepted", qtype)
		}
	})
}

func FuzzParseUDPPacket(f *testing.F) {
	for _, m := range seedMessages(f) {
		f.Add(ipv4UDP(coreDNS, podIP, 53, 40000, m))
		f.Add(ipv6UDP("fd00::a", "fd00::7", 53, 40000, m))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		pkt, err := ParseUDPPacket(b)
		if err != nil {
			return
		}
		if !pkt.Src.IsValid() || !pkt.Dst.IsValid() {
			t.Fatal("invalid addresses on success")
		}
		if len(pkt.Payload) > len(b) {
			t.Fatal("payload larger than input")
		}
	})
}

func fuzzTracker() *Tracker {
	now := t0
	return NewTracker(Config{
		MaxEntries: 64,
		Now:        func() time.Time { return now },
		LocalAddrs: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr(hostIP)}, nil },
		Routes:     func() (*RouteTable, error) { return testRoutes(), nil },
	})
}

// FuzzHandlePacketAlone is the trust model as a property: with no prior
// query, no single packet - whatever its bytes, leg or interface - can teach
// a name. (The reviewed version learned from any well-formed response.)
func FuzzHandlePacketAlone(f *testing.F) {
	for _, m := range seedMessages(f) {
		f.Add(ipv4UDP(coreDNS, podIP, 53, 40000, m), ifPod, uint8(PacketOutgoing))
		f.Add(ipv4UDP("127.0.0.53", "127.0.0.1", 53, 40000, m), ifLo, uint8(PacketHost))
	}
	f.Fuzz(func(t *testing.T, b []byte, ifindex int, pktType uint8) {
		tr := fuzzTracker()
		if err := tr.safeHandle(PacketMeta{Ifindex: ifindex, PktType: pktType}, b); err != nil {
			t.Fatal(err)
		}
		if s := tr.Stats(); s.Entries != 0 || s.Learned != 0 {
			t.Fatalf("a lone packet taught a name: %+v", s)
		}
	})
}

// FuzzHandleQueryThenResponse drives the whole userspace path - IP, UDP,
// query, match, answers, cache - with an arbitrary query followed by an
// arbitrary response on the pod's legs. Whatever is learned must be a
// normalized name the query itself asked for.
func FuzzHandleQueryThenResponse(f *testing.F) {
	f.Add(stripeQuery(f), stripeAnswer(f), uint16(40000))
	f.Add(buildQuery(f, testID, "a.example.", dnsmessage.TypeA), hostileResponse(f, testID, 40), uint16(1))
	f.Fuzz(func(t *testing.T, query, answer []byte, port uint16) {
		tr := fuzzTracker()
		if err := tr.safeHandle(podQueryLeg, ipv4UDP(podIP, coreDNS, port, 53, query)); err != nil {
			t.Fatal(err)
		}
		if err := tr.safeHandle(podRespLeg, ipv4UDP(coreDNS, podIP, 53, port, answer)); err != nil {
			t.Fatal(err)
		}
		if tr.Stats().Entries > 64/16 {
			t.Fatal("per-client quota exceeded")
		}
		resp, err := ParseResponse(answer)
		if err != nil {
			return
		}
		_, asked, _, qerr := ParseQuery(query)
		for _, a := range resp.Answers {
			name := tr.Hostname(podIP, a.Addr.String())
			if name == "" {
				continue
			}
			if qerr != nil || name != asked {
				t.Fatalf("learned %q, but the query asked for %q (%v)", name, asked, qerr)
			}
		}
	})
}

// FuzzFilterNeverDropsUsablePackets checks the kernel filter against the
// userspace parser: any query or response userspace would use must pass the
// filter. (The converse need not hold - userspace re-validates.)
func FuzzFilterNeverDropsUsablePackets(f *testing.F) {
	for _, m := range seedMessages(f) {
		f.Add(ipv4UDP(coreDNS, podIP, 53, 40000, m))
		f.Add(ipv4UDP(podIP, coreDNS, 40000, 53, m))
		f.Add(ipv6UDP("fd00::a", "fd00::7", 53, 40000, m))
		f.Add(ipv6UDP("fd00::7", "fd00::a", 40000, 53, m))
		f.Add(ipv4UDPWithOptions(coreDNS, podIP, 53, 40000, []byte{1, 1, 1, 0}, m))
	}
	vm, err := bpf.NewVM(Filter())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		pkt, err := ParseUDPPacket(b)
		if err != nil || len(pkt.Payload) > maxDNSMessage {
			return
		}
		usable := false
		if pkt.SrcPort == dnsPort {
			_, err := ParseResponse(pkt.Payload)
			usable = err == nil
		}
		if !usable && pkt.DstPort == dnsPort {
			_, _, _, err := ParseQuery(pkt.Payload)
			usable = err == nil
		}
		if !usable {
			return
		}
		n, err := vm.Run(b)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("filter dropped a packet userspace uses: % x", b)
		}
	})
}

func FuzzNormalizeName(f *testing.F) {
	for _, s := range []string{"api.stripe.com.", "A.B", "..", "a_b-c.d", "\xff", "1.2.3.4", "a.b1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, ok := NormalizeName(s)
		if !ok {
			return
		}
		if again, ok2 := NormalizeName(n); !ok2 || again != n {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, n, again)
		}
		if _, err := netip.ParseAddr(n); err == nil {
			t.Fatalf("an IP literal %q passed as a name", n)
		}
		allDigits := true
		for i := len(n) - 1; i >= 0 && n[i] != '.'; i-- {
			c := n[i]
			allDigits = allDigits && c >= '0' && c <= '9'
		}
		if allDigits {
			t.Fatalf("numeric final label in %q", n)
		}
		for i := 0; i < len(n); i++ {
			c := n[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				t.Fatalf("unexpected byte %q in %q", c, n)
			}
		}
	})
}
