// Package ebpf provides eBPF-based network tracing for the InfraLens agent.
package ebpf

import (
	"fmt"
	"net"
)

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
	return fmt.Sprintf("pid=%d comm=%s IPv%d %s -> %s:%d",
		e.PID,
		e.Comm,
		e.IPVer,
		e.SAddr.String(),
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
	return e.IPVer == 6
}

// IsIPv4 returns true if this event is for an IPv4 connection.
func (e *Event) IsIPv4() bool {
	return e.IPVer == 4
}
