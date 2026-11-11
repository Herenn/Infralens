package dnscache

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

const (
	ipv4MinHeader = 20
	ipv6Header    = 40
	udpHeader     = 8
	protoUDP      = 17
	dnsPort       = 53
)

// Errors returned by ParseUDPPacket.
var (
	ErrShortPacket = errors.New("packet: truncated")
	ErrNotUDP      = errors.New("packet: not an unfragmented UDP datagram")
	ErrBadVersion  = errors.New("packet: not IPv4 or IPv6")
)

// Packet types, as reported in sockaddr_ll.sll_pkttype (linux/if_packet.h).
// Defined here so the leg-selection logic builds and is tested everywhere.
const (
	PacketHost      uint8 = 0 // addressed to this host (received)
	PacketBroadcast uint8 = 1
	PacketMulticast uint8 = 2
	PacketOtherHost uint8 = 3 // received, addressed to another host (bridge port, promisc)
	PacketOutgoing  uint8 = 4 // transmitted by this host
)

// PacketMeta is what the capture socket says about where a packet was seen.
// A packet forwarded through the host is seen more than once (received on
// one interface, transmitted on another); these two fields are what lets the
// tracker use exactly one of those copies.
type PacketMeta struct {
	Ifindex int
	PktType uint8
}

// UDPPacket is a parsed IPv4/IPv6 UDP datagram.
type UDPPacket struct {
	Src, Dst         netip.Addr
	SrcPort, DstPort uint16
	// Payload aliases the input buffer.
	Payload []byte
}

// ParseUDPPacket parses a network-layer packet (starting at the IP header, as
// an AF_PACKET SOCK_DGRAM socket delivers it) carrying UDP.
//
// Every length field is checked against the bytes actually present, in both
// directions: a header claiming more than was captured is rejected, and
// trailing bytes beyond what the header claims (link-layer padding) are cut
// off rather than handed to the DNS parser. Fragments are rejected: a DNS
// response that needed IP fragmentation is rare, and reassembly is not worth
// its attack surface here. IPv6 extension headers are not walked for the same
// reason; DNS responses do not use them in practice.
func ParseUDPPacket(b []byte) (UDPPacket, error) {
	if len(b) < 1 {
		return UDPPacket{}, ErrShortPacket
	}
	var (
		pkt  UDPPacket
		rest []byte
	)
	switch b[0] >> 4 {
	case 4:
		if len(b) < ipv4MinHeader {
			return UDPPacket{}, ErrShortPacket
		}
		ihl := int(b[0]&0x0f) * 4
		total := int(binary.BigEndian.Uint16(b[2:4]))
		if ihl < ipv4MinHeader || total < ihl || total > len(b) {
			return UDPPacket{}, ErrShortPacket
		}
		// More-fragments flag or a non-zero fragment offset.
		if binary.BigEndian.Uint16(b[6:8])&0x3fff != 0 || b[9] != protoUDP {
			return UDPPacket{}, ErrNotUDP
		}
		pkt.Src = netip.AddrFrom4([4]byte(b[12:16]))
		pkt.Dst = netip.AddrFrom4([4]byte(b[16:20]))
		rest = b[ihl:total]
	case 6:
		if len(b) < ipv6Header {
			return UDPPacket{}, ErrShortPacket
		}
		payloadLen := int(binary.BigEndian.Uint16(b[4:6]))
		if ipv6Header+payloadLen > len(b) {
			return UDPPacket{}, ErrShortPacket
		}
		if b[6] != protoUDP {
			return UDPPacket{}, ErrNotUDP
		}
		pkt.Src = netip.AddrFrom16([16]byte(b[8:24])).Unmap()
		pkt.Dst = netip.AddrFrom16([16]byte(b[24:40])).Unmap()
		rest = b[ipv6Header : ipv6Header+payloadLen]
	default:
		return UDPPacket{}, ErrBadVersion
	}

	if len(rest) < udpHeader {
		return UDPPacket{}, ErrShortPacket
	}
	udpLen := int(binary.BigEndian.Uint16(rest[4:6]))
	if udpLen < udpHeader || udpLen > len(rest) {
		return UDPPacket{}, ErrShortPacket
	}
	pkt.SrcPort = binary.BigEndian.Uint16(rest[0:2])
	pkt.DstPort = binary.BigEndian.Uint16(rest[2:4])
	pkt.Payload = rest[udpHeader:udpLen]
	return pkt, nil
}
