package dnscache

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// hostileLongName is a 253-byte question name (from the review PoC): long
// names maximise per-record copying and comparison work.
func hostileLongName() string {
	long := ""
	for i := 0; i < 3; i++ {
		long += fmt.Sprintf("%063d.", i)
	}
	return long + "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstu."
}

// hostileResponse is the review PoC's worst case: a maximum-length question
// and n CNAME records with long, compressed names.
func hostileResponse(tb testing.TB, id uint16, n int) []byte {
	tb.Helper()
	long := hostileLongName()
	bld := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true})
	bld.EnableCompression()
	_ = bld.StartQuestions()
	_ = bld.Question(dnsmessage.Question{Name: mustName(tb, long), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	_ = bld.StartAnswers()
	for i := 0; i < n; i++ {
		_ = bld.CNAMEResource(dnsmessage.ResourceHeader{Name: mustName(tb, fmt.Sprintf("a%d.%s", i, long[65:])), Class: dnsmessage.ClassINET},
			dnsmessage.CNAMEResource{CNAME: mustName(tb, fmt.Sprintf("b%d.%s", i, long[65:]))})
	}
	m, err := bld.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	return m
}

// hostileRecordsUnder returns the most CNAME records that fit in size bytes
// of IPv4 packet.
func hostileRecordsUnder(tb testing.TB, size int) int {
	n := 1
	for len(ipv4UDP(coreDNS, podIP, 53, 40000, hostileResponse(tb, testID, n+1))) <= size {
		n++
	}
	return n
}

func benchTracker() *Tracker {
	return NewTracker(Config{
		LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Routes:     func() (*RouteTable, error) { return testRoutes(), nil },
	})
}

// BenchmarkUnsolicitedTypical: a well-formed answer nobody asked for - the
// common case for forged or duplicate packets. Rejected after header and
// question parsing.
func BenchmarkUnsolicitedTypical(b *testing.B) {
	tr := benchTracker()
	p := ipv4UDP(coreDNS, podIP, 53, 40000, stripeAnswer(b))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.HandlePacket(podRespLeg, p)
	}
}

// BenchmarkUnsolicitedHostile1500: the review's 1.5 KB hostile packet
// (23 us, 66 KB, 322 allocs per packet before), unsolicited.
func BenchmarkUnsolicitedHostile1500(b *testing.B) {
	tr := benchTracker()
	p := ipv4UDP(coreDNS, podIP, 53, 40000, hostileResponse(b, testID, hostileRecordsUnder(b, 1500)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.HandlePacket(podRespLeg, p)
	}
}

// BenchmarkOversizedHostile: the review's 7.3 KB hostile packet (117 us,
// 339 KB, 1552 allocs before) is now over maxDNSMessage and rejected on
// length - and in the kernel it would be truncated by the snap length.
func BenchmarkOversizedHostile(b *testing.B) {
	tr := benchTracker()
	p := ipv4UDP(coreDNS, podIP, 53, 40000, hostileResponse(b, testID, 300))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.HandlePacket(podRespLeg, p)
	}
}

// BenchmarkOtherLeg: a duplicate copy on a non-client leg (the common case
// on a forwarding node): rejected before any DNS parsing.
func BenchmarkOtherLeg(b *testing.B) {
	tr := benchTracker()
	p := ipv4UDP(coreDNS, podIP, 53, 40000, stripeAnswer(b))
	leg := PacketMeta{Ifindex: ifCoreDNS, PktType: PacketHost}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.HandlePacket(leg, p)
	}
}

// BenchmarkMatchedTypical: a real lookup - query plus matching answer with a
// CNAME and two addresses - per iteration (was 496 ns / 9 allocs for the
// answer alone before, with no matching).
func BenchmarkMatchedTypical(b *testing.B) {
	tr := benchTracker()
	q := ipv4UDP(podIP, coreDNS, 40000, 53, stripeQuery(b))
	a := ipv4UDP(coreDNS, podIP, 53, 40000, stripeAnswer(b))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.HandlePacket(podQueryLeg, q)
		tr.HandlePacket(podRespLeg, a)
	}
}

// BenchmarkMatchedHostile1500: worst case for a compromised or malicious
// resolver (the only party that can get a hostile answer matched): the 1.5 KB
// hostile answer, with its query, per iteration.
func BenchmarkMatchedHostile1500(b *testing.B) {
	tr := benchTracker()
	n := hostileRecordsUnder(b, 1500)
	q := ipv4UDP(podIP, coreDNS, 40000, 53, buildQuery(b, testID, hostileLongName(), dnsmessage.TypeA))
	a := ipv4UDP(coreDNS, podIP, 53, 40000, hostileResponse(b, testID, n))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.HandlePacket(podQueryLeg, q)
		tr.HandlePacket(podRespLeg, a)
	}
}

// BenchmarkMatchedHostile4096: the largest accepted message, matched.
func BenchmarkMatchedHostile4096(b *testing.B) {
	tr := benchTracker()
	n := hostileRecordsUnder(b, 20+8+maxDNSMessage)
	q := ipv4UDP(podIP, coreDNS, 40000, 53, buildQuery(b, testID, hostileLongName(), dnsmessage.TypeA))
	a := ipv4UDP(coreDNS, podIP, 53, 40000, hostileResponse(b, testID, n))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.HandlePacket(podQueryLeg, q)
		tr.HandlePacket(podRespLeg, a)
	}
}

func BenchmarkHostname(b *testing.B) {
	now := time.Now()
	tr := benchTracker()
	tr.now = func() time.Time { return now }
	resolve(tr, podQueryLeg, podRespLeg, podIP, 40000, coreDNS, stripeQuery(b), stripeAnswer(b))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = tr.Hostname(podIP, stripeIP)
	}
}
