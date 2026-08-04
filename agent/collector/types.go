// Package collector provides eBPF-based network event collection for the InfraLens agent.
// It encapsulates all BPF program loading, attaching, and event parsing logic.
package collector

import (
	"encoding/binary"
	"fmt"
	"net"
)

// Address family constants (matching kernel definitions)
const (
	AF_INET  = 2
	AF_INET6 = 10
)

// Direction constants (matching BPF program)
const (
	DirectionOutbound = 0 // Client initiated (connect)
	DirectionInbound  = 1 // Server accepted (accept)
)

// Protocol constants (matching BPF program)
const (
	ProtocolTCP = 0
	ProtocolUDP = 1
)

// Event struct layout offsets (must match C struct event_t in traffic.c)
// UNIFIED 64-byte layout
const (
	EventSize          = 64
	eventOffsetDir     = 0  // direction (1 byte)
	eventOffsetPad1    = 1  // _pad1 (3 bytes)
	eventOffsetPid     = 4  // pid (4 bytes)
	eventOffsetComm    = 8  // comm (16 bytes)
	eventOffsetSrcAddr = 24 // src_addr (16 bytes)
	eventOffsetDstAddr = 40 // dst_addr (16 bytes)
	eventOffsetFamily   = 56 // family (2 bytes)
	eventOffsetSrcPort  = 58 // src_port (2 bytes)
	eventOffsetDstPort  = 60 // dst_port (2 bytes)
	eventOffsetProtocol = 62 // protocol (1 byte)
	eventOffsetPad2     = 63 // _pad2 (1 byte)
	eventCommLen        = 16
)

// Event represents a TCP connection event from the BPF program.
// UNIFIED 64-byte layout with consistent address storage for IPv4/IPv6.
//
// Layout:
//
//	offset 0:  direction  (1 byte)
//	offset 1:  _pad1      (3 bytes)
//	offset 4:  pid        (4 bytes)
//	offset 8:  comm       (16 bytes)
//	offset 24: src_addr   (16 bytes) - IPv4 in [0:4], IPv6 in [0:16]
//	offset 40: dst_addr   (16 bytes) - IPv4 in [0:4], IPv6 in [0:16]
//	offset 56: family     (2 bytes)  - AF_INET (2) or AF_INET6 (10)
//	offset 58: src_port   (2 bytes)
//	offset 60: dst_port   (2 bytes)
//	offset 62: protocol   (1 byte)   - ProtocolTCP (0) or ProtocolUDP (1)
//	offset 63: _pad2      (1 byte)
type Event struct {
	Direction uint8  // 0 = outbound (connect), 1 = inbound (accept)
	Pid       uint32 // Process ID
	Comm      string // Process name
	SrcAddr   net.IP // Source IP address
	DstAddr   net.IP // Destination IP address
	Family    uint16 // Address family: AF_INET (2) or AF_INET6 (10)
	SrcPort   uint16 // Source port
	DstPort   uint16 // Destination port (network byte order converted)
	Protocol  uint8  // ProtocolTCP (0) or ProtocolUDP (1)
}

// ConnKey is used to identify connections for stats tracking.
type ConnKey struct {
	SockPtr uint64
}

// ConnStats represents connection statistics from the BPF program.
// UNIFIED layout with consistent address storage for IPv4/IPv6.
//
// Layout:
//
//	offset 0:  bytes_sent   (8 bytes)
//	offset 8:  bytes_recv   (8 bytes)
//	offset 16: packets_sent (8 bytes)
//	offset 24: packets_recv (8 bytes)
//	offset 32: pid          (4 bytes)
//	offset 36: family       (2 bytes)
//	offset 38: src_port     (2 bytes)
//	offset 40: dst_port     (2 bytes)
//	offset 42: protocol     (1 byte)
//	offset 43: _pad         (1 byte)
//	offset 44: src_addr     (16 bytes)
//	offset 60: dst_addr     (16 bytes)
//	offset 76: comm         (16 bytes)
//	offset 92: _pad2        (4 bytes) - for 8-byte alignment
//	offset 96: last_update  (8 bytes)
//	Total: 104 bytes
type ConnStats struct {
	BytesSent   uint64
	BytesRecv   uint64
	PacketsSent uint64
	PacketsRecv uint64
	Pid         uint32
	Family      uint16
	SrcPort     uint16
	DstPort     uint16
	Protocol    uint8
	SrcAddr     net.IP
	DstAddr     net.IP
	Comm        string
	LastUpdate  uint64
}

