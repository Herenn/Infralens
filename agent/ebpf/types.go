// Package ebpf provides eBPF-based network tracing types for the InfraLens agent.
//
// The main agent implementation in agent/main.go directly loads and attaches
// BPF programs using the generated bpfObjects from bpf_bpfel_*.go.
//
// To regenerate the BPF bindings after modifying tracer.c or traffic.c, run:
//
//	cd agent/ebpf && go generate
//
// This requires clang/LLVM to be installed.
package ebpf

import (
	"fmt"
	"net"
)

// Address family constants (matching kernel definitions)
const (
	AF_INET  = 2
	AF_INET6 = 10
)

// Event represents a TCP connection event from the eBPF program.
// Supports both IPv4 and IPv6 addresses (dual-stack).
type Event struct {
	PID    uint32
	SAddr  net.IP // Source IP address (IPv4 or IPv6)
	DAddr  net.IP // Destination IP address (IPv4 or IPv6)
	DPort  uint16
	SPort  uint16
	Comm   string
	Family uint16 // AF_INET (2) or AF_INET6 (10)
}

// IPToString converts a uint32 IP address to dotted notation.
// Deprecated: Use net.IP.String() directly for dual-stack support.
func IPToString(ip uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d",
		byte(ip),
		byte(ip>>8),
		byte(ip>>16),
		byte(ip>>24))
}

// IPToNetIP converts a uint32 IP address to net.IP.
// Deprecated: Event.SAddr and Event.DAddr are now net.IP directly.
func IPToNetIP(ip uint32) net.IP {
	return net.IPv4(
		byte(ip),
		byte(ip>>8),
		byte(ip>>16),
		byte(ip>>24))
}

// String returns a human-readable representation of the event.
// Supports both IPv4 and IPv6 addresses.
func (e *Event) String() string {
	ipVer := 4
	if e.IsIPv6() {
		ipVer = 6
	}
	return fmt.Sprintf("pid=%d comm=%s IPv%d %s:%d -> %s:%d",
		e.PID,
		e.Comm,
		ipVer,
		e.SAddr.String(),
		e.SPort,
		e.DAddr.String(),
		e.DPort)
}

// SrcIP returns the source IP as a string.
// Works for both IPv4 and IPv6.
func (e *Event) SrcIP() string {
	if e.SAddr == nil {
		return ""
	}
	return e.SAddr.String()
}

// DstIP returns the destination IP as a string.
// Works for both IPv4 and IPv6.
func (e *Event) DstIP() string {
	if e.DAddr == nil {
		return ""
	}
	return e.DAddr.String()
}

// IsIPv6 returns true if this event is for an IPv6 connection.
func (e *Event) IsIPv6() bool {
	return e.Family == AF_INET6
}

// IsIPv4 returns true if this event is for an IPv4 connection.
func (e *Event) IsIPv4() bool {
	return e.Family == AF_INET
}
