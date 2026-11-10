package dnscache

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// rr is a compact answer-record description for buildResponse.
type rr struct {
	name  string
	typ   dnsmessage.Type
	ttl   uint32
	value string // IP for A/AAAA, target for CNAME
}

func mustName(t testing.TB, s string) dnsmessage.Name {
	t.Helper()
	n, err := dnsmessage.NewName(s)
	if err != nil {
		t.Fatalf("NewName(%q): %v", s, err)
	}
	return n
}

// buildMessage builds a DNS message with name compression enabled, so the
// tests exercise compression pointers the way real resolvers emit them.
func buildMessage(t testing.TB, hdr dnsmessage.Header, questions []dnsmessage.Question, answers []rr) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), hdr)
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		if err := b.Question(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	for _, a := range answers {
		h := dnsmessage.ResourceHeader{Name: mustName(t, a.name), Class: dnsmessage.ClassINET, TTL: a.ttl}
		var err error
		switch a.typ {
		case dnsmessage.TypeA:
			err = b.AResource(h, dnsmessage.AResource{A: netip.MustParseAddr(a.value).As4()})
		case dnsmessage.TypeAAAA:
			err = b.AAAAResource(h, dnsmessage.AAAAResource{AAAA: netip.MustParseAddr(a.value).As16()})
		case dnsmessage.TypeCNAME:
			err = b.CNAMEResource(h, dnsmessage.CNAMEResource{CNAME: mustName(t, a.value)})
		case dnsmessage.TypeTXT:
			err = b.TXTResource(h, dnsmessage.TXTResource{TXT: []string{a.value}})
		default:
			t.Fatalf("unsupported test record type %v", a.typ)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// testID is the transaction ID buildQuery/buildResponse use by default.
const testID = 0x1234

// buildResponse builds a standard NOERROR response to one IN question.
func buildResponse(t testing.TB, qname string, qtype dnsmessage.Type, answers ...rr) []byte {
	t.Helper()
	return buildResponseID(t, testID, qname, qtype, answers...)
}

func buildResponseID(t testing.TB, id uint16, qname string, qtype dnsmessage.Type, answers ...rr) []byte {
	t.Helper()
	return buildMessage(t,
		dnsmessage.Header{ID: id, Response: true, RecursionDesired: true, RecursionAvailable: true},
		[]dnsmessage.Question{{Name: mustName(t, qname), Type: qtype, Class: dnsmessage.ClassINET}},
		answers)
}

// buildQuery builds a standard recursive query.
func buildQuery(t testing.TB, id uint16, qname string, qtype dnsmessage.Type) []byte {
	t.Helper()
	return buildMessage(t,
		dnsmessage.Header{ID: id, RecursionDesired: true},
		[]dnsmessage.Question{{Name: mustName(t, qname), Type: qtype, Class: dnsmessage.ClassINET}},
		nil)
}

// ipv4UDP wraps payload in UDP and IPv4 headers. Checksums are left zero:
// nothing here validates them, matching the kernel's checksum offload reality.
func ipv4UDP(src, dst string, sport, dport uint16, payload []byte) []byte {
	return ipv4UDPWithOptions(src, dst, sport, dport, nil, payload)
}

func ipv4UDPWithOptions(src, dst string, sport, dport uint16, options []byte, payload []byte) []byte {
	ihl := 20 + len(options)
	total := ihl + 8 + len(payload)
	b := make([]byte, total)
	b[0] = 0x40 | byte(ihl/4)
	binary.BigEndian.PutUint16(b[2:4], uint16(total))
	b[8] = 64
	b[9] = protoUDP
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(b[12:16], s[:])
	copy(b[16:20], d[:])
	copy(b[20:ihl], options)
	u := b[ihl:]
	binary.BigEndian.PutUint16(u[0:2], sport)
	binary.BigEndian.PutUint16(u[2:4], dport)
	binary.BigEndian.PutUint16(u[4:6], uint16(8+len(payload)))
	copy(u[8:], payload)
	return b
}

func ipv6UDP(src, dst string, sport, dport uint16, payload []byte) []byte {
	b := make([]byte, 40+8+len(payload))
	b[0] = 0x60
	binary.BigEndian.PutUint16(b[4:6], uint16(8+len(payload)))
	b[6] = protoUDP
	b[7] = 64
	s, d := netip.MustParseAddr(src).As16(), netip.MustParseAddr(dst).As16()
	copy(b[8:24], s[:])
	copy(b[24:40], d[:])
	u := b[40:]
	binary.BigEndian.PutUint16(u[0:2], sport)
	binary.BigEndian.PutUint16(u[2:4], dport)
	binary.BigEndian.PutUint16(u[4:6], uint16(8+len(payload)))
	copy(u[8:], payload)
	return b
}
