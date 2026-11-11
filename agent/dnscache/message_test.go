package dnscache

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestNormalizeName(t *testing.T) {
	long63 := strings.Repeat("a", 63)
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"api.stripe.com.", "api.stripe.com", true},
		{"API.Stripe.COM", "api.stripe.com", true},
		{"_dmarc.example.com", "_dmarc.example.com", true},
		{"xn--bcher-kva.example", "xn--bcher-kva.example", true},
		{"localhost", "localhost", true},
		{long63 + ".com", long63 + ".com", true},
		{strings.Repeat("a", 64) + ".com", "", false},
		{strings.Repeat("a.", 126) + "ab", "", false}, // 254 chars
		{strings.Repeat("a.", 126) + "a", strings.Repeat("a.", 126) + "a", true},
		{"", "", false},
		{".", "", false},
		{"..", "", false},
		{"a..b", "", false},
		{".a", "", false},
		{"a.b..", "", false},
		{"exa mple.com", "", false},
		{"evil\".com", "", false},
		{"a<b>.com", "", false},
		{"caf\xc3\xa9.com", "", false},
		{"a\x00b.com", "", false},
		{"a\\.b.com", "", false},
		// A numeric final label is never a hostname: this is what keeps an
		// IP literal from labelling an IP node with a different IP.
		{"52.1.2.3", "", false},
		{"52.1.2.3.", "", false},
		{"host.123", "", false},
		{"123", "", false},
		{"123.example", "123.example", true},
		{"a.b2", "a.b2", true},
		{"0x1.0x2", "0x1.0x2", true},
	}
	for _, c := range cases {
		got, ok := NormalizeName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func answersOf(t *testing.T, msg []byte) (string, map[string]uint32) {
	t.Helper()
	resp, err := ParseResponse(msg)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	got := make(map[string]uint32)
	for _, a := range resp.Answers {
		got[a.Addr.String()] = a.TTL
	}
	return resp.Name, got
}

func TestParseResponseSimpleA(t *testing.T) {
	msg := buildResponse(t, "api.stripe.com.", dnsmessage.TypeA,
		rr{"api.stripe.com.", dnsmessage.TypeA, 60, "52.1.2.3"},
		rr{"api.stripe.com.", dnsmessage.TypeA, 30, "52.1.2.4"},
	)
	name, got := answersOf(t, msg)
	if name != "api.stripe.com" {
		t.Errorf("name = %q", name)
	}
	if len(got) != 2 || got["52.1.2.3"] != 60 || got["52.1.2.4"] != 30 {
		t.Errorf("answers = %v", got)
	}
}

func TestParseResponseAAAA(t *testing.T) {
	msg := buildResponse(t, "ipv6.example.com.", dnsmessage.TypeAAAA,
		rr{"ipv6.example.com.", dnsmessage.TypeAAAA, 300, "2001:db8::1"},
		// An IPv4-mapped answer must come out in its IPv4 form, matching how
		// the collector reports such a destination.
		rr{"ipv6.example.com.", dnsmessage.TypeAAAA, 300, "::ffff:192.0.2.7"},
	)
	_, got := answersOf(t, msg)
	if _, ok := got["2001:db8::1"]; !ok {
		t.Errorf("missing 2001:db8::1 in %v", got)
	}
	if _, ok := got["192.0.2.7"]; !ok {
		t.Errorf("mapped address not unmapped: %v", got)
	}
}

// The name reported must be the one the application asked for, not the CDN
// alias it went through, and the TTL the minimum along the chain.
func TestParseResponseCNAMEChain(t *testing.T) {
	msg := buildResponse(t, "api.stripe.com.", dnsmessage.TypeA,
		rr{"api.stripe.com.", dnsmessage.TypeCNAME, 300, "api.stripe.com.cdn.cloudflare.net."},
		rr{"api.stripe.com.cdn.cloudflare.net.", dnsmessage.TypeCNAME, 20, "edge.cloudflare.net."},
		rr{"edge.cloudflare.net.", dnsmessage.TypeA, 120, "104.16.0.1"},
		// Not on the chain: must be ignored even though it is in the answer.
		rr{"unrelated.example.", dnsmessage.TypeA, 120, "198.51.100.66"},
	)
	name, got := answersOf(t, msg)
	if name != "api.stripe.com" {
		t.Errorf("name = %q, want the question name", name)
	}
	if len(got) != 1 {
		t.Fatalf("answers = %v, want only the on-chain address", got)
	}
	if ttl := got["104.16.0.1"]; ttl != 20 {
		t.Errorf("ttl = %d, want 20 (minimum along the chain)", ttl)
	}
}

// Answer records appear in any order in the wild; the chain is followed by
// name, not position.
func TestParseResponseCNAMEOutOfOrder(t *testing.T) {
	msg := buildResponse(t, "a.example.", dnsmessage.TypeA,
		rr{"c.example.", dnsmessage.TypeA, 60, "192.0.2.10"},
		rr{"b.example.", dnsmessage.TypeCNAME, 60, "c.example."},
		rr{"a.example.", dnsmessage.TypeCNAME, 60, "b.example."},
	)
	name, got := answersOf(t, msg)
	if name != "a.example" || len(got) != 1 {
		t.Errorf("got %q %v", name, got)
	}
}

func TestParseResponseCNAMELoopTerminates(t *testing.T) {
	msg := buildResponse(t, "a.example.", dnsmessage.TypeA,
		rr{"a.example.", dnsmessage.TypeCNAME, 60, "b.example."},
		rr{"b.example.", dnsmessage.TypeCNAME, 60, "a.example."},
		rr{"b.example.", dnsmessage.TypeA, 60, "192.0.2.1"},
	)
	// b is on the chain, so its address is attributable; the loop must
	// simply not hang.
	name, got := answersOf(t, msg)
	if name != "a.example" || len(got) != 1 {
		t.Errorf("got %q %v", name, got)
	}
}

func TestParseResponseLongChainIsBounded(t *testing.T) {
	var answers []rr
	for i := 0; i < maxCNAMEChain+5; i++ {
		answers = append(answers, rr{
			name: "n" + strings.Repeat("x", i) + ".example.", typ: dnsmessage.TypeCNAME, ttl: 60,
			value: "n" + strings.Repeat("x", i+1) + ".example.",
		})
	}
	// Address at the very end of an over-long chain: not attributable.
	answers = append(answers, rr{"n" + strings.Repeat("x", maxCNAMEChain+5) + ".example.", dnsmessage.TypeA, 60, "192.0.2.1"})
	if _, err := ParseResponse(buildResponse(t, "n.example.", dnsmessage.TypeA, answers...)); !errors.Is(err, ErrNoAddresses) {
		t.Errorf("err = %v, want ErrNoAddresses", err)
	}
}

func TestParseResponseFirstCNAMEWins(t *testing.T) {
	msg := buildResponse(t, "a.example.", dnsmessage.TypeA,
		rr{"a.example.", dnsmessage.TypeCNAME, 60, "good.example."},
		rr{"a.example.", dnsmessage.TypeCNAME, 60, "evil.example."},
		rr{"good.example.", dnsmessage.TypeA, 60, "192.0.2.1"},
		rr{"evil.example.", dnsmessage.TypeA, 60, "192.0.2.66"},
	)
	_, got := answersOf(t, msg)
	if _, ok := got["192.0.2.1"]; !ok || len(got) != 1 {
		t.Errorf("answers = %v", got)
	}
}

// DNS 0x20 randomizes the question's case; owners must still match.
func TestParseResponseCaseInsensitive(t *testing.T) {
	msg := buildResponse(t, "ApI.StRiPe.CoM.", dnsmessage.TypeA,
		rr{"api.stripe.com.", dnsmessage.TypeA, 60, "52.1.2.3"},
	)
	name, got := answersOf(t, msg)
	if name != "api.stripe.com" || len(got) != 1 {
		t.Errorf("got %q %v", name, got)
	}
}

func TestParseResponseDedupesAndSkipsUnspecified(t *testing.T) {
	msg := buildResponse(t, "a.example.", dnsmessage.TypeA,
		rr{"a.example.", dnsmessage.TypeA, 10, "192.0.2.1"},
		rr{"a.example.", dnsmessage.TypeA, 90, "192.0.2.1"},
		rr{"a.example.", dnsmessage.TypeA, 90, "0.0.0.0"},
		rr{"a.example.", dnsmessage.TypeAAAA, 90, "::ffff:0.0.0.0"}, // unspecified once unmapped
		rr{"a.example.", dnsmessage.TypeAAAA, 90, "::"},
		rr{"a.example.", dnsmessage.TypeTXT, 90, "v=spf1"},
	)
	_, got := answersOf(t, msg)
	if len(got) != 1 || got["192.0.2.1"] != 90 {
		t.Errorf("answers = %v, want one address with the larger TTL", got)
	}
}

func TestParseResponseCapsAddresses(t *testing.T) {
	var answers []rr
	for i := 0; i < maxAddrsPerAnswer+20; i++ {
		answers = append(answers, rr{"a.example.", dnsmessage.TypeA, 60, netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}).String()})
	}
	resp, err := ParseResponse(buildResponse(t, "a.example.", dnsmessage.TypeA, answers...))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Answers) != maxAddrsPerAnswer {
		t.Errorf("kept %d addresses, want cap %d", len(resp.Answers), maxAddrsPerAnswer)
	}
}

