package collector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/perf"
	"github.com/cilium/ebpf/rlimit"
)

// Collector manages eBPF program loading, attaching, and event collection.
type Collector struct {
	objs   *bpfObjects
	reader *perf.Reader
	links  []link.Link

	// For throughput tracking
	mu            sync.Mutex
	previousStats map[connTrackKey]prevStats
	lastPollTime  time.Time
}

// connTrackKey is used to track connections across polls
type connTrackKey struct {
	srcAddr string
	dstAddr string
	dstPort uint16
}

// prevStats stores previous poll values for delta calculation
type prevStats struct {
	bytesSent   uint64
	bytesRecv   uint64
	packetsSent uint64
	packetsRecv uint64
}

// New creates a new Collector instance.
// It loads the BPF programs and attaches to kernel hooks.
func New() (*Collector, error) {
	// Remove memory lock limit for eBPF (required on older kernels)
	if err := rlimit.RemoveMemlock(); err != nil {
		fmt.Printf("Warning: Failed to remove memlock limit: %v\n", err)
	}

	// Load BPF objects (generated code)
	var objs bpfObjects
	if err := loadBpfObjects(&objs, nil); err != nil {
		var ve *ebpf.VerifierError
		if errors.As(err, &ve) {
			return nil, fmt.Errorf("BPF verifier error: %+v", ve)
		}
		return nil, fmt.Errorf("loading BPF objects: %w", err)
	}

	c := &Collector{
		objs:          &objs,
		links:         make([]link.Link, 0, 10),
		previousStats: make(map[connTrackKey]prevStats),
		lastPollTime:  time.Now(),
	}

	// Attach probes
	if err := c.attachProbes(); err != nil {
		c.Close()
		return nil, err
	}

	// Initialize perf reader
	reader, err := perf.NewReader(objs.Events, 64*4096)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("creating perf reader: %w", err)
	}
	c.reader = reader

	return c, nil
}

// attachProbes attaches all kprobes/kretprobes to the kernel.
func (c *Collector) attachProbes() error {
	var err error
	var l link.Link

	// IPv4 connect probes (required)
	l, err = link.Kprobe("tcp_v4_connect", c.objs.KprobeTcpV4Connect, nil)
	if err != nil {
		return fmt.Errorf("attaching kprobe/tcp_v4_connect: %w", err)
	}
	c.links = append(c.links, l)
	fmt.Println("✓ Kprobe attached to tcp_v4_connect")

	l, err = link.Kretprobe("tcp_v4_connect", c.objs.KretprobeTcpV4Connect, nil)
	if err != nil {
		return fmt.Errorf("attaching kretprobe/tcp_v4_connect: %w", err)
	}
	c.links = append(c.links, l)
	fmt.Println("✓ Kretprobe attached to tcp_v4_connect")

	// IPv6 connect probes (optional - some kernels may not support)
	l, err = link.Kprobe("tcp_v6_connect", c.objs.KprobeTcpV6Connect, nil)
	if err != nil {
		fmt.Printf("⚠ Warning: Failed to attach kprobe/tcp_v6_connect: %v\n", err)
		fmt.Println("  (IPv6 outbound tracing disabled)")
	} else {
		c.links = append(c.links, l)
		fmt.Println("✓ Kprobe attached to tcp_v6_connect")

		l, err = link.Kretprobe("tcp_v6_connect", c.objs.KretprobeTcpV6Connect, nil)
		if err != nil {
			fmt.Printf("⚠ Warning: Failed to attach kretprobe/tcp_v6_connect: %v\n", err)
		} else {
			c.links = append(c.links, l)
			fmt.Println("✓ Kretprobe attached to tcp_v6_connect")
		}
	}

	// tcp_sendmsg for bytes sent tracking (optional)
	l, err = link.Kprobe("tcp_sendmsg", c.objs.KprobeTcpSendmsg, nil)
	if err != nil {
		fmt.Printf("⚠ Warning: Failed to attach kprobe/tcp_sendmsg: %v\n", err)
		fmt.Println("  (Bytes sent tracking disabled)")
	} else {
		c.links = append(c.links, l)
		fmt.Println("✓ Kprobe attached to tcp_sendmsg")
	}

	// tcp_recvmsg for bytes received tracking (optional)
	l, err = link.Kprobe("tcp_recvmsg", c.objs.KprobeTcpRecvmsg, nil)
	if err != nil {
		fmt.Printf("⚠ Warning: Failed to attach kprobe/tcp_recvmsg: %v\n", err)
		fmt.Println("  (Bytes recv tracking disabled)")
	} else {
		c.links = append(c.links, l)
		fmt.Println("✓ Kprobe attached to tcp_recvmsg")

		l, err = link.Kretprobe("tcp_recvmsg", c.objs.KretprobeTcpRecvmsg, nil)
		if err != nil {
			fmt.Printf("⚠ Warning: Failed to attach kretprobe/tcp_recvmsg: %v\n", err)
		} else {
			c.links = append(c.links, l)
			fmt.Println("✓ Kretprobe attached to tcp_recvmsg")
		}
	}

	// tcp_close for cleanup (optional)
	l, err = link.Kprobe("tcp_close", c.objs.KprobeTcpClose, nil)
	if err != nil {
		fmt.Printf("⚠ Warning: Failed to attach kprobe/tcp_close: %v\n", err)
	} else {
		c.links = append(c.links, l)
		fmt.Println("✓ Kprobe attached to tcp_close")
	}

	// inet_csk_accept for incoming connections (optional)
	l, err = link.Kretprobe("inet_csk_accept", c.objs.KretprobeInetCskAccept, nil)
	if err != nil {
		fmt.Printf("⚠ Warning: Failed to attach kretprobe/inet_csk_accept: %v\n", err)
		fmt.Println("  (Incoming connection tracing disabled)")
	} else {
		c.links = append(c.links, l)
		fmt.Println("✓ Kretprobe attached to inet_csk_accept")
	}

	return nil
}

