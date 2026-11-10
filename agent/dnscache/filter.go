package dnscache

import (
	"fmt"

	"golang.org/x/net/bpf"
)

// maxDNSMessage is the largest DNS message accepted. EDNS answers are capped
// by resolvers at 4096 bytes in practice (1232 since DNS Flag Day 2020);
// anything bigger is dropped rather than parsed.
const maxDNSMessage = 4096

// captureSnapLen is how much of a matching packet the kernel copies to us:
// the largest IP header (IPv4 with options: 60; IPv6 fixed: 40) + UDP + the
// largest message we accept. A longer datagram arrives truncated and is
// rejected by ParseUDPPacket (its IP length exceeds what was captured), so an
// oversized packet costs neither a large copy nor a parse.
const captureSnapLen = 60 + udpHeader + maxDNSMessage

// insn is a filter instruction whose conditional jumps name their targets by
// label instead of by hand-counted offsets.
type insn struct {
	label  string // optional: marks this instruction as a jump target
	ins    bpf.Instruction
	jt, jf string // for JumpIf: target labels ("" = next instruction)
}

func assemble(prog []insn) ([]bpf.Instruction, error) {
	at := make(map[string]int, len(prog))
	for i, p := range prog {
		if p.label != "" {
			if _, dup := at[p.label]; dup {
				return nil, fmt.Errorf("duplicate label %q", p.label)
			}
			at[p.label] = i
		}
	}
	skip := func(from int, label string) (uint8, error) {
		if label == "" {
			return 0, nil
		}
		to, ok := at[label]
		if !ok {
			return 0, fmt.Errorf("unknown label %q", label)
		}
		d := to - (from + 1)
		if d < 0 || d > 255 {
			return 0, fmt.Errorf("jump to %q out of range", label)
		}
		return uint8(d), nil
	}
	out := make([]bpf.Instruction, len(prog))
	for i, p := range prog {
		j, isJump := p.ins.(bpf.JumpIf)
		if !isJump {
			if p.jt != "" || p.jf != "" {
				return nil, fmt.Errorf("instruction %d has labels but is not a JumpIf", i)
			}
			out[i] = p.ins
			continue
		}
		var err error
		if j.SkipTrue, err = skip(i, p.jt); err != nil {
			return nil, err
		}
		if j.SkipFalse, err = skip(i, p.jf); err != nil {
			return nil, err
		}
		out[i] = j
	}
	return out, nil
}

// Filter returns the classic-BPF program attached to the capture socket. It
// runs in the kernel on every packet the socket's tap sees, so it is kept
// tiny and only lets DNS over UDP through, in the two shapes userspace uses:
//
//	query:    destination port 53, DNS header QR bit clear
//	response: source port 53,      DNS header QR bit set
//
// for IPv4 (no fragments) and IPv6 (no extension headers). Queries are needed
// because a response is only believed if it answers a query this host's
// client actually sent (see Tracker).
//
// Offsets are relative to the network header because the socket is
// AF_PACKET/SOCK_DGRAM, for which the kernel runs the filter on the packet
// with the link-layer header already removed (net/packet/af_packet.c,
// packet_rcv). That also makes it independent of the interface type.
//
// Everything here is re-checked in userspace; the filter is an optimisation,
// not a trust boundary.
func Filter() []bpf.Instruction {
	const flags = udpHeader + 2 // DNS flags byte, relative to the UDP header
	prog := []insn{
		{ins: bpf.LoadAbsolute{Off: 0, Size: 1}},
		{ins: bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0}},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x40}, jt: "v4"},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x60}, jt: "v6", jf: "drop"},

		// ---- IPv4 ----
		{label: "v4", ins: bpf.LoadAbsolute{Off: 9, Size: 1}},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: protoUDP}, jf: "drop"},
		{ins: bpf.LoadAbsolute{Off: 6, Size: 2}},
		{ins: bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x3fff}, jt: "drop"}, // MF or fragment offset
		{ins: bpf.LoadMemShift{Off: 0}},                                   // X = IPv4 header length
		{ins: bpf.LoadIndirect{Off: 0, Size: 2}},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: dnsPort}, jf: "v4query"},
		{ins: bpf.LoadIndirect{Off: flags, Size: 1}},
		// From port 53 with QR clear: not a response, but may still be a
		// query to port 53 (resolver to resolver) - userspace handles those.
		{ins: bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x80}, jt: "accept", jf: "v4query"},
		{label: "v4query", ins: bpf.LoadIndirect{Off: 2, Size: 2}},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: dnsPort}, jf: "drop"},
		{ins: bpf.LoadIndirect{Off: flags, Size: 1}},
		{ins: bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x80}, jt: "drop", jf: "accept"},

		// ---- IPv6 ----
		{label: "v6", ins: bpf.LoadAbsolute{Off: 6, Size: 1}},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: protoUDP}, jf: "drop"},
		{ins: bpf.LoadAbsolute{Off: ipv6Header, Size: 2}},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: dnsPort}, jf: "v6query"},
		{ins: bpf.LoadAbsolute{Off: ipv6Header + flags, Size: 1}},
		{ins: bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x80}, jt: "accept", jf: "v6query"},
		{label: "v6query", ins: bpf.LoadAbsolute{Off: ipv6Header + 2, Size: 2}},
		{ins: bpf.JumpIf{Cond: bpf.JumpEqual, Val: dnsPort}, jf: "drop"},
		{ins: bpf.LoadAbsolute{Off: ipv6Header + flags, Size: 1}},
		{ins: bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x80}, jt: "drop", jf: "accept"},

		{label: "accept", ins: bpf.RetConstant{Val: captureSnapLen}},
		{label: "drop", ins: bpf.RetConstant{Val: 0}},
	}
	out, err := assemble(prog)
	if err != nil {
		panic("dnscache: bad filter program: " + err.Error()) // static program; covered by tests
	}
	return out
}