func TestParseResponseRejects(t *testing.T) {
	q := func(name string, typ dnsmessage.Type, class dnsmessage.Class) dnsmessage.Question {
		return dnsmessage.Question{Name: mustName(t, name), Type: typ, Class: class}
	}
	ok := dnsmessage.Header{Response: true}
	a := []rr{{"a.example.", dnsmessage.TypeA, 60, "192.0.2.1"}}
	cases := []struct {
		name string
		msg  []byte
		want error
	}{
		{"query", buildMessage(t, dnsmessage.Header{}, []dnsmessage.Question{q("a.example.", dnsmessage.TypeA, dnsmessage.ClassINET)}, a), ErrNotResponse},
		{"nxdomain", buildMessage(t, dnsmessage.Header{Response: true, RCode: dnsmessage.RCodeNameError}, []dnsmessage.Question{q("a.example.", dnsmessage.TypeA, dnsmessage.ClassINET)}, nil), ErrUnsupported},
		{"notify opcode", buildMessage(t, dnsmessage.Header{Response: true, OpCode: 4}, []dnsmessage.Question{q("a.example.", dnsmessage.TypeA, dnsmessage.ClassINET)}, a), ErrUnsupported},
		{"mx question", buildMessage(t, ok, []dnsmessage.Question{q("a.example.", dnsmessage.TypeMX, dnsmessage.ClassINET)}, a), ErrUnsupported},
		{"chaos class", buildMessage(t, ok, []dnsmessage.Question{q("a.example.", dnsmessage.TypeA, dnsmessage.ClassCHAOS)}, a), ErrUnsupported},
		{"two questions", buildMessage(t, ok, []dnsmessage.Question{q("a.example.", dnsmessage.TypeA, dnsmessage.ClassINET), q("b.example.", dnsmessage.TypeA, dnsmessage.ClassINET)}, a), ErrUnsupported},
		{"no answers", buildMessage(t, ok, []dnsmessage.Question{q("a.example.", dnsmessage.TypeA, dnsmessage.ClassINET)}, nil), ErrNoAddresses},
		{"bad question name", buildMessage(t, ok, []dnsmessage.Question{q("bad name.example.", dnsmessage.TypeA, dnsmessage.ClassINET)}, nil), ErrBadQuestion},
		{"root question", buildMessage(t, ok, []dnsmessage.Question{q(".", dnsmessage.TypeA, dnsmessage.ClassINET)}, nil), ErrBadQuestion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseResponse(c.msg); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// A message cut off mid-record keeps whatever parsed cleanly before the cut.
func TestParseResponseTruncatedKeepsCompleteRecords(t *testing.T) {
	full := buildResponse(t, "a.example.", dnsmessage.TypeA,
		rr{"a.example.", dnsmessage.TypeA, 60, "192.0.2.1"},
		rr{"a.example.", dnsmessage.TypeA, 60, "192.0.2.2"},
	)
	cut := full[:len(full)-2] // inside the second A record's RDATA
	resp, err := ParseResponse(cut)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if len(resp.Answers) != 1 || resp.Answers[0].Addr != netip.MustParseAddr("192.0.2.1") {
		t.Errorf("answers = %v", resp.Answers)
	}

	// Every possible truncation must be handled without panicking.
	for i := 0; i <= len(full); i++ {
		_, _ = ParseResponse(full[:i])
	}
}

// rawHeader is a 12-byte DNS header: response, NOERROR, qd=1, an=ancount.
func rawHeader(ancount byte) []byte {
	return []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, ancount, 0, 0, 0, 0}
}

// Hand-built messages exercising compression pointers in ways a builder
// never would.
func TestParseResponseHostileCompression(t *testing.T) {
	question := []byte{1, 'a', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 0, 1, 0, 1} // a.example A IN
	aRecord := func(namePtr ...byte) []byte {
		r := append([]byte{}, namePtr...)
		return append(r, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
	}

	t.Run("valid pointer to question", func(t *testing.T) {
		msg := append(append(rawHeader(1), question...), aRecord(0xc0, 12)...)
		if _, got := answersOf(t, msg); len(got) != 1 {
			t.Errorf("answers = %v", got)
		}
	})

	cases := map[string][]byte{
		// Answer owner is a pointer to itself.
		"self loop": append(append(rawHeader(1), question...), aRecord(0xc0, 27)...),
		// Two pointers pointing at each other.
		"mutual loop": func() []byte {
			m := append(rawHeader(1), question...)
			// offset 27: pointer to 29; offset 29: pointer to 27
			m = append(m, 0xc0, 29, 0xc0, 27)
			return append(m, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
		}(),
		// Pointer past the end of the message.
		"pointer out of range": append(append(rawHeader(1), question...), aRecord(0xff, 0xff)...),
		// Question name is a pointer forward into nothing.
		"question pointer": append(rawHeader(0), 0xc0, 0x40, 0, 1, 0, 1),
		// Reserved label type bits (0x40/0x80).
		"reserved label type": append(append(rawHeader(1), question...), aRecord(0x40, 1, 'a', 0)...),
		// A record claiming more RDATA than present.
		"rdlength overflow": func() []byte {
			m := append(append(rawHeader(1), question...), 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0xff, 0xff)
			return append(m, 192, 0, 2, 1)
		}(),
		// Answer count far larger than the records present.
		"ancount lies": append(append(rawHeader(200), question...), aRecord(0xc0, 12)...),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := ParseResponse(msg)
			// Only the "ancount lies" case may yield the one valid record.
			if err == nil && name != "ancount lies" {
				t.Errorf("accepted hostile message: %+v", resp)
			}
		})
	}
}

// An A record whose RDLENGTH is not 4 is malformed. The parser reads a fixed
// four bytes and would otherwise accept it.
func TestParseResponseWrongRDLength(t *testing.T) {
	question := []byte{1, 'a', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 0, 1, 0, 1}
	msg := append(append(rawHeader(1), question...), 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 6, 192, 0, 2, 1, 9, 9)
	if _, err := ParseResponse(msg); !errors.Is(err, ErrNoAddresses) {
		t.Errorf("err = %v, want ErrNoAddresses", err)
	}
}

func TestParseQuery(t *testing.T) {
	id, name, qtype, err := ParseQuery(buildQuery(t, 0xbeef, "Api.Stripe.com.", dnsmessage.TypeAAAA))
	if err != nil || id != 0xbeef || name != "api.stripe.com" || qtype != dnsmessage.TypeAAAA {
		t.Errorf("ParseQuery = %x %q %v %v", id, name, qtype, err)
	}
	if _, _, _, err := ParseQuery(buildResponse(t, "a.example.", dnsmessage.TypeA)); !errors.Is(err, ErrNotQuery) {
		t.Errorf("response accepted as query: %v", err)
	}
	if _, _, _, err := ParseQuery(buildQuery(t, 1, "a.example.", dnsmessage.TypeMX)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("MX query: %v", err)
	}
	if _, _, _, err := ParseQuery(buildQuery(t, 1, "1.2.3.4.", dnsmessage.TypeA)); !errors.Is(err, ErrBadQuestion) {
		t.Errorf("numeric query name: %v", err)
	}
	big := append(buildQuery(t, 1, "a.example.", dnsmessage.TypeA), make([]byte, maxDNSMessage)...)
	if _, _, _, err := ParseQuery(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized query: %v", err)
	}
}

// Parsing a matched answer must not allocate per record (the reviewed
// version did, via Name.String, up to 339 KB per hostile packet).
func TestParseAnswersDoesNotAllocate(t *testing.T) {
	msg := buildResponse(t, "api.stripe.com.", dnsmessage.TypeA,
		rr{"api.stripe.com.", dnsmessage.TypeCNAME, 60, "api.stripe.com.cdn.example.net."},
		rr{"api.stripe.com.cdn.example.net.", dnsmessage.TypeA, 30, "52.1.2.3"},
		rr{"api.stripe.com.cdn.example.net.", dnsmessage.TypeA, 30, "52.1.2.4"})
	var p dnsmessage.Parser
	var buf [maxNameLen + 1]byte
	s := new(answerScratch)
	out := make([]Answer, 0, maxAddrsPerAnswer)
	allocs := testing.AllocsPerRun(100, func() {
		h, err := parseHead(&p, msg, buf[:], true)
		if err != nil {
			t.Fatal(err)
		}
		out = parseAnswers(&p, &h, s, out[:0])
	})
	if allocs != 0 || len(out) != 2 {
		t.Errorf("allocs=%v answers=%v", allocs, out)
	}
}