// Start begins collecting events and sends them to the provided channel.
// This method blocks until the context is cancelled.
func (c *Collector) Start(ctx context.Context, eventsCh chan<- *Event) error {
	fmt.Println("✓ BPF objects loaded successfully")
	fmt.Println("🌐 Dual-Stack (IPv4/IPv6) Monitoring Enabled")
	fmt.Println("✓ Perf reader initialized")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		record, err := c.reader.Read()
		if err != nil {
			if errors.Is(err, perf.ErrClosed) {
				return nil
			}
			fmt.Printf("Error reading perf event: %v\n", err)
			continue
		}

		if record.LostSamples > 0 {
			fmt.Printf("Warning: lost %d samples\n", record.LostSamples)
			continue
		}

		event, err := ParseEvent(record.RawSample)
		if err != nil {
			fmt.Printf("Failed to parse event: %v\n", err)
			continue
		}

		select {
		case eventsCh <- event:
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Channel full, drop event
		}
	}
}

// Throughput represents throughput statistics for a single connection.
type Throughput struct {
	Pid           uint32
	Comm          string
	SrcAddr       string
	DstAddr       string
	SrcPort       uint16
	DstPort       uint16
	BytesSent     uint64
	BytesRecv     uint64
	BytesSentRate float64
	BytesRecvRate float64
	PacketsSent   uint64
	PacketsRecv   uint64
}

// PollThroughput reads connection stats from the BPF map and calculates throughput.
func (c *Collector) PollThroughput() []Throughput {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	intervalSec := now.Sub(c.lastPollTime).Seconds()
	c.lastPollTime = now

	var throughputs []Throughput
	var key ConnKey
	var value connStatsRaw
	currentStats := make(map[connTrackKey]connStatsRaw)

	// Read all current stats from BPF map
	iter := c.objs.ConnStats.Iterate()
	for iter.Next(&key, &value) {
		var srcAddr, dstAddr string
		switch value.Family {
		case AF_INET:
			srcAddr = net.IP(value.SrcAddr[:4]).String()
			dstAddr = net.IP(value.DstAddr[:4]).String()
		case AF_INET6:
			srcAddr = net.IP(value.SrcAddr[:]).String()
			dstAddr = net.IP(value.DstAddr[:]).String()
		default:
			continue
		}

		ck := connTrackKey{
			srcAddr: srcAddr,
			dstAddr: dstAddr,
			dstPort: Ntohs(value.DstPort),
		}
		currentStats[ck] = value
	}

	if err := iter.Err(); err != nil {
		fmt.Printf("Error iterating conn_stats map: %v\n", err)
	}

	// Calculate deltas and throughput
	for ck, current := range currentStats {
		prev, hasPrev := c.previousStats[ck]

		var deltaSent, deltaRecv, deltaPktSent, deltaPktRecv uint64
		if hasPrev {
			if current.BytesSent >= prev.bytesSent {
				deltaSent = current.BytesSent - prev.bytesSent
			}
			if current.BytesRecv >= prev.bytesRecv {
				deltaRecv = current.BytesRecv - prev.bytesRecv
			}
			if current.PacketsSent >= prev.packetsSent {
				deltaPktSent = current.PacketsSent - prev.packetsSent
			}
			if current.PacketsRecv >= prev.packetsRecv {
				deltaPktRecv = current.PacketsRecv - prev.packetsRecv
			}
		} else {
			deltaSent = current.BytesSent
			deltaRecv = current.BytesRecv
			deltaPktSent = current.PacketsSent
			deltaPktRecv = current.PacketsRecv
		}

		// Only report if there's actual traffic
		if deltaSent > 0 || deltaRecv > 0 {
			var sentRate, recvRate float64
			if intervalSec > 0 {
				sentRate = float64(deltaSent) / intervalSec
				recvRate = float64(deltaRecv) / intervalSec
			}

			throughputs = append(throughputs, Throughput{
				Pid:           current.Pid,
				Comm:          extractComm(current.Comm[:]),
				SrcAddr:       ck.srcAddr,
				DstAddr:       ck.dstAddr,
				SrcPort:       current.SrcPort,
				DstPort:       ck.dstPort,
				BytesSent:     current.BytesSent,
				BytesRecv:     current.BytesRecv,
				BytesSentRate: sentRate,
				BytesRecvRate: recvRate,
				PacketsSent:   deltaPktSent,
				PacketsRecv:   deltaPktRecv,
			})
		}

		// Update previous stats
		c.previousStats[ck] = prevStats{
			bytesSent:   current.BytesSent,
			bytesRecv:   current.BytesRecv,
			packetsSent: current.PacketsSent,
			packetsRecv: current.PacketsRecv,
		}
	}

	// Clean up stale entries
	for ck := range c.previousStats {
		if _, exists := currentStats[ck]; !exists {
			delete(c.previousStats, ck)
		}
	}

	return throughputs
}

// Close releases all resources associated with the Collector.
func (c *Collector) Close() error {
	var errs []error

	if c.reader != nil {
		if err := c.reader.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing perf reader: %w", err))
		}
	}

	for _, l := range c.links {
		if err := l.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing link: %w", err))
		}
	}

	if c.objs != nil {
		if err := c.objs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing BPF objects: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("closing collector: %v", errs)
	}
	return nil
}

