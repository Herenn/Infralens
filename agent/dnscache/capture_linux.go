//go:build linux

package dnscache

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// pollInterval bounds how long a read blocks, so the capture loop notices
// cancellation promptly.
const pollInterval = 500 // milliseconds

// recvBufferBytes is the socket receive buffer requested. With
// SO_RCVBUFFORCE (needs CAP_NET_ADMIN, which the DaemonSet grants) it is
// honoured as asked; with plain SO_RCVBUF the kernel caps it at
// net.core.rmem_max (often 208 KiB). Either way the kernel doubles the value
// for bookkeeping; the effective size is reported in Stats.RecvBuffer. When
// the buffer fills - a burst, or the read loop waiting for packet budget -
// the kernel drops, which costs names, never correctness.
const recvBufferBytes = 1 << 20

// packetSocket is an AF_PACKET/SOCK_DGRAM socket bound to every interface
// in the agent's network namespace, with Filter attached.
//
// With hostNetwork (how the DaemonSet runs the agent) that namespace is the
// host's. The socket is a ptype_all tap, so it sees both received and
// transmitted packets. Transmitted copies are needed, and that is why
// PACKET_IGNORE_OUTGOING is not used: the leg that delivers a DNS answer to a
// pod is the host transmitting it onto the pod's veth/bridge, and answers
// generated on the host (NodeLocal DNSCache, a host dnsmasq) and queries
// sent by host processes exist *only* as transmitted packets. The cost is
// that the kernel clones each transmitted packet before running the filter
// on it - the same per-packet cost as an idle `tcpdump port 53`.
type packetSocket struct {
	fd     int
	rcvbuf int
}

func htons(v uint16) uint16 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return binary.NativeEndian.Uint16(b[:])
}

// openPacketSource opens the capture socket. Requires CAP_NET_RAW.
func openPacketSource() (packetSource, error) {
	raw, err := bpf.Assemble(Filter())
	if err != nil {
		return nil, fmt.Errorf("assembling DNS capture filter: %w", err)
	}
	prog := make([]unix.SockFilter, len(raw))
	for i, ins := range raw {
		prog[i] = unix.SockFilter{Code: ins.Op, Jt: ins.Jt, Jf: ins.Jf, K: ins.K}
	}

	// Protocol 0: the socket receives nothing until it is bound below, so no
	// unfiltered packet can be queued between creation and filter attach.
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return nil, fmt.Errorf("opening AF_PACKET socket: %w (needs CAP_NET_RAW)", err)
		}
		return nil, fmt.Errorf("opening AF_PACKET socket: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = unix.Close(fd)
		}
	}()

	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if err := unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &fprog); err != nil {
		return nil, fmt.Errorf("attaching DNS capture filter: %w", err)
	}
	// Best effort, both: a smaller buffer only means more drops under a burst.
	if unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, recvBufferBytes) != nil {
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, recvBufferBytes)
	}
	rcvbuf, _ := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF)

	tv := unix.NsecToTimeval(int64(pollInterval) * 1e6)
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return nil, fmt.Errorf("setting capture read timeout: %w", err)
	}

	// Ifindex 0 = all interfaces.
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL)}); err != nil {
		return nil, fmt.Errorf("binding AF_PACKET socket: %w", err)
	}

	// Clear the kernel counters so the first KernelDrops reading starts here.
	_, _ = unix.GetsockoptTpacketStats(fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)

	ok = true
	return &packetSocket{fd: fd, rcvbuf: rcvbuf}, nil
}

// ReadPacket reads one packet and where it was seen (sockaddr_ll ifindex and
// pkttype). A plain blocking read on the capture goroutine: the socket is not
// registered with the Go netpoller, and SO_RCVTIMEO bounds each call. The
// filter's snap length keeps every packet within buf.
func (s *packetSocket) ReadPacket(buf []byte) (int, PacketMeta, error) {
	n, from, err := unix.Recvfrom(s.fd, buf, 0)
	if err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			return 0, PacketMeta{}, errReadTimeout
		}
		return 0, PacketMeta{}, err
	}
	ll, ok := from.(*unix.SockaddrLinklayer)
	if !ok {
		return 0, PacketMeta{}, fmt.Errorf("unexpected source address type %T", from)
	}
	return n, PacketMeta{Ifindex: ll.Ifindex, PktType: ll.Pkttype}, nil
}

// KernelDrops returns packets dropped since the previous call (the kernel
// resets the counter on read).
func (s *packetSocket) KernelDrops() (uint64, error) {
	st, err := unix.GetsockoptTpacketStats(s.fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)
	if err != nil {
		return 0, err
	}
	return uint64(st.Drops), nil
}

// RecvBuffer is the effective receive buffer the kernel granted.
func (s *packetSocket) RecvBuffer() int { return s.rcvbuf }

func (s *packetSocket) Close() error {
	return unix.Close(s.fd)
}