// connStatsRaw is the raw byte-aligned struct matching the C layout.
// Used for reading from BPF maps.
type connStatsRaw struct {
	BytesSent   uint64
	BytesRecv   uint64
	PacketsSent uint64
	PacketsRecv uint64
	Pid         uint32
	Family      uint16
	SrcPort     uint16
	DstPort     uint16
	Protocol    uint8
	_pad        uint8
	SrcAddr     [16]byte
	DstAddr     [16]byte
	Comm        [16]byte
	_pad2       [4]byte
	LastUpdate  uint64
}

// ParseEvent safely parses raw bytes into Event using explicit offsets.
// This avoids relying on Go struct alignment matching C struct layout.
func ParseEvent(data []byte) (*Event, error) {
	if len(data) < EventSize {
		return nil, fmt.Errorf("event data too short: got %d bytes, need %d", len(data), EventSize)
	}

	direction := data[eventOffsetDir]
	pid := binary.LittleEndian.Uint32(data[eventOffsetPid:])
	family := binary.LittleEndian.Uint16(data[eventOffsetFamily:])
	srcPort := binary.LittleEndian.Uint16(data[eventOffsetSrcPort:])
	dstPort := binary.LittleEndian.Uint16(data[eventOffsetDstPort:])
	protocol := data[eventOffsetProtocol]

	// Extract comm (null-terminated string)
	comm := extractComm(data[eventOffsetComm : eventOffsetComm+eventCommLen])

	// Extract addresses based on family
	var srcAddr, dstAddr net.IP
	switch family {
	case AF_INET:
		// IPv4: stored in first 4 bytes
		srcAddr = net.IP(data[eventOffsetSrcAddr : eventOffsetSrcAddr+4])
		dstAddr = net.IP(data[eventOffsetDstAddr : eventOffsetDstAddr+4])
	case AF_INET6:
		// IPv6: uses all 16 bytes
		srcAddr = net.IP(data[eventOffsetSrcAddr : eventOffsetSrcAddr+16])
		dstAddr = net.IP(data[eventOffsetDstAddr : eventOffsetDstAddr+16])
	default:
		return nil, fmt.Errorf("unsupported address family: %d", family)
	}

	return &Event{
		Direction: direction,
		Pid:       pid,
		Comm:      comm,
		SrcAddr:   srcAddr,
		DstAddr:   dstAddr,
		Family:    family,
		SrcPort:   srcPort,
		DstPort:   Ntohs(dstPort),
		Protocol:  protocol,
	}, nil
}

// ProtocolString returns "tcp" or "udp" for the event's protocol.
func (e *Event) ProtocolString() string {
	if e.Protocol == ProtocolUDP {
		return "udp"
	}
	return "tcp"
}

// extractComm extracts a null-terminated string from a byte slice.
func extractComm(data []byte) string {
	for i, b := range data {
		if b == 0 {
			return string(data[:i])
		}
	}
	return string(data)
}

// Ntohs converts a uint16 from network byte order to host byte order.
func Ntohs(n uint16) uint16 {
	return (n>>8)&0xff | (n&0xff)<<8
}

// IsOutbound returns true if this is an outbound connection.
func (e *Event) IsOutbound() bool {
	return e.Direction == DirectionOutbound
}

// IsInbound returns true if this is an inbound connection.
func (e *Event) IsInbound() bool {
	return e.Direction == DirectionInbound
}

// IsIPv4 returns true if this is an IPv4 connection.
func (e *Event) IsIPv4() bool {
	return e.Family == AF_INET
}

// IsIPv6 returns true if this is an IPv6 connection.
func (e *Event) IsIPv6() bool {
	return e.Family == AF_INET6
}

// String returns a human-readable representation of the event.
func (e *Event) String() string {
	dir := "→"
	if e.IsInbound() {
		dir = "←"
	}
	return fmt.Sprintf("[%d] %s %s %s %s:%d",
		e.Pid, e.Comm, e.SrcAddr, dir, e.DstAddr, e.DstPort)
}
