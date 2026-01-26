// Package ebpf provides eBPF-based network tracing for the InfraLens agent.
package ebpf

import (
	"fmt"
	"net"
)

// IPToString converts a uint32 IP address to dotted notation.
func IPToString(ip uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d",
		byte(ip),
		byte(ip>>8),
		byte(ip>>16),
		byte(ip>>24))
}

// IPToNetIP converts a uint32 IP address to net.IP.
func IPToNetIP(ip uint32) net.IP {
	return net.IPv4(
		byte(ip),
		byte(ip>>8),
		byte(ip>>16),
		byte(ip>>24))
}

// String returns a human-readable representation of the event.
func (e *Event) String() string {
	return fmt.Sprintf("pid=%d comm=%s %s -> %s:%d",
		e.PID,
		e.Comm,
		IPToString(e.SAddr),
		IPToString(e.DAddr),
		e.DPort)
}

// SrcIP returns the source IP as a string.
func (e *Event) SrcIP() string {
	return IPToString(e.SAddr)
}

// DstIP returns the destination IP as a string.
func (e *Event) DstIP() string {
	return IPToString(e.DAddr)
}
