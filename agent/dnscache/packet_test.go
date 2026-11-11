package dnscache

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/net/dns/dnsmessage"
)

func TestParseUDPPacketIPv4(t *testing.T) {
	payload := []byte("hello")
	pkt, err := ParseUDPPacket(ipv4UDP("10.96.0.10", "10.244.1.7", 53, 41000, payload))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Src != netip.MustParseAddr("10.96.0.10") || pkt.Dst != netip.MustParseAddr("10.244.1.7") ||
		pkt.SrcPort != 53 || pkt.DstPort != 41000 || string(pkt.Payload) != "hello" {
		t.Errorf("got %+v", pkt)
	}
}

func TestParseUDPPacketIPv4Options(t *testing.T) {
	pkt, err := ParseUDPPacket(ipv4UDPWithOptions("192.0.2.1", "192.0.2.2", 53, 999, []byte{1, 1, 1, 1, 1, 1, 1, 0}, []byte("xy")))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.SrcPort != 53 || string(pkt.Payload) != "xy" {
		t.Errorf("got %+v", pkt)
	}
}

// Link layers pad short frames; bytes beyond the IP total length are not
// payload and must not reach the DNS parser.
func TestParseUDPPacketTrimsPadding(t *testing.T) {
	b := append(ipv4UDP("192.0.2.1", "192.0.2.2", 53, 999, []byte("xy")), 0, 0, 0, 0, 0, 0)
	pkt, err := ParseUDPPacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(pkt.Payload) != "xy" {
		t.Errorf("payload = %q", pkt.Payload)
	}
}

func TestParseUDPPacketIPv6(t *testing.T) {
	pkt, err := ParseUDPPacket(ipv6UDP("2001:db8::53", "2001:db8::7", 53, 5000, []byte("abc")))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Dst != netip.MustParseAddr("2001:db8::7") || string(pkt.Payload) != "abc" {
		t.Errorf("got %+v", pkt)
	}
	// IPv4-mapped addresses come out as IPv4.
	pkt, err = ParseUDPPacket(ipv6UDP("::ffff:192.0.2.53", "::ffff:192.0.2.7", 53, 5000, nil))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Dst != netip.MustParseAddr("192.0.2.7") {
		t.Errorf("dst = %v", pkt.Dst)
	}
}

func TestParseUDPPacketRejects(t *testing.T) {
	good4 := ipv4UDP("192.0.2.1", "192.0.2.2", 53, 999, []byte("payload"))
	good6 := ipv6UDP("2001:db8::1", "2001:db8::2", 53, 999, []byte("payload"))
	mutate := func(b []byte, f func([]byte)) []byte {
		c := append([]byte(nil), b...)
		f(c)
		return c
	}
	cases := map[string]struct {
		pkt  []byte
		want error
	}{
		"empty":                   {nil, ErrShortPacket},
		"ipv4 short header":       {good4[:19], ErrShortPacket},
		"ipv6 short header":       {good6[:39], ErrShortPacket},
		"bad version":             {mutate(good4, func(b []byte) { b[0] = 0x50 }), ErrBadVersion},
		"ihl too small":           {mutate(good4, func(b []byte) { b[0] = 0x44 }), ErrShortPacket},
		"ihl beyond packet":       {mutate(good4, func(b []byte) { b[0] = 0x4f; binary.BigEndian.PutUint16(b[2:4], 40) }), ErrShortPacket},
		"total > captured":        {good4[:len(good4)-1], ErrShortPacket},
		"total < ihl":             {mutate(good4, func(b []byte) { binary.BigEndian.PutUint16(b[2:4], 10) }), ErrShortPacket},
		"tcp":                     {mutate(good4, func(b []byte) { b[9] = 6 }), ErrNotUDP},
		"more fragments":          {mutate(good4, func(b []byte) { b[6] = 0x20 }), ErrNotUDP},
		"fragment offset":         {mutate(good4, func(b []byte) { b[7] = 0x01 }), ErrNotUDP},
		"udp len > rest":          {mutate(good4, func(b []byte) { binary.BigEndian.PutUint16(b[24:26], 200) }), ErrShortPacket},
		"udp len < 8":             {mutate(good4, func(b []byte) { binary.BigEndian.PutUint16(b[24:26], 7) }), ErrShortPacket},
		"no udp header":           {mutate(good4[:24], func(b []byte) { binary.BigEndian.PutUint16(b[2:4], 24) }), ErrShortPacket},
		"ipv6 payload > captured": {good6[:len(good6)-1], ErrShortPacket},
		"ipv6 ext header":         {mutate(good6, func(b []byte) { b[6] = 0 }), ErrNotUDP},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseUDPPacket(c.pkt); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// The classic-BPF filter runs in the kernel and cannot be loaded here, but
// x/net/bpf ships a VM implementing the same instruction semantics
// (including "out-of-bounds load drops the packet"), so the program's logic
// is tested against crafted packets directly.
func runFilter(t *testing.T, pkt []byte) bool {
	t.Helper()
	vm, err := bpf.NewVM(Filter())
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	n, err := vm.Run(pkt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return n > 0
}

func TestFilterAssembles(t *testing.T) {
	raw, err := bpf.Assemble(Filter())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || len(raw) > 4096 { // BPF_MAXINSNS
		t.Fatalf("program length %d", len(raw))
	}
	vm, err := bpf.NewVM(Filter())
	if err != nil {
		t.Fatal(err)
	}
	// The return value is the snap length. A maximum-size accepted message
	// fits whole; anything longer is cut short (and then rejected by
	// ParseUDPPacket), so an oversized packet is never copied in full.
	full := ipv4UDPWithOptions("192.0.2.1", "192.0.2.2", 53, 999, make([]byte, 40), make([]byte, maxDNSMessage))
	full[60+8+2] = 0x80
	if n, _ := vm.Run(full); n < len(full) {
		t.Errorf("snap %d < %d: a %d-byte message must fit", n, len(full), maxDNSMessage)
	}
	huge := ipv4UDP("192.0.2.1", "192.0.2.2", 53, 999, make([]byte, 30000))
	huge[28+2] = 0x80
	if n, _ := vm.Run(huge); n != captureSnapLen {
		t.Errorf("snap for a huge packet = %d, want %d", n, captureSnapLen)
	} else if _, err := ParseUDPPacket(huge[:n]); err == nil {
		t.Error("a truncated oversized packet must not parse")
	}
}

func TestAssembleRejectsBadPrograms(t *testing.T) {
	ret := bpf.RetConstant{Val: 0}
	for name, prog := range map[string][]insn{
		"unknown label":  {{ins: bpf.JumpIf{Cond: bpf.JumpEqual}, jt: "nowhere"}, {ins: ret}},
		"backward jump":  {{label: "a", ins: ret}, {ins: bpf.JumpIf{Cond: bpf.JumpEqual}, jt: "a"}},
		"duplicate":      {{label: "a", ins: ret}, {label: "a", ins: ret}},
		"label non-jump": {{ins: ret, jt: "a"}, {label: "a", ins: ret}},
	} {
		if _, err := assemble(prog); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFilter(t *testing.T) {
	resp := buildResponse(t, "a.example.", dnsmessage.TypeA, rr{"a.example.", dnsmessage.TypeA, 60, "192.0.2.1"})
	query := buildQuery(t, testID, "a.example.", dnsmessage.TypeA)

	withProto := func(b []byte, proto byte) []byte { c := append([]byte(nil), b...); c[9] = proto; return c }
	frag := func(b []byte, hi, lo byte) []byte { c := append([]byte(nil), b...); c[6], c[7] = hi, lo; return c }
	v6ext := func(b []byte) []byte { c := append([]byte(nil), b...); c[6] = 0; return c }

	v4 := ipv4UDP("10.96.0.10", "10.244.1.7", 53, 41000, resp)
	v4q := ipv4UDP("10.244.1.7", "10.96.0.10", 41000, 53, query)
	v6 := ipv6UDP("fd00::a", "fd00::7", 53, 41000, resp)
	v6q := ipv6UDP("fd00::7", "fd00::a", 41000, 53, query)
	opts := []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0}

	cases := []struct {
		name   string
		pkt    []byte
		accept bool
	}{
		{"ipv4 response", v4, true},
		{"ipv4 response with options", ipv4UDPWithOptions("10.96.0.10", "10.244.1.7", 53, 41000, opts, resp), true},
		{"ipv4 query", v4q, true},
		{"ipv4 query with options", ipv4UDPWithOptions("10.244.1.7", "10.96.0.10", 41000, 53, opts, query), true},
		{"ipv4 query from port 53", ipv4UDP("10.96.0.10", "10.244.1.7", 53, 41000, query), false},
		// Found by FuzzFilterNeverDropsUsablePackets: resolver-to-resolver
		// traffic uses port 53 on both ends, and userspace handles it.
		{"ipv4 query 53 to 53", ipv4UDP("10.96.0.10", "10.244.1.7", 53, 53, query), true},
		{"ipv4 response 53 to 53", ipv4UDP("10.96.0.10", "10.244.1.7", 53, 53, resp), true},
		{"ipv6 query 53 to 53", ipv6UDP("fd00::a", "fd00::7", 53, 53, query), true},
		{"ipv4 response to port 53", ipv4UDP("10.244.1.7", "10.96.0.10", 41000, 53, resp), false},
		{"ipv4 response from 5353", ipv4UDP("10.96.0.10", "10.244.1.7", 5353, 41000, resp), false},
		{"ipv4 query to 5353", ipv4UDP("10.244.1.7", "10.96.0.10", 41000, 5353, query), false},
		{"ipv4 tcp", withProto(v4, 6), false},
		{"ipv4 tcp query", withProto(v4q, 6), false},
		{"ipv4 more fragments", frag(v4, 0x20, 0), false},
		{"ipv4 later fragment", frag(v4q, 0, 0x10), false},
		{"ipv4 dont-fragment set", frag(v4, 0x40, 0), true},
		{"ipv4 truncated before udp", v4[:22], false},
		{"ipv6 response", v6, true},
		{"ipv6 query", v6q, true},
		{"ipv6 query from 53", ipv6UDP("fd00::a", "fd00::7", 53, 41000, query), false},
		{"ipv6 response to 53", ipv6UDP("fd00::7", "fd00::a", 41000, 53, resp), false},
		{"ipv6 ext header", v6ext(v6), false},
		{"ipv6 ext header query", v6ext(v6q), false},
		{"ipv6 truncated", v6[:44], false},
		{"empty", nil, false},
		{"garbage version", []byte{0x10, 0, 0, 0}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runFilter(t, c.pkt); got != c.accept {
				t.Errorf("filter accepted=%v, want %v", got, c.accept)
			}
		})
	}
}
